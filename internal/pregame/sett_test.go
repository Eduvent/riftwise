package pregame

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"lolengine/internal/catalog"
	"lolengine/internal/domain"
	"lolengine/internal/itemization"
	"lolengine/internal/itemprofiles"
	"lolengine/internal/itemrules"
)

func TestSettPregameOptionsAndCuratedBaseline(t *testing.T) {
	service := newSettTestService(t)
	options, err := service.Options(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if options.Patch != "16.19.1" || options.Profiles.Source != itemprofiles.SourceExternalExact || options.Profiles.Fallback {
		t.Fatalf("options patch/profile status = %q / %#v", options.Patch, options.Profiles)
	}
	foundSett := false
	for _, champion := range options.Champions {
		foundSett = foundSett || champion.ID == "Sett"
	}
	if !foundSett {
		t.Fatal("Sett is absent from pregame options")
	}

	plan := settPlan(t, service, validSettScenario())
	if plan.Provenance.Profile.ProfileID != "sett_top" || plan.Provenance.Profile.Level != itemprofiles.KindChampionRole || plan.Provenance.Profile.Fallback {
		t.Fatalf("Sett profile provenance = %#v", plan.Provenance.Profile)
	}
	if plan.Provenance.ProfileStatus.Patch != "16.19.1" || plan.Provenance.ProfileStatus.Source != itemprofiles.SourceExternalExact {
		t.Fatalf("Sett profile status = %#v", plan.Provenance.ProfileStatus)
	}
	if plan.TargetItem == nil || plan.TargetItem.ID != 6631 {
		t.Fatalf("baseline Sett target = %#v, want Stridebreaker", plan.TargetItem)
	}
	assertThreeDistinctSettChoices(t, plan)
	for subject, copy := range map[string]itemprofiles.PlanCopy{"runes": plan.RunePlan, "summoners": plan.SummonerPlan, "initial": plan.InitialPlan} {
		if copy.Source != itemprofiles.PlanSourceProfileCurated || len(copy.Options) < 2 {
			t.Errorf("%s plan = %#v", subject, copy)
		}
	}
}

func TestSettPregameAdaptsToStageAndPressure(t *testing.T) {
	service := newSettTestService(t)
	tests := []struct {
		name   string
		change func(*Scenario)
		want   int
	}{
		{name: "late health scaling", change: func(value *Scenario) { value.Stage = StageLateGame }, want: 2501},
		{name: "physical lane", change: func(value *Scenario) { value.Pressures.Physical = PressureHigh }, want: 3047},
		{name: "magic lane", change: func(value *Scenario) { value.Pressures.Magic = PressureHigh }, want: 3111},
		{name: "crowd control lane", change: func(value *Scenario) { value.Pressures.CrowdControl = PressureHigh }, want: 3111},
		{name: "crowd control despite target access pressure", change: func(value *Scenario) {
			value.Pressures.CrowdControl = PressureHigh
			value.Pressures.LanePressure = PressureHigh
		}, want: 3111},
		{name: "physical attacks despite target access pressure", change: func(value *Scenario) {
			value.Pressures.Physical = PressureHigh
			value.Pressures.LanePressure = PressureHigh
		}, want: 3047},
		{name: "magic damage despite target access pressure", change: func(value *Scenario) {
			value.Pressures.Magic = PressureHigh
			value.Pressures.LanePressure = PressureHigh
		}, want: 3111},
		{name: "burst despite target access pressure", change: func(value *Scenario) {
			value.Pressures.Burst = PressureHigh
			value.Pressures.LanePressure = PressureHigh
		}, want: 3053},
		{name: "burst lane", change: func(value *Scenario) { value.Pressures.Burst = PressureHigh }, want: 3053},
		{name: "side lane after stridebreaker", change: func(value *Scenario) {
			value.Stage = StageMidGame
			value.Strategy = domain.StrategySplitPush
			value.Inventory = []InventoryStack{{ItemID: 6631, Count: 1}}
		}, want: 3181},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := validSettScenario()
			test.change(&value)
			plan := settPlan(t, service, value)
			if plan.TargetItem == nil || plan.TargetItem.ID != test.want {
				t.Fatalf("target = %#v, want item %d; ranked=%#v", plan.TargetItem, test.want, plan.RankedOptions)
			}
			assertThreeDistinctSettChoices(t, plan)
		})
	}
}

func TestGarenPregameUsesPinnedV2RulesOnSettPatch(t *testing.T) {
	service := newSettTestService(t)
	value := validScenario()
	value.Gold = 3500
	plan, err := service.Plan(context.Background(), value)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != itemization.StatusReady && plan.Status != itemization.StatusSaveForComponent {
		t.Fatalf("Garen plan status = %q; warnings=%#v", plan.Status, plan.Warnings)
	}
	if plan.Patch != "16.19.1" || plan.Provenance.Rules == nil || !plan.Provenance.Rules.Ready || plan.Provenance.Rules.Source != itemrules.SourceExternalExact || plan.Provenance.Rules.Patch != "16.19.1" {
		t.Fatalf("Garen rules provenance = %#v", plan.Provenance.Rules)
	}
	if plan.Provenance.Profile.ProfileID != "garen_top" || plan.TargetItem == nil || len(plan.RankedOptions) < 3 {
		t.Fatalf("Garen profile/choices = %#v / %#v", plan.Provenance.Profile, plan.RankedOptions)
	}
}

func newSettTestService(t *testing.T) *Service {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join("..", "itemprofiles", "testdata", "catalog-16.19.1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var knowledge catalog.Catalog
	if err := json.Unmarshal(payload, &knowledge); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(
		fixedCatalogProvider{knowledge: knowledge, status: catalog.Status{Ready: true, Patch: knowledge.Patch, Locale: knowledge.Locale, Source: "test"}},
		itemrules.NewManager(filepath.Join("..", "..", "rules", "garen", "v2")),
		itemprofiles.NewManager(filepath.Join("..", "..", "rules", "generic", "v1")),
	)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func validSettScenario() Scenario {
	value := validScenario()
	value.Champion = "Sett"
	value.Role = domain.RoleTop
	value.Gold = 3500
	return value
}

func settPlan(t *testing.T, service *Service, value Scenario) Plan {
	t.Helper()
	plan, err := service.Plan(context.Background(), value)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != itemization.StatusReady && plan.Status != itemization.StatusSaveForComponent {
		t.Fatalf("Sett plan status = %q; warnings=%#v", plan.Status, plan.Warnings)
	}
	return plan
}

func assertThreeDistinctSettChoices(t *testing.T, plan Plan) {
	t.Helper()
	if len(plan.RankedOptions) < 3 || len(plan.Alternatives) < 2 || plan.TargetItem == nil {
		t.Fatalf("Sett choices = target %#v, ranked %#v, alternatives %#v", plan.TargetItem, plan.RankedOptions, plan.Alternatives)
	}
	seen := make(map[int]bool, len(plan.RankedOptions))
	for _, ranked := range plan.RankedOptions {
		if seen[ranked.Item.ID] {
			t.Fatalf("duplicate ranked item %d", ranked.Item.ID)
		}
		seen[ranked.Item.ID] = true
	}
	if !seen[plan.TargetItem.ID] || plan.Alternatives[0].Item.ID == plan.Alternatives[1].Item.ID {
		t.Fatalf("target/alternatives are not distinct: %#v", plan)
	}
}
