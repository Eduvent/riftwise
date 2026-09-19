package itemrules

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lolengine/internal/catalog"
	"lolengine/internal/domain"
)

func TestLoaderResolvesExactThenNonFutureSameMinor(t *testing.T) {
	directory := t.TempDir()
	knowledge := rulesCatalog(1001, 1002, 1003, 1004)
	writeRuleFile(t, directory, validRuleSet("16.18.1"))
	writeRuleFile(t, directory, validRuleSet("16.18.3"))
	loader := Loader{Dir: directory}

	tests := []struct {
		name      string
		patch     string
		wantPatch string
		fallback  bool
		wantErr   bool
	}{
		{name: "exact", patch: "16.18.3", wantPatch: "16.18.3"},
		{name: "non future same minor", patch: "16.18.2", wantPatch: "16.18.1", fallback: true},
		{name: "no arbitrary cross patch fallback", patch: "16.19.1", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			knowledge.Patch = test.patch
			value, status, err := loader.Resolve(test.patch, knowledge)
			if (err != nil) != test.wantErr {
				t.Fatalf("Resolve() error = %v, wantErr %v", err, test.wantErr)
			}
			if test.wantErr {
				return
			}
			if value.Patch != test.wantPatch || status.Patch != test.wantPatch || status.Fallback != test.fallback {
				t.Fatalf("resolved = %#v, status = %#v", value, status)
			}
		})
	}
}

func TestRulePatchParsingRejectsNoncanonicalNumericComponents(t *testing.T) {
	tests := []string{"016.18.1", "16.018.1", "16.18.01"}
	for _, value := range tests {
		t.Run(value, func(t *testing.T) {
			if _, ok := parsePatch(value); ok {
				t.Fatalf("parsePatch(%q) accepted a noncanonical alias", value)
			}
		})
	}
}

func TestRuleLoaderRejectsEquivalentPatchAliasInEitherWriteOrder(t *testing.T) {
	orders := [][]string{{"16.18.1", "16.18.01"}, {"16.18.01", "16.18.1"}}
	for index, order := range orders {
		t.Run(fmt.Sprintf("order_%d", index+1), func(t *testing.T) {
			directory := t.TempDir()
			for _, patch := range order {
				writeRuleFile(t, directory, validRuleSet(patch))
			}
			knowledge := rulesCatalog(1001, 1002, 1003, 1004)
			if _, _, err := (Loader{Dir: directory}).Resolve(knowledge.Patch, knowledge); err == nil || !strings.Contains(err.Error(), "invalid rules patch") {
				t.Fatalf("Resolve() error = %v, want noncanonical alias rejection", err)
			}
		})
	}
}

func TestLoaderRejectsMalformedAndInvalidRules(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		mutate  func(*RuleSet, *catalog.Catalog)
		want    string
	}{
		{name: "unknown field", payload: `{"schema_version":2,"unknown":true}`, want: "unknown field"},
		{name: "trailing JSON", payload: `{"schema_version":2} {}`, want: "trailing"},
		{name: "oversized file", payload: strings.Repeat("x", maximumRuleFileBytes+1), want: "exceeds"},
		{name: "duplicate rule id", mutate: func(value *RuleSet, _ *catalog.Catalog) { value.Candidates[1].ID = value.Candidates[0].ID }, want: "duplicate candidate ID"},
		{name: "duplicate item id", mutate: func(value *RuleSet, _ *catalog.Catalog) { value.Candidates[1].ItemID = value.Candidates[0].ItemID }, want: "duplicate candidate item ID"},
		{name: "invalid signal", mutate: func(value *RuleSet, _ *catalog.Catalog) { value.Candidates[0].Adjustments[0].Signal = "guesswork" }, want: "invalid signal"},
		{name: "invalid strategy", mutate: func(value *RuleSet, _ *catalog.Catalog) { value.Candidates[0].StrategyScores["roam_only"] = 10 }, want: "invalid strategy"},
		{name: "invalid mutual exclusion group", mutate: func(value *RuleSet, _ *catalog.Catalog) { value.Candidates[0].MutualExclusionGroup = "bootz" }, want: "mutual exclusion group"},
		{name: "boots missing exclusion group", mutate: func(value *RuleSet, _ *catalog.Catalog) { value.Candidates[1].MutualExclusionGroup = "" }, want: "boots candidates must use"},
		{name: "non boots in boots group", mutate: func(value *RuleSet, _ *catalog.Catalog) { value.Candidates[0].MutualExclusionGroup = CategoryBoots }, want: "only boots candidates"},
		{name: "invalid bound", mutate: func(value *RuleSet, _ *catalog.Catalog) { value.Candidates[0].Adjustments[0].Minimum = 1001 }, want: "minimum"},
		{name: "unbounded phase", mutate: func(value *RuleSet, _ *catalog.Catalog) { value.Phases.MidEndSeconds = maximumPhaseBoundarySeconds + 1 }, want: "phase bounds"},
		{name: "zero missing trait penalty", mutate: func(value *RuleSet, _ *catalog.Catalog) { value.Context.MissingTraitPenalty = 0 }, want: "missing_trait_penalty"},
		{name: "invalid patch token", mutate: func(value *RuleSet, _ *catalog.Catalog) { value.Patch = "+16.18.1" }, want: "invalid rules patch"},
		{name: "unknown champion trait", mutate: func(value *RuleSet, _ *catalog.Catalog) { value.ChampionTraits["Dariuss"] = TraitProfile{Healing: 500} }, want: "unknown champion trait"},
		{name: "catalog missing item", mutate: func(value *RuleSet, _ *catalog.Catalog) { value.Candidates[0].ItemID = 9999 }, want: "missing from catalog"},
		{name: "not purchasable", mutate: func(_ *RuleSet, knowledge *catalog.Catalog) {
			item := knowledge.Items[1001]
			item.Purchasable = false
			knowledge.Items[1001] = item
		}, want: "not purchasable"},
		{name: "map 11 ineligible", mutate: func(_ *RuleSet, knowledge *catalog.Catalog) {
			item := knowledge.Items[1001]
			item.AvailableMap11 = false
			knowledge.Items[1001] = item
		}, want: "not available on map 11"},
		{name: "item trait not purchasable", mutate: func(value *RuleSet, knowledge *catalog.Catalog) {
			value.ItemTraits[1005] = TraitProfile{Shielding: 200}
			knowledge.Items[1005] = catalog.Item{ID: 1005, Name: "Trait item", Purchasable: true, AvailableMap11: true, Gold: catalog.Gold{Total: 1000, Purchasable: false}}
		}, want: "item trait 1005 is not purchasable"},
		{name: "catalog item key mismatch", mutate: func(_ *RuleSet, knowledge *catalog.Catalog) {
			item := knowledge.Items[1001]
			item.ID = 9999
			knowledge.Items[1001] = item
		}, want: "catalog item key 1001 does not match item ID 9999"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			knowledge := rulesCatalog(1001, 1002, 1003, 1004)
			payload := test.payload
			if payload == "" {
				value := validRuleSet("16.18.1")
				if test.mutate != nil {
					test.mutate(&value, &knowledge)
				}
				payload = mustMarshalRules(t, value)
			}
			if err := os.WriteFile(filepath.Join(directory, "16.18.1.json"), []byte(payload), 0o600); err != nil {
				t.Fatal(err)
			}
			_, _, err := (Loader{Dir: directory}).Resolve("16.18.1", knowledge)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Resolve() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestLoaderRejectsCatalogPatchMismatch(t *testing.T) {
	directory := t.TempDir()
	writeRuleFile(t, directory, validRuleSet("16.18.1"))
	knowledge := rulesCatalog(1001, 1002, 1003, 1004)
	knowledge.Patch = "16.18.2"
	_, _, err := (Loader{Dir: directory}).Resolve("16.18.1", knowledge)
	if err == nil || !strings.Contains(err.Error(), "does not match catalog patch") {
		t.Fatalf("Resolve() error = %v, want catalog patch mismatch", err)
	}
}

func TestManagerKeepsLastKnownGoodThenFallsBackExplicitly(t *testing.T) {
	directory := t.TempDir()
	knowledge := rulesCatalog(1001, 1002, 1003, 1004)
	writeRuleFile(t, directory, validRuleSet("16.18.1"))
	manager := NewManager(directory)

	first := manager.Resolve("16.18.1", knowledge)
	if first.Legacy || first.Status.Source != SourceExternalExact {
		t.Fatalf("first resolution = %#v", first)
	}
	if err := os.WriteFile(filepath.Join(directory, "16.18.1.json"), []byte(`{"bad":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	retained := manager.Reload("16.18.1", knowledge)
	if retained.Legacy || retained.Status.Source != SourceExternalLastKnownGood || retained.Status.LastError == "" {
		t.Fatalf("retained resolution = %#v", retained)
	}
	mismatchedCatalog := knowledge
	mismatchedCatalog.Patch = "16.18.2"
	mismatch := manager.Resolve("16.18.1", mismatchedCatalog)
	if !mismatch.Legacy || mismatch.Status.Source != SourceLegacyBuiltin {
		t.Fatalf("catalog mismatch reused last-known-good rules: %#v", mismatch)
	}

	missing := NewManager(filepath.Join(t.TempDir(), "missing")).Resolve("16.18.1", knowledge)
	if !missing.Legacy || missing.Status.Source != SourceLegacyBuiltin || !missing.Status.Fallback || missing.Status.LastError == "" {
		t.Fatalf("legacy resolution = %#v", missing)
	}
}

func TestManagerKeepsLastKnownGoodWhenReloadHasUnknownChampionTrait(t *testing.T) {
	directory := t.TempDir()
	knowledge := rulesCatalog(1001, 1002, 1003, 1004)
	writeRuleFile(t, directory, validRuleSet("16.18.1"))
	manager := NewManager(directory)
	first := manager.Resolve("16.18.1", knowledge)
	if first.Legacy || first.Status.Source != SourceExternalExact {
		t.Fatalf("first resolution = %#v", first)
	}

	invalid := validRuleSet("16.18.1")
	invalid.RulesetVersion = "garen-v2-typo"
	invalid.ChampionTraits["Dariuss"] = TraitProfile{Healing: 700}
	writeRuleFile(t, directory, invalid)
	retained := manager.Reload("16.18.1", knowledge)
	if retained.Legacy || retained.Status.Source != SourceExternalLastKnownGood || !strings.Contains(retained.Status.LastError, "unknown champion trait") {
		t.Fatalf("retained resolution = %#v", retained)
	}
	if retained.Rules.RulesetVersion != first.Rules.RulesetVersion {
		t.Fatalf("ruleset version = %q, want retained %q", retained.Rules.RulesetVersion, first.Rules.RulesetVersion)
	}
}

func TestBundledGarenV2RulesAreStrictAndCoverP0Signals(t *testing.T) {
	path := filepath.Join("..", "..", "rules", "garen", "v2", "16.18.1.json")
	value, err := decodeRuleFile(path)
	if err != nil {
		t.Fatalf("decode bundled rules: %v", err)
	}
	fixture, err := os.ReadFile(filepath.Join("testdata", "catalog-16.18.1.json"))
	if err != nil {
		t.Fatalf("read bundled catalog fixture: %v", err)
	}
	var knowledge catalog.Catalog
	if err := json.Unmarshal(fixture, &knowledge); err != nil {
		t.Fatalf("decode bundled catalog fixture: %v", err)
	}
	if err := value.Validate(knowledge); err != nil {
		t.Fatalf("validate bundled rules: %v", err)
	}
	expectedCandidates := map[string]int{
		"steelcaps_vs_physical_sustained":    3047,
		"mercs_vs_magic_cc":                  3111,
		"swifties_vs_slows":                  3009,
		"stridebreaker_core":                 6631,
		"dead_mans_split_pressure":           3742,
		"steraks_vs_burst":                   3053,
		"jaksho_vs_sustained_teamfight":      6665,
		"force_of_nature_vs_magic_sustained": 4401,
		"kaenic_vs_magic_burst":              2504,
		"randuins_vs_physical_burst":         3143,
		"thornmail_anti_heal_tank":           3075,
		"chempunk_anti_heal_damage":          6609,
		"serpents_fang_anti_shield":          6695,
		"black_cleaver_vs_armor":             3071,
		"seryldas_vs_extreme_armor":          6694,
	}
	if len(value.Candidates) != len(expectedCandidates) {
		t.Fatalf("bundled candidates = %d, want %d", len(value.Candidates), len(expectedCandidates))
	}
	wantedSignals := map[Signal]bool{
		SignalPhysical: false, SignalMagic: false, SignalHealing: false, SignalShielding: false,
		SignalCrowdControl: false, SignalSlows: false, SignalArmor: false, SignalBurst: false, SignalSustained: false,
		SignalLanePressure: false,
	}
	wantedPhases := map[domain.Phase]bool{domain.PhaseEarly: false, domain.PhaseMid: false, domain.PhaseLate: false}
	wantedStrategies := map[domain.Strategy]bool{
		domain.StrategyBalanced: false, domain.StrategySplitPush: false, domain.StrategyTeamfight: false,
	}
	hasBoots := false
	for _, candidate := range value.Candidates {
		if expectedID, exists := expectedCandidates[candidate.ID]; !exists || candidate.ItemID != expectedID {
			t.Errorf("unexpected bundled candidate mapping %q -> %d", candidate.ID, candidate.ItemID)
		}
		hasBoots = hasBoots || candidate.Category == CategoryBoots
		for _, signal := range candidate.Covers {
			if _, wanted := wantedSignals[signal]; wanted {
				wantedSignals[signal] = true
			}
		}
		for strategy := range candidate.StrategyScores {
			wantedStrategies[strategy] = true
		}
		for phase := range candidate.PhaseScores {
			wantedPhases[phase] = true
		}
	}
	if !hasBoots {
		t.Fatal("bundled rules do not contain upgraded boots")
	}
	for signal, covered := range wantedSignals {
		if !covered {
			t.Errorf("bundled rules do not cover signal %q", signal)
		}
	}
	for strategy, covered := range wantedStrategies {
		if !covered {
			t.Errorf("bundled rules do not cover strategy %q", strategy)
		}
	}
	for phase, covered := range wantedPhases {
		if !covered {
			t.Errorf("bundled rules do not cover phase %q", phase)
		}
	}
}

func validRuleSet(patch string) RuleSet {
	return RuleSet{
		SchemaVersion:    CurrentSchemaVersion,
		RulesetVersion:   "garen-v2-test",
		Champion:         "Garen",
		Patch:            patch,
		AlternativeCount: 2,
		Phases:           PhaseBoundaries{EarlyEndSeconds: 840, MidEndSeconds: 1500},
		Context: ContextTuning{
			LaneWeightByPhase:   map[domain.Phase]int{domain.PhaseEarly: 700, domain.PhaseMid: 400, domain.PhaseLate: 150},
			MissingTraitPenalty: 250,
			ArmorFullScale:      150,
		},
		Stability:      StabilityPolicy{SwitchMargin: 100, ConfirmationSeconds: 10, MinimumHoldSeconds: 20, CriticalThreshold: 800},
		ChampionTraits: map[string]TraitProfile{"Darius": {Healing: 600, CrowdControl: 300, Sustained: 700}},
		ItemTraits:     map[int]TraitProfile{},
		Candidates: []CandidateRule{
			{ID: "one", ItemID: 1001, Category: CategoryCore, BaseScore: 100, Covers: []Signal{SignalHealing}, StrategyScores: map[domain.Strategy]int{domain.StrategyBalanced: 1}, Adjustments: []ScoreAdjustment{{Signal: SignalHealing, Minimum: 500, Add: 200, ReasonCode: "healing"}}},
			{ID: "two", ItemID: 1002, Category: CategoryBoots, MutualExclusionGroup: CategoryBoots, BaseScore: 90, Covers: []Signal{SignalCrowdControl}, StrategyScores: map[domain.Strategy]int{domain.StrategyTeamfight: 2}},
			{ID: "three", ItemID: 1003, Category: CategorySituational, BaseScore: 80, Covers: []Signal{SignalArmor}, StrategyScores: map[domain.Strategy]int{domain.StrategySplitPush: 3}},
			{ID: "four", ItemID: 1004, Category: CategorySituational, BaseScore: 70},
		},
	}
}

func rulesCatalog(ids ...int) catalog.Catalog {
	items := make(map[int]catalog.Item, len(ids))
	for _, id := range ids {
		items[id] = catalog.Item{ID: id, Name: "Item", Purchasable: true, AvailableMap11: true, Gold: catalog.Gold{Total: 1000, Purchasable: true}}
	}
	return catalog.Catalog{Patch: "16.18.1", Locale: "es_MX", Items: items, Champions: map[string]catalog.Champion{
		"Garen":  {ID: "Garen", Key: "86", Name: "Garen"},
		"Darius": {ID: "Darius", Key: "122", Name: "Darius"},
	}}
}

func writeRuleFile(t *testing.T, directory string, value RuleSet) {
	t.Helper()
	path := filepath.Join(directory, value.Patch+".json")
	if err := os.WriteFile(path, []byte(mustMarshalRules(t, value)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustMarshalRules(t *testing.T, value RuleSet) string {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(payload)
}
