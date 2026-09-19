package recommendation

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"lolengine/internal/catalog"
	"lolengine/internal/domain"
	"lolengine/internal/itemization"
	"lolengine/internal/itemrules"
	"lolengine/internal/state"
	"lolengine/internal/storage"
)

func TestCoordinatorAvoidsDuplicatePersistence(t *testing.T) {
	gameStore := state.NewStore(testGameSnapshot(1, 500))
	catalogs := &fakeCatalogProvider{value: coordinatorCatalog(), status: catalog.Status{Ready: true, Patch: "16.18.1", Source: "cache"}}
	persistence := &fakePersistence{}
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	coordinator, err := NewCoordinator(gameStore, catalogs, fakeEngine{}, persistence, Config{
		Now: func() time.Time {
			now = now.Add(time.Second)
			return now
		},
	})
	if err != nil {
		t.Fatalf("NewCoordinator() error = %v", err)
	}

	if _, err := coordinator.Recalculate(context.Background()); err != nil {
		t.Fatalf("Recalculate() error = %v", err)
	}
	if _, err := coordinator.Recalculate(context.Background()); err != nil {
		t.Fatalf("second Recalculate() error = %v", err)
	}
	if persistence.writes != 1 {
		t.Fatalf("persistence writes = %d, want 1", persistence.writes)
	}
}

func TestCoordinatorAutomaticallyPublishesGameChanges(t *testing.T) {
	gameStore := state.NewStore(testGameSnapshot(1, 500))
	catalogs := &fakeCatalogProvider{value: coordinatorCatalog(), status: catalog.Status{Ready: true, Patch: "16.18.1", Source: "remote"}}
	coordinator, err := NewCoordinator(gameStore, catalogs, fakeEngine{}, nil, Config{})
	if err != nil {
		t.Fatalf("NewCoordinator() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go coordinator.Run(ctx)
	_, updates, unsubscribe := coordinator.Subscribe(2)
	defer unsubscribe()

	next := testGameSnapshot(2, 900)
	next.Game.GameVersion = "16.18.99.1"
	gameStore.Update(next)

	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		select {
		case dashboard := <-updates:
			// Run performs an initial recalculation, which may be delivered before the
			// game update when the scheduler is slower under the race detector.
			if dashboard.Game.Game != nil && dashboard.Game.Game.ActivePlayer.CurrentGold == 900 {
				goto updated
			}
		case <-deadline.C:
			t.Fatal("coordinator did not publish a game change")
		}
	}

updated:
	if catalogs.ensureCallCount() == 0 {
		t.Fatal("live game version did not trigger catalog patch selection")
	}
}

func TestCoordinatorSkipsTelemetryChurnButHonorsSemanticAndAffordabilityTriggers(t *testing.T) {
	now := time.Date(2026, 9, 19, 2, 0, 0, 0, time.UTC)
	initial := testGameSnapshot(1, 500)
	initial.DecisionRevision = 1
	initial.Game.GameTimeSeconds = 100
	gameStore := state.NewStore(initial)
	engine := &countingEngine{}
	catalogs := &fakeCatalogProvider{value: coordinatorCatalog(), status: catalog.Status{Ready: true, Patch: "16.18.1", Source: "cache"}}
	coordinator, err := NewCoordinator(gameStore, catalogs, engine, nil, Config{
		Now: func() time.Time { return now }, RefreshInterval: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewCoordinator() error = %v", err)
	}
	if engine.callCount() != 1 {
		t.Fatalf("initial engine calls = %d", engine.callCount())
	}

	tick := gameStore.Current()
	tick.Game.GameTimeSeconds = 102
	tick.Game.ActivePlayer.CurrentGold = 505
	gameStore.Update(tick)
	if _, err := coordinator.handleGameUpdate(context.Background(), gameStore.Current()); err != nil {
		t.Fatal(err)
	}
	if engine.callCount() != 1 {
		t.Fatalf("time/passive-gold tick recalculated %d times", engine.callCount())
	}
	if coordinator.Current().Game.Game.GameTimeSeconds != 102 {
		t.Fatal("dashboard did not receive raw telemetry tick")
	}

	affordable := gameStore.Current()
	affordable.Game.GameTimeSeconds = 104
	affordable.Game.ActivePlayer.CurrentGold = 650
	gameStore.Update(affordable)
	if _, err := coordinator.handleGameUpdate(context.Background(), gameStore.Current()); err != nil {
		t.Fatal(err)
	}
	if engine.callCount() != 2 {
		t.Fatalf("affordability crossing calls = %d, want 2", engine.callCount())
	}

	semantic := gameStore.Current()
	semantic.Game.GameTimeSeconds = 106
	semantic.DecisionRevision++
	gameStore.Update(semantic)
	if _, err := coordinator.handleGameUpdate(context.Background(), gameStore.Current()); err != nil {
		t.Fatal(err)
	}
	if engine.callCount() != 3 {
		t.Fatalf("semantic change calls = %d, want 3", engine.callCount())
	}

	engine.bumpRulesRevision()
	rulesChanged := gameStore.Current()
	rulesChanged.Game.GameTimeSeconds = 107
	gameStore.Update(rulesChanged)
	if _, err := coordinator.handleGameUpdate(context.Background(), gameStore.Current()); err != nil {
		t.Fatal(err)
	}
	if engine.callCount() != 4 {
		t.Fatalf("rules revision calls = %d, want 4", engine.callCount())
	}

	now = now.Add(31 * time.Second)
	periodic := gameStore.Current()
	periodic.Game.GameTimeSeconds = 137
	periodic.Game.ActivePlayer.CurrentGold++
	gameStore.Update(periodic)
	if _, err := coordinator.handleGameUpdate(context.Background(), gameStore.Current()); err != nil {
		t.Fatal(err)
	}
	if engine.callCount() != 5 {
		t.Fatalf("periodic refresh calls = %d, want 5", engine.callCount())
	}
}

func TestCoordinatorCoalescesConcurrentStaleInputs(t *testing.T) {
	gameStore := state.NewStore(testGameSnapshot(1, 500))
	catalogs := &fakeCatalogProvider{value: coordinatorCatalog(), status: catalog.Status{Ready: true, Patch: "16.18.1", Source: "cache"}}
	coordinator, err := NewCoordinator(gameStore, catalogs, fakeEngine{}, nil, Config{})
	if err != nil {
		t.Fatalf("NewCoordinator() error = %v", err)
	}

	stale := gameStore.Current()
	next := testGameSnapshot(99, 1300)
	gameStore.Update(next)
	newest := gameStore.Current()
	inputs := []*domain.Snapshot{&stale, &newest, &stale, nil}
	start := make(chan struct{})
	results := make(chan DashboardSnapshot, len(inputs))
	var wait sync.WaitGroup
	for _, input := range inputs {
		input := input
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			dashboard, recalculateErr := coordinator.recalculate(context.Background(), input)
			if recalculateErr != nil {
				t.Errorf("recalculate() error = %v", recalculateErr)
				return
			}
			results <- dashboard
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	for dashboard := range results {
		if dashboard.Game.Revision != newest.Revision || dashboard.Game.Game == nil || dashboard.Game.Game.ActivePlayer.CurrentGold != 1300 {
			t.Fatalf("stale dashboard published = %#v", dashboard.Game)
		}
	}
	if current := coordinator.Current(); current.Game.Revision != newest.Revision {
		t.Fatalf("final game revision = %d, want %d", current.Game.Revision, newest.Revision)
	}
}

func TestCoordinatorRetriesScoringWhenProviderRevisionMutatesMidCalculation(t *testing.T) {
	gameStore := state.NewStore(testGameSnapshot(1, 500))
	catalogs := &fakeCatalogProvider{value: coordinatorCatalog(), status: catalog.Status{Ready: true, Patch: "16.18.1", Source: "cache"}}
	engine := newBlockingRevisionEngine()
	coordinator, err := NewCoordinator(gameStore, catalogs, engine, nil, Config{})
	if err != nil {
		t.Fatal(err)
	}
	engine.arm()
	result := make(chan DashboardSnapshot, 1)
	errors := make(chan error, 1)
	go func() {
		dashboard, recalculateErr := coordinator.Recalculate(context.Background())
		result <- dashboard
		errors <- recalculateErr
	}()
	<-engine.started
	engine.bump()
	close(engine.release)
	if err := <-errors; err != nil {
		t.Fatal(err)
	}
	dashboard := <-result
	if dashboard.Recommendation.TargetItem == nil || dashboard.Recommendation.TargetItem.ID != 8002 {
		t.Fatalf("old scoring snapshot was committed under new revision: %#v", dashboard.Recommendation)
	}
	if got := engine.callCount(); got != 3 { // one constructor call, one discarded attempt, one stable retry
		t.Fatalf("engine calls = %d, want 3", got)
	}
	if coordinator.rulesRevision != 1 {
		t.Fatalf("committed provider revision = %d, want 1", coordinator.rulesRevision)
	}
}

func TestFeedbackEligibilityRequiresLatestPersistedEvidenceToMatchCurrentContext(t *testing.T) {
	game := testGameSnapshot(1, 500)
	game.Session = &domain.MatchSession{ID: "match-current", State: domain.MatchActive}
	game.Game.ActivePlayer.Position = "TOP"
	recommendationResult := revisionResult(time.Unix(100, 0), game, 1)
	evidence := recommendationEvidence(recommendationResult)
	event := &domain.DecisionEvent{
		ID: "match-current:decision:000001", SessionID: "match-current", Sequence: 1,
		Kind: domain.DecisionTargetChanged, Importance: domain.DecisionImportant, Evidence: evidence,
	}
	coordinator := &Coordinator{dashboard: NewStore(DashboardSnapshot{
		Epoch: "eligibility-test", Game: game, Recommendation: recommendationResult,
		DecisionEvents: domain.DecisionEventAvailability{Available: true, Status: domain.DecisionEventStatusReady, Latest: event},
	})}
	eligible, ready := coordinator.CurrentFeedbackEligibility()
	if !ready || eligible.EventID != event.ID || eligible.SessionID != game.Session.ID || len(eligible.VisibleItemIDs) != len(recommendationResult.RankedOptions) {
		t.Fatalf("matching eligibility = %+v, %v", eligible, ready)
	}

	contextChanged := coordinator.dashboard.Current()
	contextChanged.Recommendation.Phase = domain.PhaseLate
	coordinator.dashboard = NewStore(contextChanged)
	if _, ready := coordinator.CurrentFeedbackEligibility(); ready {
		t.Fatal("context-only change retained controls backed by stale persisted evidence")
	}

	statusError := contextChanged
	statusError.Recommendation = recommendationResult
	statusError.DecisionEvents.Status = domain.DecisionEventStatusError
	coordinator.dashboard = NewStore(statusError)
	if _, ready := coordinator.CurrentFeedbackEligibility(); ready {
		t.Fatal("decision persistence error remained feedback eligible")
	}
}

func TestCoordinatorPreferenceRevisionResetsStabilizerBeforeApplyingNewRanking(t *testing.T) {
	game := testGameSnapshot(1, 500)
	game.Session = &domain.MatchSession{ID: "preference-match", State: domain.MatchActive}
	game.Game.GameTimeSeconds = 500
	gameStore := state.NewStore(game)
	catalogs := &fakeCatalogProvider{value: coordinatorCatalog(), status: catalog.Status{Ready: true, Patch: "16.18.1", Source: "cache"}}
	engine := &preferenceRevisionEngine{}
	coordinator, err := NewCoordinator(gameStore, catalogs, engine, nil, Config{Now: func() time.Time { return time.Date(2026, 9, 19, 15, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	if got := coordinator.Current().Recommendation.TargetItem; got == nil || got.ID != 8001 {
		t.Fatalf("initial target = %#v", got)
	}
	engine.bump()
	dashboard, err := coordinator.Recalculate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if dashboard.Recommendation.TargetItem == nil || dashboard.Recommendation.TargetItem.ID != 8002 {
		t.Fatalf("preference revision stayed behind hysteresis: %#v", dashboard.Recommendation)
	}
}

func TestCoordinatorReportsUnpersistedRemoteCatalogAccurately(t *testing.T) {
	game := testGameSnapshot(1, 500)
	game.Game.GameVersion = "16.18.99.1"
	gameStore := state.NewStore(game)
	catalogs := &fakeCatalogProvider{
		value:     coordinatorCatalog(),
		status:    catalog.Status{Ready: true, Patch: "16.18.1", Source: "remote_unpersisted"},
		ensureErr: errors.New("write SQLite catalog: disk full"),
	}
	coordinator, err := NewCoordinator(gameStore, catalogs, fakeEngine{}, nil, Config{})
	if err != nil {
		t.Fatalf("NewCoordinator() error = %v", err)
	}

	dashboard, err := coordinator.Recalculate(context.Background())
	if err != nil {
		t.Fatalf("Recalculate() error = %v", err)
	}
	warnings := strings.Join(dashboard.Recommendation.Warnings, "\n")
	if !strings.Contains(warnings, "catálogo remoto actualizado está activo") || !strings.Contains(warnings, "no pudo guardarse localmente") {
		t.Fatalf("warnings = %q, want active remote persistence warning", warnings)
	}
	if strings.Contains(warnings, "se usa el catálogo en caché") || strings.Contains(warnings, "guardado en disco") {
		t.Fatalf("warnings = %q, must not claim an active remote catalog came from cache", warnings)
	}
}

func TestCatalogEnsureWarningMatchesActiveSource(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{name: "unpersisted remote", source: "remote_unpersisted", want: "remoto actualizado está activo"},
		{name: "memory cache", source: "memory_cache", want: "validado en memoria"},
		{name: "disk cache", source: "cache", want: "guardado en disco"},
		{name: "unknown ready source", source: "future_source", want: "catálogo validado disponible"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if warning := catalogEnsureWarning(test.source); !strings.Contains(warning, test.want) {
				t.Fatalf("catalogEnsureWarning(%q) = %q, want substring %q", test.source, warning, test.want)
			}
		})
	}
}

type fakeEngine struct{}

func (fakeEngine) Recommend(now time.Time, snapshot domain.Snapshot, knowledge catalog.Catalog) itemization.Result {
	return itemization.Result{
		Status: itemization.StatusReady, GeneratedAt: now, SourceGameRevision: snapshot.Revision,
		Patch: knowledge.Patch, Reason: "stable recommendation", Confidence: 0.8,
	}
}

type countingEngine struct {
	mu            sync.Mutex
	calls         int
	rulesRevision uint64
}

type preferenceRevisionEngine struct {
	mu       sync.Mutex
	revision uint64
}

type blockingRevisionEngine struct {
	mu        sync.Mutex
	revision  uint64
	calls     int
	blockNext bool
	started   chan struct{}
	release   chan struct{}
}

func newBlockingRevisionEngine() *blockingRevisionEngine {
	return &blockingRevisionEngine{started: make(chan struct{}), release: make(chan struct{})}
}

func (engine *blockingRevisionEngine) arm() {
	engine.mu.Lock()
	engine.blockNext = true
	engine.mu.Unlock()
}

func (engine *blockingRevisionEngine) bump() {
	engine.mu.Lock()
	engine.revision++
	engine.mu.Unlock()
}

func (engine *blockingRevisionEngine) RulesRevision() uint64 {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return engine.revision
}

func (engine *blockingRevisionEngine) callCount() int {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return engine.calls
}

func (engine *blockingRevisionEngine) Recommend(now time.Time, snapshot domain.Snapshot, _ catalog.Catalog) itemization.Result {
	engine.mu.Lock()
	engine.calls++
	revision := engine.revision
	block := engine.blockNext
	if block {
		engine.blockNext = false
	}
	engine.mu.Unlock()
	if block {
		close(engine.started)
		<-engine.release
	}
	return revisionResult(now, snapshot, revision)
}

func revisionResult(now time.Time, snapshot domain.Snapshot, revision uint64) itemization.Result {
	targetID := 8001
	if revision > 0 {
		targetID = 8002
	}
	result := itemization.Result{
		Status: itemization.StatusReady, GeneratedAt: now, SourceGameRevision: snapshot.Revision, Patch: "16.18.1",
		Phase: domain.PhaseMid, Strategy: domain.StrategyTeamfight,
		Rules:            itemrules.Status{Ready: true, Version: "rules-v1", Patch: "16.18.1", Source: itemrules.SourceExternalExact},
		AlternativeLimit: 2, Stability: itemrules.StabilityPolicy{}, DecisionPlans: map[int]itemization.DecisionPlan{},
	}
	for _, id := range []int{8001, 8002, 8003} {
		result.DecisionPlans[id] = itemization.DecisionPlan{
			Ranked: itemization.RankedItem{Item: itemization.ItemRef{ID: id, Name: "Option"}, Score: 9000 - id, Category: itemrules.CategoryCore},
			Status: itemization.StatusReady, NextComponent: &itemization.ItemRef{ID: id + 100, Name: "Component", Cost: 300},
		}
	}
	result.DecisionOrder = []int{targetID, 16003 - targetID, 8003}
	itemization.SelectTarget(&result, targetID)
	return result
}

func (engine *preferenceRevisionEngine) RulesRevision() uint64 {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return engine.revision
}
func (engine *preferenceRevisionEngine) bump() {
	engine.mu.Lock()
	engine.revision++
	engine.mu.Unlock()
}
func (engine *preferenceRevisionEngine) Recommend(now time.Time, snapshot domain.Snapshot, _ catalog.Catalog) itemization.Result {
	engine.mu.Lock()
	revision := engine.revision
	engine.mu.Unlock()
	rawTarget := 8001
	if revision > 0 {
		rawTarget = 8002
	}
	result := itemization.Result{
		Status: itemization.StatusReady, GeneratedAt: now, SourceGameRevision: snapshot.Revision,
		Rules:            itemrules.Status{Ready: true, Version: "rules", Patch: "16.18.1", Source: itemrules.SourceExternalExact},
		Stability:        itemrules.StabilityPolicy{SwitchMargin: 1000, ConfirmationSeconds: 1000, MinimumHoldSeconds: 1000},
		AlternativeLimit: 2, DecisionOrder: []int{rawTarget}, DecisionPlans: map[int]itemization.DecisionPlan{},
	}
	for _, id := range []int{8001, 8002} {
		result.DecisionPlans[id] = itemization.DecisionPlan{Ranked: itemization.RankedItem{Item: itemization.ItemRef{ID: id, Name: "Option"}, Score: map[int]int{8001: 100, 8002: 200}[id]}, Status: itemization.StatusReady}
	}
	result.DecisionOrder = []int{rawTarget, 16003 - rawTarget}
	itemization.SelectTarget(&result, rawTarget)
	return result
}

func (engine *countingEngine) RulesRevision() uint64 {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return engine.rulesRevision
}

func (engine *countingEngine) bumpRulesRevision() {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.rulesRevision++
}

func (engine *countingEngine) Recommend(now time.Time, snapshot domain.Snapshot, knowledge catalog.Catalog) itemization.Result {
	engine.mu.Lock()
	engine.calls++
	engine.mu.Unlock()
	return itemization.Result{
		Status: itemization.StatusSaveForComponent, GeneratedAt: now, SourceGameRevision: snapshot.Revision,
		Patch: knowledge.Patch, NextComponent: &itemization.ItemRef{ID: 1028, Name: "Ruby Crystal", Cost: 600}, GoldNeeded: 100,
	}
}

func (engine *countingEngine) callCount() int {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return engine.calls
}

type fakeCatalogProvider struct {
	mu          sync.Mutex
	value       catalog.Catalog
	status      catalog.Status
	ensureErr   error
	ensureCalls int
}

func (provider *fakeCatalogProvider) Current() (catalog.Catalog, catalog.Status, bool) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return catalog.Clone(provider.value), provider.status, provider.status.Ready
}

func (provider *fakeCatalogProvider) EnsureForGame(context.Context, string) error {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.ensureCalls++
	return provider.ensureErr
}

func (provider *fakeCatalogProvider) ensureCallCount() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.ensureCalls
}

type fakePersistence struct {
	writes int
}

func (persistence *fakePersistence) SaveRecommendation(_ context.Context, _ storage.RecommendationRecord) (bool, error) {
	persistence.writes++
	return true, nil
}

func testGameSnapshot(revision uint64, gold float64) domain.Snapshot {
	game := domain.GameState{
		GameMode: "CLASSIC", MapNumber: 11,
		ActivePlayer: domain.ActivePlayer{ChampionName: "Garen", Team: "ORDER", CurrentGold: gold},
	}
	return domain.Snapshot{Status: domain.StatusInGame, Game: &game, Revision: revision, UpdatedAt: time.Now().UTC()}
}

func coordinatorCatalog() catalog.Catalog {
	return catalog.Catalog{
		Patch: "16.18.1", Locale: "es_MX",
		Items:     map[int]catalog.Item{1001: {ID: 1001, Name: "Boots"}},
		Champions: map[string]catalog.Champion{"Garen": {ID: "Garen", Key: "86", Name: "Garen"}},
	}
}
