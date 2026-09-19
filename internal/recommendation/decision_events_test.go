package recommendation

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"lolengine/internal/catalog"
	"lolengine/internal/domain"
	"lolengine/internal/itemization"
	"lolengine/internal/itemprofiles"
	"lolengine/internal/itemrules"
	"lolengine/internal/state"
	"lolengine/internal/storage"
)

func TestDecisionCursorEmitsOnlySemanticTransitions(t *testing.T) {
	baseTime := time.Date(2026, 9, 19, 8, 0, 0, 0, time.UTC)
	baselineSnapshot := decisionSnapshot("decision-match", 100, 500)
	baselineResult := decisionResult(4401, "Force of Nature", 1057, "Negatron Cloak", itemization.StatusSaveForComponent, false)

	tests := []struct {
		name          string
		mutate        func(*domain.Snapshot, *itemization.Result)
		lifecycle     *domain.SemanticEvent
		wantKinds     []domain.DecisionKind
		wantRelatedID string
	}{
		{
			name: "stable target change",
			mutate: func(_ *domain.Snapshot, result *itemization.Result) {
				result.TargetItem = &itemization.ItemRef{ID: 2504, Name: "Kaenic Rookern", Cost: 2900}
				result.RankedOptions[0].Item = *result.TargetItem
			},
			wantKinds: []domain.DecisionKind{domain.DecisionTargetChanged},
		},
		{
			name: "component change after inventory activity",
			mutate: func(snapshot *domain.Snapshot, result *itemization.Result) {
				event := decisionLifecycle(snapshot.Session.ID, 1, domain.EventInventoryChanged, baseTime.Add(time.Second))
				snapshot.RecentEvents = []domain.SemanticEvent{event}
				snapshot.Session.LastEventSequence = 1
				result.NextComponent = &itemization.ItemRef{ID: 1033, Name: "Null-Magic Mantle", Cost: 400}
			},
			wantKinds:     []domain.DecisionKind{domain.DecisionComponentChanged},
			wantRelatedID: "decision-match:000001",
		},
		{
			name: "component change after inferred purchase",
			mutate: func(snapshot *domain.Snapshot, result *itemization.Result) {
				event := decisionLifecycle(snapshot.Session.ID, 1, domain.EventItemPurchased, baseTime.Add(time.Second))
				event.Source = domain.EventSourceInferred
				snapshot.RecentEvents = []domain.SemanticEvent{event}
				snapshot.Session.LastEventSequence = 1
				result.NextComponent = &itemization.ItemRef{ID: 1033, Name: "Null-Magic Mantle", Cost: 400}
			},
			wantKinds:     []domain.DecisionKind{domain.DecisionComponentChanged},
			wantRelatedID: "decision-match:000001",
		},
		{
			name: "component change after inferred base activity",
			mutate: func(snapshot *domain.Snapshot, result *itemization.Result) {
				event := decisionLifecycle(snapshot.Session.ID, 1, domain.EventReturnedToBase, baseTime.Add(time.Second))
				event.Source = domain.EventSourceInferred
				snapshot.RecentEvents = []domain.SemanticEvent{event}
				snapshot.Session.LastEventSequence = 1
				result.NextComponent = &itemization.ItemRef{ID: 1033, Name: "Null-Magic Mantle", Cost: 400}
				result.Status = itemization.StatusComplete // suppress the distinct actionable base-options event
			},
			wantKinds:     []domain.DecisionKind{domain.DecisionComponentChanged},
			wantRelatedID: "decision-match:000001",
		},
		{
			name: "component becomes affordable once",
			mutate: func(snapshot *domain.Snapshot, result *itemization.Result) {
				snapshot.Game.ActivePlayer.CurrentGold = 900
				result.Status = itemization.StatusReady
				result.GoldNeeded = 0
			},
			wantKinds: []domain.DecisionKind{domain.DecisionComponentAffordable},
		},
		{
			name: "critical counter activates",
			mutate: func(_ *domain.Snapshot, result *itemization.Result) {
				result.RankedOptions[0].Critical = true
				result.ReasonCodes = []string{"counter_healing"}
			},
			wantKinds: []domain.DecisionKind{domain.DecisionCriticalCounterActivated},
		},
		{
			name: "inventory becomes full",
			mutate: func(_ *domain.Snapshot, result *itemization.Result) {
				result.Status = itemization.StatusInventoryFull
				result.InventorySlotsUsed = 6
			},
			wantKinds: []domain.DecisionKind{domain.DecisionInventoryFull},
		},
		{
			name: "data becomes stale",
			mutate: func(snapshot *domain.Snapshot, result *itemization.Result) {
				snapshot.Status = domain.StatusStale
				result.Status = itemization.StatusStale
				result.TargetItem = nil
				result.NextComponent = nil
			},
			wantKinds: []domain.DecisionKind{domain.DecisionDataStale},
		},
		{
			name: "legacy target churn emits only rules unavailable",
			mutate: func(_ *domain.Snapshot, result *itemization.Result) {
				result.Rules = itemrules.Status{Legacy: true, Fallback: true, Source: itemrules.SourceLegacyBuiltin}
				result.TargetItem = &itemization.ItemRef{ID: 2504, Name: "Kaenic Rookern", Cost: 2900}
				result.RankedOptions[0].Item = *result.TargetItem
			},
			wantKinds: []domain.DecisionKind{domain.DecisionRulesUnavailable},
		},
		{
			name: "connection restored links lifecycle event",
			mutate: func(snapshot *domain.Snapshot, _ *itemization.Result) {
				event := decisionLifecycle(snapshot.Session.ID, 1, domain.EventConnectionRestored, baseTime.Add(time.Second))
				snapshot.RecentEvents = []domain.SemanticEvent{event}
				snapshot.Session.LastEventSequence = 1
			},
			wantKinds:     []domain.DecisionKind{domain.DecisionConnectionRestored},
			wantRelatedID: "decision-match:000001",
		},
		{
			name: "inferred return to base exposes choice update",
			mutate: func(snapshot *domain.Snapshot, _ *itemization.Result) {
				event := decisionLifecycle(snapshot.Session.ID, 1, domain.EventReturnedToBase, baseTime.Add(time.Second))
				event.Source = domain.EventSourceInferred
				snapshot.RecentEvents = []domain.SemanticEvent{event}
				snapshot.Session.LastEventSequence = 1
			},
			wantKinds:     []domain.DecisionKind{domain.DecisionReturnToBaseOptions},
			wantRelatedID: "decision-match:000001",
		},
		{
			name: "component change without inventory activity is suppressed",
			mutate: func(_ *domain.Snapshot, result *itemization.Result) {
				result.NextComponent = &itemization.ItemRef{ID: 1033, Name: "Null-Magic Mantle", Cost: 400}
			},
		},
		{
			name: "observed return to base is suppressed",
			mutate: func(snapshot *domain.Snapshot, _ *itemization.Result) {
				event := decisionLifecycle(snapshot.Session.ID, 1, domain.EventReturnedToBase, baseTime.Add(time.Second))
				snapshot.RecentEvents = []domain.SemanticEvent{event}
				snapshot.Session.LastEventSequence = 1
			},
		},
		{
			name: "inferred return without actionable choices is suppressed",
			mutate: func(snapshot *domain.Snapshot, result *itemization.Result) {
				event := decisionLifecycle(snapshot.Session.ID, 1, domain.EventReturnedToBase, baseTime.Add(time.Second))
				event.Source = domain.EventSourceInferred
				snapshot.RecentEvents = []domain.SemanticEvent{event}
				snapshot.Session.LastEventSequence = 1
				result.Status = itemization.StatusComplete
				result.NextComponent = nil
			},
		},
		{
			name: "connection loss itself is suppressed",
			mutate: func(snapshot *domain.Snapshot, _ *itemization.Result) {
				event := decisionLifecycle(snapshot.Session.ID, 1, domain.EventConnectionLost, baseTime.Add(time.Second))
				snapshot.RecentEvents = []domain.SemanticEvent{event}
				snapshot.Session.LastEventSequence = 1
			},
		},
		{
			name: "raw score and alternative reorder are suppressed",
			mutate: func(_ *domain.Snapshot, result *itemization.Result) {
				result.RankedOptions[0].Score += 500
				result.Alternatives = []itemization.RankedItem{
					{Item: itemization.ItemRef{ID: 3111, Name: "Mercury's Treads", Cost: 1250}, Score: 900},
					{Item: itemization.ItemRef{ID: 3047, Name: "Plated Steelcaps", Cost: 1200}, Score: 800},
				}
			},
		},
		{
			name: "time revision and passive gold below threshold are suppressed",
			mutate: func(snapshot *domain.Snapshot, result *itemization.Result) {
				snapshot.Revision++
				snapshot.UpdatedAt = snapshot.UpdatedAt.Add(time.Minute)
				snapshot.Game.GameTimeSeconds += 60
				snapshot.Game.ActivePlayer.CurrentGold += 25
				result.GeneratedAt = result.GeneratedAt.Add(time.Minute)
				result.SourceGameRevision++
				result.GoldNeeded -= 25
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cursor := newDecisionCursor(baselineSnapshot, baselineResult)
			nextSnapshot := cloneDecisionSnapshot(baselineSnapshot)
			nextSnapshot.UpdatedAt = baseTime.Add(time.Second)
			nextResult := baselineResult
			nextResult.RankedOptions = append([]itemization.RankedItem(nil), baselineResult.RankedOptions...)
			test.mutate(&nextSnapshot, &nextResult)

			_, events := cursor.advance(baseTime.Add(time.Second), nextSnapshot, nextResult)
			if got := decisionKinds(events); !equalDecisionKinds(got, test.wantKinds) {
				t.Fatalf("decision kinds = %v, want %v; events=%#v", got, test.wantKinds, events)
			}
			for _, event := range events {
				if strings.HasPrefix(strings.TrimSpace(event.CurrentSummary), "Compra ") {
					t.Fatalf("imperative summary = %q", event.CurrentSummary)
				}
			}
			if test.wantRelatedID != "" && (len(events) != 1 || events[0].RelatedLifecycleEventID != test.wantRelatedID) {
				t.Fatalf("related lifecycle ID = %#v, want %q", events, test.wantRelatedID)
			}
			if len(events) > 0 && events[0].Sequence != 1 {
				t.Fatalf("first decision sequence = %d", events[0].Sequence)
			}
		})
	}
}

func TestDecisionEvidenceIsStructuredVersionedAndIgnoresVolatileFields(t *testing.T) {
	now := time.Date(2026, 9, 19, 9, 30, 0, 0, time.UTC)
	snapshot := decisionSnapshot("evidence-match", 100, 500)
	result := decisionResult(4401, "Force of Nature", 1057, "Negatron Cloak", itemization.StatusSaveForComponent, false)
	result.GeneratedAt = now
	result.SourceGameRevision = 10
	result.Confidence = .83
	result.TraitConfidence = .72
	result.Patch = "16.18.1"
	result.Phase = domain.PhaseMid
	result.Strategy = domain.StrategyBalanced
	result.Rules = itemrules.Status{Ready: true, Version: "garen-v2", Patch: "16.18", Source: itemrules.SourceExternalExact}
	result.ReasonCodes = []string{"need_magic_resist", "counter_burst"}

	cursor := newDecisionCursor(snapshot, result)
	checkpoint, err := cursor.checkpoint(now)
	if err != nil {
		t.Fatalf("checkpoint() error = %v", err)
	}
	if checkpoint.Evidence == nil || checkpoint.Evidence.SchemaVersion != domain.RecommendationEvidenceSchemaVersion {
		t.Fatalf("checkpoint evidence = %+v", checkpoint.Evidence)
	}
	if checkpoint.Evidence.Target == nil || checkpoint.Evidence.Target.ID != 4401 || checkpoint.Evidence.Component == nil || checkpoint.Evidence.Component.ID != 1057 {
		t.Fatalf("checkpoint items = %+v", checkpoint.Evidence)
	}
	if checkpoint.Evidence.Rules.Version != "garen-v2" || checkpoint.Evidence.Confidence != .83 || len(checkpoint.Evidence.RankedChoices) != 1 {
		t.Fatalf("checkpoint provenance = %+v", checkpoint.Evidence)
	}

	volatile := result
	volatile.GeneratedAt = now.Add(time.Minute)
	volatile.SourceGameRevision = 999
	volatile.Warnings = []string{"transient warning"}
	volatile.Sources = []string{"volatile source display"}
	next, events := cursor.advance(now.Add(time.Minute), snapshot, volatile)
	if len(events) != 0 || !decisionCursorsEqual(next, cursor) {
		t.Fatalf("volatile-only result wrote decision state: events=%+v next=%+v cursor=%+v", events, next, cursor)
	}

	changed := result
	changed.TargetItem = &itemization.ItemRef{ID: 2504, Name: "Kaenic Rookern"}
	changed.RankedOptions = append([]itemization.RankedItem(nil), result.RankedOptions...)
	changed.RankedOptions[0].Item = *changed.TargetItem
	next, events = cursor.advance(now.Add(2*time.Minute), snapshot, changed)
	if len(events) != 1 || events[0].Evidence == nil || events[0].Evidence.Target.ID != 2504 {
		t.Fatalf("changed evidence events = %+v", events)
	}
	if next.evidence == nil || next.evidence.Target.ID != 2504 {
		t.Fatalf("cursor evidence = %+v", next.evidence)
	}
}

func TestDecisionCursorSuppressesTargetChurnWithoutExternalRuleEvidence(t *testing.T) {
	now := time.Date(2026, 9, 19, 8, 5, 0, 0, time.UTC)
	snapshot := decisionSnapshot("legacy-target", 100, 500)
	result := decisionResult(4401, "Force of Nature", 1057, "Negatron Cloak", itemization.StatusSaveForComponent, false)
	result.Rules = itemrules.Status{Legacy: true, Fallback: true, Source: itemrules.SourceLegacyBuiltin}
	cursor := newDecisionCursor(snapshot, result)
	result.TargetItem = &itemization.ItemRef{ID: 2504, Name: "Kaenic Rookern", Cost: 2900}
	result.RankedOptions[0].Item = *result.TargetItem

	next, events := cursor.advance(now, snapshot, result)
	if len(events) != 0 {
		t.Fatalf("legacy target churn emitted important event: %#v", events)
	}
	if next.state.TargetID != 2504 || next.sequence != 0 {
		t.Fatalf("legacy target churn was not checkpointed without alert: %#v", next)
	}
}

func TestExternalRuleEvidenceRequiresReadyStructuredSource(t *testing.T) {
	tests := []struct {
		name   string
		status itemrules.Status
		want   bool
	}{
		{name: "exact", status: itemrules.Status{Ready: true, Source: itemrules.SourceExternalExact}, want: true},
		{name: "patch fallback", status: itemrules.Status{Ready: true, Source: itemrules.SourceExternalFallback}, want: true},
		{name: "last known good", status: itemrules.Status{Ready: true, Source: itemrules.SourceExternalLastKnownGood}, want: true},
		{name: "legacy", status: itemrules.Status{Ready: true, Legacy: true, Source: itemrules.SourceLegacyBuiltin}},
		{name: "unknown source", status: itemrules.Status{Ready: true, Source: "unknown"}},
		{name: "not ready", status: itemrules.Status{Source: itemrules.SourceExternalExact}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := externalRuleEvidence(test.status); got != test.want {
				t.Fatalf("externalRuleEvidence(%#v) = %t, want %t", test.status, got, test.want)
			}
		})
	}
}

func TestDecisionCursorAcceptsReadyExternalGenericProfileEvidence(t *testing.T) {
	now := time.Date(2026, 9, 19, 8, 6, 0, 0, time.UTC)
	snapshot := decisionSnapshot("generic-target", 100, 500)
	result := decisionResult(4401, "Force of Nature", 1057, "Negatron Cloak", itemization.StatusReady, false)
	profileStatus := itemprofiles.Status{Ready: true, Source: itemprofiles.SourceExternalExact, Version: "generic-v1", Patch: "16.18.1"}
	result.ProfileStatus = &profileStatus
	result.Profile = &itemprofiles.Provenance{Level: itemprofiles.KindChampionRole, ProfileID: "ahri_middle"}
	result.Rules = itemrules.Status{Ready: true, Source: "generic_" + itemprofiles.SourceExternalExact, Version: profileStatus.Version, Patch: profileStatus.Patch}
	cursor := newDecisionCursor(snapshot, result)
	result.TargetItem = &itemization.ItemRef{ID: 2504, Name: "Kaenic Rookern", Cost: 2900}
	result.RankedOptions[0].Item = *result.TargetItem

	_, events := cursor.advance(now, snapshot, result)
	if got := decisionKinds(events); !equalDecisionKinds(got, []domain.DecisionKind{domain.DecisionTargetChanged}) {
		t.Fatalf("generic target change kinds = %v; events=%#v", got, events)
	}
}

func TestDecisionCursorDoesNotRepeatAnAffordabilityTransition(t *testing.T) {
	now := time.Date(2026, 9, 19, 8, 10, 0, 0, time.UTC)
	snapshot := decisionSnapshot("affordability-once", 100, 500)
	result := decisionResult(4401, "Force of Nature", 1057, "Negatron Cloak", itemization.StatusSaveForComponent, false)
	cursor := newDecisionCursor(snapshot, result)

	affordableSnapshot := cloneDecisionSnapshot(snapshot)
	affordableSnapshot.Game.ActivePlayer.CurrentGold = 900
	affordableResult := result
	affordableResult.Status = itemization.StatusReady
	cursor, events := cursor.advance(now, affordableSnapshot, affordableResult)
	if got := decisionKinds(events); !equalDecisionKinds(got, []domain.DecisionKind{domain.DecisionComponentAffordable}) {
		t.Fatalf("first affordability transition = %v", got)
	}
	affordableSnapshot.UpdatedAt = now.Add(time.Minute)
	_, events = cursor.advance(now.Add(time.Minute), affordableSnapshot, affordableResult)
	if len(events) != 0 {
		t.Fatalf("unchanged affordability replayed events: %#v", events)
	}
}

func TestDecisionCursorDoesNotEmitForInitialBaseline(t *testing.T) {
	snapshot := decisionSnapshot("initial", 10, 500)
	result := decisionResult(4401, "Force of Nature", 1057, "Negatron Cloak", itemization.StatusReady, false)
	cursor := newDecisionCursor(snapshot, result)
	if cursor.sequence != 0 || cursor.sessionID != "initial" {
		t.Fatalf("initial cursor = %#v", cursor)
	}
	_, events := cursor.advance(snapshot.UpdatedAt, snapshot, result)
	if len(events) != 0 {
		t.Fatalf("initial page-equivalent evaluation emitted %#v", events)
	}
}

func TestCoordinatorPublishesDecisionOnlyAfterDurableCommit(t *testing.T) {
	now := time.Date(2026, 9, 19, 8, 30, 0, 0, time.UTC)
	snapshot := decisionSnapshot("durable-match", 100, 500)
	gameStore := state.NewStore(snapshot)
	engine := &mutableDecisionEngine{result: decisionResult(4401, "Force of Nature", 1057, "Negatron Cloak", itemization.StatusReady, false)}
	persistence := &memoryDecisionPersistence{}
	catalogs := &fakeCatalogProvider{value: coordinatorCatalog(), status: catalog.Status{Ready: true, Patch: "16.18.1", Source: "cache"}}
	coordinator, err := NewCoordinator(gameStore, catalogs, engine, persistence, Config{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	updates, unsubscribe := coordinator.SubscribeDecisionEvents(2)
	defer unsubscribe()
	if _, err := coordinator.Recalculate(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertNoDecisionNotification(t, updates)

	engine.set(decisionResult(2504, "Kaenic Rookern", 1057, "Negatron Cloak", itemization.StatusReady, false))
	persistence.fail = true
	failed, err := coordinator.Recalculate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if failed.DecisionEvents.Status != domain.DecisionEventStatusError || failed.DecisionEvents.Latest != nil {
		t.Fatalf("failed decision status = %#v", failed.DecisionEvents)
	}
	assertNoDecisionNotification(t, updates)

	persistence.fail = false
	succeeded, err := coordinator.Recalculate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-updates:
		if event.Kind != domain.DecisionTargetChanged || event.Sequence != 1 {
			t.Fatalf("published event = %#v", event)
		}
		if len(persistence.events) != 1 || persistence.events[0].ID != event.ID {
			t.Fatalf("event was published before durable storage: stored=%#v published=%#v", persistence.events, event)
		}
	case <-time.After(time.Second):
		t.Fatal("durably committed decision was not published")
	}
	if succeeded.DecisionEvents.Status != domain.DecisionEventStatusReady || succeeded.DecisionEvents.Latest == nil {
		t.Fatalf("successful decision status = %#v", succeeded.DecisionEvents)
	}
}

func TestCoordinatorExposesInjectedEpochAcrossDashboardUpdates(t *testing.T) {
	now := time.Date(2026, 9, 19, 8, 32, 0, 0, time.UTC)
	snapshot := decisionSnapshot("epoch-match", 100, 500)
	coordinator, err := NewCoordinator(
		state.NewStore(snapshot),
		&fakeCatalogProvider{value: coordinatorCatalog(), status: catalog.Status{Ready: true, Patch: "16.18.1", Source: "cache"}},
		&mutableDecisionEngine{result: decisionResult(4401, "Force of Nature", 1057, "Negatron Cloak", itemization.StatusReady, false)},
		&memoryDecisionPersistence{},
		Config{Now: func() time.Time { return now }, Epoch: "test-epoch-01"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := coordinator.Current().Epoch; got != "test-epoch-01" {
		t.Fatalf("initial epoch = %q", got)
	}
	dashboard, err := coordinator.Recalculate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if dashboard.Epoch != "test-epoch-01" {
		t.Fatalf("updated epoch = %q", dashboard.Epoch)
	}
}

func TestCoordinatorRejectsUnsafeInjectedEpoch(t *testing.T) {
	snapshot := decisionSnapshot("epoch-match", 100, 500)
	_, err := NewCoordinator(
		state.NewStore(snapshot),
		&fakeCatalogProvider{value: coordinatorCatalog(), status: catalog.Status{Ready: true}},
		&mutableDecisionEngine{result: decisionResult(4401, "Force of Nature", 1057, "Negatron Cloak", itemization.StatusReady, false)},
		nil,
		Config{Epoch: "unsafe\nepoch"},
	)
	if err == nil || !strings.Contains(err.Error(), "dashboard epoch") {
		t.Fatalf("NewCoordinator() error = %v", err)
	}
	if generated := NewEpoch(); !ValidEpoch(generated) {
		t.Fatalf("NewEpoch() returned invalid value %q", generated)
	}
}

func TestCoordinatorRetriesExactDecisionBatchAfterUncertainCommit(t *testing.T) {
	now := time.Date(2026, 9, 19, 8, 35, 0, 0, time.UTC)
	snapshot := decisionSnapshot("uncertain-commit", 100, 500)
	gameStore := state.NewStore(snapshot)
	engine := &mutableDecisionEngine{result: decisionResult(4401, "Force of Nature", 1057, "Negatron Cloak", itemization.StatusReady, false)}
	persistence := &memoryDecisionPersistence{}
	catalogs := &fakeCatalogProvider{value: coordinatorCatalog(), status: catalog.Status{Ready: true, Patch: "16.18.1", Source: "cache"}}
	coordinator, err := NewCoordinator(gameStore, catalogs, engine, persistence, Config{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	updates, unsubscribe := coordinator.SubscribeDecisionEvents(2)
	defer unsubscribe()
	if _, err := coordinator.Recalculate(context.Background()); err != nil {
		t.Fatal(err)
	}

	engine.set(decisionResult(2504, "Kaenic Rookern", 1057, "Negatron Cloak", itemization.StatusReady, false))
	persistence.commitThenFail = true
	failed, err := coordinator.Recalculate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if failed.DecisionEvents.Status != domain.DecisionEventStatusError || len(persistence.events) != 1 {
		t.Fatalf("uncertain commit status/events = %#v, %#v", failed.DecisionEvents, persistence.events)
	}
	committedAt := persistence.events[0].OccurredAt
	assertNoDecisionNotification(t, updates)

	now = now.Add(time.Minute)
	later := cloneDecisionSnapshot(snapshot)
	later.UpdatedAt = now
	later.Game.GameTimeSeconds += 60
	gameStore.Update(later)
	succeeded, err := coordinator.Recalculate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-updates:
		if event.ID != persistence.events[0].ID || !event.OccurredAt.Equal(committedAt) {
			t.Fatalf("retry changed durable identity/time: stored=%#v published=%#v", persistence.events[0], event)
		}
	case <-time.After(time.Second):
		t.Fatal("exact retry was not published after durable confirmation")
	}
	if len(persistence.events) != 1 {
		t.Fatalf("exact retry duplicated durable event: %#v", persistence.events)
	}
	if succeeded.DecisionEvents.Status != domain.DecisionEventStatusReady || succeeded.DecisionEvents.Latest == nil {
		t.Fatalf("successful exact retry status = %#v", succeeded.DecisionEvents)
	}
}

func TestCoordinatorRetainsPendingDecisionAcrossMatchEnd(t *testing.T) {
	now := time.Date(2026, 9, 19, 8, 40, 0, 0, time.UTC)
	snapshot := decisionSnapshot("ending-match", 100, 500)
	gameStore := state.NewStore(snapshot)
	engine := &mutableDecisionEngine{result: decisionResult(4401, "Force of Nature", 1057, "Negatron Cloak", itemization.StatusReady, false)}
	persistence := &memoryDecisionPersistence{}
	catalogs := &fakeCatalogProvider{value: coordinatorCatalog(), status: catalog.Status{Ready: true, Patch: "16.18.1", Source: "cache"}}
	coordinator, err := NewCoordinator(gameStore, catalogs, engine, persistence, Config{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	updates, unsubscribe := coordinator.SubscribeDecisionEvents(2)
	defer unsubscribe()
	if _, err := coordinator.Recalculate(context.Background()); err != nil {
		t.Fatal(err)
	}
	engine.set(decisionResult(2504, "Kaenic Rookern", 1057, "Negatron Cloak", itemization.StatusReady, false))
	persistence.fail = true
	if _, err := coordinator.Recalculate(context.Background()); err != nil {
		t.Fatal(err)
	}

	ended := cloneDecisionSnapshot(snapshot)
	ended.Session.State = domain.MatchEnded
	ended.Status = domain.StatusWaitingForGame
	ended.Game = nil
	gameStore.Update(ended)
	stillFailed, err := coordinator.Recalculate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stillFailed.DecisionEvents.Status != domain.DecisionEventStatusError || len(persistence.events) != 0 {
		t.Fatalf("ended match silently discarded pending batch: status=%#v events=%#v", stillFailed.DecisionEvents, persistence.events)
	}

	persistence.fail = false
	recovered, err := coordinator.Recalculate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(persistence.events) != 1 || persistence.events[0].SessionID != "ending-match" {
		t.Fatalf("old-session decision was not retained in history: %#v", persistence.events)
	}
	if recovered.DecisionEvents.Status != domain.DecisionEventStatusReady || recovered.DecisionEvents.Latest != nil {
		t.Fatalf("ended-session retry leaked as current latest: %#v", recovered.DecisionEvents)
	}
	assertNoDecisionNotification(t, updates)
}

func TestCoordinatorRetainsPendingDecisionAcrossNewSession(t *testing.T) {
	now := time.Date(2026, 9, 19, 8, 50, 0, 0, time.UTC)
	firstSnapshot := decisionSnapshot("old-match", 100, 500)
	gameStore := state.NewStore(firstSnapshot)
	engine := &mutableDecisionEngine{result: decisionResult(4401, "Force of Nature", 1057, "Negatron Cloak", itemization.StatusReady, false)}
	persistence := &memoryDecisionPersistence{}
	catalogs := &fakeCatalogProvider{value: coordinatorCatalog(), status: catalog.Status{Ready: true, Patch: "16.18.1", Source: "cache"}}
	coordinator, err := NewCoordinator(gameStore, catalogs, engine, persistence, Config{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	updates, unsubscribe := coordinator.SubscribeDecisionEvents(2)
	defer unsubscribe()
	if _, err := coordinator.Recalculate(context.Background()); err != nil {
		t.Fatal(err)
	}
	engine.set(decisionResult(2504, "Kaenic Rookern", 1057, "Negatron Cloak", itemization.StatusReady, false))
	persistence.fail = true
	if _, err := coordinator.Recalculate(context.Background()); err != nil {
		t.Fatal(err)
	}

	nextSnapshot := decisionSnapshot("new-match", 10, 500)
	nextSnapshot.UpdatedAt = now.Add(time.Minute)
	gameStore.Update(nextSnapshot)
	stillFailed, err := coordinator.Recalculate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stillFailed.DecisionEvents.Status != domain.DecisionEventStatusError || stillFailed.DecisionEvents.Latest != nil {
		t.Fatalf("new-session view did not expose unsaved status safely: %#v", stillFailed.DecisionEvents)
	}
	if len(persistence.events) != 0 {
		t.Fatalf("failed retry unexpectedly stored events: %#v", persistence.events)
	}

	persistence.fail = false
	recovered, err := coordinator.Recalculate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(persistence.events) != 1 || persistence.events[0].SessionID != "old-match" {
		t.Fatalf("old-session pending event missing after recovery: %#v", persistence.events)
	}
	if persistence.checkpoint.SessionID != "new-match" {
		t.Fatalf("new session baseline was not established: %#v", persistence.checkpoint)
	}
	if recovered.DecisionEvents.Status != domain.DecisionEventStatusReady || recovered.DecisionEvents.Latest != nil {
		t.Fatalf("old-session retry leaked into new-session status: %#v", recovered.DecisionEvents)
	}
	assertNoDecisionNotification(t, updates)
}

func TestCoordinatorRestartDoesNotReplayDurableDecisionEvents(t *testing.T) {
	now := time.Date(2026, 9, 19, 8, 45, 0, 0, time.UTC)
	snapshot := decisionSnapshot("restart-match", 100, 500)
	persistence := &memoryDecisionPersistence{}
	catalogs := &fakeCatalogProvider{value: coordinatorCatalog(), status: catalog.Status{Ready: true, Patch: "16.18.1", Source: "cache"}}
	engine := &mutableDecisionEngine{result: decisionResult(4401, "Force of Nature", 1057, "Negatron Cloak", itemization.StatusReady, false)}
	first, err := NewCoordinator(state.NewStore(snapshot), catalogs, engine, persistence, Config{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Recalculate(context.Background()); err != nil {
		t.Fatal(err)
	}
	engine.set(decisionResult(2504, "Kaenic Rookern", 1057, "Negatron Cloak", itemization.StatusReady, false))
	if _, err := first.Recalculate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(persistence.events) != 1 {
		t.Fatalf("first process events = %#v", persistence.events)
	}

	restarted, err := NewCoordinator(state.NewStore(snapshot), catalogs, engine, persistence, Config{Now: func() time.Time { return now.Add(time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	updates, unsubscribe := restarted.SubscribeDecisionEvents(1)
	defer unsubscribe()
	if _, err := restarted.Recalculate(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertNoDecisionNotification(t, updates)
	if len(persistence.events) != 1 {
		t.Fatalf("restart replayed historic decisions: %#v", persistence.events)
	}
}

func decisionSnapshot(sessionID string, gameTime, gold float64) domain.Snapshot {
	now := time.Date(2026, 9, 19, 8, 0, 0, 0, time.UTC)
	game := domain.GameState{
		GameTimeSeconds: gameTime,
		ActivePlayer:    domain.ActivePlayer{ChampionName: "Garen", CurrentGold: gold},
	}
	return domain.Snapshot{
		Status: domain.StatusInGame, Game: &game, Revision: 1, UpdatedAt: now,
		Session: &domain.MatchSession{ID: sessionID, State: domain.MatchActive, LastGameTime: gameTime},
	}
}

func decisionResult(targetID int, targetName string, componentID int, componentName string, status itemization.Status, critical bool) itemization.Result {
	target := itemization.ItemRef{ID: targetID, Name: targetName, Cost: 2800}
	component := itemization.ItemRef{ID: componentID, Name: componentName, Cost: 850}
	return itemization.Result{
		Status: status, TargetItem: &target, NextComponent: &component, GoldNeeded: 350,
		RankedOptions: []itemization.RankedItem{{Item: target, Score: 900, Critical: critical}},
		Rules:         itemrules.Status{Ready: true, Version: "garen-v2", Patch: "16.18.1", Source: itemrules.SourceExternalExact},
		ReasonCodes:   []string{"stable_option"},
	}
}

func decisionLifecycle(sessionID string, sequence uint64, kind domain.EventKind, occurredAt time.Time) domain.SemanticEvent {
	return domain.SemanticEvent{
		ID: sessionID + ":000001", SessionID: sessionID, Sequence: sequence,
		Kind: kind, OccurredAt: occurredAt, ObservedAt: occurredAt, GameTimeSeconds: 101,
		Source: domain.EventSourceObserved,
	}
}

func cloneDecisionSnapshot(source domain.Snapshot) domain.Snapshot {
	clone := source
	if source.Game != nil {
		game := *source.Game
		clone.Game = &game
	}
	if source.Session != nil {
		session := *source.Session
		clone.Session = &session
	}
	clone.RecentEvents = append([]domain.SemanticEvent(nil), source.RecentEvents...)
	return clone
}

func decisionKinds(events []domain.DecisionEvent) []domain.DecisionKind {
	result := make([]domain.DecisionKind, len(events))
	for index, event := range events {
		result[index] = event.Kind
	}
	return result
}

func equalDecisionKinds(left, right []domain.DecisionKind) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

type mutableDecisionEngine struct {
	mu     sync.Mutex
	result itemization.Result
}

func (engine *mutableDecisionEngine) Recommend(now time.Time, snapshot domain.Snapshot, _ catalog.Catalog) itemization.Result {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	result := engine.result
	result.GeneratedAt = now
	result.SourceGameRevision = snapshot.Revision
	result.RankedOptions = append([]itemization.RankedItem(nil), engine.result.RankedOptions...)
	return result
}

func (engine *mutableDecisionEngine) set(result itemization.Result) {
	engine.mu.Lock()
	engine.result = result
	engine.mu.Unlock()
}

type memoryDecisionPersistence struct {
	fail           bool
	commitThenFail bool
	checkpoint     storage.DecisionCheckpoint
	events         []domain.DecisionEvent
}

func (persistence *memoryDecisionPersistence) SaveRecommendation(context.Context, storage.RecommendationRecord) (bool, error) {
	return true, nil
}

func (persistence *memoryDecisionPersistence) LoadDecisionCheckpoint(_ context.Context, sessionID string) (storage.DecisionCheckpoint, error) {
	if persistence.checkpoint.SessionID != sessionID {
		return storage.DecisionCheckpoint{}, catalog.ErrNotFound
	}
	return persistence.checkpoint, nil
}

func (persistence *memoryDecisionPersistence) SaveDecisionUpdates(_ context.Context, checkpoint storage.DecisionCheckpoint, events []domain.DecisionEvent) error {
	if persistence.fail {
		return errors.New("disk full")
	}
	nextEvents := cloneDecisionEvents(persistence.events)
	for _, event := range events {
		found := false
		for _, stored := range nextEvents {
			if stored.ID != event.ID {
				continue
			}
			found = true
			if !reflect.DeepEqual(stored, event) {
				return errors.New("decision event conflict")
			}
		}
		if !found {
			nextEvents = append(nextEvents, cloneDecisionEvent(event))
		}
	}
	persistence.checkpoint = checkpoint
	persistence.checkpoint.State = append(json.RawMessage(nil), checkpoint.State...)
	persistence.events = nextEvents
	if persistence.commitThenFail {
		persistence.commitThenFail = false
		return errors.New("commit outcome unknown")
	}
	return nil
}

func (persistence *memoryDecisionPersistence) ListDecisionEvents(_ context.Context, sessionID string, after uint64, limit int) ([]domain.DecisionEvent, error) {
	result := make([]domain.DecisionEvent, 0, limit)
	for _, event := range persistence.events {
		if event.SessionID == sessionID && event.Sequence > after && len(result) < limit {
			result = append(result, event)
		}
	}
	return result, nil
}

func assertNoDecisionNotification(t *testing.T, updates <-chan domain.DecisionEvent) {
	t.Helper()
	select {
	case event := <-updates:
		t.Fatalf("unexpected decision notification: %#v", event)
	case <-time.After(20 * time.Millisecond):
	}
}
