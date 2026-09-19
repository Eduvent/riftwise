package recommendation

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"time"

	"lolengine/internal/catalog"
	"lolengine/internal/domain"
	"lolengine/internal/itemization"
	"lolengine/internal/personalization"
	"lolengine/internal/state"
	"lolengine/internal/storage"
)

// DashboardSnapshot keeps game, catalog, and recommendation revisions consistent for clients.
type DashboardSnapshot struct {
	Epoch           string                           `json:"epoch"`
	Revision        uint64                           `json:"revision"`
	UpdatedAt       time.Time                        `json:"updated_at"`
	Game            domain.Snapshot                  `json:"game"`
	Catalog         catalog.Status                   `json:"catalog"`
	Recommendation  itemization.Result               `json:"recommendation"`
	DecisionEvents  domain.DecisionEventAvailability `json:"decision_events"`
	Personalization personalization.Status           `json:"personalization"`
}

// Service is the API boundary for dashboard reads, subscriptions, and manual recalculation.
type Service interface {
	Current() DashboardSnapshot
	Subscribe(int) (DashboardSnapshot, <-chan DashboardSnapshot, func())
	Recalculate(context.Context) (DashboardSnapshot, error)
}

// CatalogProvider supports current reads and live-patch synchronization.
type CatalogProvider interface {
	Current() (catalog.Catalog, catalog.Status, bool)
	EnsureForGame(context.Context, string) error
}

// RecommendationPersistence stores only semantic recommendation changes.
type RecommendationPersistence interface {
	SaveRecommendation(context.Context, storage.RecommendationRecord) (bool, error)
}

// Engine is the pure itemization boundary.
type Engine interface {
	Recommend(time.Time, domain.Snapshot, catalog.Catalog) itemization.Result
}

// Config controls deterministic time in tests.
type Config struct {
	Now             func() time.Time
	RefreshInterval time.Duration
	Epoch           string
}

// Coordinator combines game and catalog state into one dashboard stream.
type Coordinator struct {
	gameStore             *state.Store
	catalogs              CatalogProvider
	engine                Engine
	persistence           RecommendationPersistence
	dashboard             *Store
	now                   func() time.Time
	stabilizer            *Stabilizer
	refreshInterval       time.Duration
	decisionPersistence   DecisionPersistence
	decisionNotifications *decisionEventStore

	calculateMu               sync.Mutex
	lastSignature             string
	lastRecommendationMatchID string
	lastCalculatedAt          time.Time
	rulesRevision             uint64
	catalogRevision           uint64
	decisionCursor            decisionCursor
	decisionLoaded            bool
	decisionStatus            domain.DecisionEventAvailability
	pendingDecision           *pendingDecisionUpdate
}

// NewCoordinator creates the recommendation pipeline.
func NewCoordinator(gameStore *state.Store, catalogs CatalogProvider, engine Engine, persistence RecommendationPersistence, cfg Config) (*Coordinator, error) {
	if gameStore == nil || catalogs == nil || engine == nil {
		return nil, errors.New("game store, catalog provider, and engine are required")
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.RefreshInterval <= 0 {
		cfg.RefreshInterval = 30 * time.Second
	}
	epoch := strings.TrimSpace(cfg.Epoch)
	if epoch == "" {
		epoch = NewEpoch()
	} else if !ValidEpoch(epoch) {
		return nil, errors.New("dashboard epoch must contain 1-64 letters, digits, hyphens, or underscores")
	}
	game := gameStore.Current()
	knowledge, catalogStatus, ready := catalogs.Current()
	if !ready {
		knowledge = catalog.Catalog{}
	}
	stabilizer := NewStabilizer()
	calculatedAt := cfg.Now()
	recommendation, rulesRevision := recommendAtStableRevision(engine, calculatedAt, game, knowledge)
	recommendation = stabilizer.Apply(calculatedAt, game, recommendation)
	personalizationStatus := enginePersonalizationStatus(engine)
	decisionPersistence, decisionAvailable := persistence.(DecisionPersistence)
	decisionStatus := domain.DecisionEventAvailability{Status: domain.DecisionEventStatusUnavailable}
	if decisionAvailable {
		decisionStatus.Available = true
		decisionStatus.Status = domain.DecisionEventStatusReady
	}
	return &Coordinator{
		gameStore: gameStore, catalogs: catalogs, engine: engine, persistence: persistence, now: cfg.Now,
		stabilizer: stabilizer, refreshInterval: cfg.RefreshInterval, lastCalculatedAt: calculatedAt,
		rulesRevision: rulesRevision, catalogRevision: catalogSnapshotRevision(knowledge),
		decisionPersistence: decisionPersistence, decisionNotifications: newDecisionEventStore(), decisionStatus: decisionStatus,
		dashboard: NewStore(DashboardSnapshot{
			Epoch: epoch, Game: game, Catalog: catalogStatus, Recommendation: recommendation, DecisionEvents: decisionStatus, Personalization: personalizationStatus, UpdatedAt: cfg.Now(),
		}),
	}, nil
}

// Run recalculates immediately and after each meaningful tracker update.
func (coordinator *Coordinator) Run(ctx context.Context) {
	initial, updates, unsubscribe := coordinator.gameStore.Subscribe(2)
	defer unsubscribe()
	_, _ = coordinator.recalculate(ctx, &initial)
	for {
		select {
		case <-ctx.Done():
			return
		case game, open := <-updates:
			if !open {
				return
			}
			_, _ = coordinator.handleGameUpdate(ctx, game)
		}
	}
}

// HistorySessionSettled reports whether no uncertain decision batch still
// needs to be committed for a session before privacy deletion.
func (coordinator *Coordinator) HistorySessionSettled(sessionID string) bool {
	sessionID = strings.TrimSpace(sessionID)
	coordinator.calculateMu.Lock()
	defer coordinator.calculateMu.Unlock()
	return coordinator.pendingDecision == nil || coordinator.pendingDecision.sessionID != sessionID
}

// Recalculate handles the manual API trigger against the latest game snapshot.
func (coordinator *Coordinator) Recalculate(ctx context.Context) (DashboardSnapshot, error) {
	return coordinator.recalculate(ctx, nil)
}

// Current returns one isolated dashboard snapshot.
func (coordinator *Coordinator) Current() DashboardSnapshot {
	return coordinator.dashboard.Current()
}

// CurrentFeedbackEligibility exposes only a current, actionable recommendation
// whose exact evidence has already been committed as the latest decision event.
func (coordinator *Coordinator) CurrentFeedbackEligibility() (personalization.FeedbackEligibility, bool) {
	dashboard := coordinator.dashboard.Current()
	if dashboard.Game.Session == nil || dashboard.Game.Session.State != domain.MatchActive ||
		dashboard.Game.Game == nil || dashboard.DecisionEvents.Status != domain.DecisionEventStatusReady ||
		dashboard.DecisionEvents.Latest == nil {
		return personalization.FeedbackEligibility{}, false
	}
	result := dashboard.Recommendation
	if (result.Status != itemization.StatusReady && result.Status != itemization.StatusSaveForComponent) ||
		!structuredRuleEvidence(result) {
		return personalization.FeedbackEligibility{}, false
	}
	latest := dashboard.DecisionEvents.Latest
	if latest.SessionID != dashboard.Game.Session.ID || latest.Evidence == nil {
		return personalization.FeedbackEligibility{}, false
	}
	currentEvidence := recommendationEvidence(result)
	if recommendationEvidenceDigest(latest.Evidence) != recommendationEvidenceDigest(currentEvidence) {
		return personalization.FeedbackEligibility{}, false
	}
	role, _ := domain.ParseRole(dashboard.Game.Game.ActivePlayer.Position)
	championID := dashboard.Game.Game.ActivePlayer.ChampionName
	if result.Profile != nil {
		if result.Profile.Role.Valid() {
			role = result.Profile.Role
		}
		if strings.TrimSpace(result.Profile.ChampionID) != "" {
			championID = result.Profile.ChampionID
		}
	}
	if !role.Valid() || strings.TrimSpace(championID) == "" {
		return personalization.FeedbackEligibility{}, false
	}
	visible := make([]int, 0, len(currentEvidence.RankedChoices))
	for _, choice := range currentEvidence.RankedChoices {
		if choice.Item.ID > 0 {
			visible = append(visible, choice.Item.ID)
		}
	}
	if len(visible) == 0 {
		return personalization.FeedbackEligibility{}, false
	}
	return personalization.FeedbackEligibility{
		EventID: latest.ID, SessionID: latest.SessionID, ChampionID: championID, Role: role,
		Evidence: *domain.CloneRecommendationEvidence(latest.Evidence), VisibleItemIDs: visible,
	}, true
}

// Subscribe registers for complete dashboard updates.
func (coordinator *Coordinator) Subscribe(buffer int) (DashboardSnapshot, <-chan DashboardSnapshot, func()) {
	return coordinator.dashboard.Subscribe(buffer)
}

// SubscribeDecisionEvents returns only newly committed decision notifications.
// Durable history remains authoritative through the REST API.
func (coordinator *Coordinator) SubscribeDecisionEvents(buffer int) (<-chan domain.DecisionEvent, func()) {
	return coordinator.decisionNotifications.Subscribe(buffer)
}

func (coordinator *Coordinator) recalculate(ctx context.Context, requested *domain.Snapshot) (DashboardSnapshot, error) {
	coordinator.calculateMu.Lock()
	defer coordinator.calculateMu.Unlock()
	return coordinator.calculateLocked(ctx, requested)
}

func (coordinator *Coordinator) handleGameUpdate(ctx context.Context, requested domain.Snapshot) (DashboardSnapshot, error) {
	coordinator.calculateMu.Lock()
	defer coordinator.calculateMu.Unlock()
	game := coordinator.latestGame(&requested)
	currentDashboard := coordinator.dashboard.Current()
	if game.Revision < currentDashboard.Game.Revision {
		return currentDashboard, nil
	}
	now := coordinator.now().UTC()
	knowledge, catalogStatus, ready := coordinator.catalogs.Current()
	if !ready {
		knowledge = catalog.Catalog{}
	}
	if !coordinator.shouldRecalculate(currentDashboard, game, knowledge, catalogStatus, now) {
		return coordinator.dashboard.Update(game, currentDashboard.Catalog, currentDashboard.Recommendation, now), nil
	}
	return coordinator.calculateLocked(ctx, &game)
}

func (coordinator *Coordinator) calculateLocked(ctx context.Context, requested *domain.Snapshot) (DashboardSnapshot, error) {

	// Read while holding the calculation lock. Event payloads are only wake-up
	// hints: another poll or a manual recalculation may already have advanced the
	// store while this calculation was waiting.
	game := coordinator.latestGame(requested)
	currentDashboard := coordinator.dashboard.Current()
	if game.Revision < currentDashboard.Game.Revision {
		return currentDashboard, nil
	}

	var syncErr error
	if game.Game != nil && game.Game.GameVersion != "" {
		syncErr = coordinator.catalogs.EnsureForGame(ctx, game.Game.GameVersion)
	}
	knowledge, catalogStatus, ready := coordinator.catalogs.Current()
	if !ready {
		knowledge = catalog.Catalog{}
	}
	calculatedAt := coordinator.now()
	result, nextRulesRevision := recommendAtStableRevision(coordinator.engine, calculatedAt, game, knowledge)
	nextCatalogRevision := catalogSnapshotRevision(knowledge)
	if nextRulesRevision != coordinator.rulesRevision || nextCatalogRevision != coordinator.catalogRevision {
		coordinator.stabilizer.Reset()
	}
	result = coordinator.stabilizer.Apply(calculatedAt, game, result)
	coordinator.lastCalculatedAt = calculatedAt.UTC()
	coordinator.rulesRevision = nextRulesRevision
	coordinator.catalogRevision = nextCatalogRevision
	if syncErr != nil && catalogStatus.Ready {
		result.Warnings = append(result.Warnings, catalogEnsureWarning(catalogStatus.Source))
		syncErr = nil
	}

	var persistenceErr error
	if coordinator.persistence != nil {
		matchID := ""
		if game.Session != nil {
			matchID = strings.TrimSpace(game.Session.ID)
		}
		signature, err := semanticSignature(result)
		if err != nil {
			persistenceErr = err
		} else if signature != coordinator.lastSignature || matchID != coordinator.lastRecommendationMatchID {
			payload, marshalErr := json.Marshal(result)
			if marshalErr != nil {
				persistenceErr = marshalErr
			} else {
				_, persistenceErr = coordinator.persistence.SaveRecommendation(ctx, storage.RecommendationRecord{
					MatchID: matchID, Signature: signature, Payload: payload, UpdatedAt: coordinator.now(),
				})
				if persistenceErr == nil {
					coordinator.lastSignature = signature
					coordinator.lastRecommendationMatchID = matchID
				}
			}
		}
	}
	if persistenceErr != nil {
		result.Warnings = append(result.Warnings, "La recomendación actual no pudo guardarse localmente.")
	}
	decisionEvents, decisionErr := coordinator.persistDecisionTransitions(ctx, calculatedAt, game, result)
	if decisionErr != nil {
		result.Warnings = append(result.Warnings, "Los cambios importantes permanecen visibles, pero su historial local no pudo actualizarse.")
	}
	dashboard := coordinator.dashboard.UpdateWithStatuses(game, catalogStatus, result, coordinator.decisionStatus, enginePersonalizationStatus(coordinator.engine), coordinator.now())
	coordinator.decisionNotifications.Publish(decisionEvents)
	// Operational failures are represented by catalog status and recommendation warnings;
	// a valid dashboard snapshot remains usable offline.
	return dashboard, nil
}

func (coordinator *Coordinator) persistDecisionTransitions(ctx context.Context, now time.Time, game domain.Snapshot, result itemization.Result) ([]domain.DecisionEvent, error) {
	if coordinator.decisionPersistence == nil {
		coordinator.decisionStatus = domain.DecisionEventAvailability{Status: domain.DecisionEventStatusUnavailable}
		return nil, nil
	}
	coordinator.decisionStatus.Available = true
	activeSessionID := ""
	if game.Session != nil && game.Session.State == domain.MatchActive {
		activeSessionID = strings.TrimSpace(game.Session.ID)
	}
	committed := make([]domain.DecisionEvent, 0, 2)
	if pending := coordinator.pendingDecision; pending != nil {
		belongsToActiveSession := activeSessionID != "" && pending.sessionID == activeSessionID
		if err := coordinator.decisionPersistence.SaveDecisionUpdates(ctx, pending.checkpoint, pending.events); err != nil {
			if belongsToActiveSession {
				coordinator.decisionStatus.Status = domain.DecisionEventStatusError
			} else {
				coordinator.decisionStatus = domain.DecisionEventAvailability{
					Available: true, Status: domain.DecisionEventStatusError,
				}
			}
			return nil, err
		}
		if belongsToActiveSession {
			coordinator.decisionCursor = pending.cursor
			coordinator.decisionLoaded = true
			coordinator.decisionStatus.Status = domain.DecisionEventStatusReady
			if latest := latestDecision(pending.events); latest != nil {
				coordinator.decisionStatus.Latest = latest
			}
			committed = append(committed, cloneDecisionEvents(pending.events)...)
		} else {
			// The old decision is now authoritative history, but it is not a live
			// notification or the latest decision for a different/no session.
			coordinator.decisionCursor = decisionCursor{}
			coordinator.decisionLoaded = false
			coordinator.decisionStatus = domain.DecisionEventAvailability{
				Available: true, Status: domain.DecisionEventStatusReady,
			}
		}
		coordinator.pendingDecision = nil
	}
	if activeSessionID == "" {
		coordinator.decisionCursor = decisionCursor{}
		coordinator.decisionLoaded = false
		coordinator.decisionStatus = domain.DecisionEventAvailability{Available: true, Status: domain.DecisionEventStatusReady}
		return nil, nil
	}
	if !coordinator.decisionLoaded || coordinator.decisionCursor.sessionID != activeSessionID {
		// Latest is scoped to the active session. Do not leak a previous match's
		// decision into health/dashboard if loading the new checkpoint fails.
		coordinator.decisionStatus = domain.DecisionEventAvailability{
			Available: true, Status: domain.DecisionEventStatusReady,
		}
		loaded, latest, err := loadDecisionCursor(ctx, coordinator.decisionPersistence, activeSessionID)
		if err != nil && !isDecisionCheckpointNotFound(err) {
			coordinator.decisionStatus.Status = domain.DecisionEventStatusError
			return nil, err
		}
		if isDecisionCheckpointNotFound(err) {
			loaded = newDecisionCursor(game, result)
			checkpoint, checkpointErr := loaded.checkpoint(now)
			if checkpointErr != nil {
				coordinator.decisionStatus.Status = domain.DecisionEventStatusError
				return nil, checkpointErr
			}
			if saveErr := coordinator.decisionPersistence.SaveDecisionUpdates(ctx, checkpoint, nil); saveErr != nil {
				coordinator.decisionStatus.Status = domain.DecisionEventStatusError
				return nil, saveErr
			}
		}
		coordinator.decisionCursor = loaded
		coordinator.decisionLoaded = true
		coordinator.decisionStatus = domain.DecisionEventAvailability{
			Available: true, Status: domain.DecisionEventStatusReady, Latest: latest,
		}
		if err != nil {
			return nil, nil
		}
	}

	next, events := coordinator.decisionCursor.advance(now, game, result)
	if decisionCursorsEqual(next, coordinator.decisionCursor) {
		coordinator.decisionStatus.Status = domain.DecisionEventStatusReady
		return committed, nil
	}
	checkpoint, err := next.checkpoint(now)
	if err != nil {
		coordinator.decisionStatus.Status = domain.DecisionEventStatusError
		return nil, err
	}
	if err := coordinator.decisionPersistence.SaveDecisionUpdates(ctx, checkpoint, events); err != nil {
		coordinator.pendingDecision = newPendingDecisionUpdate(next, checkpoint, events)
		coordinator.decisionStatus.Status = domain.DecisionEventStatusError
		return committed, err
	}
	coordinator.decisionCursor = next
	coordinator.decisionStatus.Status = domain.DecisionEventStatusReady
	if latest := latestDecision(events); latest != nil {
		coordinator.decisionStatus.Latest = latest
	}
	return append(committed, events...), nil
}

func (coordinator *Coordinator) latestGame(requested *domain.Snapshot) domain.Snapshot {
	game := coordinator.gameStore.Current()
	if requested != nil && requested.Revision > game.Revision {
		game = *requested
	}
	return game
}

func (coordinator *Coordinator) shouldRecalculate(current DashboardSnapshot, next domain.Snapshot, knowledge catalog.Catalog, catalogStatus catalog.Status, now time.Time) bool {
	if engineRulesRevision(coordinator.engine) != coordinator.rulesRevision {
		return true
	}
	if catalogSnapshotRevision(knowledge) != coordinator.catalogRevision {
		return true
	}
	if current.Game.Status != next.Status || current.Game.DecisionRevision != next.DecisionRevision {
		return true
	}
	if (current.Game.Game == nil) != (next.Game == nil) {
		return true
	}
	if current.Catalog.Patch != catalogStatus.Patch || current.Catalog.Ready != catalogStatus.Ready || current.Catalog.Source != catalogStatus.Source {
		return true
	}
	if next.Game != nil && current.Game.Game != nil {
		if current.Game.Game.GameVersion != next.Game.GameVersion || current.Game.Game.MapNumber != next.Game.MapNumber || current.Game.Game.GameMode != next.Game.GameMode {
			return true
		}
		if current.Recommendation.NextComponent != nil {
			cost := float64(current.Recommendation.NextComponent.Cost)
			before := current.Game.Game.ActivePlayer.CurrentGold >= cost
			after := next.Game.ActivePlayer.CurrentGold >= cost
			if before != after {
				return true
			}
		}
	}
	return coordinator.lastCalculatedAt.IsZero() || now.Sub(coordinator.lastCalculatedAt) >= coordinator.refreshInterval
}

func engineRulesRevision(engine Engine) uint64 {
	if provider, ok := engine.(interface{ RulesRevision() uint64 }); ok {
		return provider.RulesRevision()
	}
	return 0
}

const stableRecommendationAttempts = 4

func recommendAtStableRevision(engine Engine, now time.Time, snapshot domain.Snapshot, knowledge catalog.Catalog) (itemization.Result, uint64) {
	for attempt := 0; attempt < stableRecommendationAttempts; attempt++ {
		before := engineRulesRevision(engine)
		result := engine.Recommend(now, snapshot, knowledge)
		after := engineRulesRevision(engine)
		if before == after {
			return result, after
		}
	}
	revision := engineRulesRevision(engine)
	return itemization.Result{
		Status: itemization.StatusError, GeneratedAt: now.UTC(), SourceGameRevision: snapshot.Revision,
		Patch:    knowledge.Patch,
		Reason:   "Las reglas o preferencias cambiaron durante el cálculo; se espera una instantánea coherente antes de mostrar opciones.",
		Warnings: []string{"El cálculo inestable se descartó sin modificar la opción estabilizada."},
	}, revision
}

func catalogSnapshotRevision(knowledge catalog.Catalog) uint64 {
	payload, err := json.Marshal(knowledge)
	if err != nil {
		return 0
	}
	digest := sha256.Sum256(payload)
	return binary.BigEndian.Uint64(digest[:8])
}

func enginePersonalizationStatus(engine Engine) personalization.Status {
	if provider, ok := engine.(interface{ PersonalizationStatus() personalization.Status }); ok {
		return provider.PersonalizationStatus()
	}
	return personalization.Status{Style: personalization.StyleBalanced, State: "unavailable"}
}

func catalogEnsureWarning(source string) string {
	switch source {
	case "remote_unpersisted":
		return "El catálogo remoto actualizado está activo, pero no pudo guardarse localmente y podría perderse al reiniciar."
	case "memory_cache":
		return "No se pudo actualizar el parche en línea; se mantiene el catálogo validado en memoria."
	case "cache":
		return "No se pudo actualizar el parche en línea; se usa el catálogo guardado en disco."
	default:
		return "No se pudo completar la actualización del catálogo; se mantiene el catálogo validado disponible."
	}
}

func semanticSignature(result itemization.Result) (string, error) {
	result.GeneratedAt = time.Time{}
	result.SourceGameRevision = 0
	payload, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

// Store is a bounded fan-out store for dashboard snapshots.
type Store struct {
	mu          sync.RWMutex
	current     DashboardSnapshot
	subscribers map[uint64]chan DashboardSnapshot
	nextID      uint64
}

// NewStore creates a dashboard store.
func NewStore(initial DashboardSnapshot) *Store {
	if !ValidEpoch(initial.Epoch) {
		initial.Epoch = NewEpoch()
	}
	if initial.Revision == 0 {
		initial.Revision = 1
	}
	if initial.UpdatedAt.IsZero() {
		initial.UpdatedAt = time.Now().UTC()
	}
	return &Store{current: cloneDashboard(initial), subscribers: make(map[uint64]chan DashboardSnapshot)}
}

// Current returns an isolated dashboard value.
func (store *Store) Current() DashboardSnapshot {
	store.mu.RLock()
	defer store.mu.RUnlock()
	return cloneDashboard(store.current)
}

// Update publishes only changed dashboard content.
func (store *Store) Update(game domain.Snapshot, catalogStatus catalog.Status, result itemization.Result, now time.Time) DashboardSnapshot {
	store.mu.RLock()
	decisionEvents := store.current.DecisionEvents
	store.mu.RUnlock()
	return store.UpdateWithDecisionEvents(game, catalogStatus, result, decisionEvents, now)
}

// UpdateWithDecisionEvents publishes one coherent dashboard including the
// latest durable decision-event availability and status.
func (store *Store) UpdateWithDecisionEvents(game domain.Snapshot, catalogStatus catalog.Status, result itemization.Result, decisionEvents domain.DecisionEventAvailability, now time.Time) DashboardSnapshot {
	store.mu.RLock()
	personalizationStatus := store.current.Personalization
	store.mu.RUnlock()
	return store.UpdateWithStatuses(game, catalogStatus, result, decisionEvents, personalizationStatus, now)
}

// UpdateWithStatuses publishes all additive service statuses atomically with
// the recommendation so clients never combine different preference versions.
func (store *Store) UpdateWithStatuses(game domain.Snapshot, catalogStatus catalog.Status, result itemization.Result, decisionEvents domain.DecisionEventAvailability, personalizationStatus personalization.Status, now time.Time) DashboardSnapshot {
	store.mu.Lock()
	defer store.mu.Unlock()
	if game.Revision < store.current.Game.Revision {
		return cloneDashboard(store.current)
	}
	next := DashboardSnapshot{Epoch: store.current.Epoch, Game: game, Catalog: catalogStatus, Recommendation: result, DecisionEvents: decisionEvents, Personalization: personalizationStatus, UpdatedAt: now.UTC()}
	if dashboardEquivalent(store.current, next) {
		return cloneDashboard(store.current)
	}
	next.Revision = store.current.Revision + 1
	store.current = cloneDashboard(next)
	for _, subscriber := range store.subscribers {
		offerLatest(subscriber, cloneDashboard(store.current))
	}
	return cloneDashboard(store.current)
}

// Subscribe atomically returns the current dashboard and a bounded update channel.
func (store *Store) Subscribe(buffer int) (DashboardSnapshot, <-chan DashboardSnapshot, func()) {
	if buffer < 1 {
		buffer = 1
	}
	store.mu.Lock()
	id := store.nextID
	store.nextID++
	updates := make(chan DashboardSnapshot, buffer)
	store.subscribers[id] = updates
	initial := cloneDashboard(store.current)
	store.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			store.mu.Lock()
			if channel, exists := store.subscribers[id]; exists {
				delete(store.subscribers, id)
				close(channel)
			}
			store.mu.Unlock()
		})
	}
	return initial, updates, unsubscribe
}

func dashboardEquivalent(left, right DashboardSnapshot) bool {
	left.Revision, right.Revision = 0, 0
	left.UpdatedAt, right.UpdatedAt = time.Time{}, time.Time{}
	return reflect.DeepEqual(left, right)
}

func offerLatest(channel chan DashboardSnapshot, value DashboardSnapshot) {
	select {
	case channel <- value:
		return
	default:
	}
	select {
	case <-channel:
	default:
	}
	select {
	case channel <- value:
	default:
	}
}

func cloneDashboard(source DashboardSnapshot) DashboardSnapshot {
	payload, err := json.Marshal(source)
	if err != nil {
		return source
	}
	var clone DashboardSnapshot
	if err := json.Unmarshal(payload, &clone); err != nil {
		return source
	}
	return clone
}
