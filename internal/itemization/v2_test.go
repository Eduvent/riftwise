package itemization

import (
	"strings"
	"testing"
	"time"

	"lolengine/internal/catalog"
	"lolengine/internal/domain"
	"lolengine/internal/itemrules"
)

func TestV2EngineRanksPrimaryAndCompatibleAlternatives(t *testing.T) {
	knowledge := engineCatalog()
	for _, item := range []catalog.Item{
		{ID: 3047, Name: "Steelcaps", Tags: []string{"Boots", "Armor"}},
		{ID: 3111, Name: "Mercury's Treads", Tags: []string{"Boots", "Tenacity"}},
		{ID: 3075, Name: "Thornmail", Tags: []string{"Armor"}},
		{ID: 6695, Name: "Serpent's Fang", Tags: []string{"ArmorPenetration"}},
	} {
		item.Purchasable = true
		item.AvailableMap11 = true
		item.Gold = catalog.Gold{Base: 1000, Total: 1000, Purchasable: true}
		knowledge.Items[item.ID] = item
	}
	rules := itemrules.RuleSet{
		SchemaVersion: itemrules.CurrentSchemaVersion, RulesetVersion: "garen-v2-test", Champion: "Garen", Patch: "16.18.1", AlternativeCount: 3,
		Phases:         itemrules.PhaseBoundaries{EarlyEndSeconds: 840, MidEndSeconds: 1500},
		Context:        itemrules.ContextTuning{LaneWeightByPhase: map[domain.Phase]int{domain.PhaseEarly: 700, domain.PhaseMid: 400, domain.PhaseLate: 150}, MissingTraitPenalty: 200, ArmorFullScale: 150},
		Stability:      itemrules.StabilityPolicy{SwitchMargin: 100, ConfirmationSeconds: 10, MinimumHoldSeconds: 20, CriticalThreshold: 800},
		ChampionTraits: map[string]itemrules.TraitProfile{"Darius": {Healing: 900, CrowdControl: 300, Sustained: 800}},
		Candidates: []itemrules.CandidateRule{
			{ID: "anti-heal", ItemID: 3075, Category: itemrules.CategorySituational, BaseScore: 100, Covers: []itemrules.Signal{itemrules.SignalHealing}, Adjustments: []itemrules.ScoreAdjustment{{Signal: itemrules.SignalHealing, Minimum: 600, Add: 900, ReasonCode: "counter_healing", Critical: true}}},
			{ID: "steelcaps", ItemID: 3047, Category: itemrules.CategoryBoots, MutualExclusionGroup: "boots", BaseScore: 200, Covers: []itemrules.Signal{itemrules.SignalPhysical}},
			{ID: "mercs", ItemID: 3111, Category: itemrules.CategoryBoots, MutualExclusionGroup: "boots", BaseScore: 190, Covers: []itemrules.Signal{itemrules.SignalCrowdControl}},
			{ID: "anti-shield", ItemID: 6695, Category: itemrules.CategorySituational, BaseScore: 180, Covers: []itemrules.Signal{itemrules.SignalShielding}},
		},
	}
	game := engineGame([]domain.Player{engineEnemy("Darius", 10, 80)}, 10, 80, 1200)
	game.GameTimeSeconds = 500
	result := (Engine{Rules: fixedRuleProvider{rules: rules}, Strategy: domain.StrategyBalanced}).Recommend(time.Unix(100, 0), gameSnapshot(game), knowledge)

	if result.Status != StatusReady || result.TargetItem == nil || result.TargetItem.ID != 3075 {
		t.Fatalf("primary result = %#v", result)
	}
	if len(result.Alternatives) != 3 || len(result.RankedOptions) != 4 {
		t.Fatalf("ranked options = %d alternatives = %d", len(result.RankedOptions), len(result.Alternatives))
	}
	if result.Alternative == nil || result.Alternative.ID != result.Alternatives[0].Item.ID {
		t.Fatalf("legacy alternative not populated from ranked alternatives: %#v", result)
	}
	if !result.RankedOptions[0].Critical || !containsString(result.RankedOptions[0].ReasonCodes, "counter_healing") {
		t.Fatalf("critical reason missing: %#v", result.RankedOptions[0])
	}
	seen := map[int]bool{}
	for _, option := range result.RankedOptions {
		if seen[option.Item.ID] {
			t.Fatalf("duplicate ranked item %d", option.Item.ID)
		}
		seen[option.Item.ID] = true
	}
}

func TestV2EngineExcludesAllBootCandidatesAfterBootsOwned(t *testing.T) {
	knowledge := engineCatalog()
	for _, id := range []int{3047, 3111, 3075, 6695} {
		knowledge.Items[id] = catalog.Item{ID: id, Name: "Item", Purchasable: true, AvailableMap11: true, Gold: catalog.Gold{Base: 1000, Total: 1000, Purchasable: true}}
	}
	steelcaps := knowledge.Items[3047]
	steelcaps.Tags = []string{"Boots"}
	knowledge.Items[3047] = steelcaps
	mercs := knowledge.Items[3111]
	mercs.Tags = []string{"Boots"}
	knowledge.Items[3111] = mercs
	rules := itemrules.RuleSet{
		SchemaVersion: itemrules.CurrentSchemaVersion, RulesetVersion: "garen-v2-test", Champion: "Garen", Patch: "16.18.1", AlternativeCount: 2,
		Phases:  itemrules.PhaseBoundaries{EarlyEndSeconds: 840, MidEndSeconds: 1500},
		Context: itemrules.ContextTuning{LaneWeightByPhase: map[domain.Phase]int{domain.PhaseEarly: 700, domain.PhaseMid: 400, domain.PhaseLate: 150}, MissingTraitPenalty: 200, ArmorFullScale: 150},
		Candidates: []itemrules.CandidateRule{
			{ID: "steelcaps", ItemID: 3047, Category: itemrules.CategoryBoots, MutualExclusionGroup: "boots", BaseScore: 500},
			{ID: "mercs", ItemID: 3111, Category: itemrules.CategoryBoots, MutualExclusionGroup: "boots", BaseScore: 490},
			{ID: "anti-heal", ItemID: 3075, Category: itemrules.CategorySituational, BaseScore: 300},
			{ID: "anti-shield", ItemID: 6695, Category: itemrules.CategorySituational, BaseScore: 200},
			{ID: "core", ItemID: itemStridebreaker, Category: itemrules.CategoryCore, BaseScore: 100},
		},
	}
	game := engineGame([]domain.Player{engineEnemy("Darius", 10, 80)}, 10, 80, 1200)
	game.ActivePlayer.Items = []domain.Item{{ID: 3047, Count: 1}}
	result := (Engine{Rules: fixedRuleProvider{rules: rules}, Strategy: domain.StrategyBalanced}).Recommend(time.Unix(100, 0), gameSnapshot(game), knowledge)
	for _, option := range result.RankedOptions {
		if option.Item.ID == 3047 || option.Item.ID == 3111 {
			t.Fatalf("boots candidate remained after upgraded boots owned: %#v", result.RankedOptions)
		}
	}
}

func TestV2EngineStillOffersUpgradedBootsWhenOnlyBasicBootsAreOwned(t *testing.T) {
	knowledge := engineCatalog()
	knowledge.Items[1001] = catalog.Item{ID: 1001, Name: "Boots", Tags: []string{"Boots"}, Purchasable: true, AvailableMap11: true, Gold: catalog.Gold{Base: 300, Total: 300, Purchasable: true}}
	for _, item := range []catalog.Item{
		{ID: 3047, Name: "Steelcaps", Tags: []string{"Boots"}, From: []int{1001}},
		{ID: 3111, Name: "Mercury's Treads", Tags: []string{"Boots"}, From: []int{1001}},
		{ID: 3075, Name: "Thornmail"},
		{ID: 6695, Name: "Serpent's Fang"},
	} {
		item.Purchasable = true
		item.AvailableMap11 = true
		item.Gold = catalog.Gold{Base: 1000, Total: 1000, Purchasable: true}
		knowledge.Items[item.ID] = item
	}
	rules := itemrules.RuleSet{
		SchemaVersion: itemrules.CurrentSchemaVersion, RulesetVersion: "garen-v2-test", Champion: "Garen", Patch: "16.18.1", AlternativeCount: 2,
		Phases:  itemrules.PhaseBoundaries{EarlyEndSeconds: 840, MidEndSeconds: 1500},
		Context: itemrules.ContextTuning{LaneWeightByPhase: map[domain.Phase]int{domain.PhaseEarly: 700, domain.PhaseMid: 400, domain.PhaseLate: 150}, MissingTraitPenalty: 200, ArmorFullScale: 150},
		Candidates: []itemrules.CandidateRule{
			{ID: "steelcaps", ItemID: 3047, Category: itemrules.CategoryBoots, MutualExclusionGroup: "boots", BaseScore: 500},
			{ID: "mercs", ItemID: 3111, Category: itemrules.CategoryBoots, MutualExclusionGroup: "boots", BaseScore: 490},
			{ID: "anti-heal", ItemID: 3075, Category: itemrules.CategorySituational, BaseScore: 300},
			{ID: "anti-shield", ItemID: 6695, Category: itemrules.CategorySituational, BaseScore: 200},
		},
	}
	game := engineGame([]domain.Player{engineEnemy("Darius", 10, 80)}, 10, 80, 1200)
	game.ActivePlayer.Items = []domain.Item{{ID: 1001, Count: 1}}
	result := (Engine{Rules: fixedRuleProvider{rules: rules}, Strategy: domain.StrategyBalanced}).Recommend(time.Unix(100, 0), gameSnapshot(game), knowledge)
	if result.TargetItem == nil || result.TargetItem.ID != 3047 {
		t.Fatalf("basic boots incorrectly suppressed upgrades: %#v", result.RankedOptions)
	}
}

func TestV2EngineReportsExplicitLegacyFallback(t *testing.T) {
	game := engineGame([]domain.Player{engineEnemy("Ahri", 10, 80)}, 10, 80, 900)
	result := (Engine{Rules: fixedRuleProvider{legacy: true}, Strategy: domain.StrategyBalanced}).Recommend(time.Unix(100, 0), gameSnapshot(game), engineCatalog())
	if !result.Rules.Legacy || result.Rules.Source != itemrules.SourceLegacyBuiltin || !strings.Contains(strings.Join(result.Warnings, "\n"), "reglas heredadas") {
		t.Fatalf("fallback result = %#v", result)
	}
}

func TestV2EngineUsesPhaseStrategyAndStableRuleOrder(t *testing.T) {
	knowledge := engineCatalog()
	for _, id := range []int{8004, 8003, 8002, 8001} {
		knowledge.Items[id] = catalog.Item{ID: id, Name: "Option", Purchasable: true, AvailableMap11: true, Gold: catalog.Gold{Base: 1000, Total: 1000, Purchasable: true}}
	}
	rules := itemrules.RuleSet{
		SchemaVersion: itemrules.CurrentSchemaVersion, RulesetVersion: "garen-v2-order", Champion: "Garen", Patch: "16.18.1", AlternativeCount: 2,
		Phases:  itemrules.PhaseBoundaries{EarlyEndSeconds: 840, MidEndSeconds: 1500},
		Context: itemrules.ContextTuning{LaneWeightByPhase: map[domain.Phase]int{domain.PhaseEarly: 700, domain.PhaseMid: 400, domain.PhaseLate: 150}, MissingTraitPenalty: 200, ArmorFullScale: 150},
		Candidates: []itemrules.CandidateRule{
			{ID: "declared-first", ItemID: 8004, Category: itemrules.CategoryCore, BaseScore: 100},
			{ID: "lower-item-id", ItemID: 8003, Category: itemrules.CategoryCore, BaseScore: 100},
			{ID: "split", ItemID: 8002, Category: itemrules.CategoryCore, BaseScore: 100, StrategyScores: map[domain.Strategy]int{domain.StrategySplitPush: 150}},
			{ID: "early", ItemID: 8001, Category: itemrules.CategoryCore, BaseScore: 100, PhaseScores: map[domain.Phase]int{domain.PhaseEarly: 200}},
		},
	}
	tests := []struct {
		name     string
		time     float64
		strategy domain.Strategy
		want     int
	}{
		{name: "early phase", time: 500, strategy: domain.StrategyBalanced, want: 8001},
		{name: "split push", time: 1600, strategy: domain.StrategySplitPush, want: 8002},
		{name: "declared order breaks ties before item id", time: 1600, strategy: domain.StrategyBalanced, want: 8004},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			game := engineGame([]domain.Player{engineEnemy("Unknown", 10, 80)}, 10, 80, 1200)
			game.GameTimeSeconds = test.time
			result := (Engine{Rules: fixedRuleProvider{rules: rules}, Strategy: test.strategy}).Recommend(time.Unix(100, 0), gameSnapshot(game), knowledge)
			if result.TargetItem == nil || result.TargetItem.ID != test.want {
				t.Fatalf("target = %#v, want %d; ranked %#v", result.TargetItem, test.want, result.RankedOptions)
			}
		})
	}
}

func TestV2EngineDoesNotPublishWithoutTwoDistinctAlternatives(t *testing.T) {
	knowledge := engineCatalog()
	for _, id := range []int{8101, 8102, 8103, 8104} {
		knowledge.Items[id] = catalog.Item{ID: id, Name: "Option", Purchasable: true, AvailableMap11: true, Gold: catalog.Gold{Base: 1000, Total: 1000, Purchasable: true}}
	}
	rules := itemrules.RuleSet{
		SchemaVersion: itemrules.CurrentSchemaVersion, RulesetVersion: "garen-v2-diversity", Champion: "Garen", Patch: "16.18.1", AlternativeCount: 2,
		Phases:  itemrules.PhaseBoundaries{EarlyEndSeconds: 840, MidEndSeconds: 1500},
		Context: itemrules.ContextTuning{LaneWeightByPhase: map[domain.Phase]int{domain.PhaseEarly: 700, domain.PhaseMid: 400, domain.PhaseLate: 150}, MissingTraitPenalty: 200, ArmorFullScale: 150},
		Candidates: []itemrules.CandidateRule{
			{ID: "one", ItemID: 8101, Category: itemrules.CategoryCore, BaseScore: 400},
			{ID: "two", ItemID: 8102, Category: itemrules.CategoryCore, BaseScore: 300},
			{ID: "three", ItemID: 8103, Category: itemrules.CategoryCore, BaseScore: 200},
			{ID: "four", ItemID: 8104, Category: itemrules.CategoryCore, BaseScore: 100},
		},
	}
	game := engineGame([]domain.Player{engineEnemy("Unknown", 10, 80)}, 10, 80, 1200)
	game.ActivePlayer.Items = []domain.Item{{ID: 8101, Count: 1}, {ID: 8102, Count: 1}}
	result := (Engine{Rules: fixedRuleProvider{rules: rules}, Strategy: domain.StrategyBalanced}).Recommend(time.Unix(100, 0), gameSnapshot(game), knowledge)
	if result.Status != StatusError || result.TargetItem != nil || len(result.Alternatives) != 0 {
		t.Fatalf("insufficient choice set was published: %#v", result)
	}
}

type fixedRuleProvider struct {
	rules  itemrules.RuleSet
	legacy bool
}

func (provider fixedRuleProvider) Resolve(_ string, _ catalog.Catalog) itemrules.Resolution {
	if provider.legacy {
		return itemrules.Resolution{Legacy: true, Status: itemrules.Status{Ready: true, Source: itemrules.SourceLegacyBuiltin, Fallback: true, Legacy: true, LastError: "missing external rules"}}
	}
	return itemrules.Resolution{Rules: provider.rules, Status: itemrules.Status{Ready: true, Version: provider.rules.RulesetVersion, Patch: provider.rules.Patch, Source: itemrules.SourceExternalExact}}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
