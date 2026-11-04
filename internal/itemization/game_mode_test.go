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
)

func TestEngineGameMode(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("..", "itemrules", "testdata", "catalog-16.18.1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var garenKnowledge catalog.Catalog
	if err := json.Unmarshal(payload, &garenKnowledge); err != nil {
		t.Fatal(err)
	}
	for _, route := range []struct {
		name      string
		engine    Engine
		knowledge catalog.Catalog
		champion  string
		position  string
	}{
		{name: "legacy Garen", engine: Engine{}, knowledge: engineCatalog(), champion: "Garen", position: "TOP"},
		{name: "v2 Garen", engine: genericEngine(), knowledge: garenKnowledge, champion: "Garen", position: "TOP"},
		{name: "generic Ahri", engine: genericEngine(), knowledge: genericEngineCatalog(t), champion: "Ahri", position: "MIDDLE"},
	} {
		t.Run(route.name, func(t *testing.T) {
			now := time.Unix(100, 0)
			game := engineGame([]domain.Player{engineEnemy("Darius", 10, 80)}, 10, 80, 3000)
			game.ActivePlayer.ChampionName = route.champion
			game.ActivePlayer.Position = route.position
			classic := route.engine.Recommend(now, gameSnapshot(game), route.knowledge)
			if classic.Status != StatusReady || classic.TargetItem == nil || classic.NextComponent == nil || classic.Alternative == nil {
				t.Fatalf("CLASSIC map 11 must remain actionable: %#v", classic)
			}
			if route.name == "v2 Garen" && (classic.Rules.Legacy || !classic.Rules.Ready || classic.Rules.Version == "" || len(classic.RankedOptions) < 3 || classic.Profile != nil) {
				t.Fatalf("Garen must use rule-backed v2, not legacy or generic: %#v", classic)
			}
			if route.name == "generic Ahri" && (classic.Profile == nil || classic.Profile.ProfileID != "ahri_middle" || len(classic.RankedOptions) < 3) {
				t.Fatalf("Ahri must use its generic profile: %#v", classic)
			}

			t.Run("PRACTICETOOL map 11 equals CLASSIC", func(t *testing.T) {
				practice := game
				practice.GameMode = "PRACTICETOOL"
				result := route.engine.Recommend(now, gameSnapshot(practice), route.knowledge)
				if !reflect.DeepEqual(result, classic) {
					t.Fatalf("PRACTICETOOL differs from CLASSIC: status %q, want %q; result = %#v", result.Status, classic.Status, result)
				}
			})

			for _, unsupported := range []struct {
				name      string
				mode      string
				mapNumber int
			}{
				{name: "ARAM map 11", mode: "ARAM", mapNumber: 11},
				{name: "ARAM map 12", mode: "ARAM", mapNumber: 12},
				{name: "CLASSIC map 0", mode: "CLASSIC", mapNumber: 0},
				{name: "CLASSIC map 12", mode: "CLASSIC", mapNumber: 12},
				{name: "PRACTICETOOL map 0", mode: "PRACTICETOOL", mapNumber: 0},
				{name: "PRACTICETOOL map 12", mode: "PRACTICETOOL", mapNumber: 12},
			} {
				t.Run(unsupported.name, func(t *testing.T) {
					rejected := game
					rejected.GameMode = unsupported.mode
					rejected.MapNumber = unsupported.mapNumber
					result := route.engine.Recommend(now, gameSnapshot(rejected), route.knowledge)
					wantReason := "Este MVP solo admite la Grieta del Invocador."
					if route.champion == "Ahri" {
						wantReason = "Las recomendaciones de perfiles solo admiten la Grieta del Invocador."
					}
					if result.Status != StatusUnsupported || result.Reason != wantReason || result.TargetItem != nil || result.NextComponent != nil || result.Alternative != nil || len(result.RankedOptions) != 0 || len(result.Alternatives) != 0 {
						t.Fatalf("unsupported mode/map must not offer purchases: %#v", result)
					}
					if after := route.engine.Recommend(now, gameSnapshot(game), route.knowledge); !reflect.DeepEqual(after, classic) {
						t.Fatalf("rejected input changed subsequent CLASSIC result: %#v", after)
					}
				})
			}
		})
	}
}
