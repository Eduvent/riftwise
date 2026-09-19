package recommendation

import (
	"testing"
	"time"

	"lolengine/internal/domain"
	"lolengine/internal/itemization"
	"lolengine/internal/itemrules"
)

func TestStabilizerRequiresMarginConfirmationAndMinimumHold(t *testing.T) {
	base := time.Date(2026, 9, 19, 1, 0, 0, 0, time.UTC)
	stabilizer := NewStabilizer()
	snapshot := stabilizationSnapshot(100)

	first := stabilizer.Apply(base, snapshot, stabilizationResult(100, 900, 200, 700, false, 1100))
	if first.TargetItem.ID != 100 {
		t.Fatalf("initial target = %d", first.TargetItem.ID)
	}

	belowMargin := stabilizer.Apply(base.Add(5*time.Second), snapshot, stabilizationResult(200, 950, 100, 900, false, 1200))
	if belowMargin.TargetItem.ID != 100 {
		t.Fatalf("below-margin challenger switched target: %#v", belowMargin)
	}

	confirmedTooEarly := stabilizer.Apply(base.Add(10*time.Second), snapshot, stabilizationResult(200, 1200, 100, 900, false, 1300))
	if confirmedTooEarly.TargetItem.ID != 100 {
		t.Fatalf("unconfirmed challenger switched target: %#v", confirmedTooEarly)
	}

	confirmed := stabilizer.Apply(base.Add(25*time.Second), snapshot, stabilizationResult(200, 1200, 100, 900, false, 1400))
	if confirmed.TargetItem.ID != 200 {
		t.Fatalf("confirmed challenger did not switch: %#v", confirmed)
	}
}

func TestStabilizerAllowsCriticalAndInvalidImmediateSwitches(t *testing.T) {
	base := time.Date(2026, 9, 19, 1, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		next itemization.Result
	}{
		{name: "critical", next: stabilizationResult(200, 1000, 100, 990, true, 1200)},
		{name: "incumbent removed", next: stabilizationResultWithoutIncumbent(200, 1000)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stabilizer := NewStabilizer()
			snapshot := stabilizationSnapshot(100)
			stabilizer.Apply(base, snapshot, stabilizationResult(100, 900, 200, 700, false, 1100))
			got := stabilizer.Apply(base.Add(time.Second), snapshot, test.next)
			if got.TargetItem == nil || got.TargetItem.ID != 200 {
				t.Fatalf("immediate switch result = %#v", got)
			}
		})
	}
	t.Run("incumbent completed", func(t *testing.T) {
		stabilizer := NewStabilizer()
		snapshot := stabilizationSnapshot(100)
		stabilizer.Apply(base, snapshot, stabilizationResult(100, 900, 200, 700, false, 1100))
		snapshot.Game.ActivePlayer.Items = []domain.Item{{ID: 100, Count: 1}}
		got := stabilizer.Apply(base.Add(time.Second), snapshot, stabilizationResult(200, 1000, 100, 990, false, 1200))
		if got.TargetItem == nil || got.TargetItem.ID != 200 {
			t.Fatalf("completed incumbent did not switch immediately: %#v", got)
		}
	})
}

func TestStabilizerKeepsIncumbentButRefreshesItsComponent(t *testing.T) {
	base := time.Date(2026, 9, 19, 1, 0, 0, 0, time.UTC)
	stabilizer := NewStabilizer()
	snapshot := stabilizationSnapshot(100)
	stabilizer.Apply(base, snapshot, stabilizationResult(100, 900, 200, 700, false, 1100))

	candidate := stabilizationResult(200, 1200, 100, 900, false, 1500)
	candidate.DecisionPlans[100] = itemization.DecisionPlan{
		Ranked:        candidate.DecisionPlans[100].Ranked,
		Status:        itemization.StatusReady,
		NextComponent: &itemization.ItemRef{ID: 777, Name: "fresh component", Cost: 500},
		Reason:        "updated incumbent component",
	}
	got := stabilizer.Apply(base.Add(5*time.Second), snapshot, candidate)
	if got.TargetItem.ID != 100 || got.NextComponent == nil || got.NextComponent.ID != 777 || got.Status != itemization.StatusReady {
		t.Fatalf("held decision did not refresh component: %#v", got)
	}
}

func TestStabilizerResetsWhenGameTimeMovesBack(t *testing.T) {
	base := time.Date(2026, 9, 19, 1, 0, 0, 0, time.UTC)
	stabilizer := NewStabilizer()
	stabilizer.Apply(base, stabilizationSnapshot(600), stabilizationResult(100, 900, 200, 700, false, 1100))
	got := stabilizer.Apply(base.Add(time.Second), stabilizationSnapshot(10), stabilizationResult(200, 901, 100, 900, false, 1200))
	if got.TargetItem.ID != 200 {
		t.Fatalf("new match did not reset stability: %#v", got)
	}
}

func TestStabilizerPreservesIncumbentAcrossStaleReconnect(t *testing.T) {
	base := time.Date(2026, 9, 19, 1, 0, 0, 0, time.UTC)
	stabilizer := NewStabilizer()
	active := stabilizationSnapshot(100)
	stabilizer.Apply(base, active, stabilizationResult(100, 900, 200, 700, false, 1100))

	stale := active
	stale.Status = domain.StatusStale
	stabilizer.Apply(base.Add(time.Second), stale, itemization.Result{Status: itemization.StatusStale})

	reconnected := stabilizationSnapshot(105)
	got := stabilizer.Apply(base.Add(2*time.Second), reconnected, stabilizationResult(200, 950, 100, 900, false, 1200))
	if got.TargetItem == nil || got.TargetItem.ID != 100 {
		t.Fatalf("brief stale interval reset incumbent: %#v", got)
	}
}

func TestStabilizerResetsImmediatelyWhenSessionIDChanges(t *testing.T) {
	base := time.Date(2026, 9, 19, 1, 0, 0, 0, time.UTC)
	stabilizer := NewStabilizer()
	firstSession := stabilizationSnapshot(100)
	stabilizer.Apply(base, firstSession, stabilizationResult(100, 900, 200, 700, false, 1100))

	nextSession := stabilizationSnapshot(105)
	nextSession.Session.ID = "match-2"
	got := stabilizer.Apply(base.Add(time.Second), nextSession, stabilizationResult(200, 901, 100, 900, false, 1200))
	if got.TargetItem == nil || got.TargetItem.ID != 200 {
		t.Fatalf("changed session ID retained prior incumbent: %#v", got)
	}
}

func TestStabilizerRequiresFreshTimingEvidenceAfterStale(t *testing.T) {
	base := time.Date(2026, 9, 19, 1, 0, 0, 0, time.UTC)
	stabilizer := NewStabilizer()
	active := stabilizationSnapshot(100)
	stabilizer.Apply(base, active, stabilizationResult(100, 900, 200, 700, false, 1100))

	started := stabilizer.Apply(base.Add(5*time.Second), stabilizationSnapshot(101), stabilizationResult(200, 1200, 100, 900, false, 1200))
	if started.TargetItem.ID != 100 {
		t.Fatalf("challenger switched before confirmation: %#v", started)
	}
	stale := stabilizationSnapshot(102)
	stale.Status = domain.StatusStale
	stabilizer.Apply(base.Add(6*time.Second), stale, itemization.Result{Status: itemization.StatusStale})

	reconnected := stabilizer.Apply(base.Add(100*time.Second), stabilizationSnapshot(103), stabilizationResult(200, 1200, 100, 900, false, 1300))
	if reconnected.TargetItem.ID != 100 {
		t.Fatalf("stale time confirmed challenger immediately: %#v", reconnected)
	}
	stillConfirming := stabilizer.Apply(base.Add(110*time.Second), stabilizationSnapshot(113), stabilizationResult(200, 1200, 100, 900, false, 1400))
	if stillConfirming.TargetItem.ID != 100 {
		t.Fatalf("stale time satisfied minimum hold: %#v", stillConfirming)
	}
	confirmed := stabilizer.Apply(base.Add(114*time.Second), stabilizationSnapshot(117), stabilizationResult(200, 1200, 100, 900, false, 1500))
	if confirmed.TargetItem.ID != 200 {
		t.Fatalf("fresh confirmation and hold did not switch: %#v", confirmed)
	}
}

func TestStabilizerResetsOnWaitingAndRulesetChanges(t *testing.T) {
	base := time.Date(2026, 9, 19, 1, 0, 0, 0, time.UTC)
	t.Run("waiting lifecycle", func(t *testing.T) {
		stabilizer := NewStabilizer()
		stabilizer.Apply(base, stabilizationSnapshot(100), stabilizationResult(100, 900, 200, 700, false, 1100))
		stabilizer.Apply(base.Add(time.Second), domain.Snapshot{Status: domain.StatusWaitingForGame}, itemization.Result{Status: itemization.StatusWaiting})
		got := stabilizer.Apply(base.Add(2*time.Second), stabilizationSnapshot(105), stabilizationResult(200, 950, 100, 900, false, 1200))
		if got.TargetItem == nil || got.TargetItem.ID != 200 {
			t.Fatalf("waiting did not reset incumbent: %#v", got)
		}
	})
	t.Run("ruleset version", func(t *testing.T) {
		stabilizer := NewStabilizer()
		stabilizer.Apply(base, stabilizationSnapshot(100), stabilizationResult(100, 900, 200, 700, false, 1100))
		next := stabilizationResult(200, 950, 100, 900, false, 1200)
		next.Rules.Version = "v3"
		got := stabilizer.Apply(base.Add(time.Second), stabilizationSnapshot(101), next)
		if got.TargetItem == nil || got.TargetItem.ID != 200 {
			t.Fatalf("ruleset change did not reset incumbent: %#v", got)
		}
	})
}

func stabilizationResult(primaryID, primaryScore, otherID, otherScore int, critical bool, componentID int) itemization.Result {
	primary := itemization.RankedItem{Item: itemization.ItemRef{ID: primaryID, Name: "primary", Cost: 2000}, Score: primaryScore, Critical: critical}
	other := itemization.RankedItem{Item: itemization.ItemRef{ID: otherID, Name: "other", Cost: 2000}, Score: otherScore}
	result := itemization.Result{
		Status:        itemization.StatusReady,
		TargetItem:    &primary.Item,
		NextComponent: &itemization.ItemRef{ID: componentID, Name: "component", Cost: 500},
		RankedOptions: []itemization.RankedItem{primary, other},
		DecisionPlans: map[int]itemization.DecisionPlan{
			primaryID: {Ranked: primary, Status: itemization.StatusReady, NextComponent: &itemization.ItemRef{ID: componentID, Name: "component", Cost: 500}, Reason: "primary"},
			otherID:   {Ranked: other, Status: itemization.StatusReady, NextComponent: &itemization.ItemRef{ID: componentID + 1, Name: "other component", Cost: 600}, Reason: "other"},
		},
		DecisionOrder:    []int{primaryID, otherID},
		AlternativeLimit: 2,
		Stability:        itemrules.StabilityPolicy{SwitchMargin: 100, ConfirmationSeconds: 10, MinimumHoldSeconds: 20, CriticalThreshold: 800},
		Rules:            itemrules.Status{Ready: true, Version: "v2", Patch: "16.18.1", Source: itemrules.SourceExternalExact},
	}
	itemization.SelectTarget(&result, primaryID)
	return result
}

func stabilizationResultWithoutIncumbent(primaryID, score int) itemization.Result {
	primary := itemization.RankedItem{Item: itemization.ItemRef{ID: primaryID, Name: "primary", Cost: 2000}, Score: score}
	result := itemization.Result{
		Status:        itemization.StatusReady,
		TargetItem:    &primary.Item,
		RankedOptions: []itemization.RankedItem{primary},
		DecisionPlans: map[int]itemization.DecisionPlan{primaryID: {Ranked: primary, Status: itemization.StatusReady, NextComponent: &itemization.ItemRef{ID: 999, Name: "component", Cost: 500}}},
		DecisionOrder: []int{primaryID}, AlternativeLimit: 2,
		Stability: itemrules.StabilityPolicy{SwitchMargin: 100, ConfirmationSeconds: 10, MinimumHoldSeconds: 20, CriticalThreshold: 800},
		Rules:     itemrules.Status{Ready: true, Version: "v2", Patch: "16.18.1", Source: itemrules.SourceExternalExact},
	}
	itemization.SelectTarget(&result, primaryID)
	return result
}

func stabilizationSnapshot(gameTime float64) domain.Snapshot {
	game := domain.GameState{GameMode: "CLASSIC", MapNumber: 11, GameTimeSeconds: gameTime, ActivePlayer: domain.ActivePlayer{ChampionName: "Garen"}}
	return domain.Snapshot{Status: domain.StatusInGame, Game: &game, Session: &domain.MatchSession{ID: "match-1", State: domain.MatchActive}, Revision: 2}
}
