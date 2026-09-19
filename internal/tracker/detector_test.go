package tracker

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"lolengine/internal/domain"
)

func TestDetectorFullLifecycleIsReconnectSafe(t *testing.T) {
	now := time.Date(2026, 9, 19, 1, 0, 0, 0, time.UTC)
	nextID := 0
	detector := NewDetector(DetectorConfig{
		Now:        func() time.Time { return now },
		StaleAfter: 5 * time.Second,
		EndAfter:   30 * time.Second,
		NewSessionID: func(string, time.Time) string {
			nextID++
			return "match-" + string(rune('0'+nextID))
		},
	})

	game := detectorGame(60, 500)
	game.ActivePlayer.Score.Deaths = 3 // Late attachment must establish a baseline only.
	game.ActivePlayer.Items = []domain.Item{{ID: 1001, Count: 1, Slot: 0}}
	game.Players[0].Score.Deaths = 3
	game.Players[0].Items = append([]domain.Item(nil), game.ActivePlayer.Items...)
	game.SourceEvents = []domain.SourceEvent{{ID: 1, Name: "GameStart", GameTimeSeconds: 0, DedupeKey: "source:1"}}
	started := detector.Observe(game)
	assertEventKinds(t, started.Events, domain.EventMatchStarted)
	if started.Session == nil || started.Session.ID != "match-1" || !started.RecommendationRelevant {
		t.Fatalf("start detection = %#v", started)
	}

	now = now.Add(2 * time.Second)
	tick := cloneDetectorGame(game)
	tick.GameTimeSeconds = 62
	tick.ActivePlayer.CurrentGold = 504
	tick.Players[0].Score.CreepScore++
	unchanged := detector.Observe(tick)
	assertEventKinds(t, unchanged.Events)
	if unchanged.RecommendationRelevant {
		t.Fatal("time, passive gold, and CS-only tick was recommendation relevant")
	}

	now = now.Add(time.Second)
	lost := detector.ObserveFailure(domain.FetchTransport, "connection refused")
	assertEventKinds(t, lost.Events, domain.EventConnectionLost)
	now = now.Add(time.Second)
	repeated := detector.ObserveFailure(domain.FetchTransport, "connection refused")
	assertEventKinds(t, repeated.Events)

	now = now.Add(time.Second)
	reconnectedGame := cloneDetectorGame(tick)
	reconnectedGame.GameTimeSeconds = 65
	reconnected := detector.Observe(reconnectedGame)
	assertEventKinds(t, reconnected.Events, domain.EventConnectionRestored)
	if reconnected.Session.ID != "match-1" {
		t.Fatalf("reconnect changed session to %q", reconnected.Session.ID)
	}

	now = now.Add(2 * time.Second)
	purchasedGame := cloneDetectorGame(reconnectedGame)
	purchasedGame.GameTimeSeconds = 67
	purchasedGame.ActivePlayer.CurrentGold = 104
	purchasedGame.ActivePlayer.Items = []domain.Item{{ID: 1001, Count: 1, Slot: 1}, {ID: 1028, Count: 1, Slot: 0}}
	purchasedGame.Players[0].Items = append([]domain.Item(nil), purchasedGame.ActivePlayer.Items...)
	purchased := detector.Observe(purchasedGame)
	assertEventKinds(t, purchased.Events, domain.EventInventoryChanged, domain.EventItemPurchased, domain.EventReturnedToBase)
	var basePayload map[string]any
	if err := json.Unmarshal(purchased.Events[2].Payload, &basePayload); err != nil {
		t.Fatalf("decode returned_to_base payload: %v", err)
	}
	if basePayload["inventory_event_id"] != purchased.Events[0].ID || basePayload["purchase_event_id"] != purchased.Events[1].ID {
		t.Fatalf("returned_to_base association = %#v", basePayload)
	}

	now = now.Add(2 * time.Second)
	deathGame := cloneDetectorGame(purchasedGame)
	deathGame.GameTimeSeconds = 69
	deathGame.ActivePlayer.Score.Deaths++
	deathGame.ActivePlayer.IsDead = true
	deathGame.ActivePlayer.RespawnTimer = 12
	deathGame.Players[0].Score.Deaths++
	deathGame.Players[0].IsDead = true
	deathGame.Players[0].RespawnTimer = 12
	death := detector.Observe(deathGame)
	assertEventKinds(t, death.Events, domain.EventPlayerDied)

	now = now.Add(time.Second)
	replayed := detector.Observe(cloneDetectorGame(deathGame))
	assertEventKinds(t, replayed.Events)

	now = now.Add(time.Second)
	endedGame := cloneDetectorGame(deathGame)
	endedGame.GameTimeSeconds = 70
	endedGame.SourceEvents = append(endedGame.SourceEvents, domain.SourceEvent{ID: 99, Name: "GameEnd", GameTimeSeconds: 70, DedupeKey: "source:99"})
	ended := detector.Observe(endedGame)
	assertEventKinds(t, ended.Events, domain.EventMatchEnded)
	if ended.Session == nil || ended.Session.State != domain.MatchEnded || ended.Session.EndReason != "terminal_event" {
		t.Fatalf("ended session = %#v", ended.Session)
	}
	now = now.Add(time.Second)
	repeatedTerminal := cloneDetectorGame(endedGame)
	repeatedTerminal.GameTimeSeconds = 71
	repeatedEnd := detector.Observe(repeatedTerminal)
	assertEventKinds(t, repeatedEnd.Events)
	if repeatedEnd.Session.ID != "match-1" || repeatedEnd.Session.State != domain.MatchEnded {
		t.Fatalf("replayed terminal event created session %#v", repeatedEnd.Session)
	}

	now = now.Add(time.Minute)
	nextGame := detectorGame(5, 500)
	nextGame.ActivePlayer.Identity.RiotID = "Player#LAS"
	nextGame.Players[1].ChampionName = "Darius"
	next := detector.Observe(nextGame)
	assertEventKinds(t, next.Events, domain.EventMatchStarted)
	if next.Session == nil || next.Session.ID != "match-2" {
		t.Fatalf("next session = %#v", next.Session)
	}

	seenIDs := make(map[string]struct{})
	for _, event := range detector.RecentEvents() {
		if _, duplicate := seenIDs[event.ID]; duplicate {
			t.Fatalf("duplicate semantic event ID %q", event.ID)
		}
		seenIDs[event.ID] = struct{}{}
	}
}

func TestDetectorPersistsIdentityFreePlayerCheckpointOnEveryObservation(t *testing.T) {
	now := time.Date(2026, 9, 19, 6, 0, 0, 0, time.UTC)
	detector := NewDetector(DetectorConfig{
		Now: func() time.Time { return now }, StaleAfter: 5 * time.Second, EndAfter: 30 * time.Second,
		NewSessionID: func(string, time.Time) string { return "match-checkpoint" },
	})
	game := detectorGame(600, 500)
	game.ActivePlayer.Level = 11
	game.ActivePlayer.Score = domain.Score{Kills: 2, Deaths: 1, Assists: 3, CreepScore: 101}
	game.ActivePlayer.Items = []domain.Item{
		{ID: 2003, Count: 1, Slot: 5}, {ID: 1001, Count: 1, Slot: 2}, {ID: 2003, Count: 2, Slot: 1},
	}
	game.Players[0].Level = game.ActivePlayer.Level
	game.Players[0].Score = game.ActivePlayer.Score
	game.Players[0].Items = append([]domain.Item(nil), game.ActivePlayer.Items...)
	first := detector.Observe(game)
	checkpoint := first.Sessions[len(first.Sessions)-1].PlayerCheckpoint
	if checkpoint == nil {
		t.Fatal("player checkpoint is nil")
	}
	if checkpoint.MatchID != "match-checkpoint" || checkpoint.ChampionName != "Garen" || !checkpoint.RoleKnown || checkpoint.Role != domain.RoleTop {
		t.Fatalf("checkpoint identity-free context = %+v", checkpoint)
	}
	if checkpoint.Level != 11 || checkpoint.Kills != 2 || checkpoint.Deaths != 1 || checkpoint.Assists != 3 || checkpoint.CreepScore != 101 {
		t.Fatalf("checkpoint stats = %+v", checkpoint)
	}
	wantInventory := []domain.InventoryCount{{ItemID: 1001, Count: 1}, {ItemID: 2003, Count: 3}}
	if !reflect.DeepEqual(checkpoint.Inventory, wantInventory) {
		t.Fatalf("checkpoint inventory = %+v, want %+v", checkpoint.Inventory, wantInventory)
	}

	now = now.Add(2 * time.Second)
	tick := cloneDetectorGame(game)
	tick.GameTimeSeconds = 602
	tick.ActivePlayer.Score.CreepScore = 102
	tick.Players[0].Score.CreepScore = 102
	result := detector.Observe(tick)
	if result.RecommendationRelevant {
		t.Fatal("CS-only tick unexpectedly changed recommendation relevance")
	}
	checkpoint = result.Sessions[len(result.Sessions)-1].PlayerCheckpoint
	if checkpoint.CreepScore != 102 || !checkpoint.ObservedAt.Equal(now) {
		t.Fatalf("time-only checkpoint was not refreshed: %+v", checkpoint)
	}
}

func TestDetectorInventoryDiffIgnoresSlotsAndClassifiesRemoval(t *testing.T) {
	now := time.Date(2026, 9, 19, 2, 0, 0, 0, time.UTC)
	detector := NewDetector(DetectorConfig{
		Now: func() time.Time { return now }, StaleAfter: time.Second, EndAfter: time.Minute,
		NewSessionID: func(string, time.Time) string { return "inventory-match" },
	})
	game := detectorGame(10, 500)
	game.ActivePlayer.Items = []domain.Item{{ID: 2003, Count: 2, Slot: 0}, {ID: 1001, Count: 1, Slot: 1}}
	game.Players[0].Items = append([]domain.Item(nil), game.ActivePlayer.Items...)
	detector.Observe(game)

	now = now.Add(time.Second)
	reordered := cloneDetectorGame(game)
	reordered.GameTimeSeconds++
	reordered.ActivePlayer.Items[0].Slot = 1
	reordered.ActivePlayer.Items[1].Slot = 0
	reordered.Players[0].Items = append([]domain.Item(nil), reordered.ActivePlayer.Items...)
	assertEventKinds(t, detector.Observe(reordered).Events)

	now = now.Add(time.Second)
	removed := cloneDetectorGame(reordered)
	removed.GameTimeSeconds++
	removed.ActivePlayer.Items = []domain.Item{{ID: 2003, Count: 1, Slot: 1}, {ID: 1001, Count: 1, Slot: 0}}
	removed.Players[0].Items = append([]domain.Item(nil), removed.ActivePlayer.Items...)
	result := detector.Observe(removed)
	assertEventKinds(t, result.Events, domain.EventInventoryChanged)
	var payload map[string]any
	if err := json.Unmarshal(result.Events[0].Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload["classification"] != "removal_or_consumption" {
		t.Fatalf("classification = %#v", payload["classification"])
	}
	if payload["classification_source"] != string(domain.EventSourceInferred) || payload["classification_confidence"].(float64) >= 1 {
		t.Fatalf("classification provenance = %#v", payload)
	}
}

func TestDetectorInventoryCountsSalesAndCombinesConservatively(t *testing.T) {
	tests := []struct {
		name           string
		before         []domain.Item
		after          []domain.Item
		beforeGold     float64
		afterGold      float64
		wantKinds      []domain.EventKind
		classification string
		wantAddedCount float64
	}{
		{
			name: "stack count acquisition", before: []domain.Item{{ID: 2003, Count: 1}}, after: []domain.Item{{ID: 2003, Count: 2}},
			beforeGold: 500, afterGold: 450, wantKinds: []domain.EventKind{domain.EventInventoryChanged, domain.EventItemPurchased, domain.EventReturnedToBase},
			classification: "acquisition", wantAddedCount: 1,
		},
		{
			name: "sale or undo", before: []domain.Item{{ID: 1028, Count: 1}}, after: nil,
			beforeGold: 100, afterGold: 280, wantKinds: []domain.EventKind{domain.EventInventoryChanged}, classification: "sale_or_undo",
		},
		{
			name: "component combine", before: []domain.Item{{ID: 1036, Count: 1}, {ID: 1028, Count: 1}}, after: []domain.Item{{ID: 3044, Count: 1}},
			beforeGold: 500, afterGold: 100, wantKinds: []domain.EventKind{domain.EventInventoryChanged, domain.EventItemPurchased, domain.EventReturnedToBase},
			classification: "component_combine_or_replacement", wantAddedCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 9, 19, 2, 30, 0, 0, time.UTC)
			detector := NewDetector(DetectorConfig{
				Now: func() time.Time { return now }, StaleAfter: time.Second, EndAfter: time.Minute,
				NewSessionID: func(string, time.Time) string { return "inventory-table" },
			})
			before := detectorGame(10, tt.beforeGold)
			before.ActivePlayer.Items = append([]domain.Item(nil), tt.before...)
			before.Players[0].Items = append([]domain.Item(nil), tt.before...)
			detector.Observe(before)

			now = now.Add(time.Second)
			after := cloneDetectorGame(before)
			after.GameTimeSeconds++
			after.ActivePlayer.CurrentGold = tt.afterGold
			after.ActivePlayer.Items = append([]domain.Item(nil), tt.after...)
			after.Players[0].Items = append([]domain.Item(nil), tt.after...)
			result := detector.Observe(after)
			assertEventKinds(t, result.Events, tt.wantKinds...)
			var payload map[string]any
			if err := json.Unmarshal(result.Events[0].Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload["classification"] != tt.classification || payload["classification_source"] != string(domain.EventSourceInferred) {
				t.Fatalf("classification payload = %#v", payload)
			}
			if tt.wantAddedCount > 0 {
				added := payload["added"].([]any)
				if len(added) != 1 || added[0].(map[string]any)["count"] != tt.wantAddedCount {
					t.Fatalf("added counts = %#v", added)
				}
			}
			if len(result.Events) > 1 && (result.Events[1].Source != domain.EventSourceInferred || result.Events[1].Confidence >= 1) {
				t.Fatalf("purchase provenance = %#v", result.Events[1])
			}
		})
	}
}

func TestDetectorRepeatedIdenticalPurchasesHaveDistinctOccurrenceKeys(t *testing.T) {
	now := time.Date(2026, 9, 19, 2, 45, 0, 0, time.UTC)
	detector := NewDetector(DetectorConfig{
		Now: func() time.Time { return now }, StaleAfter: time.Second, EndAfter: time.Minute,
		NewSessionID: func(string, time.Time) string { return "repeat-purchase" },
	})
	baseline := detectorGame(10, 500)
	detector.Observe(baseline)

	now = now.Add(time.Second)
	firstPurchase := cloneDetectorGame(baseline)
	firstPurchase.GameTimeSeconds = 11
	firstPurchase.ActivePlayer.CurrentGold = 450
	firstPurchase.ActivePlayer.Items = []domain.Item{{ID: 2003, Count: 1}}
	firstPurchase.Players[0].Items = append([]domain.Item(nil), firstPurchase.ActivePlayer.Items...)
	first := detector.Observe(firstPurchase)
	assertEventKinds(t, first.Events, domain.EventInventoryChanged, domain.EventItemPurchased, domain.EventReturnedToBase)

	now = now.Add(time.Second)
	consumed := cloneDetectorGame(firstPurchase)
	consumed.GameTimeSeconds = 12
	consumed.ActivePlayer.Items = nil
	consumed.Players[0].Items = nil
	detector.Observe(consumed)

	now = now.Add(time.Second)
	secondPurchase := cloneDetectorGame(consumed)
	secondPurchase.GameTimeSeconds = 13
	secondPurchase.ActivePlayer.CurrentGold = 400
	secondPurchase.ActivePlayer.Items = []domain.Item{{ID: 2003, Count: 1}}
	secondPurchase.Players[0].Items = append([]domain.Item(nil), secondPurchase.ActivePlayer.Items...)
	second := detector.Observe(secondPurchase)
	assertEventKinds(t, second.Events, domain.EventInventoryChanged, domain.EventItemPurchased, domain.EventReturnedToBase)

	for index := range first.Events {
		if first.Events[index].DedupeKey == second.Events[index].DedupeKey {
			t.Fatalf("repeated occurrence reused %q for %s", first.Events[index].DedupeKey, first.Events[index].Kind)
		}
	}
}

func TestDetectorSessionBoundaryUsesMaterialRollback(t *testing.T) {
	now := time.Date(2026, 9, 19, 3, 0, 0, 0, time.UTC)
	ids := []string{"first", "second"}
	detector := NewDetector(DetectorConfig{
		Now: func() time.Time { return now }, StaleAfter: time.Second, EndAfter: time.Minute,
		NewSessionID: func(string, time.Time) string { id := ids[0]; ids = ids[1:]; return id },
	})
	base := detectorGame(100, 500)
	detector.Observe(base)

	now = now.Add(time.Second)
	small := cloneDetectorGame(base)
	small.GameTimeSeconds = 95
	result := detector.Observe(small)
	if result.Session.ID != "first" {
		t.Fatalf("small rollback started %q", result.Session.ID)
	}

	now = now.Add(time.Second)
	large := cloneDetectorGame(small)
	large.GameTimeSeconds = 10
	result = detector.Observe(large)
	assertEventKinds(t, result.Events, domain.EventMatchEnded, domain.EventMatchStarted)
	if result.Session.ID != "second" {
		t.Fatalf("material rollback kept %q", result.Session.ID)
	}
}

func TestDetectorGameStartEvidenceUpgradeDoesNotSplitSessionAcrossRestart(t *testing.T) {
	now := time.Date(2026, 9, 19, 3, 30, 0, 0, time.UTC)
	first := NewDetector(DetectorConfig{
		Now: func() time.Time { return now }, StaleAfter: time.Second, EndAfter: time.Minute,
		NewSessionID: func(string, time.Time) string { return "evidence-match" },
	})
	baseline := detectorGame(10, 500)
	started := first.Observe(baseline)

	now = now.Add(2 * time.Second)
	restarted := NewDetector(DetectorConfig{
		Now: func() time.Time { return now }, StaleAfter: time.Second, EndAfter: time.Minute,
		InitialSession: started.Session,
		NewSessionID: func(string, time.Time) string {
			t.Fatal("GameStart evidence upgrade created a new session")
			return "unexpected"
		},
	})
	withEvidence := cloneDetectorGame(baseline)
	withEvidence.GameTimeSeconds = 12
	withEvidence.SourceEvents = []domain.SourceEvent{{ID: 1, Name: "GameStart", GameTimeSeconds: 0, DedupeKey: "source:1"}}
	result := restarted.Observe(withEvidence)
	assertEventKinds(t, result.Events)
	if result.Session == nil || result.Session.ID != "evidence-match" || result.Session.State != domain.MatchActive {
		t.Fatalf("evidence upgrade split session: %#v", result.Session)
	}
}

func TestDetectorGameVersionEvidenceUpgradeDoesNotSplitSessionAcrossRestart(t *testing.T) {
	now := time.Date(2026, 9, 19, 3, 40, 0, 0, time.UTC)
	first := NewDetector(DetectorConfig{
		Now: func() time.Time { return now }, StaleAfter: time.Second, EndAfter: time.Minute,
		NewSessionID: func(string, time.Time) string { return "version-match" },
	})
	baseline := detectorGame(10, 500)
	baseline.GameVersion = ""
	started := first.Observe(baseline)

	now = now.Add(2 * time.Second)
	restarted := NewDetector(DetectorConfig{
		Now: func() time.Time { return now }, StaleAfter: time.Second, EndAfter: time.Minute,
		InitialSession: started.Session,
		NewSessionID: func(string, time.Time) string {
			t.Fatal("GameVersion evidence upgrade created a new session")
			return "unexpected"
		},
	})
	withVersion := cloneDetectorGame(baseline)
	withVersion.GameTimeSeconds = 12
	withVersion.GameVersion = "16.18.1"
	result := restarted.Observe(withVersion)
	assertEventKinds(t, result.Events)
	if result.Session == nil || result.Session.ID != "version-match" || result.Session.GameVersion != "16.18.1" {
		t.Fatalf("version evidence upgrade split or failed to strengthen session: %#v", result.Session)
	}
}

func TestDetectorRecoveredActiveSessionProcessesTerminalBaselineEvent(t *testing.T) {
	now := time.Date(2026, 9, 19, 3, 50, 0, 0, time.UTC)
	first := NewDetector(DetectorConfig{
		Now: func() time.Time { return now }, StaleAfter: time.Second, EndAfter: time.Minute,
		NewSessionID: func(string, time.Time) string { return "recovered-terminal" },
	})
	game := detectorGame(100, 500)
	started := first.Observe(game)

	now = now.Add(time.Second)
	restarted := NewDetector(DetectorConfig{
		Now: func() time.Time { return now }, StaleAfter: time.Second, EndAfter: time.Minute,
		InitialSession: started.Session,
	})
	terminal := cloneDetectorGame(game)
	terminal.GameTimeSeconds = 101
	terminal.SourceEvents = []domain.SourceEvent{{ID: 99, Name: "GameEnd", GameTimeSeconds: 101, DedupeKey: "source:99"}}
	result := restarted.Observe(terminal)
	assertEventKinds(t, result.Events, domain.EventMatchEnded)
	if result.Session == nil || result.Session.State != domain.MatchEnded {
		t.Fatalf("recovered terminal session = %#v", result.Session)
	}
}

func TestDetectorConservativeEndRequiresUnavailableGrace(t *testing.T) {
	tests := []struct {
		name      string
		kind      domain.FetchErrorClass
		wantEnded bool
	}{
		{name: "game unavailable ends after grace", kind: domain.FetchGameUnavailable, wantEnded: true},
		{name: "transport outage remains recoverable", kind: domain.FetchTransport, wantEnded: false},
		{name: "invalid payload remains recoverable", kind: domain.FetchInvalidPayload, wantEnded: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 9, 19, 4, 0, 0, 0, time.UTC)
			detector := NewDetector(DetectorConfig{
				Now: func() time.Time { return now }, StaleAfter: 5 * time.Second, EndAfter: 30 * time.Second,
				NewSessionID: func(string, time.Time) string { return "grace-match" },
			})
			detector.Observe(detectorGame(10, 500))
			now = now.Add(time.Second)
			assertEventKinds(t, detector.ObserveFailure(tt.kind, "failure").Events, domain.EventConnectionLost)
			now = now.Add(31 * time.Second)
			result := detector.ObserveFailure(tt.kind, "failure")
			if tt.wantEnded {
				assertEventKinds(t, result.Events, domain.EventMatchEnded)
			} else {
				assertEventKinds(t, result.Events)
				if result.Session.State != domain.MatchActive {
					t.Fatalf("session state = %q", result.Session.State)
				}
			}
		})
	}
}

func TestNormalizedSourceEventsResolvesDuplicateKeysDeterministically(t *testing.T) {
	alpha := domain.SourceEvent{ID: 7, Name: "Alpha", GameTimeSeconds: 20, DedupeKey: "source:7", Payload: json.RawMessage(`{"value":"same"}`)}
	zeta := domain.SourceEvent{ID: 7, Name: "Zeta", GameTimeSeconds: 20, DedupeKey: "source:7", Payload: json.RawMessage(`{"value":"same"}`)}
	forward := normalizedSourceEvents([]domain.SourceEvent{zeta, alpha})
	reversed := normalizedSourceEvents([]domain.SourceEvent{alpha, zeta})
	if len(forward) != 1 || len(reversed) != 1 || forward[0].Name != reversed[0].Name {
		t.Fatalf("duplicate normalization depends on input order: forward=%#v reversed=%#v", forward, reversed)
	}
}

func detectorGame(gameTime, gold float64) domain.GameState {
	self := domain.Player{
		Identity: domain.PlayerIdentity{RiotID: "Player#LAS"}, ChampionName: "Garen", Team: "ORDER", Position: "TOP",
		Level: 10, Score: domain.Score{CreepScore: 90}, Items: []domain.Item{},
	}
	return domain.GameState{
		GameTimeSeconds: gameTime, GameMode: "CLASSIC", GameVersion: "16.18.1", MapNumber: 11, MapName: "Summoner's Rift",
		ActivePlayer: domain.ActivePlayer{
			Identity: self.Identity, ChampionName: self.ChampionName, Team: self.Team, Position: self.Position,
			Level: self.Level, CurrentGold: gold, Score: self.Score, Items: []domain.Item{},
		},
		Players: []domain.Player{self, {
			Identity: domain.PlayerIdentity{RiotID: "Opponent#LAS"}, ChampionName: "Ahri", Team: "CHAOS", Position: "TOP",
			Level: 10, Score: domain.Score{CreepScore: 90}, Items: []domain.Item{},
		}},
	}
}

func cloneDetectorGame(source domain.GameState) domain.GameState {
	payload, _ := json.Marshal(source)
	var clone domain.GameState
	_ = json.Unmarshal(payload, &clone)
	return clone
}

func assertEventKinds(t *testing.T, events []domain.SemanticEvent, want ...domain.EventKind) {
	t.Helper()
	if len(events) != len(want) {
		t.Fatalf("event kinds = %#v, want %#v", eventKinds(events), want)
	}
	for index := range want {
		if events[index].Kind != want[index] {
			t.Fatalf("event kinds = %#v, want %#v", eventKinds(events), want)
		}
	}
}

func eventKinds(events []domain.SemanticEvent) []domain.EventKind {
	result := make([]domain.EventKind, len(events))
	for index, event := range events {
		result[index] = event.Kind
	}
	return result
}
