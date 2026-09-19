package itemization

import (
	"reflect"
	"testing"

	"lolengine/internal/analysis"
	"lolengine/internal/catalog"
	"lolengine/internal/domain"
	"lolengine/internal/itemrules"
	"lolengine/internal/personalization"
)

func TestDefaultPersonalizationPreservesExactRanking(t *testing.T) {
	request, knowledge := personalizationRankingFixture()
	baseline := RankPlanning(request, knowledge)
	request.Personalization = personalization.Snapshot{Preferences: personalization.Preferences{Style: personalization.StyleBalanced, Version: 7}}
	withDefaults := RankPlanning(request, knowledge)
	if !reflect.DeepEqual(baseline, withDefaults) {
		t.Fatalf("default personalization changed ranking:\nbaseline=%#v\nwith=%#v", baseline, withDefaults)
	}
}

func TestPersonalizationStylePreferredAndFeedbackAreDeterministic(t *testing.T) {
	tests := []struct {
		name     string
		snapshot personalization.Snapshot
		want     int
		code     string
	}{
		{name: "safe favors structured survival", snapshot: personalization.Snapshot{Preferences: personalization.Preferences{Style: personalization.StyleSafe, Version: 2}}, want: 7101, code: personalization.ReasonStyleSafe},
		{name: "aggressive favors penetration", snapshot: personalization.Snapshot{Preferences: personalization.Preferences{Style: personalization.StyleAggressive, Version: 3}}, want: 7102, code: personalization.ReasonStyleAggressive},
		{name: "preferred bounded bonus", snapshot: personalization.Snapshot{Preferences: personalization.Preferences{Style: personalization.StyleBalanced, Version: 4, PreferredItemIDs: []int{7103}}}, want: 7103, code: personalization.ReasonPreferredItem},
		{name: "feedback explainable bonus", snapshot: personalization.Snapshot{Preferences: personalization.Preferences{Style: personalization.StyleBalanced, Version: 5, FeedbackEnabled: true}, Feedback: map[int]personalization.FeedbackEffect{7104: {Samples: 4, Adjustment: 9, ReasonCode: personalization.ReasonFeedbackUseful}}}, want: 7104, code: personalization.ReasonFeedbackUseful},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, knowledge := personalizationRankingFixture()
			request.Personalization = test.snapshot
			result := RankPlanning(request, knowledge)
			if result.TargetItem == nil || result.TargetItem.ID != test.want {
				t.Fatalf("target = %#v, options=%#v", result.TargetItem, result.RankedOptions)
			}
			if result.Personalization == nil || !containsString(result.RankedOptions[0].ReasonCodes, test.code) {
				t.Fatalf("personalization provenance/reason = %#v / %#v", result.Personalization, result.RankedOptions[0].ReasonCodes)
			}
		})
	}
}

func TestAvoidedItemsFailClosedButCannotRemoveCriticalCoverage(t *testing.T) {
	request, knowledge := personalizationRankingFixture()
	request.Input.Needs[itemrules.SignalHealing] = 1000
	request.Candidates[3].Adjustments = []itemrules.ScoreAdjustment{{Signal: itemrules.SignalHealing, Minimum: 800, Add: 20, Critical: true, ReasonCode: "critical_healing"}}
	request.Personalization = personalization.Snapshot{Preferences: personalization.Preferences{
		Style: personalization.StyleBalanced, Version: 4, AvoidedItemIDs: []int{7104},
	}}
	result := RankPlanning(request, knowledge)
	if _, exists := result.DecisionPlans[7104]; !exists {
		t.Fatalf("critical avoided option was erased: %#v", result)
	}
	if result.Personalization == nil || !containsString(result.Personalization.WarningCodes, personalization.ReasonAvoidanceCriticalOverride) {
		t.Fatalf("critical override not disclosed: %#v", result.Personalization)
	}

	request, knowledge = personalizationRankingFixture()
	request.Candidates = request.Candidates[:3]
	request.Personalization = personalization.Snapshot{Preferences: personalization.Preferences{
		Style: personalization.StyleBalanced, Version: 5, AvoidedItemIDs: []int{7101},
	}}
	result = RankPlanning(request, knowledge)
	if result.Status != StatusInsufficientContext || result.TargetItem != nil {
		t.Fatalf("preference exclusion did not fail closed: %#v", result)
	}
	if result.Personalization == nil || !containsString(result.Personalization.WarningCodes, personalization.WarningTooFewChoices) {
		t.Fatalf("missing insufficient-choice warning: %#v", result.Personalization)
	}

	request, knowledge = personalizationRankingFixture()
	request.Personalization = personalization.Snapshot{Preferences: personalization.Preferences{
		Style: personalization.StyleBalanced, Version: 6, AvoidedItemIDs: []int{7101, 7102, 7103, 7104},
	}}
	result = RankPlanning(request, knowledge)
	if result.Status != StatusInsufficientContext || len(result.DecisionOrder) != 0 || result.Reason == "" {
		t.Fatalf("all avoided options were mistaken for a complete build: %#v", result)
	}
}

func TestPersonalizationCannotTruncateRequiredCounterCoverage(t *testing.T) {
	tests := []struct {
		name         string
		critical     bool
		criticalNeed int
		wantItemID   int
	}{
		{name: "critical counter remains visible despite a negative feedback penalty", critical: true, criticalNeed: 1000, wantItemID: 7106},
		{name: "baseline counter remains visible despite personalized reordering", critical: false, criticalNeed: 700, wantItemID: 7104},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, knowledge := personalizationRankingFixture()
			for id := 7105; id <= 7106; id++ {
				knowledge.Items[id] = catalog.Item{ID: id, Name: "Option", Purchasable: true, AvailableMap11: true, Gold: catalog.Gold{Purchasable: true, Total: 1000}}
			}
			request.Base.AlternativeLimit = 2
			request.Input.Needs[itemrules.SignalHealing] = test.criticalNeed
			request.Candidates = []itemrules.CandidateRule{
				{ID: "first", ItemID: 7101, Category: itemrules.CategoryCore, BaseScore: 102},
				{ID: "second", ItemID: 7102, Category: itemrules.CategoryCore, BaseScore: 101},
				{ID: "third", ItemID: 7103, Category: itemrules.CategoryCore, BaseScore: 100},
				{ID: "baseline-counter", ItemID: 7104, Category: itemrules.CategorySituational, BaseScore: 100,
					Covers: []itemrules.Signal{itemrules.SignalHealing}, Adjustments: []itemrules.ScoreAdjustment{{Signal: itemrules.SignalHealing, Minimum: 600, Add: 3, ReasonCode: "counter_healing"}}},
				{ID: "preferred", ItemID: 7105, Category: itemrules.CategoryCore, BaseScore: 99},
				{ID: "critical-counter", ItemID: 7106, Category: itemrules.CategorySituational, BaseScore: 1,
					Covers: []itemrules.Signal{itemrules.SignalHealing}, Adjustments: []itemrules.ScoreAdjustment{{Signal: itemrules.SignalHealing, Minimum: 800, Add: 1, Critical: true, ReasonCode: "critical_healing"}}},
			}
			if test.critical {
				// Keep only the genuinely critical rule active in this case.
				request.Candidates[3].Adjustments[0].Minimum = 1100
			}
			request.Personalization = personalization.Snapshot{
				Preferences: personalization.Preferences{Style: personalization.StyleBalanced, Version: 9, PreferredItemIDs: []int{7105}, FeedbackEnabled: true},
				Feedback: map[int]personalization.FeedbackEffect{
					test.wantItemID: {Samples: 5, Adjustment: -9, ReasonCode: personalization.ReasonFeedbackNotUseful},
				},
			}
			result := RankPlanning(request, knowledge)
			if len(result.RankedOptions) != 3 {
				t.Fatalf("visible options = %#v", result.RankedOptions)
			}
			visible := false
			for _, option := range result.RankedOptions {
				if option.Item.ID == test.wantItemID {
					visible = true
				}
			}
			if !visible {
				t.Fatalf("required counter %d was truncated: %#v", test.wantItemID, result.RankedOptions)
			}
		})
	}
}

func personalizationRankingFixture() (PlanningRankingRequest, catalog.Catalog) {
	knowledge := catalog.Catalog{Patch: "16.18.1", Locale: "es_MX", Champions: map[string]catalog.Champion{"Garen": {ID: "Garen", Name: "Garen"}}, Items: map[int]catalog.Item{}}
	for id := 7101; id <= 7104; id++ {
		knowledge.Items[id] = catalog.Item{ID: id, Name: "Option", Purchasable: true, AvailableMap11: true, Gold: catalog.Gold{Purchasable: true, Total: 1000}}
	}
	candidates := []itemrules.CandidateRule{
		{ID: "survival", ItemID: 7101, Category: itemrules.CategoryCore, BaseScore: 100, Covers: []itemrules.Signal{itemrules.SignalPhysical, itemrules.SignalBurst}},
		{ID: "penetration", ItemID: 7102, Category: itemrules.CategoryCore, BaseScore: 100, Covers: []itemrules.Signal{itemrules.SignalArmor}},
		{ID: "mobility", ItemID: 7103, Category: itemrules.CategoryBoots, BaseScore: 100},
		{ID: "neutral", ItemID: 7104, Category: itemrules.CategorySituational, BaseScore: 100},
	}
	return PlanningRankingRequest{
		Base:       Result{AlternativeLimit: 3, Stability: itemrules.StabilityPolicy{CriticalThreshold: 800}},
		Input:      RankingInput{Phase: domain.PhaseMid, Strategy: domain.StrategyBalanced, Needs: analysis.NeedScores{}, Gold: 1500},
		Candidates: candidates, Route: PlanningRouteGarenV2,
	}, knowledge
}
