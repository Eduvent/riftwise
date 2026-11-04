package itemization

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"lolengine/internal/analysis"
	"lolengine/internal/catalog"
	"lolengine/internal/domain"
	"lolengine/internal/itemrules"
	"lolengine/internal/personalization"
)

func TestGarenPatch16191DamagePressureRanking(t *testing.T) {
	knowledge, rules := garenDamagePressureFixture(t)
	candidates := make([]itemrules.CandidateRule, 0)
	for _, rule := range rules.Candidates {
		switch rule.ItemID {
		case itemRanduins, itemDeadMans, itemJakSho, itemForceNature, 2504:
			candidates = append(candidates, rule)
		}
	}
	tests := []struct {
		name            string
		physical, magic int
		want            int
	}{
		{name: "physical-dominant burst", physical: 531, magic: 450, want: itemRanduins},
		{name: "magic-heavy burst", physical: 350, magic: 650, want: 2504},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			needs := analysis.NeedScores{
				itemrules.SignalPhysical:  tt.physical,
				itemrules.SignalMagic:     tt.magic,
				itemrules.SignalBurst:     800,
				itemrules.SignalSustained: 800,
			}
			context := analysis.MatchContext{Phase: domain.PhaseLate, Strategy: domain.StrategyTeamfight, Needs: needs}
			input := RankingInput{Phase: context.Phase, Strategy: context.Strategy, Needs: needs, Gold: 1000}
			result := rankAndPlan(Result{Stability: rules.Stability, AlternativeLimit: 3}, input, knowledge, context, candidates, rankingCopy{}, personalization.Snapshot{})
			if result.TargetItem == nil || result.TargetItem.ID != tt.want {
				t.Fatalf("target = %#v, want %d; order = %v", result.TargetItem, tt.want, result.DecisionOrder)
			}
		})
	}
}

func TestJakShoComponentTracksDominantDamageType(t *testing.T) {
	knowledge, _ := garenDamagePressureFixture(t)
	tests := []struct {
		name            string
		physical, magic int
		gold            int
		inventory       []RankingInventoryItem
		want            int
		wantStatus      Status
	}{
		{name: "physical pressure buys armor first", physical: 531, magic: 450, want: 1029},
		{name: "owned null-magic mantle does not pull physical pressure into MR", physical: 531, magic: 450, inventory: []RankingInventoryItem{{ID: 1033, Count: 1}}, want: 1029},
		{name: "full inventory keeps combining MR path open", physical: 531, magic: 450, inventory: []RankingInventoryItem{
			{ID: itemForceNature, Count: 1, Slot: 0}, {ID: itemStridebreaker, Count: 1, Slot: 1},
			{ID: 3006, Count: 1, Slot: 2}, {ID: 2504, Count: 1, Slot: 3},
			{ID: itemSteraks, Count: 1, Slot: 4}, {ID: 1033, Count: 1, Slot: 5},
		}, want: 1057, wantStatus: StatusReady},
		{name: "full inventory buys affordable MR combine over unaffordable armor combine", physical: 531, magic: 450, gold: 450, inventory: []RankingInventoryItem{
			{ID: itemForceNature, Count: 1, Slot: 0}, {ID: itemStridebreaker, Count: 1, Slot: 1},
			{ID: 3006, Count: 1, Slot: 2}, {ID: 2504, Count: 1, Slot: 3},
			{ID: 1029, Count: 1, Slot: 4}, {ID: 1033, Count: 1, Slot: 5},
		}, want: 1057, wantStatus: StatusReady},
		{name: "magic pressure keeps MR path", physical: 350, magic: 650, want: 1033},
		{name: "near-even pressure keeps neutral recipe choice", physical: 500, magic: 480, want: 1028},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gold := tt.gold
			if gold == 0 {
				gold = 1000
			}
			input := RankingInput{Gold: gold, Inventory: tt.inventory, Needs: analysis.NeedScores{
				itemrules.SignalPhysical: tt.physical,
				itemrules.SignalMagic:    tt.magic,
			}}
			plan, err := buildDecisionPlan(RankedItem{Item: ItemRef{ID: itemJakSho}}, input, ownedMultiset(domainInventory(input.Inventory)), knowledge, "")
			if err != nil {
				t.Fatal(err)
			}
			if plan.NextComponent == nil || plan.NextComponent.ID != tt.want {
				t.Fatalf("component = %#v, want %d", plan.NextComponent, tt.want)
			}
			if tt.wantStatus != "" && plan.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", plan.Status, tt.wantStatus)
			}
		})
	}
}

func garenDamagePressureFixture(t *testing.T) (catalog.Catalog, itemrules.RuleSet) {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join("..", "itemprofiles", "testdata", "catalog-16.19.1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var knowledge catalog.Catalog
	if err := json.Unmarshal(payload, &knowledge); err != nil {
		t.Fatal(err)
	}
	rules, _, err := (itemrules.Loader{Dir: filepath.Join("..", "..", "rules", "garen", "v2")}).Resolve(knowledge.Patch, knowledge)
	if err != nil {
		t.Fatal(err)
	}
	return knowledge, rules
}
