package itemization

import (
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"lolengine/internal/catalog"
	"lolengine/internal/domain"
	"lolengine/internal/itemprofiles"
	"lolengine/internal/itemrules"
	"lolengine/internal/personalization"
)

var updateGarenGolden = flag.Bool("update-garen-golden", false, "update Garen v2 compatibility golden files")

func TestBundledGarenRulesChecksum(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("..", "..", "rules", "garen", "v2", "16.18.1.json"))
	if err != nil {
		t.Fatal(err)
	}
	const want = "4136f3cb488cd9bb33ccb9a47613dfcafeb6655e91e5d7caf7c5cbc5083c05d7"
	if got := fmt.Sprintf("%x", sha256.Sum256(payload)); got != want {
		t.Fatalf("bundled Garen rules checksum = %s, want %s", got, want)
	}
}

func TestGarenV2CompatibilityGolden(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("..", "itemrules", "testdata", "catalog-16.18.1.json"))
	if err != nil {
		t.Fatalf("read catalog fixture: %v", err)
	}
	var knowledge catalog.Catalog
	if err := json.Unmarshal(payload, &knowledge); err != nil {
		t.Fatalf("decode catalog fixture: %v", err)
	}
	manager := itemrules.NewManager(filepath.Join("..", "..", "rules", "garen", "v2"))
	engine := Engine{Rules: manager, Strategy: domain.StrategyBalanced}
	now := time.Date(2026, time.September, 19, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		mutate func(*domain.GameState)
	}{
		{name: "early_darius"},
		{name: "mid_magic_burst", mutate: func(game *domain.GameState) {
			game.GameTimeSeconds = 1100
			game.Players = []domain.Player{engineEnemy("Ahri", 13, 130)}
		}},
		{name: "basic_boots_owned", mutate: func(game *domain.GameState) {
			game.ActivePlayer.Items = []domain.Item{{ID: 1001, Count: 1, Slot: 0}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			game := engineGame([]domain.Player{engineEnemy("Darius", 10, 80)}, 10, 80, 1200)
			game.GameTimeSeconds = 500
			if test.mutate != nil {
				test.mutate(&game)
			}
			result := engine.Recommend(now, gameSnapshot(game), knowledge)
			got, err := json.MarshalIndent(result, "", "  ")
			if err != nil {
				t.Fatalf("marshal recommendation: %v", err)
			}
			got = append(got, '\n')
			path := filepath.Join("testdata", "garen-v2-"+test.name+".golden.json")
			if *updateGarenGolden {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatalf("create golden directory: %v", err)
				}
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden: %v", err)
			}
			if string(got) != string(want) {
				t.Fatalf("Garen v2 output changed; run with -update-garen-golden only after intentional compatibility review\n--- got ---\n%s\n--- want ---\n%s", got, want)
			}
		})
	}
}

func TestGenericWiringDoesNotChangePinnedGarenResult(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("..", "itemrules", "testdata", "catalog-16.18.1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var knowledge catalog.Catalog
	if err := json.Unmarshal(payload, &knowledge); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.September, 19, 12, 0, 0, 0, time.UTC)
	game := engineGame([]domain.Player{engineEnemy("Darius", 10, 80)}, 10, 80, 1200)
	game.GameTimeSeconds = 500
	snapshot := gameSnapshot(game)
	rulesDir := filepath.Join("..", "..", "rules", "garen", "v2")

	garenOnly := Engine{Rules: itemrules.NewManager(rulesDir), Strategy: domain.StrategyBalanced}
	combined := Engine{
		Rules: itemrules.NewManager(rulesDir), Profiles: itemprofiles.NewManager(filepath.Join("..", "..", "rules", "generic", "v1")),
		GenericEnabled: true, Strategy: domain.StrategyBalanced,
	}
	want := garenOnly.Recommend(now, snapshot, knowledge)
	got := combined.Recommend(now, snapshot, knowledge)
	if !reflect.DeepEqual(got, want) {
		gotJSON, _ := json.MarshalIndent(got, "", "  ")
		wantJSON, _ := json.MarshalIndent(want, "", "  ")
		t.Fatalf("combined wiring changed Garen output\n--- got ---\n%s\n--- want ---\n%s", gotJSON, wantJSON)
	}
	combined.Personalization = defaultPersonalizationProvider{}
	got = combined.Recommend(now, snapshot, knowledge)
	if !reflect.DeepEqual(got, want) {
		gotJSON, _ := json.MarshalIndent(got, "", "  ")
		wantJSON, _ := json.MarshalIndent(want, "", "  ")
		t.Fatalf("default personalization wiring changed Garen output\n--- got ---\n%s\n--- want ---\n%s", gotJSON, wantJSON)
	}
}

type defaultPersonalizationProvider struct{}

func (defaultPersonalizationProvider) Snapshot(personalization.ContextKey) personalization.Snapshot {
	return personalization.Snapshot{Preferences: personalization.Preferences{Style: personalization.StyleBalanced, Version: 1}}
}

func (defaultPersonalizationProvider) Revision() uint64 { return 1 }

func (defaultPersonalizationProvider) Status() personalization.Status {
	return personalization.Status{Available: true, Style: personalization.StyleBalanced, Version: 1, Revision: 1, State: "ready"}
}
