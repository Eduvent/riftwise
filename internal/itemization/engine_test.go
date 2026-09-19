package itemization

import (
	"testing"
	"time"

	"lolengine/internal/catalog"
	"lolengine/internal/domain"
)

func TestEngineSituationalTargets(t *testing.T) {
	tests := []struct {
		name       string
		game       domain.GameState
		mutate     func(*catalog.Catalog)
		wantStatus Status
		wantTarget int
	}{
		{name: "AP heavy", game: engineGame([]domain.Player{engineEnemy("Ahri", 10, 80)}, 10, 80, 900), wantStatus: StatusReady, wantTarget: itemForceNature},
		{name: "AD heavy behind", game: engineGame([]domain.Player{engineEnemy("Darius", 12, 140)}, 9, 70, 900), wantStatus: StatusReady, wantTarget: itemRanduins},
		{name: "mixed ahead", game: engineGame([]domain.Player{engineEnemy("Darius", 10, 80), engineEnemy("Ahri", 10, 80)}, 12, 150, 900), wantStatus: StatusReady, wantTarget: itemStridebreaker},
		{name: "mixed behind", game: engineGame([]domain.Player{engineEnemy("Darius", 12, 140), engineEnemy("Ahri", 12, 140)}, 8, 50, 900), wantStatus: StatusReady, wantTarget: itemSteraks},
		{
			name: "completed target skipped",
			game: func() domain.GameState {
				game := engineGame([]domain.Player{engineEnemy("Ahri", 10, 80)}, 10, 80, 900)
				game.ActivePlayer.Items = []domain.Item{{ID: itemForceNature, Count: 1}}
				return game
			}(),
			wantStatus: StatusReady, wantTarget: itemSpiritVisage,
		},
		{
			name: "invalid catalog candidate skipped",
			game: engineGame([]domain.Player{engineEnemy("Ahri", 10, 80)}, 10, 80, 900),
			mutate: func(value *catalog.Catalog) {
				item := value.Items[itemForceNature]
				item.Purchasable = false
				value.Items[itemForceNature] = item
			},
			wantStatus: StatusReady, wantTarget: itemSpiritVisage,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			knowledge := engineCatalog()
			if test.mutate != nil {
				test.mutate(&knowledge)
			}
			result := (Engine{}).Recommend(time.Unix(100, 0), gameSnapshot(test.game), knowledge)
			if result.Status != test.wantStatus {
				t.Fatalf("status = %q, want %q; result = %#v", result.Status, test.wantStatus, result)
			}
			if result.TargetItem == nil || result.TargetItem.ID != test.wantTarget {
				t.Fatalf("target = %#v, want %d", result.TargetItem, test.wantTarget)
			}
			if result.Alternative == nil || result.Alternative.ID == result.TargetItem.ID {
				t.Fatalf("alternative must be distinct and valid: %#v", result)
			}
		})
	}
}

func TestEngineRecipeAndGold(t *testing.T) {
	tests := []struct {
		name          string
		gold          float64
		owned         []domain.Item
		wantStatus    Status
		wantComponent int
		wantNeeded    int
	}{
		{name: "owned component is consumed", gold: 400, owned: []domain.Item{{ID: 1057, Count: 1}}, wantStatus: StatusReady, wantComponent: 1028},
		{name: "zero gold saves for cheapest", gold: 0, wantStatus: StatusSaveForComponent, wantComponent: 1028, wantNeeded: 400},
		{name: "floor fractional gold", gold: 399.9, wantStatus: StatusSaveForComponent, wantComponent: 1028, wantNeeded: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			game := engineGame([]domain.Player{engineEnemy("Ahri", 10, 80)}, 10, 80, test.gold)
			game.ActivePlayer.Items = test.owned
			result := (Engine{}).Recommend(time.Unix(100, 0), gameSnapshot(game), engineCatalog())
			if result.Status != test.wantStatus || result.NextComponent == nil || result.NextComponent.ID != test.wantComponent || result.GoldNeeded != test.wantNeeded {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}

func TestEngineAllocatesSharedComponentsToImmediateCombine(t *testing.T) {
	game := engineGame([]domain.Player{engineEnemy("Darius", 10, 80), engineEnemy("Ahri", 10, 80)}, 12, 150, 350)
	game.ActivePlayer.Items = []domain.Item{{ID: 1036, Count: 1, Slot: 0}, {ID: 1028, Count: 1, Slot: 1}}
	result := (Engine{}).Recommend(time.Unix(100, 0), gameSnapshot(game), engineCatalog())
	if result.Status != StatusReady || result.TargetItem == nil || result.TargetItem.ID != itemStridebreaker {
		t.Fatalf("target result = %#v", result)
	}
	if result.NextComponent == nil || result.NextComponent.ID != 3044 {
		t.Fatalf("next component = %#v, want Phage allocation", result.NextComponent)
	}
}

func TestEngineInventoryCapacity(t *testing.T) {
	tests := []struct {
		name          string
		owned         []domain.Item
		gold          float64
		wantStatus    Status
		wantComponent int
	}{
		{
			name: "full inventory blocks a new leaf",
			owned: []domain.Item{
				{ID: 9001, Slot: 0}, {ID: 9002, Slot: 1}, {ID: 9003, Slot: 2},
				{ID: 9004, Slot: 3}, {ID: 9005, Slot: 4}, {ID: 9006, Slot: 5},
			},
			gold: 900, wantStatus: StatusInventoryFull, wantComponent: 1057,
		},
		{
			name: "full inventory allows immediate combine",
			owned: []domain.Item{
				{ID: 1057, Slot: 0}, {ID: 1028, Slot: 1}, {ID: 9001, Slot: 2},
				{ID: 9002, Slot: 3}, {ID: 9003, Slot: 4}, {ID: 9004, Slot: 5},
			},
			gold: 1600, wantStatus: StatusReady, wantComponent: itemForceNature,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			game := engineGame([]domain.Player{engineEnemy("Ahri", 10, 80)}, 10, 80, test.gold)
			game.ActivePlayer.Items = test.owned
			result := (Engine{}).Recommend(time.Unix(100, 0), gameSnapshot(game), engineCatalog())
			if result.Status != test.wantStatus || result.NextComponent == nil || result.NextComponent.ID != test.wantComponent {
				t.Fatalf("result = %#v", result)
			}
			if result.InventorySlotsUsed != 6 {
				t.Fatalf("inventory slots = %d, want 6", result.InventorySlotsUsed)
			}
		})
	}
}

func TestEngineRequiresDistinctValidAlternative(t *testing.T) {
	knowledge := engineCatalog()
	for _, id := range []int{itemSpiritVisage, itemSteraks, itemJakSho, itemStridebreaker, itemRanduins, itemDeadMans} {
		delete(knowledge.Items, id)
	}
	game := engineGame([]domain.Player{engineEnemy("Ahri", 10, 80)}, 10, 80, 900)
	result := (Engine{}).Recommend(time.Unix(100, 0), gameSnapshot(game), knowledge)
	if result.Status != StatusError || result.Alternative != nil || result.TargetItem == nil || result.TargetItem.ID != itemForceNature {
		t.Fatalf("single-candidate result = %#v", result)
	}
}

func TestEngineUnsupportedAndLifecycleStates(t *testing.T) {
	tests := []struct {
		name     string
		snapshot domain.Snapshot
		want     Status
	}{
		{name: "waiting", snapshot: domain.Snapshot{Status: domain.StatusWaitingForGame}, want: StatusWaiting},
		{name: "stale", snapshot: domain.Snapshot{Status: domain.StatusStale, Game: pointerGame(engineGame(nil, 10, 0, 0))}, want: StatusStale},
		{name: "non Garen", snapshot: func() domain.Snapshot {
			game := engineGame(nil, 10, 0, 0)
			game.ActivePlayer.ChampionName = "Darius"
			return gameSnapshot(game)
		}(), want: StatusUnsupported},
		{name: "non classic", snapshot: func() domain.Snapshot {
			game := engineGame(nil, 10, 0, 0)
			game.GameMode = "ARAM"
			return gameSnapshot(game)
		}(), want: StatusUnsupported},
		{name: "unknown map zero", snapshot: func() domain.Snapshot {
			game := engineGame(nil, 10, 0, 0)
			game.MapNumber = 0
			return gameSnapshot(game)
		}(), want: StatusUnsupported},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := (Engine{}).Recommend(time.Unix(100, 0), test.snapshot, engineCatalog())
			if result.Status != test.want {
				t.Fatalf("status = %q, want %q", result.Status, test.want)
			}
		})
	}
}

func engineCatalog() catalog.Catalog {
	items := map[int]catalog.Item{}
	component := func(id int, name string, total int) {
		items[id] = catalog.Item{ID: id, Name: name, Purchasable: true, AvailableMap11: true, Gold: catalog.Gold{Base: total, Total: total, Purchasable: true}}
	}
	finished := func(id int, name string, total, base int, from ...int) {
		items[id] = catalog.Item{ID: id, Name: name, Purchasable: true, AvailableMap11: true, Gold: catalog.Gold{Base: base, Total: total, Purchasable: true}, From: from}
	}
	component(1028, "Ruby Crystal", 400)
	component(1057, "Negatron Cloak", 850)
	component(1029, "Cloth Armor", 300)
	component(1036, "Long Sword", 350)
	component(1042, "Dagger", 250)
	component(1018, "Cloak of Agility", 600)
	finished(itemForceNature, "Force of Nature", 2800, 1550, 1057, 1028)
	finished(itemSpiritVisage, "Spirit Visage", 2900, 1650, 1057, 1028)
	finished(itemRanduins, "Randuin's Omen", 2700, 2000, 1029, 1028)
	finished(itemDeadMans, "Dead Man's Plate", 2900, 2200, 1029, 1028)
	finished(itemSteraks, "Sterak's Gage", 3200, 2450, 1036, 1028)
	finished(itemJakSho, "Jak'Sho", 3200, 1950, 1057, 1029, 1028)
	finished(3077, "Tiamat", 1200, 500, 1036, 1036)
	finished(3044, "Phage", 1100, 350, 1028, 1036)
	finished(itemStridebreaker, "Stridebreaker", 3300, 750, 3077, 3044, 1042)
	return catalog.Catalog{
		Patch: "16.18.1", Locale: "es_MX", Items: items,
		Champions: map[string]catalog.Champion{
			"Garen":  {ID: "Garen", Key: "86", Name: "Garen", Tags: []string{"Fighter"}},
			"Darius": {ID: "Darius", Key: "122", Name: "Darius", Tags: []string{"Fighter"}, Spells: []catalog.AbilityText{{Tooltip: "<physicalDamage>damage</physicalDamage>"}}},
			"Ahri":   {ID: "Ahri", Key: "103", Name: "Ahri", Tags: []string{"Mage"}, Spells: []catalog.AbilityText{{Tooltip: "<magicDamage>damage</magicDamage>"}}},
		},
	}
}

func engineGame(enemies []domain.Player, level, creepScore int, gold float64) domain.GameState {
	return domain.GameState{
		GameMode: "CLASSIC", MapNumber: 11,
		ActivePlayer: domain.ActivePlayer{
			Identity: domain.PlayerIdentity{RiotID: "Player#LAS"}, ChampionName: "Garen", Team: "ORDER", Position: "TOP",
			Level: level, CurrentGold: gold, Score: domain.Score{CreepScore: creepScore},
		},
		Players: enemies,
	}
}

func engineEnemy(champion string, level, creepScore int) domain.Player {
	return domain.Player{
		Identity: domain.PlayerIdentity{RiotID: champion + "#LAS"}, ChampionName: champion,
		Team: "CHAOS", Position: "TOP", Level: level, Score: domain.Score{CreepScore: creepScore},
	}
}

func gameSnapshot(game domain.GameState) domain.Snapshot {
	return domain.Snapshot{Status: domain.StatusInGame, Game: &game, Revision: 7}
}

func pointerGame(game domain.GameState) *domain.GameState {
	return &game
}
