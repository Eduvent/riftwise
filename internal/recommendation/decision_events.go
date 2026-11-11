package recommendation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"lolengine/internal/catalog"
	"lolengine/internal/domain"
	"lolengine/internal/itemization"
	"lolengine/internal/itemprofiles"
	"lolengine/internal/itemrules"
	"lolengine/internal/storage"
)

// DecisionPersistence is the durable boundary for semantic recommendation
// checkpoints and their authoritative history.
type DecisionPersistence interface {
	LoadDecisionCheckpoint(context.Context, string) (storage.DecisionCheckpoint, error)
	SaveDecisionUpdates(context.Context, storage.DecisionCheckpoint, []domain.DecisionEvent) error
	ListDecisionEvents(context.Context, string, uint64, int) ([]domain.DecisionEvent, error)
}

type decisionState struct {
	Status         itemization.Status `json:"status"`
	RulesAvailable bool               `json:"rules_available"`
	TargetID       int                `json:"target_id,omitempty"`
	TargetName     string             `json:"target_name,omitempty"`
	ComponentID    int                `json:"component_id,omitempty"`
	ComponentName  string             `json:"component_name,omitempty"`
	Affordable     bool               `json:"affordable"`
	Critical       bool               `json:"critical"`
	InventoryFull  bool               `json:"inventory_full"`
	Stale          bool               `json:"stale"`
	Actionable     bool               `json:"actionable"`
}

type decisionCursor struct {
	sessionID         string
	sequence          uint64
	lifecycleSequence uint64
	state             decisionState
	evidence          *domain.RecommendationEvidence
	evidenceDigest    string
}

// pendingDecisionUpdate preserves the exact bytes and timestamps of an
// uncertain persistence attempt. Retrying this batch verbatim lets SQLite
// distinguish an exact post-commit retry from an identity collision.
type pendingDecisionUpdate struct {
	sessionID  string
	cursor     decisionCursor
	checkpoint storage.DecisionCheckpoint
	events     []domain.DecisionEvent
}

func newPendingDecisionUpdate(cursor decisionCursor, checkpoint storage.DecisionCheckpoint, events []domain.DecisionEvent) *pendingDecisionUpdate {
	checkpoint.State = append(json.RawMessage(nil), checkpoint.State...)
	checkpoint.Evidence = domain.CloneRecommendationEvidence(checkpoint.Evidence)
	return &pendingDecisionUpdate{
		sessionID:  cursor.sessionID,
		cursor:     cursor,
		checkpoint: checkpoint,
		events:     cloneDecisionEvents(events),
	}
}

func newDecisionCursor(snapshot domain.Snapshot, result itemization.Result) decisionCursor {
	evidence := recommendationEvidence(result)
	cursor := decisionCursor{state: decisionStateFor(snapshot, result), evidence: evidence, evidenceDigest: recommendationEvidenceDigest(evidence)}
	if snapshot.Session != nil {
		cursor.sessionID = snapshot.Session.ID
		cursor.lifecycleSequence = snapshot.Session.LastEventSequence
	}
	return cursor
}

func decisionCursorFromCheckpoint(checkpoint storage.DecisionCheckpoint) (decisionCursor, error) {
	var state decisionState
	if err := json.Unmarshal(checkpoint.State, &state); err != nil {
		return decisionCursor{}, fmt.Errorf("decode decision checkpoint: %w", err)
	}
	return decisionCursor{
		sessionID: checkpoint.SessionID, sequence: checkpoint.LastSequence,
		lifecycleSequence: checkpoint.LastLifecycleSequence, state: state,
		evidence: domain.CloneRecommendationEvidence(checkpoint.Evidence), evidenceDigest: recommendationEvidenceDigest(checkpoint.Evidence),
	}, nil
}

func (cursor decisionCursor) checkpoint(now time.Time) (storage.DecisionCheckpoint, error) {
	state, err := json.Marshal(cursor.state)
	if err != nil {
		return storage.DecisionCheckpoint{}, fmt.Errorf("encode decision checkpoint: %w", err)
	}
	return storage.DecisionCheckpoint{
		SessionID: cursor.sessionID, LastSequence: cursor.sequence,
		LastLifecycleSequence: cursor.lifecycleSequence, State: state,
		Evidence: domain.CloneRecommendationEvidence(cursor.evidence), UpdatedAt: now.UTC(),
	}, nil
}

func (cursor decisionCursor) advance(now time.Time, snapshot domain.Snapshot, result itemization.Result) (decisionCursor, []domain.DecisionEvent) {
	if snapshot.Session == nil || strings.TrimSpace(snapshot.Session.ID) == "" || snapshot.Session.State != domain.MatchActive {
		return decisionCursor{}, nil
	}
	if cursor.sessionID != snapshot.Session.ID {
		return newDecisionCursor(snapshot, result), nil
	}
	current := decisionStateFor(snapshot, result)
	currentEvidence := recommendationEvidence(result)
	lifecycle := newLifecycleEvents(snapshot, cursor.lifecycleSequence)
	previousSummary := summarizeDecisionState(cursor.state)
	currentSummary := summarizeDecisionState(current)
	events := make([]domain.DecisionEvent, 0, 6)
	appendEvent := func(kind domain.DecisionKind, importance domain.DecisionImportance, summary, reason string, related *domain.SemanticEvent) {
		cursor.sequence++
		occurredAt := snapshot.UpdatedAt.UTC()
		gameTime := currentGameTime(snapshot)
		relatedID := ""
		if related != nil {
			occurredAt = related.OccurredAt.UTC()
			gameTime = related.GameTimeSeconds
			relatedID = related.ID
		}
		if occurredAt.IsZero() {
			occurredAt = now.UTC()
		}
		reasons := appendUniqueReasons([]string{reason}, result.ReasonCodes...)
		fingerprint, _ := json.Marshal(struct {
			Sequence  uint64              `json:"sequence"`
			Kind      domain.DecisionKind `json:"kind"`
			Target    int                 `json:"target"`
			Component int                 `json:"component"`
			Related   string              `json:"related"`
		}{cursor.sequence, kind, current.TargetID, current.ComponentID, relatedID})
		digest := sha256.Sum256(fingerprint)
		events = append(events, domain.DecisionEvent{
			ID:        fmt.Sprintf("%s:decision:%06d", cursor.sessionID, cursor.sequence),
			SessionID: cursor.sessionID, Sequence: cursor.sequence,
			DedupeKey: fmt.Sprintf("decision:%06d:%s:%s", cursor.sequence, kind, hex.EncodeToString(digest[:6])),
			Kind:      kind, Importance: importance, OccurredAt: occurredAt, GameTimeSeconds: gameTime,
			PreviousSummary: previousSummary, CurrentSummary: summary,
			ReasonCodes: reasons, RelatedLifecycleEventID: relatedID,
			Evidence: domain.CloneRecommendationEvidence(currentEvidence),
		})
	}

	if !cursor.state.Stale && current.Stale {
		appendEvent(domain.DecisionDataStale, domain.DecisionInformational,
			"Los datos de la partida están desactualizados; las opciones permanecen pausadas.", "data_stale", nil)
	}
	if cursor.state.RulesAvailable && !current.RulesAvailable {
		appendEvent(domain.DecisionRulesUnavailable, domain.DecisionInformational,
			"Las reglas externas no están disponibles; el estado de las opciones es limitado.", "rules_unavailable", nil)
	}
	for index := range lifecycle {
		if lifecycle[index].Kind == domain.EventConnectionRestored {
			appendEvent(domain.DecisionConnectionRestored, domain.DecisionInformational,
				"La conexión se restableció; las opciones vigentes vuelven a estar disponibles.", "connection_restored", &lifecycle[index])
		}
	}
	if !cursor.state.Stale && current.RulesAvailable && cursor.state.TargetID > 0 && current.TargetID > 0 && cursor.state.TargetID != current.TargetID {
		appendEvent(domain.DecisionTargetChanged, domain.DecisionImportant,
			fmt.Sprintf("Las opciones cambiaron; %s figura como opción principal.", neutralItemName(current.TargetName)),
			"stable_target_changed", nil)
	}
	if !cursor.state.Stale && cursor.state.ComponentID > 0 && current.ComponentID > 0 && cursor.state.ComponentID != current.ComponentID {
		if related := latestLifecycle(lifecycle, domain.EventInventoryChanged, domain.EventItemPurchased, domain.EventReturnedToBase); related != nil {
			appendEvent(domain.DecisionComponentChanged, domain.DecisionImportant,
				fmt.Sprintf("El componente sugerido cambió a %s después de actividad de inventario.", neutralItemName(current.ComponentName)),
				"component_changed_after_inventory", related)
		}
	}
	wasAffordable := cursor.state.Affordable
	if cursor.state.Stale && (cursor.state.TargetID != current.TargetID || cursor.state.ComponentID != current.ComponentID) {
		// Last-fresh affordability belongs only to that target/component pair.
		wasAffordable = false
	}
	if !current.Stale && !wasAffordable && current.Affordable && current.ComponentID > 0 {
		appendEvent(domain.DecisionComponentAffordable, domain.DecisionImportant,
			fmt.Sprintf("El componente sugerido %s ahora está dentro del oro disponible.", neutralItemName(current.ComponentName)),
			"component_affordable", nil)
	}
	if !cursor.state.Critical && current.Critical {
		appendEvent(domain.DecisionCriticalCounterActivated, domain.DecisionCritical,
			"Se activó una necesidad crítica; las opciones se actualizaron para reflejarla.", "critical_counter_activated", nil)
	}
	if !cursor.state.InventoryFull && current.InventoryFull {
		appendEvent(domain.DecisionInventoryFull, domain.DecisionImportant,
			"El inventario no tiene un espacio libre; las opciones permanecen visibles para comparar.", "inventory_full", nil)
	}
	if current.Actionable {
		for index := range lifecycle {
			if lifecycle[index].Kind == domain.EventReturnedToBase && lifecycle[index].Source == domain.EventSourceInferred {
				appendEvent(domain.DecisionReturnToBaseOptions, domain.DecisionImportant,
					fmt.Sprintf("Al volver a base, hay opciones actualizadas disponibles: %s", currentSummary),
					"return_to_base_options_available", &lifecycle[index])
			}
		}
	}

	if current.Stale {
		// Stale results pause advice; they contain no new economic observation.
		// Keep the last-fresh opportunity in the existing checkpoint fields so
		// reconnects (including after restart) cannot invent an affordability
		// crossing. Target/component alerts above still require a fresh prior
		// state, preserving their existing recovery semantics.
		current.TargetID, current.TargetName = cursor.state.TargetID, cursor.state.TargetName
		current.ComponentID, current.ComponentName = cursor.state.ComponentID, cursor.state.ComponentName
		current.Affordable = cursor.state.Affordable
	}
	cursor.state = current
	cursor.evidence = currentEvidence
	cursor.evidenceDigest = recommendationEvidenceDigest(currentEvidence)
	cursor.lifecycleSequence = max(cursor.lifecycleSequence, snapshot.Session.LastEventSequence)
	for _, event := range lifecycle {
		cursor.lifecycleSequence = max(cursor.lifecycleSequence, event.Sequence)
	}
	return cursor, events
}

func decisionStateFor(snapshot domain.Snapshot, result itemization.Result) decisionState {
	state := decisionState{
		Status: result.Status, RulesAvailable: structuredRuleEvidence(result),
		Affordable:    result.Status == itemization.StatusReady,
		InventoryFull: result.Status == itemization.StatusInventoryFull,
		Stale:         snapshot.Status == domain.StatusStale || result.Status == itemization.StatusStale,
	}
	if result.TargetItem != nil {
		state.TargetID = result.TargetItem.ID
		state.TargetName = result.TargetItem.Name
	}
	if result.NextComponent != nil {
		state.ComponentID = result.NextComponent.ID
		state.ComponentName = result.NextComponent.Name
	}
	for _, option := range result.RankedOptions {
		if option.Item.ID == state.TargetID {
			state.Critical = option.Critical
			break
		}
	}
	state.Actionable = !state.Stale && state.TargetID > 0 && state.ComponentID > 0 &&
		(result.Status == itemization.StatusReady || result.Status == itemization.StatusSaveForComponent)
	return state
}

func recommendationEvidence(result itemization.Result) *domain.RecommendationEvidence {
	evidence := &domain.RecommendationEvidence{
		SchemaVersion: domain.RecommendationEvidenceSchemaVersion,
		Status:        string(result.Status), Patch: strings.TrimSpace(result.Patch), Phase: result.Phase, Strategy: result.Strategy,
		Confidence: result.Confidence, TraitConfidence: result.TraitConfidence,
		Rules: domain.RuleProvenance{
			Version: strings.TrimSpace(result.Rules.Version), Patch: strings.TrimSpace(result.Rules.Patch),
			Source: strings.TrimSpace(result.Rules.Source), Fallback: result.Rules.Fallback, Legacy: result.Rules.Legacy,
		},
		ReasonCodes: sortedUniqueStrings(result.ReasonCodes),
	}
	if result.TargetItem != nil {
		evidence.Target = &domain.EvidenceItem{ID: result.TargetItem.ID, Name: strings.TrimSpace(result.TargetItem.Name)}
	}
	if result.NextComponent != nil {
		evidence.Component = &domain.EvidenceItem{ID: result.NextComponent.ID, Name: strings.TrimSpace(result.NextComponent.Name)}
	}
	for index, option := range result.RankedOptions {
		evidence.RankedChoices = append(evidence.RankedChoices, domain.EvidenceChoice{
			Rank: index + 1, Item: domain.EvidenceItem{ID: option.Item.ID, Name: strings.TrimSpace(option.Item.Name)},
			Score: option.Score, Category: strings.TrimSpace(option.Category),
			ReasonCodes: sortedUniqueStrings(option.ReasonCodes), Critical: option.Critical,
		})
	}
	if result.Profile != nil {
		evidence.Profile = &domain.ProfileProvenance{
			Level: string(result.Profile.Level), ProfileID: strings.TrimSpace(result.Profile.ProfileID),
			FamilyIDs: sortedUniqueStrings(result.Profile.FamilyIDs), RulesetVersion: strings.TrimSpace(result.Profile.RulesetVersion),
			Patch: strings.TrimSpace(result.Profile.Patch), ChampionID: strings.TrimSpace(result.Profile.ChampionID),
			Role: result.Profile.Role, ChampionTag: strings.TrimSpace(result.Profile.ChampionTag),
			Fallback: result.Profile.Fallback, ConfidenceCap: result.Profile.ConfidenceCap,
		}
	}
	if result.Personalization != nil {
		evidence.Personalization = &domain.PersonalizationProvenance{
			Version: result.Personalization.Version, Style: string(result.Personalization.Style),
			ReasonCodes: sortedUniqueStrings(result.Personalization.ReasonCodes), WarningCodes: sortedUniqueStrings(result.Personalization.WarningCodes),
		}
	}
	if result.ProfileStatus != nil && evidence.Rules.Source == "" {
		evidence.Rules.Version = strings.TrimSpace(result.ProfileStatus.Version)
		evidence.Rules.Patch = strings.TrimSpace(result.ProfileStatus.Patch)
		evidence.Rules.Source = strings.TrimSpace(result.ProfileStatus.Source)
		evidence.Rules.Fallback = result.ProfileStatus.Fallback
	}
	return evidence
}

func recommendationEvidenceDigest(evidence *domain.RecommendationEvidence) string {
	if evidence == nil {
		return ""
	}
	payload, _ := json.Marshal(evidence)
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func sortedUniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func structuredRuleEvidence(result itemization.Result) bool {
	if result.ProfileStatus == nil || result.Profile == nil {
		return externalRuleEvidence(result.Rules)
	}
	if !result.ProfileStatus.Ready || strings.TrimSpace(result.Profile.ProfileID) == "" {
		return false
	}
	switch result.ProfileStatus.Source {
	case itemprofiles.SourceExternalExact, itemprofiles.SourceExternalPatchFallback, itemprofiles.SourceExternalLastKnownGood:
		return true
	default:
		return false
	}
}

func externalRuleEvidence(status itemrules.Status) bool {
	if !status.Ready || status.Legacy {
		return false
	}
	switch status.Source {
	case itemrules.SourceExternalExact, itemrules.SourceExternalFallback, itemrules.SourceExternalLastKnownGood:
		return true
	default:
		return false
	}
}

func summarizeDecisionState(state decisionState) string {
	if state.Stale {
		return "Datos desactualizados; opciones pausadas."
	}
	if state.InventoryFull {
		return "Inventario sin espacio libre; opciones visibles para comparar."
	}
	if state.TargetID == 0 {
		return "No hay opciones accionables disponibles."
	}
	summary := fmt.Sprintf("Opción principal: %s", neutralItemName(state.TargetName))
	if state.ComponentID > 0 {
		summary += fmt.Sprintf("; componente sugerido: %s", neutralItemName(state.ComponentName))
	}
	if state.Affordable {
		summary += "; dentro del oro disponible."
	} else {
		summary += "; requiere más oro."
	}
	return summary
}

func neutralItemName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "opción sin nombre"
	}
	return name
}

func newLifecycleEvents(snapshot domain.Snapshot, after uint64) []domain.SemanticEvent {
	events := make([]domain.SemanticEvent, 0, len(snapshot.RecentEvents))
	for _, event := range snapshot.RecentEvents {
		if snapshot.Session != nil && event.SessionID == snapshot.Session.ID && event.Sequence > after {
			events = append(events, event)
		}
	}
	sort.SliceStable(events, func(left, right int) bool { return events[left].Sequence < events[right].Sequence })
	return events
}

func latestLifecycle(events []domain.SemanticEvent, kinds ...domain.EventKind) *domain.SemanticEvent {
	accepted := make(map[domain.EventKind]struct{}, len(kinds))
	for _, kind := range kinds {
		accepted[kind] = struct{}{}
	}
	for index := len(events) - 1; index >= 0; index-- {
		if _, exists := accepted[events[index].Kind]; exists {
			return &events[index]
		}
	}
	return nil
}

func currentGameTime(snapshot domain.Snapshot) float64 {
	if snapshot.Game == nil {
		return 0
	}
	return snapshot.Game.GameTimeSeconds
}

func appendUniqueReasons(base []string, values ...string) []string {
	seen := make(map[string]struct{}, len(base)+len(values))
	result := make([]string, 0, len(base)+len(values))
	for _, value := range append(base, values...) {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

type decisionEventStore struct {
	mu          sync.Mutex
	subscribers map[uint64]chan domain.DecisionEvent
	nextID      uint64
}

func newDecisionEventStore() *decisionEventStore {
	return &decisionEventStore{subscribers: make(map[uint64]chan domain.DecisionEvent)}
}

func (store *decisionEventStore) Subscribe(buffer int) (<-chan domain.DecisionEvent, func()) {
	if buffer < 1 {
		buffer = 1
	}
	store.mu.Lock()
	id := store.nextID
	store.nextID++
	updates := make(chan domain.DecisionEvent, buffer)
	store.subscribers[id] = updates
	store.mu.Unlock()
	var once sync.Once
	return updates, func() {
		once.Do(func() {
			store.mu.Lock()
			if channel, exists := store.subscribers[id]; exists {
				delete(store.subscribers, id)
				close(channel)
			}
			store.mu.Unlock()
		})
	}
}

func (store *decisionEventStore) Publish(events []domain.DecisionEvent) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, event := range events {
		for _, subscriber := range store.subscribers {
			offerLatestDecision(subscriber, cloneDecisionEvent(event))
		}
	}
}

func offerLatestDecision(channel chan domain.DecisionEvent, event domain.DecisionEvent) {
	select {
	case channel <- event:
		return
	default:
	}
	select {
	case <-channel:
	default:
	}
	select {
	case channel <- event:
	default:
	}
}

func cloneDecisionEvent(event domain.DecisionEvent) domain.DecisionEvent {
	event.ReasonCodes = append([]string(nil), event.ReasonCodes...)
	event.Evidence = domain.CloneRecommendationEvidence(event.Evidence)
	return event
}

func cloneDecisionEvents(events []domain.DecisionEvent) []domain.DecisionEvent {
	clones := make([]domain.DecisionEvent, len(events))
	for index, event := range events {
		clones[index] = cloneDecisionEvent(event)
	}
	return clones
}

func latestDecision(events []domain.DecisionEvent) *domain.DecisionEvent {
	if len(events) == 0 {
		return nil
	}
	clone := cloneDecisionEvent(events[len(events)-1])
	return &clone
}

func decisionCursorsEqual(left, right decisionCursor) bool {
	return left.sessionID == right.sessionID && left.sequence == right.sequence &&
		left.lifecycleSequence == right.lifecycleSequence && left.state == right.state &&
		left.evidenceDigest == right.evidenceDigest
}

func loadDecisionCursor(ctx context.Context, persistence DecisionPersistence, sessionID string) (decisionCursor, *domain.DecisionEvent, error) {
	checkpoint, err := persistence.LoadDecisionCheckpoint(ctx, sessionID)
	if err != nil {
		return decisionCursor{}, nil, err
	}
	cursor, err := decisionCursorFromCheckpoint(checkpoint)
	if err != nil {
		return decisionCursor{}, nil, err
	}
	if cursor.sequence == 0 {
		return cursor, nil, nil
	}
	events, err := persistence.ListDecisionEvents(ctx, sessionID, cursor.sequence-1, 1)
	if err != nil {
		return decisionCursor{}, nil, fmt.Errorf("load latest decision event: %w", err)
	}
	return cursor, latestDecision(events), nil
}

func isDecisionCheckpointNotFound(err error) bool {
	return errors.Is(err, catalog.ErrNotFound)
}
