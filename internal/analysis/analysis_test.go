package analysis

import (
	"math"
	"testing"

	"lolengine/internal/catalog"
	"lolengine/internal/domain"
)

func TestAnalyzeEnemiesDamageMix(t *testing.T) {
	knowledge := analysisCatalog()
	tests := []struct {
		name          string
		enemies       []domain.Player
		wantDominant  string
		wantThreat    string
		maxConfidence float64
	}{
		{name: "physical heavy", enemies: []domain.Player{enemy("Darius", 10, domain.Score{Kills: 2})}, wantDominant: "physical", wantThreat: "Darius"},
		{name: "magic heavy", enemies: []domain.Player{enemy("Ahri", 10, domain.Score{Kills: 2})}, wantDominant: "magic", wantThreat: "Ahri"},
		{name: "mixed", enemies: []domain.Player{enemy("Darius", 10, domain.Score{}), enemy("Ahri", 10, domain.Score{})}, wantDominant: "mixed"},
		{name: "fed threat", enemies: []domain.Player{enemy("Darius", 9, domain.Score{Kills: 1, Deaths: 3}), enemy("Ahri", 13, domain.Score{Kills: 10, Assists: 4, CreepScore: 180})}, wantDominant: "magic", wantThreat: "Ahri"},
		{name: "unknown champion", enemies: []domain.Player{enemy("Unknown", 10, domain.Score{})}, wantDominant: "mixed", wantThreat: "Unknown", maxConfidence: 0.35},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			game := testGame(test.enemies)
			result := AnalyzeEnemies(game, knowledge)
			sum := result.DamageMix.Physical + result.DamageMix.Magic + result.DamageMix.True
			if math.Abs(sum-1) > 0.0001 {
				t.Fatalf("damage mix sum = %v, mix = %#v", sum, result.DamageMix)
			}
			switch test.wantDominant {
			case "physical":
				if result.DamageMix.Physical <= 0.55 {
					t.Fatalf("physical mix = %#v", result.DamageMix)
				}
			case "magic":
				if result.DamageMix.Magic <= 0.55 {
					t.Fatalf("magic mix = %#v", result.DamageMix)
				}
			case "mixed":
				if math.Abs(result.DamageMix.Physical-result.DamageMix.Magic) > 0.25 {
					t.Fatalf("mixed profile = %#v", result.DamageMix)
				}
			}
			if test.wantThreat != "" && result.MainThreat.ChampionName != test.wantThreat {
				t.Fatalf("main threat = %q, want %q", result.MainThreat.ChampionName, test.wantThreat)
			}
			if test.maxConfidence > 0 && result.Confidence > test.maxConfidence {
				t.Fatalf("confidence = %v, want <= %v", result.Confidence, test.maxConfidence)
			}
		})
	}
}

func TestClassifyPlayerStateBoundaries(t *testing.T) {
	knowledge := analysisCatalog()
	tests := []struct {
		name     string
		active   domain.ActivePlayer
		opponent domain.Player
		want     PlayerState
	}{
		{
			name:     "ahead at threshold",
			active:   domain.ActivePlayer{Team: "ORDER", Position: "TOP", Level: 11, Score: domain.Score{CreepScore: 115}},
			opponent: domain.Player{ChampionName: "Darius", Team: "CHAOS", Position: "TOP", Level: 10, Score: domain.Score{CreepScore: 100}},
			want:     PlayerAhead,
		},
		{
			name:     "behind at threshold",
			active:   domain.ActivePlayer{Team: "ORDER", Position: "TOP", Level: 9, Score: domain.Score{CreepScore: 85}},
			opponent: domain.Player{ChampionName: "Darius", Team: "CHAOS", Position: "TOP", Level: 10, Score: domain.Score{CreepScore: 100}},
			want:     PlayerBehind,
		},
		{
			name:     "inside even boundary",
			active:   domain.ActivePlayer{Team: "ORDER", Position: "TOP", Level: 10, Score: domain.Score{CreepScore: 110}},
			opponent: domain.Player{ChampionName: "Darius", Team: "CHAOS", Position: "TOP", Level: 10, Score: domain.Score{CreepScore: 100}},
			want:     PlayerEven,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			game := domain.GameState{ActivePlayer: test.active, Players: []domain.Player{test.opponent}}
			if got := ClassifyPlayerState(game, knowledge); got != test.want {
				t.Fatalf("ClassifyPlayerState() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestChooseOpponentCanonicalizesExplicitRoleAliasesWithoutGuessing(t *testing.T) {
	knowledge := analysisCatalog()
	laner := domain.Player{
		Identity:     domain.PlayerIdentity{RiotID: "Ahri#MID"},
		ChampionName: "Ahri",
		Team:         "CHAOS",
		Position:     "MIDDLE",
		Level:        10,
	}
	fedTop := domain.Player{
		Identity:     domain.PlayerIdentity{RiotID: "Darius#TOP"},
		ChampionName: "Darius",
		Team:         "CHAOS",
		Position:     "TOP",
		Level:        14,
		Score:        domain.Score{Kills: 10, Assists: 5, CreepScore: 180},
	}

	tests := []struct {
		name     string
		position string
		want     string
	}{
		{name: "canonical alias filters the lane opponent", position: "MID", want: "Ahri"},
		{name: "missing role does not infer a lane", position: "", want: "Darius"},
		{name: "unknown role does not infer a lane", position: "ROAM", want: "Darius"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			active := domain.ActivePlayer{Team: "ORDER", Position: test.position, Level: 10}
			got := chooseOpponent(active, []domain.Player{laner, fedTop}, knowledge)
			if got.ChampionName != test.want {
				t.Fatalf("chooseOpponent() = %q, want %q", got.ChampionName, test.want)
			}
		})
	}
}

func analysisCatalog() catalog.Catalog {
	return catalog.Catalog{
		Patch:  "16.18.1",
		Locale: "es_MX",
		Items: map[int]catalog.Item{
			100: {ID: 100, Gold: catalog.Gold{Total: 3000}, Into: []int{}},
		},
		Champions: map[string]catalog.Champion{
			"Darius": {
				ID: "Darius", Name: "Darius", Tags: []string{"Fighter"},
				Spells: []catalog.AbilityText{{Description: "Deals physical damage.", Tooltip: "<physicalDamage>damage</physicalDamage>"}},
			},
			"Ahri": {
				ID: "Ahri", Name: "Ahri", Tags: []string{"Mage"},
				Spells: []catalog.AbilityText{{Description: "Deals magic damage.", Tooltip: "<magicDamage>damage</magicDamage>"}},
			},
		},
	}
}

func testGame(enemies []domain.Player) domain.GameState {
	return domain.GameState{
		ActivePlayer: domain.ActivePlayer{
			Identity: domain.PlayerIdentity{RiotID: "Player#LAS"},
			Team:     "ORDER", Position: "TOP", Level: 10,
		},
		Players: enemies,
	}
}

func enemy(champion string, level int, score domain.Score) domain.Player {
	return domain.Player{
		Identity:     domain.PlayerIdentity{RiotID: champion + "#LAS"},
		ChampionName: champion, Team: "CHAOS", Position: "TOP", Level: level, Score: score,
	}
}
