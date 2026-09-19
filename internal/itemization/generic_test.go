package itemization

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"lolengine/internal/catalog"
	"lolengine/internal/domain"
	"lolengine/internal/itemprofiles"
	"lolengine/internal/itemrules"
)

func TestGenericEngineRoutesNonGarenThroughExactProfile(t *testing.T) {
	knowledge := genericEngineCatalog(t)
	game := engineGame([]domain.Player{engineEnemy("Leona", 10, 80)}, 10, 80, 3000)
	game.ActivePlayer.ChampionName = "Ahri"
	game.ActivePlayer.Position = "MIDDLE"
	result := genericEngine().Recommend(time.Unix(100, 0), gameSnapshot(game), knowledge)

	if result.Status != StatusReady || result.TargetItem == nil {
		t.Fatalf("generic result = %#v", result)
	}
	if result.Profile == nil || result.Profile.ProfileID != "ahri_middle" || result.Profile.Level != itemprofiles.KindChampionRole {
		t.Fatalf("profile provenance = %#v", result.Profile)
	}
	if result.ProfileStatus == nil || !result.ProfileStatus.Ready || result.ProfileStatus.Fallback {
		t.Fatalf("profile status = %#v", result.ProfileStatus)
	}
	if result.RunePlan == nil || result.SummonerPlan == nil {
		t.Fatalf("profile plans were not exposed: %#v", result)
	}
	if result.Confidence > itemprofiles.ConfidenceChampionRole || result.Confidence > result.TraitConfidence {
		t.Fatalf("confidence %.3f exceeds profile/trait cap %.3f/%.3f", result.Confidence, itemprofiles.ConfidenceChampionRole, result.TraitConfidence)
	}
	if strings.Contains(strings.ToLower(result.Reason), "garen") || strings.Contains(strings.ToLower(strings.Join(result.Warnings, "\n")), "win rate") {
		t.Fatalf("generic copy leaked champion/meta claim: %#v", result)
	}
}

func TestGenericEngineSupportsBundledRepresentativeLiveRoutes(t *testing.T) {
	knowledge := genericEngineCatalog(t)
	tests := []struct {
		champion string
		position string
		profile  string
		enemy    string
	}{
		{champion: "Amumu", position: "JUNGLE", profile: "amumu_jungle", enemy: "Ahri"},
		{champion: "Ahri", position: "MID", profile: "ahri_middle", enemy: "Leona"},
		{champion: "Jinx", position: "BOT", profile: "jinx_bottom", enemy: "Amumu"},
		{champion: "Leona", position: "SUP", profile: "leona_support", enemy: "Jinx"},
	}
	for _, test := range tests {
		t.Run(test.profile, func(t *testing.T) {
			game := engineGame([]domain.Player{engineEnemy(test.enemy, 10, 80)}, 10, 80, 3000)
			game.ActivePlayer.ChampionName = test.champion
			game.ActivePlayer.Position = test.position
			result := genericEngine().Recommend(time.Unix(100, 0), gameSnapshot(game), knowledge)
			if result.Status != StatusReady && result.Status != StatusSaveForComponent {
				t.Fatalf("status = %q; result=%#v", result.Status, result)
			}
			if result.Profile == nil || result.Profile.ProfileID != test.profile || result.Profile.Level != itemprofiles.KindChampionRole {
				t.Fatalf("profile provenance = %#v", result.Profile)
			}
			if result.Profile.Role == "" || result.ProfileStatus == nil || result.ProfileStatus.Source != itemprofiles.SourceExternalExact {
				t.Fatalf("profile role/status = %#v / %#v", result.Profile, result.ProfileStatus)
			}
			if result.TargetItem == nil || len(result.RankedOptions) < 3 || result.Alternative == nil || len(result.Alternatives) < 2 {
				t.Fatalf("ranked choice set = target %#v singular %#v ranked %d alternatives %d", result.TargetItem, result.Alternative, len(result.RankedOptions), len(result.Alternatives))
			}
		})
	}
}

func TestGenericEngineFailsClosedForAmbiguousClassTags(t *testing.T) {
	knowledge := genericEngineCatalog(t)
	tests := []struct {
		champion string
		position string
	}{
		{champion: "Zed", position: "MIDDLE"},
		{champion: "Mordekaiser", position: "TOP"},
		{champion: "Soraka", position: "UTILITY"},
	}
	for _, test := range tests {
		t.Run(test.champion, func(t *testing.T) {
			game := engineGame(nil, 10, 80, 3000)
			game.ActivePlayer.ChampionName = test.champion
			game.ActivePlayer.Position = test.position
			result := genericEngine().Recommend(time.Unix(100, 0), gameSnapshot(game), knowledge)
			if result.Status != StatusInsufficientContext || result.TargetItem != nil {
				t.Fatalf("ambiguous fallback became actionable: %#v", result)
			}
			if result.Profile == nil || result.Profile.Level != itemprofiles.KindNeutral || result.Profile.ProfileID != "neutral" {
				t.Fatalf("profile = %#v, want neutral", result.Profile)
			}
			for _, forbidden := range []string{"mage", "marksman", "tank"} {
				if strings.Contains(strings.ToLower(strings.Join(result.Profile.FamilyIDs, " ")), forbidden) {
					t.Fatalf("ambiguous champion reached %q family: %#v", forbidden, result.Profile)
				}
			}
		})
	}
}

func TestGenericPenetrationSignalsMatchEnemyDefenseType(t *testing.T) {
	knowledge := genericEngineCatalog(t)
	recommend := func(champion, position string, enemyItems []domain.Item) Result {
		game := engineGame([]domain.Player{engineEnemy("Leona", 10, 80)}, 10, 80, 3000)
		game.ActivePlayer.ChampionName = champion
		game.ActivePlayer.Position = position
		game.Players[0].Position = position
		game.Players[0].Items = enemyItems
		return genericEngine().Recommend(time.Unix(100, 0), gameSnapshot(game), knowledge)
	}

	physicalArmor := []domain.Item{{ID: 3143, Count: 2}}
	magicResistance := []domain.Item{{ID: 1057, Count: 2}}

	mageVsArmor := recommend("Ahri", "MIDDLE", physicalArmor)
	mageVsMR := recommend("Ahri", "MIDDLE", magicResistance)
	voidArmor := mageVsArmor.DecisionPlans[3135]
	voidMR := mageVsMR.DecisionPlans[3135]
	if mageVsArmor.Needs[itemrules.SignalMagicResist] != 0 || mageVsArmor.Needs[itemrules.SignalArmor] == 0 {
		t.Fatalf("physical armor leaked into MR: %#v", mageVsArmor.Needs)
	}
	if mageVsMR.Needs[itemrules.SignalMagicResist] == 0 || mageVsMR.Needs[itemrules.SignalArmor] != 0 {
		t.Fatalf("magic resistance leaked into armor: %#v", mageVsMR.Needs)
	}
	if voidMR.Ranked.Score <= voidArmor.Ranked.Score || strings.Contains(strings.Join(voidArmor.Ranked.ReasonCodes, " "), "magic_resist") {
		t.Fatalf("Void Staff scores armor=%d MR=%d reasons=%#v/%#v", voidArmor.Ranked.Score, voidMR.Ranked.Score, voidArmor.Ranked.ReasonCodes, voidMR.Ranked.ReasonCodes)
	}
	if !strings.Contains(strings.ToLower(voidMR.Reason), "resistencia mágica") {
		t.Fatalf("Void Staff MR explanation = %q", voidMR.Reason)
	}

	marksmanVsArmor := recommend("Jinx", "BOTTOM", physicalArmor)
	marksmanVsMR := recommend("Jinx", "BOTTOM", magicResistance)
	ldrArmor := marksmanVsArmor.DecisionPlans[3036]
	ldrMR := marksmanVsMR.DecisionPlans[3036]
	if ldrArmor.Ranked.Score <= ldrMR.Ranked.Score {
		t.Fatalf("physical penetration scores armor=%d MR=%d", ldrArmor.Ranked.Score, ldrMR.Ranked.Score)
	}
	if !strings.Contains(strings.ToLower(ldrArmor.Reason), "armadura") {
		t.Fatalf("physical penetration explanation = %q", ldrArmor.Reason)
	}
}

func TestGenericEngineNeutralFallbackFailsClosedWithoutPressure(t *testing.T) {
	knowledge := genericEngineCatalog(t)
	game := engineGame(nil, 10, 80, 3000)
	game.ActivePlayer.ChampionName = "Unresolved Champion"
	game.ActivePlayer.Position = ""
	result := genericEngine().Recommend(time.Unix(100, 0), gameSnapshot(game), knowledge)

	if result.Status != StatusInsufficientContext || result.TargetItem != nil || len(result.RankedOptions) != 0 {
		t.Fatalf("neutral fallback did not fail closed: %#v", result)
	}
	if result.Profile == nil || result.Profile.Level != itemprofiles.KindNeutral {
		t.Fatalf("profile = %#v", result.Profile)
	}
	if !hasAdvisory(result.Advisories, itemprofiles.WarningChampionUnresolved) ||
		!hasAdvisory(result.Advisories, itemprofiles.WarningRoleUnavailable) ||
		!hasAdvisory(result.Advisories, itemprofiles.WarningInsufficientContext) {
		t.Fatalf("advisories = %#v", result.Advisories)
	}
}

func TestGenericEngineNeutralFallbackNeedsThreeReactiveChoices(t *testing.T) {
	knowledge := genericEngineCatalog(t)
	game := engineGame([]domain.Player{engineEnemy("Ahri", 10, 80)}, 10, 80, 3000)
	game.ActivePlayer.ChampionName = "Unresolved Champion"
	game.ActivePlayer.Position = ""
	result := genericEngine().Recommend(time.Unix(100, 0), gameSnapshot(game), knowledge)

	if result.Status == StatusInsufficientContext || result.TargetItem == nil || len(result.RankedOptions) < 3 {
		t.Fatalf("explicit magic pressure did not activate a choice set: %#v", result)
	}
	if result.Profile == nil || result.Profile.ConfidenceCap != itemprofiles.ConfidenceNeutral || result.Confidence > itemprofiles.ConfidenceNeutral {
		t.Fatalf("neutral confidence/provenance = %.3f %#v", result.Confidence, result.Profile)
	}
}

func TestGenericEngineIsDeterministicUnderInventoryPermutation(t *testing.T) {
	knowledge := genericEngineCatalog(t)
	game := engineGame([]domain.Player{engineEnemy("Leona", 10, 80)}, 10, 80, 3000)
	game.ActivePlayer.ChampionName = "Jinx"
	game.ActivePlayer.Position = "ADC"
	game.ActivePlayer.Items = []domain.Item{{ID: 1001, Count: 1, Slot: 0}, {ID: 1042, Count: 1, Slot: 1}}
	first := genericEngine().Recommend(time.Unix(100, 0), gameSnapshot(game), knowledge)
	game.ActivePlayer.Items[0], game.ActivePlayer.Items[1] = game.ActivePlayer.Items[1], game.ActivePlayer.Items[0]
	second := genericEngine().Recommend(time.Unix(100, 0), gameSnapshot(game), knowledge)
	if !reflect.DeepEqual(first, second) {
		left, _ := json.Marshal(first)
		right, _ := json.Marshal(second)
		t.Fatalf("inventory permutation changed result:\n%s\n%s", left, right)
	}
}

func TestGenericEngineUsesChoiceOrientedInventoryCopy(t *testing.T) {
	knowledge := genericEngineCatalog(t)
	game := engineGame([]domain.Player{engineEnemy("Leona", 10, 80)}, 10, 80, 5000)
	game.ActivePlayer.ChampionName = "Jinx"
	game.ActivePlayer.Position = "BOTTOM"
	game.ActivePlayer.Items = []domain.Item{
		{ID: 9001, Count: 1, Slot: 0},
		{ID: 9002, Count: 1, Slot: 1},
		{ID: 9003, Count: 1, Slot: 2},
		{ID: 9004, Count: 1, Slot: 3},
		{ID: 9005, Count: 1, Slot: 4},
		{ID: 9006, Count: 1, Slot: 5},
	}

	result := genericEngine().Recommend(time.Unix(100, 0), gameSnapshot(game), knowledge)
	if result.Status != StatusInventoryFull {
		t.Fatalf("status = %q; result=%#v", result.Status, result)
	}
	lower := strings.ToLower(result.Reason + " " + strings.Join(result.Warnings, " "))
	for _, imperative := range []string{"libera", "compra "} {
		if strings.Contains(lower, imperative) {
			t.Fatalf("generic inventory copy contains imperative %q: %q", imperative, lower)
		}
	}
}

func TestGenericEngineCanBeDisabledAndKeepsGarenPinned(t *testing.T) {
	knowledge := genericEngineCatalog(t)
	engine := genericEngine()
	engine.GenericEnabled = false
	ahri := engineGame(nil, 10, 80, 3000)
	ahri.ActivePlayer.ChampionName = "Ahri"
	ahri.ActivePlayer.Position = "middle"
	disabled := engine.Recommend(time.Unix(100, 0), gameSnapshot(ahri), knowledge)
	if disabled.Status != StatusUnsupported || disabled.Profile != nil {
		t.Fatalf("disabled result = %#v", disabled)
	}

	engine.GenericEnabled = true
	garen := engineGame([]domain.Player{engineEnemy("Ahri", 10, 80)}, 10, 80, 3000)
	pinned := engine.Recommend(time.Unix(100, 0), gameSnapshot(garen), knowledge)
	if pinned.Profile != nil || pinned.Rules.Version == "" || math.IsNaN(pinned.Confidence) {
		t.Fatalf("Garen did not remain on pinned v2 route: %#v", pinned)
	}
}

func genericEngine() Engine {
	return Engine{
		Rules:          itemrules.NewManager(filepath.Join("..", "..", "rules", "garen", "v2")),
		Profiles:       itemprofiles.NewManager(filepath.Join("..", "..", "rules", "generic", "v1")),
		GenericEnabled: true,
		Strategy:       domain.StrategyBalanced,
	}
}

func genericEngineCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join("..", "itemprofiles", "testdata", "catalog-16.18.1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var knowledge catalog.Catalog
	if err := json.Unmarshal(payload, &knowledge); err != nil {
		t.Fatal(err)
	}
	return knowledge
}

func hasAdvisory(values []itemprofiles.Warning, code string) bool {
	for _, value := range values {
		if value.Code == code {
			return true
		}
	}
	return false
}
