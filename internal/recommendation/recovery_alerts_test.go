package recommendation

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"lolengine/internal/catalog"
	"lolengine/internal/domain"
	"lolengine/internal/itemization"
	"lolengine/internal/state"
	"lolengine/internal/storage"
	"lolengine/internal/tracker"
)

func TestCoordinatorRecoveryAlerts(t *testing.T) {
	for _, restart := range []bool{false, true} {
		for _, scenario := range []struct {
			name         string
			alreadyReady bool
			targetID     int
			componentID  int
			wantCrossing bool
		}{
			{name: "same affordable opportunity", alreadyReady: true, targetID: 4401, componentID: 1057},
			{name: "genuine crossing during outage", targetID: 4401, componentID: 1057, wantCrossing: true},
			{name: "different component", alreadyReady: true, targetID: 4401, componentID: 1033, wantCrossing: true},
			{name: "different target", alreadyReady: true, targetID: 2504, componentID: 1057, wantCrossing: true},
		} {
			t.Run(fmt.Sprintf("%s/restart=%t", scenario.name, restart), func(t *testing.T) {
				flow := newRecoveryAlertFlow(t)
				var want []domain.DecisionKind
				if scenario.alreadyReady {
					flow.fresh(900, 4401, 1057, itemization.StatusReady)
					want = append(want, domain.DecisionComponentAffordable)
				}
				flow.stale()
				want = append(want, domain.DecisionDataStale, domain.DecisionRulesUnavailable)
				before := flow.assertHistory(want, false)
				// Repeated stale recalculations and a restart must not replay alerts.
				flow.recalculate()
				if restart {
					flow.restart()
				}
				if got := flow.assertHistory(want, false); !reflect.DeepEqual(got, before) {
					t.Fatal("stale history changed without a new semantic transition")
				}

				flow.fresh(900, scenario.targetID, scenario.componentID, itemization.StatusReady)
				want = append(want, domain.DecisionConnectionRestored)
				if scenario.wantCrossing {
					want = append(want, domain.DecisionComponentAffordable)
				}
				after := flow.assertHistory(want, true)
				if !reflect.DeepEqual(after[:len(before)], before) {
					t.Fatal("recovery changed pre-outage durable events")
				}
				if after[len(before)].RelatedLifecycleEventID != "recovery-match:000003" {
					t.Fatalf("restoration link = %q", after[len(before)].RelatedLifecycleEventID)
				}
				flow.recalculate()
				flow.fresh(901, scenario.targetID, scenario.componentID, itemization.StatusReady)
				if got := flow.assertHistory(want, true); !reflect.DeepEqual(got, after) {
					t.Fatal("unchanged fresh opportunity replayed or rewrote history")
				}
			})
		}
	}
}

func TestCoordinatorDirectAffordabilityStillEmitsOnce(t *testing.T) {
	flow := newRecoveryAlertFlow(t)
	flow.fresh(900, 4401, 1057, itemization.StatusReady)
	want := []domain.DecisionKind{domain.DecisionComponentAffordable}
	before := flow.assertHistory(want, false)
	flow.recalculate()
	flow.fresh(901, 4401, 1057, itemization.StatusReady)
	if after := flow.assertHistory(want, false); !reflect.DeepEqual(before, after) {
		t.Fatal("direct affordability crossing replayed or rewrote history")
	}
}

// Use the public lifecycle detector, coordinator and durable history boundaries;
// no checkpoint bytes or database internals are inspected by these regressions.
type recoveryAlertFlow struct {
	t           *testing.T
	ctx         context.Context
	path        string
	now         time.Time
	database    *storage.DB
	gameStore   *state.Store
	detector    *tracker.Detector
	engine      *mutableDecisionEngine
	coordinator *Coordinator
	updates     <-chan domain.DecisionEvent
}

func newRecoveryAlertFlow(t *testing.T) *recoveryAlertFlow {
	t.Helper()
	flow := &recoveryAlertFlow{
		t: t, ctx: context.Background(), path: filepath.Join(t.TempDir(), "recovery.db"),
		now:       time.Date(2026, 9, 19, 8, 0, 0, 0, time.UTC),
		gameStore: state.NewStore(domain.Snapshot{Status: domain.StatusWaitingForGame}),
		engine:    &mutableDecisionEngine{},
	}
	flow.open()
	t.Cleanup(func() { _ = flow.database.Close() })
	flow.detector = tracker.NewDetector(tracker.DetectorConfig{
		Now: func() time.Time { return flow.now }, StaleAfter: 5 * time.Second, EndAfter: time.Minute,
		NewSessionID: func(string, time.Time) string { return "recovery-match" },
	})
	flow.fresh(500, 4401, 1057, itemization.StatusSaveForComponent)
	return flow
}

func (flow *recoveryAlertFlow) open() {
	flow.t.Helper()
	var err error
	flow.database, err = storage.Open(flow.ctx, flow.path)
	if err != nil {
		flow.t.Fatal(err)
	}
}

func (flow *recoveryAlertFlow) startCoordinator() {
	flow.t.Helper()
	var err error
	flow.coordinator, err = NewCoordinator(flow.gameStore,
		&fakeCatalogProvider{value: coordinatorCatalog(), status: catalog.Status{Ready: true, Patch: "16.18.1", Source: "cache"}},
		flow.engine, flow.database, Config{Now: func() time.Time { return flow.now }})
	if err != nil {
		flow.t.Fatal(err)
	}
	updates, unsubscribe := flow.coordinator.SubscribeDecisionEvents(10)
	flow.t.Cleanup(unsubscribe)
	flow.updates = updates
}

func (flow *recoveryAlertFlow) fresh(gold float64, target, component int, status itemization.Status) {
	flow.t.Helper()
	flow.now = flow.now.Add(time.Second)
	snapshot := decisionSnapshot("recovery-match", 100+flow.now.Sub(time.Date(2026, 9, 19, 8, 0, 0, 0, time.UTC)).Seconds(), gold)
	flow.publish(flow.detector.Observe(*snapshot.Game), domain.StatusInGame, snapshot.Game)
	result := decisionResult(target, "Target", component, "Component", status, false)
	if status == itemization.StatusReady {
		result.GoldNeeded = 0
	}
	flow.engine.set(result)
	if flow.coordinator == nil {
		flow.startCoordinator()
	}
	flow.recalculate()
}

func (flow *recoveryAlertFlow) stale() {
	flow.t.Helper()
	flow.now = flow.now.Add(6 * time.Second)
	flow.publish(flow.detector.ObserveFailure(domain.FetchTransport, "offline fixture"), domain.StatusStale, flow.gameStore.Current().Game)
	flow.engine.set(itemization.Result{Status: itemization.StatusStale})
	flow.recalculate()
}

func (flow *recoveryAlertFlow) publish(detection tracker.Detection, status domain.TrackerStatus, game *domain.GameState) {
	flow.t.Helper()
	if err := flow.database.SaveMatchUpdates(flow.ctx, detection.Sessions, detection.Events); err != nil {
		flow.t.Fatal(err)
	}
	flow.gameStore.Update(domain.Snapshot{
		Status: status, Game: game, UpdatedAt: flow.now, Session: detection.Session,
		Connection: detection.Connection, RecentEvents: flow.detector.RecentEvents(),
	})
}

func (flow *recoveryAlertFlow) recalculate() {
	flow.t.Helper()
	before, err := flow.database.ListDecisionEvents(flow.ctx, "recovery-match", 0, 100)
	if err != nil {
		flow.t.Fatal(err)
	}
	dashboard, err := flow.coordinator.Recalculate(flow.ctx)
	if err != nil || dashboard.DecisionEvents.Status != domain.DecisionEventStatusReady {
		flow.t.Fatalf("Recalculate = %+v, %v", dashboard.DecisionEvents, err)
	}
	after, err := flow.database.ListDecisionEvents(flow.ctx, "recovery-match", 0, 100)
	if err != nil {
		flow.t.Fatal(err)
	}
	for _, event := range after[len(before):] {
		select {
		case notification := <-flow.updates:
			if !reflect.DeepEqual(notification, event) {
				flow.t.Fatalf("notification differs from committed history: %+v / %+v", notification, event)
			}
		default:
			flow.t.Fatalf("missing notification for %s", event.ID)
		}
	}
	select {
	case event := <-flow.updates:
		flow.t.Fatalf("replayed notification: %+v", event)
	default:
	}
}

func (flow *recoveryAlertFlow) restart() {
	flow.t.Helper()
	if err := flow.database.Close(); err != nil {
		flow.t.Fatal(err)
	}
	flow.open()
	recovery, err := flow.database.LoadLifecycleRecovery(flow.ctx, flow.now.Add(-time.Minute), 50)
	if err != nil {
		flow.t.Fatal(err)
	}
	flow.detector = tracker.NewDetector(tracker.DetectorConfig{
		Now: func() time.Time { return flow.now }, StaleAfter: 5 * time.Second, EndAfter: time.Minute,
		InitialSession: recovery.Session, InitialConnection: recovery.Connection, InitialEvents: recovery.RecentEvents,
	})
	flow.startCoordinator()
	flow.recalculate()
}

func (flow *recoveryAlertFlow) assertHistory(want []domain.DecisionKind, restored bool) []domain.DecisionEvent {
	flow.t.Helper()
	events, err := flow.database.ListDecisionEvents(flow.ctx, "recovery-match", 0, 100)
	if err != nil {
		flow.t.Fatal(err)
	}
	if got := decisionKinds(events); !equalDecisionKinds(got, want) {
		flow.t.Fatalf("decision kinds = %v, want %v", got, want)
	}
	for index, event := range events {
		sequence := uint64(index + 1)
		if event.ID != fmt.Sprintf("recovery-match:decision:%06d", sequence) || event.Sequence != sequence || event.SessionID != "recovery-match" {
			flow.t.Fatalf("unstable decision identity/order: %+v", event)
		}
	}
	lifecycle, err := flow.database.ListMatchEvents(flow.ctx, "recovery-match", 0, 100)
	if err != nil {
		flow.t.Fatal(err)
	}
	wantLifecycle := []domain.EventKind{domain.EventMatchStarted}
	if flow.gameStore.Current().Status == domain.StatusStale || restored {
		wantLifecycle = append(wantLifecycle, domain.EventConnectionLost)
	}
	if restored {
		wantLifecycle = append(wantLifecycle, domain.EventConnectionRestored)
	}
	if len(lifecycle) != len(wantLifecycle) {
		flow.t.Fatalf("lifecycle count = %d, want %v: %+v", len(lifecycle), wantLifecycle, lifecycle)
	}
	for index, event := range lifecycle {
		sequence := uint64(index + 1)
		if event.Kind != wantLifecycle[index] || event.ID != fmt.Sprintf("recovery-match:%06d", sequence) || event.Sequence != sequence || event.SessionID != "recovery-match" {
			flow.t.Fatalf("unexpected lifecycle identity/kind/order: %+v", event)
		}
	}
	matches, err := flow.database.ListMatches(flow.ctx, 10, 0)
	if err != nil || len(matches) != 1 || matches[0].ID != "recovery-match" || matches[0].State != domain.MatchActive || matches[0].LastEventSequence != uint64(len(lifecycle)) {
		flow.t.Fatalf("session changed or duplicated: %+v, %v", matches, err)
	}
	return events
}
