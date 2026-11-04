package itemization

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"lolengine/internal/catalog"
	"lolengine/internal/domain"
	"lolengine/internal/itemprofiles"
	"lolengine/internal/itemrules"
)

func TestLiveSettTopUsesPatchExactCuratedProfile(t *testing.T) {
	knowledge := loadSettLiveCatalog(t)
	game := engineGame([]domain.Player{engineEnemy("Amumu", 10, 80)}, 10, 80, 3500)
	game.ActivePlayer.ChampionName = "Sett"
	game.ActivePlayer.Position = "TOP"
	result := genericEngine().Recommend(time.Unix(100, 0), gameSnapshot(game), knowledge)
	if result.Status != StatusReady && result.Status != StatusSaveForComponent {
		t.Fatalf("Sett live status = %q; result=%#v", result.Status, result)
	}
	if result.Profile == nil || result.Profile.ProfileID != "sett_top" || result.Profile.Level != itemprofiles.KindChampionRole || result.Profile.Fallback {
		t.Fatalf("Sett live provenance = %#v", result.Profile)
	}
	if result.ProfileStatus == nil || result.ProfileStatus.Patch != "16.19.1" || result.ProfileStatus.Source != itemprofiles.SourceExternalExact {
		t.Fatalf("Sett live profile status = %#v", result.ProfileStatus)
	}
	if result.TargetItem == nil || len(result.RankedOptions) < 3 || len(result.Alternatives) < 2 {
		t.Fatalf("Sett live choice set = target %#v, ranked %#v, alternatives %#v", result.TargetItem, result.RankedOptions, result.Alternatives)
	}
}

func TestLiveSettTopPrioritizesMercsAgainstHighControlAndLanePressure(t *testing.T) {
	knowledge := loadSettLiveCatalog(t)
	game := engineGame([]domain.Player{engineEnemy("Leona", 10, 80)}, 10, 80, 3500)
	game.ActivePlayer.ChampionName = "Sett"
	game.ActivePlayer.Position = "TOP"
	result := genericEngine().Recommend(time.Unix(100, 0), gameSnapshot(game), knowledge)
	if result.Needs[itemrules.SignalCrowdControl] < 800 || result.Needs[itemrules.SignalLanePressure] < 800 {
		t.Fatalf("live pressure fixture did not exercise combined case: %#v", result.Needs)
	}
	if result.Status != StatusReady && result.Status != StatusSaveForComponent {
		t.Fatalf("Sett live status = %q", result.Status)
	}
	if result.TargetItem == nil || result.TargetItem.ID != 3111 || len(result.Alternatives) < 2 {
		t.Fatalf("combined-pressure target = %#v; alternatives=%#v", result.TargetItem, result.Alternatives)
	}
}

func loadSettLiveCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join("..", "itemprofiles", "testdata", "catalog-16.19.1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var knowledge catalog.Catalog
	if err := json.Unmarshal(payload, &knowledge); err != nil {
		t.Fatal(err)
	}
	return knowledge
}
