package state

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"lolengine/internal/domain"
)

func TestStorePublishesOnlyMeaningfulChanges(t *testing.T) {
	started := time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC)
	store := NewStore(domain.Snapshot{Status: domain.StatusWaitingForGame, UpdatedAt: started})
	initial, updates, unsubscribe := store.Subscribe(1)
	defer unsubscribe()

	if initial.Revision != 1 || initial.Status != domain.StatusWaitingForGame {
		t.Fatalf("initial snapshot = %#v", initial)
	}

	polledAt := started.Add(time.Second)
	changed := store.Update(domain.Snapshot{
		Status:        domain.StatusWaitingForGame,
		UpdatedAt:     polledAt,
		LastSuccessAt: &polledAt,
	})
	if changed {
		t.Fatal("timestamp-only update was reported as meaningful")
	}
	select {
	case update := <-updates:
		t.Fatalf("unexpected timestamp-only notification: %#v", update)
	default:
	}

	game := domain.GameState{GameMode: "CLASSIC"}
	changed = store.Update(domain.Snapshot{
		Status:        domain.StatusInGame,
		Game:          &game,
		UpdatedAt:     polledAt,
		LastSuccessAt: &polledAt,
	})
	if !changed {
		t.Fatal("game state update was not reported as meaningful")
	}
	update := <-updates
	if update.Revision != 2 || update.Status != domain.StatusInGame {
		t.Fatalf("published update = %#v", update)
	}
}

func TestStoreIgnoresPrivateOutageCheckpointChurn(t *testing.T) {
	now := time.Date(2026, time.September, 19, 6, 15, 0, 0, time.UTC)
	session := &domain.MatchSession{
		ID: "match", State: domain.MatchActive, ConnectionState: domain.ConnectionStale,
		OutageStartedAt: now, LastFailureClass: domain.FetchGameUnavailable,
		LastFailureAt: now, UnavailableSince: now,
	}
	store := NewStore(domain.Snapshot{
		Status: domain.StatusStale, Connection: domain.ConnectionStale,
		LastError: "unavailable", Session: session,
	})
	_, updates, unsubscribe := store.Subscribe(1)
	defer unsubscribe()

	changedSession := *session
	changedSession.UpdatedAt = now.Add(time.Second)
	changedSession.LastFailureClass = domain.FetchInvalidPayload
	changedSession.LastFailureAt = now.Add(time.Second)
	changedSession.UnavailableSince = time.Time{}
	changed := store.Update(domain.Snapshot{
		Status: domain.StatusStale, Connection: domain.ConnectionStale,
		LastError: "unavailable", Session: &changedSession,
	})
	if changed {
		t.Fatal("private outage checkpoint churn was reported as a public snapshot change")
	}
	select {
	case update := <-updates:
		t.Fatalf("unexpected private checkpoint notification: %#v", update)
	default:
	}
}

func TestSlowSubscriberReceivesLatestSnapshot(t *testing.T) {
	store := NewStore(domain.Snapshot{Status: domain.StatusWaitingForGame})
	_, updates, unsubscribe := store.Subscribe(1)
	defer unsubscribe()

	store.Update(domain.Snapshot{Status: domain.StatusWaitingForGame, LastError: "first"})
	store.Update(domain.Snapshot{Status: domain.StatusWaitingForGame, LastError: "latest"})

	update := <-updates
	if update.LastError != "latest" || update.Revision != 3 {
		t.Fatalf("slow subscriber update = %#v, want latest revision", update)
	}
}

func TestStoreReturnsDeepCopies(t *testing.T) {
	endedAt := time.Date(2026, time.September, 19, 6, 0, 0, 0, time.UTC)
	game := domain.GameState{
		ActivePlayer: domain.ActivePlayer{Items: []domain.Item{{ID: 1001}}},
		Players:      []domain.Player{{Items: []domain.Item{{ID: 2003}}}},
		SourceEvents: []domain.SourceEvent{{ID: 1, Payload: json.RawMessage(`{"source":true}`)}},
	}
	store := NewStore(domain.Snapshot{
		Status: domain.StatusInGame, Game: &game,
		Session:      &domain.MatchSession{ID: "match", EndedAt: &endedAt},
		RecentEvents: []domain.SemanticEvent{{ID: "match:1", Payload: json.RawMessage(`{"semantic":true}`)}},
	})

	copy := store.Current()
	copy.Game.ActivePlayer.Items[0].ID = 9999
	copy.Game.Players[0].Items[0].ID = 9999
	copy.Game.SourceEvents[0].Payload[0] = '['
	copy.RecentEvents[0].Payload[0] = '['
	*copy.Session.EndedAt = endedAt.Add(time.Hour)

	current := store.Current()
	if current.Game.ActivePlayer.Items[0].ID != 1001 || current.Game.Players[0].Items[0].ID != 2003 ||
		current.Game.SourceEvents[0].Payload[0] != '{' || current.RecentEvents[0].Payload[0] != '{' || !current.Session.EndedAt.Equal(endedAt) {
		t.Fatalf("store state was mutated through a returned copy: %#v", current.Game)
	}
}

func TestUnsubscribeClosesUpdates(t *testing.T) {
	store := NewStore(domain.Snapshot{Status: domain.StatusWaitingForGame})
	_, updates, unsubscribe := store.Subscribe(1)
	unsubscribe()
	unsubscribe()

	if _, open := <-updates; open {
		t.Fatal("updates channel remained open after unsubscribe")
	}
}

func TestStoreSupportsConcurrentReadersAndWriters(t *testing.T) {
	store := NewStore(domain.Snapshot{Status: domain.StatusWaitingForGame})
	var waitGroup sync.WaitGroup
	for reader := 0; reader < 4; reader++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			for iteration := 0; iteration < 100; iteration++ {
				_ = store.Current()
			}
		}()
	}
	waitGroup.Add(1)
	go func() {
		defer waitGroup.Done()
		for iteration := 0; iteration < 100; iteration++ {
			store.Update(domain.Snapshot{
				Status:    domain.StatusWaitingForGame,
				LastError: fmt.Sprintf("error-%d", iteration),
			})
		}
	}()
	waitGroup.Wait()

	if store.Current().LastError != "error-99" {
		t.Fatalf("final error = %q, want error-99", store.Current().LastError)
	}
}
