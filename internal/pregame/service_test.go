package pregame

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"lolengine/internal/catalog"
	"lolengine/internal/domain"
	"lolengine/internal/itemization"
	"lolengine/internal/itemprofiles"
	"lolengine/internal/itemrules"
	"lolengine/internal/personalization"
)

func TestNormalizeScenarioRejectsInvalidOrUnboundedInput(t *testing.T) {
	knowledge := combinedCatalogFixture(t)
	tests := []struct {
		name     string
		mutate   func(*Scenario)
		wantPath string
	}{
		{name: "champion required", mutate: func(value *Scenario) { value.Champion = "" }, wantPath: "champion"},
		{name: "champion must resolve", mutate: func(value *Scenario) { value.Champion = "NotAChampion" }, wantPath: "champion"},
		{name: "role must be canonical", mutate: func(value *Scenario) { value.Role = "mid" }, wantPath: "role"},
		{name: "stage enum", mutate: func(value *Scenario) { value.Stage = "draft" }, wantPath: "stage"},
		{name: "strategy enum", mutate: func(value *Scenario) { value.Strategy = "roam" }, wantPath: "strategy"},
		{name: "opponent must resolve", mutate: func(value *Scenario) { value.LaneOpponent = "Unknown" }, wantPath: "lane_opponent"},
		{name: "pressure enum", mutate: func(value *Scenario) { value.Pressures.Magic = "extreme" }, wantPath: "pressures.magic"},
		{name: "negative gold", mutate: func(value *Scenario) { value.Gold = -1 }, wantPath: "gold"},
		{name: "excess gold", mutate: func(value *Scenario) { value.Gold = 100001 }, wantPath: "gold"},
		{name: "too many inventory stacks", mutate: func(value *Scenario) {
			value.Inventory = make([]InventoryStack, 8)
			for index := range value.Inventory {
				value.Inventory[index] = InventoryStack{ItemID: 1001, Count: 1}
			}
		}, wantPath: "inventory"},
		{name: "unknown item", mutate: func(value *Scenario) { value.Inventory = []InventoryStack{{ItemID: 999999, Count: 1}} }, wantPath: "inventory[0].item_id"},
		{name: "zero count", mutate: func(value *Scenario) { value.Inventory = []InventoryStack{{ItemID: 1001, Count: 0}} }, wantPath: "inventory[0].count"},
		{name: "excess count", mutate: func(value *Scenario) { value.Inventory = []InventoryStack{{ItemID: 1001, Count: 100}} }, wantPath: "inventory[0].count"},
		{name: "duplicate item stack", mutate: func(value *Scenario) {
			value.Inventory = []InventoryStack{{ItemID: 1001, Count: 1}, {ItemID: 1001, Count: 1}}
		}, wantPath: "inventory[1].item_id"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := validScenario()
			test.mutate(&value)
			_, err := Normalize(value, knowledge)
			if err == nil || err.Fields[test.wantPath] == "" {
				t.Fatalf("Normalize() error = %#v, want field %q", err, test.wantPath)
			}
		})
	}
}

func TestNormalizeScenarioCanonicalizesCatalogIdentitiesAndInventoryOrder(t *testing.T) {
	value := validScenario()
	value.Champion = "  ahri "
	value.LaneOpponent = "Dr. Mundo"
	value.Inventory = []InventoryStack{{ItemID: 1058, Count: 1}, {ItemID: 1001, Count: 2}}
	knowledge := combinedCatalogFixture(t)
	knowledge.Champions["DrMundo"] = catalog.Champion{ID: "DrMundo", Key: "36", Name: "Dr. Mundo", Tags: []string{"Tank", "Fighter"}}

	got, validation := Normalize(value, knowledge)
	if validation != nil {
		t.Fatalf("Normalize() error: %v", validation)
	}
	if got.ChampionID != "Ahri" || got.ChampionName != "Ahri" || got.LaneOpponentID != "DrMundo" || got.LaneOpponentName != "Dr. Mundo" {
		t.Fatalf("canonical identities = %#v", got)
	}
	if len(got.Inventory) != 2 || got.Inventory[0].ItemID != 1001 || got.Inventory[1].ItemID != 1058 {
		t.Fatalf("inventory order = %#v", got.Inventory)
	}
	for _, signal := range got.Pressures.values() {
		if !signal.Valid() {
			t.Fatalf("normalized pressure = %q, want valid enum", signal)
		}
	}
}

func TestPlanUsesExplicitPressureBeforeOpponentAssumptions(t *testing.T) {
	service := newTestService(t)
	value := validScenario()
	value.Champion = "Ahri"
	value.Role = domain.RoleMiddle
	value.LaneOpponent = "Amumu"
	value.Pressures.Physical = PressureNone
	value.Pressures.CrowdControl = PressureUnset

	plan, err := service.Plan(context.Background(), value)
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Needs[itemrules.SignalPhysical]; got != 0 {
		t.Fatalf("explicit physical pressure = %d, want 0", got)
	}
	if got := plan.Needs[itemrules.SignalCrowdControl]; got == 0 {
		t.Fatal("unset crowd-control pressure was not filled from the known opponent")
	}
	if !hasWarning(plan.Warnings, WarningOpponentAssumption) {
		t.Fatalf("warnings = %#v, want opponent assumption", plan.Warnings)
	}
	if plan.Scenario.Pressures.Physical != PressureNone || plan.Scenario.Pressures.CrowdControl != PressureUnset {
		t.Fatalf("normalized scenario changed user pressure categories: %#v", plan.Scenario.Pressures)
	}
}

func TestRepresentativeManualPlansUseExactProfiles(t *testing.T) {
	service := newTestService(t)
	tests := []struct {
		champion string
		role     domain.Role
		profile  string
	}{
		{champion: "Garen", role: domain.RoleTop, profile: "garen_top"},
		{champion: "Amumu", role: domain.RoleJungle, profile: "amumu_jungle"},
		{champion: "Ahri", role: domain.RoleMiddle, profile: "ahri_middle"},
		{champion: "Jinx", role: domain.RoleBottom, profile: "jinx_bottom"},
		{champion: "Leona", role: domain.RoleSupport, profile: "leona_support"},
	}
	for _, test := range tests {
		t.Run(test.champion, func(t *testing.T) {
			value := validScenario()
			value.Champion = test.champion
			value.Role = test.role
			value.Pressures.Physical = PressureMedium
			value.Pressures.Magic = PressureMedium
			value.Pressures.CrowdControl = PressureMedium

			plan, err := service.Plan(context.Background(), value)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Mode != ModeManualPregame || plan.Provenance.Profile.ProfileID != test.profile {
				t.Fatalf("plan provenance = %#v", plan.Provenance)
			}
			if plan.Status != itemization.StatusReady && plan.Status != itemization.StatusSaveForComponent {
				t.Fatalf("status = %q, warnings=%#v", plan.Status, plan.Warnings)
			}
			if len(plan.RankedOptions) < 3 || plan.TargetItem == nil || plan.NextComponent == nil {
				t.Fatalf("ranked plan = %#v", plan)
			}
			if plan.Confidence > ManualConfidenceCap {
				t.Fatalf("confidence = %v, want <= %v", plan.Confidence, ManualConfidenceCap)
			}
			for name, copy := range map[string]itemprofiles.PlanCopy{
				"runes": plan.RunePlan, "summoners": plan.SummonerPlan, "initial": plan.InitialPlan,
			} {
				if copy.Source != itemprofiles.PlanSourceProfileCurated || len(copy.Options) < 1 || len(copy.Options) > 3 {
					t.Fatalf("%s copy = %#v", name, copy)
				}
			}
			if !containsSource(plan.Sources, "User-supplied scenario") || !containsSource(plan.Sources, "Data Dragon "+plan.Patch) || !containsSource(plan.Sources, "Profile "+plan.Provenance.Profile.RulesetVersion) {
				t.Fatalf("sources = %#v", plan.Sources)
			}
			if test.champion == "Garen" {
				if plan.Provenance.Rules == nil || !plan.Provenance.Rules.Ready || plan.Provenance.Rules.Version == "" || plan.Provenance.Rules.Source == "" {
					t.Fatalf("Garen rule provenance = %#v", plan.Provenance.Rules)
				}
				wantSource := "Garen v2 rules " + plan.Provenance.Rules.Version + " (" + plan.Provenance.Rules.Source + ")"
				if !containsSource(plan.Sources, wantSource) {
					t.Fatalf("Garen sources = %#v, want %q", plan.Sources, wantSource)
				}
			} else {
				if plan.Provenance.Rules != nil {
					t.Fatalf("%s unexpectedly exposes Garen rule provenance: %#v", test.champion, plan.Provenance.Rules)
				}
				for _, source := range plan.Sources {
					if strings.HasPrefix(source, "Garen v2 rules ") {
						t.Fatalf("%s unexpectedly exposes Garen source %q", test.champion, source)
					}
				}
			}
		})
	}
}

func TestManualPlanDisclosesExplicitPersonalization(t *testing.T) {
	knowledge := combinedCatalogFixture(t)
	provider := fixedPersonalizationProvider{snapshot: personalization.Snapshot{Preferences: personalization.Preferences{
		Style: personalization.StyleSafe, Version: 7,
	}}}
	service, err := NewService(
		fixedCatalogProvider{knowledge: knowledge, status: catalog.Status{Ready: true, Patch: knowledge.Patch, Locale: knowledge.Locale, Source: "test"}},
		itemrules.NewManager(filepath.Join("..", "..", "rules", "garen", "v2")),
		itemprofiles.NewManager(filepath.Join("..", "..", "rules", "generic", "v1")),
		provider,
	)
	if err != nil {
		t.Fatal(err)
	}
	options, err := service.Options(context.Background())
	if err != nil || !options.Personalization.Available || !options.Personalization.Enabled || options.Personalization.Version != 7 {
		t.Fatalf("manual options personalization status = %+v, %v", options.Personalization, err)
	}
	value := validScenario()
	value.Pressures.Physical = PressureMedium
	value.Pressures.Magic = PressureMedium
	value.Pressures.CrowdControl = PressureMedium
	plan, err := service.Plan(context.Background(), value)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Personalization == nil || plan.Personalization.Version != 7 || plan.Personalization.Style != personalization.StyleSafe {
		t.Fatalf("personalization provenance = %+v", plan.Personalization)
	}
	if !containsSource(plan.Sources, "Preferencias deterministas locales (versión 7)") {
		t.Fatalf("sources = %+v", plan.Sources)
	}
}

func TestNeutralManualPlanDoesNotActivateFromOpponentFillAlone(t *testing.T) {
	service := newTestService(t)
	value := validScenario()
	value.Champion = "Zed"
	value.Role = domain.RoleMiddle
	value.LaneOpponent = "Amumu"

	plan, err := service.Plan(context.Background(), value)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Provenance.Profile.Level != itemprofiles.KindNeutral {
		t.Fatalf("profile level = %q, want neutral", plan.Provenance.Profile.Level)
	}
	if plan.Needs[itemrules.SignalMagic] == 0 || plan.Needs[itemrules.SignalCrowdControl] == 0 {
		t.Fatalf("merged opponent needs = %#v, want conservative fills retained", plan.Needs)
	}
	if plan.Status != itemization.StatusInsufficientContext || plan.TargetItem != nil || len(plan.RankedOptions) != 0 {
		t.Fatalf("opponent-only neutral plan became actionable: %#v", plan)
	}

	value.Pressures.Magic = PressureHigh
	activated, err := service.Plan(context.Background(), value)
	if err != nil {
		t.Fatal(err)
	}
	if (activated.Status != itemization.StatusReady && activated.Status != itemization.StatusSaveForComponent) || len(activated.RankedOptions) < 3 {
		t.Fatalf("explicit neutral activation did not remain actionable: %#v", activated)
	}
	if activated.Needs[itemrules.SignalCrowdControl] == 0 {
		t.Fatalf("opponent fill was not retained for scoring after explicit activation: %#v", activated.Needs)
	}
}

func TestManualFallbackIsExplicitAndNeutralCanDecline(t *testing.T) {
	service := newTestService(t)

	fallback := validScenario()
	fallback.Champion = "Garen"
	fallback.Role = domain.RoleMiddle
	fallbackPlan, err := service.Plan(context.Background(), fallback)
	if err != nil {
		t.Fatal(err)
	}
	if fallbackPlan.Provenance.Profile.Level != itemprofiles.KindChampionDefault || !hasAssumptionLanguage(fallbackPlan.RunePlan.Summary+" "+fallbackPlan.SummonerPlan.Summary+" "+fallbackPlan.InitialPlan.Summary) {
		t.Fatalf("fallback plan is not explicit: %#v", fallbackPlan)
	}

	neutral := validScenario()
	neutral.Champion = "Zed"
	neutral.Role = domain.RoleMiddle
	neutralPlan, err := service.Plan(context.Background(), neutral)
	if err != nil {
		t.Fatal(err)
	}
	if neutralPlan.Provenance.Profile.Level != itemprofiles.KindNeutral || neutralPlan.Status != itemization.StatusInsufficientContext || neutralPlan.TargetItem != nil {
		t.Fatalf("neutral plan = %#v", neutralPlan)
	}
}

func TestManualPlanIsDeterministicAcrossInventoryPermutation(t *testing.T) {
	service := newTestService(t)
	left := validScenario()
	left.Champion = "Jinx"
	left.Role = domain.RoleBottom
	left.Inventory = []InventoryStack{{ItemID: 1001, Count: 1}, {ItemID: 1058, Count: 1}}
	right := left
	right.Inventory = []InventoryStack{{ItemID: 1058, Count: 1}, {ItemID: 1001, Count: 1}}

	leftPlan, err := service.Plan(context.Background(), left)
	if err != nil {
		t.Fatal(err)
	}
	rightPlan, err := service.Plan(context.Background(), right)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(leftPlan, rightPlan) {
		leftJSON, _ := json.MarshalIndent(leftPlan, "", "  ")
		rightJSON, _ := json.MarshalIndent(rightPlan, "", "  ")
		t.Fatalf("permutation changed plan\nleft=%s\nright=%s", leftJSON, rightJSON)
	}
}

func TestManualConfidenceIsCappedAtPointEight(t *testing.T) {
	service := newTestService(t)
	value := validScenario()
	value.Pressures = Pressures{
		Physical: PressureNone, Magic: PressureNone, CrowdControl: PressureNone,
		Healing: PressureNone, Shielding: PressureNone, Armor: PressureNone,
		MagicResist: PressureNone, Burst: PressureNone, Sustained: PressureNone,
		Slows: PressureNone, LanePressure: PressureNone,
	}
	plan, err := service.Plan(context.Background(), value)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Provenance.ContextConfidence != ManualConfidenceCap || plan.Provenance.Profile.ConfidenceCap <= ManualConfidenceCap || plan.Provenance.TraitConfidence <= ManualConfidenceCap {
		t.Fatalf("confidence inputs do not exercise manual cap: %#v", plan.Provenance)
	}
	if plan.Confidence != ManualConfidenceCap {
		t.Fatalf("confidence = %.2f, want manual cap %.2f", plan.Confidence, ManualConfidenceCap)
	}
}

func TestCuratedPlanCopyContainsNoMetaClaimsOrMarkup(t *testing.T) {
	service := newTestService(t)
	options, err := service.Options(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if options.Mode != ModeManualPregame || len(options.Champions) < 5 || len(options.Items) == 0 {
		t.Fatalf("options = %#v", options)
	}
	for _, champion := range []string{"Garen", "Amumu", "Ahri", "Jinx", "Leona"} {
		value := validScenario()
		value.Champion = champion
		value.Role = map[string]domain.Role{"Garen": domain.RoleTop, "Amumu": domain.RoleJungle, "Ahri": domain.RoleMiddle, "Jinx": domain.RoleBottom, "Leona": domain.RoleSupport}[champion]
		plan, err := service.Plan(context.Background(), value)
		if err != nil {
			t.Fatal(err)
		}
		payload, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(string(payload))
		for _, forbidden := range []string{"win_rate", "pick_rate", "win rate", "pick rate", "tier", "<script", "mejor build", "best build"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("%s plan contains forbidden meta/markup claim %q: %s", champion, forbidden, payload)
			}
		}
	}
}

func TestServiceFailsClosedWhenCatalogOrProfilesAreUnavailable(t *testing.T) {
	knowledge := combinedCatalogFixture(t)
	tests := []struct {
		name     string
		catalogs fixedCatalogProvider
		profiles itemprofiles.Provider
	}{
		{
			name:     "catalog unavailable",
			catalogs: fixedCatalogProvider{knowledge: knowledge, status: catalog.Status{Ready: false, Source: "unavailable"}},
			profiles: itemprofiles.NewManager(filepath.Join("..", "..", "rules", "generic", "v1")),
		},
		{
			name:     "profiles unavailable",
			catalogs: fixedCatalogProvider{knowledge: knowledge, status: catalog.Status{Ready: true, Patch: knowledge.Patch, Locale: knowledge.Locale, Source: "test"}},
			profiles: itemprofiles.NewManager(t.TempDir()),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, err := NewService(test.catalogs, itemrules.NewManager(filepath.Join("..", "..", "rules", "garen", "v2")), test.profiles)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.Options(context.Background()); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("Options() error = %v, want ErrUnavailable", err)
			}
			if _, err := service.Plan(context.Background(), validScenario()); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("Plan() error = %v, want ErrUnavailable", err)
			}
		})
	}
}

func validScenario() Scenario {
	return Scenario{
		Champion: "Garen", Role: domain.RoleTop, Stage: StageFirstRecall, Strategy: domain.StrategyBalanced,
		Gold: 1200, Pressures: Pressures{}, Inventory: []InventoryStack{},
	}
}

func newTestService(t *testing.T) *Service {
	t.Helper()
	knowledge := combinedCatalogFixture(t)
	service, err := NewService(
		fixedCatalogProvider{knowledge: knowledge, status: catalog.Status{Ready: true, Patch: knowledge.Patch, Locale: knowledge.Locale, Source: "test"}},
		itemrules.NewManager(filepath.Join("..", "..", "rules", "garen", "v2")),
		itemprofiles.NewManager(filepath.Join("..", "..", "rules", "generic", "v1")),
	)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC) }
	return service
}

func combinedCatalogFixture(t *testing.T) catalog.Catalog {
	t.Helper()
	load := func(path string) catalog.Catalog {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var value catalog.Catalog
		if err := json.Unmarshal(payload, &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	base := load(filepath.Join("..", "itemrules", "testdata", "catalog-16.18.1.json"))
	generic := load(filepath.Join("..", "itemprofiles", "testdata", "catalog-16.18.1.json"))
	for id, item := range generic.Items {
		base.Items[id] = item
	}
	for id, champion := range generic.Champions {
		base.Champions[id] = champion
	}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	return base
}

type fixedCatalogProvider struct {
	knowledge catalog.Catalog
	status    catalog.Status
	ready     bool
}

func (provider fixedCatalogProvider) Current() (catalog.Catalog, catalog.Status, bool) {
	ready := provider.ready
	if !ready && provider.status.Ready {
		ready = true
	}
	return catalog.Clone(provider.knowledge), provider.status, ready
}

type fixedPersonalizationProvider struct {
	snapshot personalization.Snapshot
}

func (provider fixedPersonalizationProvider) Snapshot(personalization.ContextKey) personalization.Snapshot {
	return provider.snapshot
}

func (provider fixedPersonalizationProvider) Revision() uint64 {
	return provider.snapshot.Preferences.Version
}

func (provider fixedPersonalizationProvider) Status() personalization.Status {
	return personalization.Status{
		Available: true, Enabled: !provider.snapshot.Preferences.Default(), FeedbackEnabled: provider.snapshot.Preferences.FeedbackEnabled,
		Style: provider.snapshot.Preferences.Style, Version: provider.snapshot.Preferences.Version,
		Revision: provider.snapshot.Preferences.Version, State: "ready",
	}
}

func hasWarning(warnings []Warning, code string) bool {
	for _, warning := range warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}

func containsSource(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func hasAssumptionLanguage(value string) bool {
	lower := strings.ToLower(value)
	return strings.Contains(lower, "genéric") || strings.Contains(lower, "asum")
}
