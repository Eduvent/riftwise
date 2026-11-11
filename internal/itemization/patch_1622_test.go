package itemization

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"lolengine/internal/catalog"
	"lolengine/internal/domain"
	"lolengine/internal/itemprofiles"
)

func TestPatch1622AhriPracticeDefault(t *testing.T) {
	knowledge := patchProfileCatalog(t, "16.22.1")
	engine := genericEngine()
	now := time.Unix(100, 0)
	// No opponent or installed game version is fabricated. The explicit Ahri
	// default supplies three curated choices even without matchup telemetry.
	game := engineGame(nil, 10, 80, 399)
	game.GameMode = "PRACTICETOOL"
	game.ActivePlayer.ChampionName = "Ahri"
	game.ActivePlayer.Position = "NONE"

	assertProvenance := func(result Result) {
		t.Helper()
		if result.Patch != "16.22.1" || result.ProfileStatus == nil || !result.ProfileStatus.Ready || result.ProfileStatus.Source != itemprofiles.SourceExternalExact || result.ProfileStatus.Version != "generic-v1.16.22.1.1" || result.ProfileStatus.Fallback || result.ProfileStatus.LastError != "" {
			t.Fatalf("current-patch profile status = %#v, patch %q", result.ProfileStatus, result.Patch)
		}
		if result.Profile == nil || result.Profile.ProfileID != "ahri_default" || result.Profile.Level != itemprofiles.KindChampionDefault || result.Profile.ChampionID != "Ahri" || result.Profile.Role != "" || result.Profile.Patch != "16.22.1" || !result.Profile.Fallback {
			t.Fatalf("champion default must preserve unavailable role: %#v", result.Profile)
		}
		if !hasAdvisory(result.Advisories, itemprofiles.WarningRoleUnavailable) || !hasAdvisory(result.Advisories, itemprofiles.WarningChampionDefault) || hasAdvisory(result.Advisories, itemprofiles.WarningPatchFallback) || hasAdvisory(result.Advisories, itemprofiles.WarningLastKnownGood) {
			t.Fatalf("advisories = %#v", result.Advisories)
		}
		if !result.Rules.Ready || result.Rules.Source != "generic_external_exact" || result.Rules.Patch != "16.22.1" || result.Rules.Fallback || result.Rules.Legacy || result.Rules.LastError != "" {
			t.Fatalf("must not use legacy, patch fallback or LKG: %#v", result.Rules)
		}
		if result.Confidence > itemprofiles.ConfidenceChampionDefault || result.Confidence > result.TraitConfidence || result.RunePlan == nil || result.SummonerPlan == nil || result.InitialPlan == nil {
			t.Fatalf("default confidence/plans = %#v", result)
		}
		if result.TargetItem == nil || result.TargetItem.ID != 6653 || result.TargetItem.Cost != 3000 || len(result.RankedOptions) != 3 || len(result.Alternatives) != 2 || result.Alternative == nil {
			t.Fatalf("expected curated Liandry target plus two choices: %#v", result)
		}
		for index, id := range []int{6653, 3089, 3157} {
			if result.RankedOptions[index].Item.ID != id {
				t.Fatalf("ranked choice %d = %#v, want item %d", index, result.RankedOptions[index], id)
			}
		}
		if !reflect.DeepEqual(result.Alternatives, result.RankedOptions[1:]) || result.Alternative.ID != result.Alternatives[0].Item.ID {
			t.Fatalf("alternative projections differ from distinct ranked choices: %#v", result)
		}
	}
	assertComponent := func(result Result, status Status, id, cost, needed, slots int) {
		t.Helper()
		assertProvenance(result)
		if result.Status != status || result.NextComponent == nil || result.NextComponent.ID != id || result.NextComponent.Cost != cost || result.GoldNeeded != needed || result.InventorySlotsUsed != slots {
			t.Fatalf("component/affordability: status %q component %#v needed %d slots %d", result.Status, result.NextComponent, result.GoldNeeded, result.InventorySlotsUsed)
		}
		item := knowledge.Items[id]
		if result.NextComponent.Name != item.Name || result.NextComponent.Image != item.Image || result.TargetItem.Name != knowledge.Items[6653].Name {
			t.Fatalf("component/target must use current catalog fields: %#v", result)
		}
	}

	// Liandry's actual 16.22.1 recipe has two 400-gold leaves. The lower-ID
	// Ruby Crystal is first; owning it moves the next action to Amplifying Tome.
	before := engine.Recommend(now, gameSnapshot(game), knowledge)
	assertComponent(before, StatusSaveForComponent, 1028, 400, 1, 0)
	game.ActivePlayer.CurrentGold = 400
	affordable := engine.Recommend(now, gameSnapshot(game), knowledge)
	assertComponent(affordable, StatusReady, 1028, 400, 0, 0)
	game.ActivePlayer.Items = []domain.Item{{ID: 1028, Count: 1, Slot: 0}}
	game.ActivePlayer.CurrentGold = 0
	afterPurchase := engine.Recommend(now, gameSnapshot(game), knowledge)
	assertComponent(afterPurchase, StatusSaveForComponent, 1052, 400, 400, 1)

	for _, rejected := range []struct {
		name      string
		mode      string
		mapNumber int
	}{
		{name: "wrong mode", mode: "ARAM", mapNumber: 11},
		{name: "wrong map", mode: "PRACTICETOOL", mapNumber: 12},
	} {
		t.Run(rejected.name, func(t *testing.T) {
			invalid := game
			invalid.GameMode, invalid.MapNumber = rejected.mode, rejected.mapNumber
			result := engine.Recommend(now, gameSnapshot(invalid), knowledge)
			if result.Status != StatusUnsupported || result.Reason != "Las recomendaciones de perfiles solo admiten la Grieta del Invocador." || result.TargetItem != nil || result.NextComponent != nil || result.Alternative != nil || len(result.RankedOptions) != 0 || len(result.Alternatives) != 0 {
				t.Fatalf("unsupported mode/map offered advice: %#v", result)
			}
			if after := engine.Recommend(now, gameSnapshot(game), knowledge); !reflect.DeepEqual(after, afterPurchase) {
				t.Fatalf("rejected input changed subsequent valid recommendation: %#v", after)
			}
		})
	}
}

func TestPatch1619AhriDefaultPreserved(t *testing.T) {
	knowledge := patchProfileCatalog(t, "16.19.1")
	game := engineGame(nil, 10, 80, 400)
	game.ActivePlayer.ChampionName = "Ahri"
	game.ActivePlayer.Position = "NONE"
	result := genericEngine().Recommend(time.Unix(100, 0), gameSnapshot(game), knowledge)
	if result.Status != StatusReady || result.TargetItem == nil || result.NextComponent == nil || len(result.RankedOptions) < 3 || len(result.Alternatives) < 2 {
		t.Fatalf("previous default stopped being actionable: %#v", result)
	}
	if result.Profile == nil || result.Profile.ProfileID != "ahri_default" || result.Profile.Level != itemprofiles.KindChampionDefault || result.Profile.Role != "" || result.ProfileStatus == nil || result.ProfileStatus.Source != itemprofiles.SourceExternalExact || result.ProfileStatus.Patch != "16.19.1" || result.ProfileStatus.Fallback || !hasAdvisory(result.Advisories, itemprofiles.WarningRoleUnavailable) {
		t.Fatalf("previous default provenance changed: %#v", result)
	}
}

func patchProfileCatalog(t *testing.T, patch string) catalog.Catalog {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join("..", "itemprofiles", "testdata", "catalog-"+patch+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var knowledge catalog.Catalog
	if err := json.Unmarshal(payload, &knowledge); err != nil {
		t.Fatal(err)
	}
	if knowledge.Patch != patch {
		t.Fatalf("fixture patch = %q, want %q", knowledge.Patch, patch)
	}
	if err := knowledge.Validate(); err != nil {
		t.Fatal(err)
	}
	return knowledge
}
