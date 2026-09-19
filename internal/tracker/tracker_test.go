package tracker

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"lolengine/internal/domain"
	"lolengine/internal/state"
)

type fetchResult struct {
	game domain.GameState
	err  error
}

type sequenceFetcher struct {
	mu      sync.Mutex
	results []fetchResult
	index   int
}

type retryLifecyclePersistence struct {
	failures int
	sessions []domain.MatchSession
	events   []domain.SemanticEvent
}

func (persistence *retryLifecyclePersistence) LoadActiveMatch(context.Context) (*domain.MatchSession, error) {
	return nil, nil
}

func (persistence *retryLifecyclePersistence) SaveMatchUpdates(_ context.Context, sessions []domain.MatchSession, events []domain.SemanticEvent) error {
	if persistence.failures > 0 {
		persistence.failures--
		return errors.New("disk busy")
	}
	persistence.sessions = append([]domain.MatchSession(nil), sessions...)
	persistence.events = append([]domain.SemanticEvent(nil), events...)
	return nil
}

func (f *sequenceFetcher) Fetch(context.Context) (domain.GameState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.results) == 0 {
		return domain.GameState{}, errors.New("no fake result")
	}
	index := f.index
	if index >= len(f.results) {
		index = len(f.results) - 1
	} else {
		f.index++
	}
	return f.results[index].game, f.results[index].err
}

func TestTrackerStateTransitionsAndRecovery(t *testing.T) {
	game := domain.GameState{
		GameMode:     "CLASSIC",
		ActivePlayer: domain.ActivePlayer{ChampionName: "Garen"},
	}
	fetcher := &sequenceFetcher{results: []fetchResult{
		{err: errors.New("connection refused")},
		{game: game},
		{err: errors.New("temporary failure")},
		{err: errors.New("temporary failure")},
		{game: game},
	}}
	store := state.NewStore(domain.Snapshot{Status: domain.StatusWaitingForGame})
	now := time.Date(2026, time.September, 18, 13, 0, 0, 0, time.UTC)
	tracker, err := New(fetcher, store, Config{
		PollInterval: time.Second,
		StaleAfter:   5 * time.Second,
		Now:          func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	tracker.poll(context.Background())
	assertSnapshotStatus(t, store.Current(), domain.StatusWaitingForGame, true)

	now = now.Add(time.Second)
	tracker.poll(context.Background())
	snapshot := store.Current()
	assertSnapshotStatus(t, snapshot, domain.StatusInGame, false)
	if snapshot.Game == nil || snapshot.Game.ActivePlayer.ChampionName != "Garen" {
		t.Fatalf("game after recovery = %#v", snapshot.Game)
	}

	now = now.Add(3 * time.Second)
	tracker.poll(context.Background())
	snapshot = store.Current()
	assertSnapshotStatus(t, snapshot, domain.StatusInGame, true)
	if snapshot.Game == nil {
		t.Fatal("temporary failure discarded the last game state")
	}

	now = now.Add(3 * time.Second)
	tracker.poll(context.Background())
	assertSnapshotStatus(t, store.Current(), domain.StatusStale, true)

	now = now.Add(time.Second)
	tracker.poll(context.Background())
	snapshot = store.Current()
	assertSnapshotStatus(t, snapshot, domain.StatusInGame, false)
	if snapshot.LastSuccessAt == nil || !snapshot.LastSuccessAt.Equal(now) {
		t.Fatalf("LastSuccessAt = %v, want %v", snapshot.LastSuccessAt, now)
	}
}

func TestTrackerSuppressesUnchangedGameNotifications(t *testing.T) {
	game := domain.GameState{GameMode: "CLASSIC", GameTimeSeconds: 10}
	fetcher := &sequenceFetcher{results: []fetchResult{{game: game}, {game: game}}}
	store := state.NewStore(domain.Snapshot{Status: domain.StatusWaitingForGame})
	now := time.Date(2026, time.September, 18, 14, 0, 0, 0, time.UTC)
	tracker, err := New(fetcher, store, Config{
		PollInterval: time.Second,
		StaleAfter:   5 * time.Second,
		Now:          func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, updates, unsubscribe := store.Subscribe(1)
	defer unsubscribe()

	tracker.poll(context.Background())
	first := <-updates
	if first.Revision != 2 {
		t.Fatalf("first revision = %d, want 2", first.Revision)
	}

	now = now.Add(time.Second)
	tracker.poll(context.Background())
	select {
	case duplicate := <-updates:
		t.Fatalf("received unchanged game notification: %#v", duplicate)
	default:
	}
	current := store.Current()
	if current.Revision != 2 {
		t.Fatalf("revision = %d after unchanged poll, want 2", current.Revision)
	}
	if current.LastSuccessAt == nil || !current.LastSuccessAt.Equal(now) {
		t.Fatalf("LastSuccessAt = %v, want %v", current.LastSuccessAt, now)
	}
}

func TestTrackerPersistsSemanticEventsBeforePublishingAndRetries(t *testing.T) {
	game := detectorGame(10, 500)
	fetcher := &sequenceFetcher{results: []fetchResult{{game: game}, {game: game}}}
	persistence := &retryLifecyclePersistence{failures: 1}
	store := state.NewStore(domain.Snapshot{Status: domain.StatusWaitingForGame, Connection: domain.ConnectionWaiting})
	now := time.Date(2026, time.September, 19, 6, 0, 0, 0, time.UTC)
	tracker, err := New(fetcher, store, Config{
		PollInterval: time.Second, StaleAfter: 5 * time.Second, EndAfter: time.Minute,
		Now: func() time.Time { return now }, Persistence: persistence,
		NewSessionID: func(string, time.Time) string { return "durable-before-publish" },
	})
	if err != nil {
		t.Fatal(err)
	}

	tracker.poll(context.Background())
	failed := store.Current()
	if failed.Status != domain.StatusWaitingForGame || failed.Game != nil || failed.Session != nil || len(failed.RecentEvents) != 0 ||
		failed.DecisionRevision != 0 || !strings.Contains(failed.LastError, "persist match lifecycle") {
		t.Fatalf("failed persistence was published: %#v", failed)
	}

	now = now.Add(time.Second)
	tracker.poll(context.Background())
	published := store.Current()
	if published.Session == nil || published.Session.ID != "durable-before-publish" || len(published.RecentEvents) != 1 || published.DecisionRevision != 1 {
		t.Fatalf("retried lifecycle was not published: %#v", published)
	}
	if len(persistence.sessions) != 1 || len(persistence.events) != 1 || persistence.events[0].Kind != domain.EventMatchStarted {
		t.Fatalf("persisted retry = sessions %#v events %#v", persistence.sessions, persistence.events)
	}
}

func TestTrackerRetryPublishesPendingSuccessfulGameAcrossNextFetchFailure(t *testing.T) {
	game := detectorGame(10, 500)
	fetchErr := classifiedTestError{class: domain.FetchTransport, err: errors.New("connection refused")}
	fetcher := &sequenceFetcher{results: []fetchResult{{game: game}, {err: fetchErr}}}
	persistence := &retryLifecyclePersistence{failures: 1}
	store := state.NewStore(domain.Snapshot{Status: domain.StatusWaitingForGame, Connection: domain.ConnectionWaiting})
	now := time.Date(2026, time.September, 19, 6, 5, 0, 0, time.UTC)
	firstSuccessAt := now
	tracker, err := New(fetcher, store, Config{
		PollInterval: time.Second, StaleAfter: 5 * time.Second, EndAfter: time.Minute,
		Now: func() time.Time { return now }, Persistence: persistence,
		NewSessionID: func(string, time.Time) string { return "cross-poll-retry" },
	})
	if err != nil {
		t.Fatal(err)
	}

	tracker.poll(context.Background())
	failed := store.Current()
	if failed.Status != domain.StatusWaitingForGame || failed.Game != nil || failed.Session != nil ||
		failed.DecisionRevision != 0 || !strings.Contains(failed.LastError, "persist match lifecycle") {
		t.Fatalf("failed successful candidate leaked into committed snapshot: %#v", failed)
	}

	now = now.Add(time.Second)
	tracker.poll(context.Background())
	published := store.Current()
	if published.Status != domain.StatusInGame || published.Game == nil || published.Game.GameTimeSeconds != game.GameTimeSeconds ||
		published.Session == nil || published.Session.ID != "cross-poll-retry" || published.Session.State != domain.MatchActive ||
		published.Session.LastGameTime != published.Game.GameTimeSeconds ||
		published.Connection != domain.ConnectionReconnecting || published.LastSuccessAt == nil || !published.LastSuccessAt.Equal(firstSuccessAt) ||
		published.LastError != fetchErr.Error() || published.DecisionRevision != 1 {
		t.Fatalf("cross-poll retry published mismatched lifecycle/game state: %#v", published)
	}
	assertEventKinds(t, published.RecentEvents, domain.EventMatchStarted, domain.EventConnectionLost)
	if len(persistence.sessions) != 1 || len(persistence.events) != 2 || persistence.sessions[0].ID != published.Session.ID {
		t.Fatalf("persisted cross-poll batch = sessions %#v events %#v", persistence.sessions, persistence.events)
	}
}

func TestTrackerRetainsCommittedActiveSnapshotWhenEndPersistenceFails(t *testing.T) {
	base := detectorGame(100, 500)
	base.SourceEvents = []domain.SourceEvent{{ID: 1, Name: "GameStart", DedupeKey: "source:1"}}
	terminal := cloneDetectorGame(base)
	terminal.GameTimeSeconds = 101
	terminal.SourceEvents = append(terminal.SourceEvents, domain.SourceEvent{ID: 99, Name: "GameEnd", GameTimeSeconds: 101, DedupeKey: "source:99"})
	fetcher := &sequenceFetcher{results: []fetchResult{{game: base}, {game: terminal}, {game: terminal}}}
	persistence := &retryLifecyclePersistence{}
	store := state.NewStore(domain.Snapshot{Status: domain.StatusWaitingForGame, Connection: domain.ConnectionWaiting})
	now := time.Date(2026, time.September, 19, 6, 10, 0, 0, time.UTC)
	tracker, err := New(fetcher, store, Config{
		PollInterval: time.Second, StaleAfter: 5 * time.Second, EndAfter: time.Minute,
		Now: func() time.Time { return now }, Persistence: persistence,
		NewSessionID: func(string, time.Time) string { return "end-persistence" },
	})
	if err != nil {
		t.Fatal(err)
	}

	tracker.poll(context.Background())
	committed := store.Current()
	if committed.Session == nil || committed.Session.State != domain.MatchActive || len(committed.RecentEvents) != 1 {
		t.Fatalf("start was not committed: %#v", committed)
	}

	persistence.failures = 1
	now = now.Add(time.Second)
	tracker.poll(context.Background())
	failed := store.Current()
	if failed.Status != domain.StatusInGame || failed.Game == nil || failed.Game.GameTimeSeconds != base.GameTimeSeconds ||
		failed.Session == nil || failed.Session.State != domain.MatchActive || len(failed.RecentEvents) != 1 ||
		!strings.Contains(failed.LastError, "persist match lifecycle") {
		t.Fatalf("failed end leaked into snapshot: %#v", failed)
	}

	now = now.Add(time.Second)
	tracker.poll(context.Background())
	retried := store.Current()
	if retried.Session == nil || retried.Session.State != domain.MatchEnded || len(retried.RecentEvents) != 2 || retried.Game.GameTimeSeconds != terminal.GameTimeSeconds {
		t.Fatalf("end retry was not published atomically: %#v", retried)
	}
}

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	validFetcher := &sequenceFetcher{results: []fetchResult{{}}}
	validStore := state.NewStore(domain.Snapshot{Status: domain.StatusWaitingForGame})
	tests := []struct {
		name    string
		fetcher Fetcher
		store   *state.Store
		config  Config
	}{
		{name: "nil fetcher", store: validStore, config: Config{PollInterval: time.Second, StaleAfter: time.Second}},
		{name: "nil store", fetcher: validFetcher, config: Config{PollInterval: time.Second, StaleAfter: time.Second}},
		{name: "zero interval", fetcher: validFetcher, store: validStore, config: Config{StaleAfter: time.Second}},
		{name: "zero stale threshold", fetcher: validFetcher, store: validStore, config: Config{PollInterval: time.Second}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(test.fetcher, test.store, test.config); err == nil {
				t.Fatal("New() unexpectedly succeeded")
			}
		})
	}
}

func assertSnapshotStatus(t *testing.T, snapshot domain.Snapshot, status domain.TrackerStatus, hasError bool) {
	t.Helper()
	if snapshot.Status != status {
		t.Errorf("Status = %q, want %q", snapshot.Status, status)
	}
	if (snapshot.LastError != "") != hasError {
		t.Errorf("LastError = %q, hasError want %v", snapshot.LastError, hasError)
	}
}
