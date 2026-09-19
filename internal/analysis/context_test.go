package analysis

import (
	"strings"
	"testing"

	"lolengine/internal/catalog"
	"lolengine/internal/domain"
	"lolengine/internal/itemrules"
)

func TestBuildMatchContextUsesPhaseStrategyLaneAndStructuredNeeds(t *testing.T) {
	knowledge := analysisCatalog()
	knowledge.Items[2001] = catalog.Item{ID: 2001, Name: "Armor", Gold: catalog.Gold{Total: 1200}, Stats: map[string]float64{"FlatArmorMod": 75}}
	game := testGame([]domain.Player{
		{Identity: domain.PlayerIdentity{RiotID: "Darius#TOP"}, ChampionName: "Darius", Team: "CHAOS", Position: "TOP", Level: 10, Score: domain.Score{Kills: 1}},
		{Identity: domain.PlayerIdentity{RiotID: "Ahri#MID"}, ChampionName: "Ahri", Team: "CHAOS", Position: "MIDDLE", Level: 14, Score: domain.Score{Kills: 9}, Items: []domain.Item{{ID: 2001, Count: 1}}},
	})
	game.GameTimeSeconds = 600
	rules := contextRules()
	result := BuildMatchContext(game, knowledge, rules, domain.StrategySplitPush)

	if result.Phase != domain.PhaseEarly || result.Strategy != domain.StrategySplitPush {
		t.Fatalf("phase/strategy = %q/%q", result.Phase, result.Strategy)
	}
	if result.LaneOpponent.ChampionName != "Darius" {
		t.Fatalf("lane opponent = %#v", result.LaneOpponent)
	}
	if result.MainThreat.ChampionName != "Ahri" {
		t.Fatalf("main threat = %#v", result.MainThreat)
	}
	if result.Needs[itemrules.SignalHealing] <= result.Needs[itemrules.SignalShielding] {
		t.Fatalf("lane-weighted healing need not reflected: %#v", result.Needs)
	}
	if result.Needs[itemrules.SignalArmor] <= 0 {
		t.Fatalf("enemy armor items not reflected: %#v", result.Needs)
	}
	if result.TraitConfidence >= 1 || !strings.Contains(strings.Join(result.Warnings, "\n"), "perfil estructurado") {
		t.Fatalf("missing trait degradation = confidence %v warnings %#v", result.TraitConfidence, result.Warnings)
	}
}

func TestPhaseForTimeBoundaries(t *testing.T) {
	boundaries := itemrules.PhaseBoundaries{EarlyEndSeconds: 840, MidEndSeconds: 1500}
	tests := []struct {
		name    string
		seconds float64
		want    domain.Phase
	}{
		{name: "negative clamps early", seconds: -1, want: domain.PhaseEarly},
		{name: "before early boundary", seconds: 839.9, want: domain.PhaseEarly},
		{name: "at mid start", seconds: 840, want: domain.PhaseMid},
		{name: "at late start", seconds: 1500, want: domain.PhaseLate},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := PhaseForTime(test.seconds, boundaries); got != test.want {
				t.Fatalf("PhaseForTime(%v) = %q, want %q", test.seconds, got, test.want)
			}
		})
	}
}

func TestBuildMatchContextUsesStructuredItemTraitsNotLocalizedDescriptions(t *testing.T) {
	knowledge := analysisCatalog()
	knowledge.Items[2002] = catalog.Item{
		ID: 2002, Name: "Localized passive", Description: "Otorga un gran escudo y curación.",
		Purchasable: true, AvailableMap11: true, Gold: catalog.Gold{Total: 1000, Purchasable: true},
	}
	game := testGame([]domain.Player{{
		Identity: domain.PlayerIdentity{RiotID: "Darius#TOP"}, ChampionName: "Darius", Team: "CHAOS", Position: "TOP", Level: 10,
		Items: []domain.Item{{ID: 2002, Count: 1}},
	}})
	tests := []struct {
		name          string
		itemTraits    map[int]itemrules.TraitProfile
		wantShielding int
	}{
		{name: "description alone is ignored", wantShielding: 0},
		{name: "structured trait is applied", itemTraits: map[int]itemrules.TraitProfile{2002: {Shielding: 800}}, wantShielding: 800},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rules := contextRules()
			rules.ItemTraits = test.itemTraits
			result := BuildMatchContext(game, knowledge, rules, domain.StrategyBalanced)
			if got := result.Needs[itemrules.SignalShielding]; got != test.wantShielding {
				t.Fatalf("shielding need = %d, want %d", got, test.wantShielding)
			}
		})
	}
}

func TestBuildMatchContextSeparatesArmorAndMagicResistance(t *testing.T) {
	tests := []struct {
		name            string
		stats           map[string]float64
		wantArmor       int
		wantMagicResist int
	}{
		{name: "physical armor", stats: map[string]float64{"FlatArmorMod": 75}, wantArmor: 500},
		{name: "magic resistance", stats: map[string]float64{"FlatSpellBlockMod": 45}, wantMagicResist: 300},
		{name: "mixed defenses", stats: map[string]float64{"FlatArmorMod": 30, "FlatSpellBlockMod": 60}, wantArmor: 200, wantMagicResist: 400},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			knowledge := analysisCatalog()
			knowledge.Items[2001] = catalog.Item{ID: 2001, Name: "Defense", Gold: catalog.Gold{Total: 1200}, Stats: test.stats}
			game := testGame([]domain.Player{{
				Identity: domain.PlayerIdentity{RiotID: "Darius#TOP"}, ChampionName: "Darius", Team: "CHAOS", Position: "TOP", Level: 10,
				Items: []domain.Item{{ID: 2001, Count: 1}},
			}})
			result := BuildMatchContext(game, knowledge, contextRules(), domain.StrategyBalanced)
			if got := result.Needs[itemrules.SignalArmor]; got != test.wantArmor {
				t.Fatalf("armor need = %d, want %d; needs=%#v", got, test.wantArmor, result.Needs)
			}
			if got := result.Needs[itemrules.SignalMagicResist]; got != test.wantMagicResist {
				t.Fatalf("magic-resist need = %d, want %d; needs=%#v", got, test.wantMagicResist, result.Needs)
			}
		})
	}
}

func TestBuildMatchContextResolvesChampionDisplayNamesToCanonicalTraitIDs(t *testing.T) {
	tests := []struct {
		name      string
		display   string
		canonical string
		profile   itemrules.TraitProfile
		signal    itemrules.Signal
		want      int
	}{
		{name: "Dr Mundo punctuation", display: "Dr. Mundo", canonical: "DrMundo", profile: itemrules.TraitProfile{Healing: 900}, signal: itemrules.SignalHealing, want: 900},
		{name: "KSante apostrophe", display: "K'Sante", canonical: "KSante", profile: itemrules.TraitProfile{Shielding: 800}, signal: itemrules.SignalShielding, want: 800},
		{name: "Tahm Kench space", display: "Tahm Kench", canonical: "TahmKench", profile: itemrules.TraitProfile{CrowdControl: 700}, signal: itemrules.SignalCrowdControl, want: 700},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			knowledge := analysisCatalog()
			knowledge.Champions[test.canonical] = catalog.Champion{ID: test.canonical, Name: test.display, Tags: []string{"Tank"}}
			rules := contextRules()
			rules.ChampionTraits = map[string]itemrules.TraitProfile{test.canonical: test.profile}
			game := testGame([]domain.Player{{
				Identity: domain.PlayerIdentity{RiotID: test.display + "#TOP"}, ChampionName: test.display,
				Team: "CHAOS", Position: "TOP", Level: 10,
			}})
			result := BuildMatchContext(game, knowledge, rules, domain.StrategyBalanced)
			if got := result.Needs[test.signal]; got != test.want {
				t.Fatalf("need %q = %d, want %d; warnings=%#v", test.signal, got, test.want, result.Warnings)
			}
			if result.TraitConfidence != 1 {
				t.Fatalf("trait confidence = %v, want 1 for canonical alias", result.TraitConfidence)
			}
		})
	}
}

func TestBuildMatchContextCapsConfidenceWhenTraitsAreMissing(t *testing.T) {
	knowledge := analysisCatalog()
	rules := contextRules()
	rules.Context.MissingTraitPenalty = 0 // Defensive behavior must remain safe even if validation is bypassed.
	rules.ChampionTraits = nil
	game := testGame([]domain.Player{{
		Identity: domain.PlayerIdentity{RiotID: "Darius#TOP"}, ChampionName: "Darius",
		Team: "CHAOS", Position: "TOP", Level: 10,
	}})
	result := BuildMatchContext(game, knowledge, rules, domain.StrategyBalanced)
	if result.TraitConfidence >= 1 {
		t.Fatalf("missing traits reported full confidence: %#v", result)
	}
	if !strings.Contains(strings.Join(result.Warnings, "\n"), "menor confianza") {
		t.Fatalf("missing traits warning = %#v", result.Warnings)
	}
}

func contextRules() itemrules.RuleSet {
	return itemrules.RuleSet{
		Phases: itemrules.PhaseBoundaries{EarlyEndSeconds: 840, MidEndSeconds: 1500},
		Context: itemrules.ContextTuning{
			LaneWeightByPhase:    map[domain.Phase]int{domain.PhaseEarly: 800, domain.PhaseMid: 400, domain.PhaseLate: 100},
			MissingTraitPenalty:  300,
			ArmorFullScale:       150,
			MagicResistFullScale: 150,
		},
		ChampionTraits: map[string]itemrules.TraitProfile{
			"Darius": {Healing: 900, CrowdControl: 400, Sustained: 800},
		},
	}
}
