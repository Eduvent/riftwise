package tracker

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"lolengine/internal/domain"
	localhistory "lolengine/internal/history"
	"lolengine/internal/state"
	"lolengine/internal/storage"
)

func TestDurableLifecycleFlowAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "lifecycle.db")
	database, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatalf("storage.Open() error = %v", err)
	}
	now := time.Date(2026, 9, 19, 5, 0, 0, 0, time.UTC)
	base := detectorGame(30, 500)
	base.SourceEvents = []domain.SourceEvent{{ID: 1, Name: "GameStart", DedupeKey: "source:1"}}
	tick := cloneDetectorGame(base)
	tick.GameTimeSeconds = 32
	tick.ActivePlayer.CurrentGold = 504
	reconnected := cloneDetectorGame(tick)
	reconnected.GameTimeSeconds = 34
	purchased := cloneDetectorGame(reconnected)
	purchased.GameTimeSeconds = 36
	purchased.ActivePlayer.CurrentGold = 104
	purchased.ActivePlayer.Items = []domain.Item{{ID: 1028, Count: 1, Slot: 0}}
	purchased.Players[0].Items = append([]domain.Item(nil), purchased.ActivePlayer.Items...)
	death := cloneDetectorGame(purchased)
	death.GameTimeSeconds = 38
	death.ActivePlayer.Score.Deaths = 1
	death.Players[0].Score.Deaths = 1
	death.ActivePlayer.IsDead = true
	death.Players[0].IsDead = true
	ended := cloneDetectorGame(death)
	ended.GameTimeSeconds = 40
	ended.SourceEvents = append(ended.SourceEvents, domain.SourceEvent{ID: 9, Name: "GameEnd", GameTimeSeconds: 40, DedupeKey: "source:9"})

	fetcher := &sequenceFetcher{results: []fetchResult{
		{err: classifiedTestError{class: domain.FetchTransport, err: errors.New("waiting")}},
		{game: base},
		{game: tick},
		{err: classifiedTestError{class: domain.FetchTransport, err: errors.New("temporary refusal")}},
		{game: reconnected},
		{game: purchased},
		{game: death},
		{game: ended},
	}}
	store := state.NewStore(domain.Snapshot{Status: domain.StatusWaitingForGame, Connection: domain.ConnectionWaiting})
	tracker, err := New(fetcher, store, Config{
		PollInterval: time.Second, StaleAfter: 5 * time.Second, EndAfter: time.Minute,
		Now: func() time.Time { return now }, Persistence: database,
		NewSessionID: func(string, time.Time) string { return "match-one" },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	tracker.poll(ctx) // waiting
	now = now.Add(time.Second)
	tracker.poll(ctx) // start
	startedDecision := store.Current().DecisionRevision
	now = now.Add(2 * time.Second)
	tracker.poll(ctx) // time/passive gold only
	if got := store.Current().DecisionRevision; got != startedDecision {
		t.Fatalf("time-only decision revision = %d, want %d", got, startedDecision)
	}
	for range 5 {
		now = now.Add(2 * time.Second)
		tracker.poll(ctx)
	}
	final := store.Current()
	if final.Session == nil || final.Session.ID != "match-one" || final.Session.State != domain.MatchEnded {
		t.Fatalf("final snapshot session = %#v", final.Session)
	}
	events, err := database.ListMatchEvents(ctx, "match-one", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	assertEventKinds(t, events,
		domain.EventMatchStarted,
		domain.EventConnectionLost,
		domain.EventConnectionRestored,
		domain.EventInventoryChanged,
		domain.EventItemPurchased,
		domain.EventReturnedToBase,
		domain.EventPlayerDied,
		domain.EventMatchEnded,
	)
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatalf("storage.Open(reopen) error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	historyService, err := localhistory.NewService(reopened, localhistory.Config{
		Now: func() time.Time { return now.Add(time.Second) }, BatchSize: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if built, err := historyService.RebuildPending(ctx); err != nil || built != 1 {
		t.Fatalf("history rebuild after restart = %d, %v", built, err)
	}
	firstSummary, err := historyService.Get(ctx, "match-one")
	if err != nil {
		t.Fatal(err)
	}
	if firstSummary.Completion != domain.SummaryCompleteTerminal || firstSummary.Stats == nil || firstSummary.Stats.Deaths != 1 ||
		len(firstSummary.InventoryChanges) != 1 || len(firstSummary.InferredPurchases) != 1 || len(firstSummary.InferredBaseReturns) != 1 {
		t.Fatalf("rebuilt full-flow summary = %+v", firstSummary)
	}
	if active, err := reopened.LoadActiveMatch(ctx); err != nil || active != nil {
		t.Fatalf("active match after reopen = %#v, %v", active, err)
	}
	nextStore := state.NewStore(domain.Snapshot{Status: domain.StatusWaitingForGame, Connection: domain.ConnectionWaiting})
	nextTracker, err := New(&sequenceFetcher{results: []fetchResult{{game: detectorGame(5, 500)}}}, nextStore, Config{
		PollInterval: time.Second, StaleAfter: 5 * time.Second, EndAfter: time.Minute,
		Now: func() time.Time { return now }, Persistence: reopened,
		NewSessionID: func(string, time.Time) string { return "match-two" },
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	nextTracker.poll(ctx)
	matches, err := reopened.ListMatches(ctx, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 || matches[0].ID != "match-two" || matches[1].ID != "match-one" {
		t.Fatalf("matches after restart = %#v", matches)
	}
	events, err = reopened.ListMatchEvents(ctx, "match-one", 0, 100)
	if err != nil || len(events) != 8 {
		t.Fatalf("old events after restart = %d, %v", len(events), err)
	}
	unchangedSummary, err := historyService.Get(ctx, "match-one")
	if err != nil || unchangedSummary.Digest != firstSummary.Digest {
		t.Fatalf("next match changed prior summary digest = %q, %v; want %q", unchangedSummary.Digest, err, firstSummary.Digest)
	}
}

func TestLifecycleRestartDuringOutageRestoresWithoutDuplicateLoss(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "outage-restart.db")
	database, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 19, 7, 0, 0, 0, time.UTC)
	game := detectorGame(100, 500)
	firstStore := state.NewStore(domain.Snapshot{Status: domain.StatusWaitingForGame, Connection: domain.ConnectionWaiting})
	first, err := New(&sequenceFetcher{results: []fetchResult{
		{game: game},
		{err: classifiedTestError{class: domain.FetchTransport, err: errors.New("connection refused")}},
	}}, firstStore, Config{
		PollInterval: time.Second, StaleAfter: 5 * time.Second, EndAfter: time.Minute,
		Now: func() time.Time { return now }, Persistence: database,
		NewSessionID: func(string, time.Time) string { return "outage-match" },
	})
	if err != nil {
		t.Fatal(err)
	}
	first.poll(ctx)
	now = now.Add(time.Second)
	first.poll(ctx)
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	secondStore := state.NewStore(domain.Snapshot{Status: domain.StatusWaitingForGame, Connection: domain.ConnectionWaiting})
	second, err := New(&sequenceFetcher{results: []fetchResult{
		{err: classifiedTestError{class: domain.FetchTransport, err: errors.New("still unavailable")}},
		{game: game},
	}}, secondStore, Config{
		PollInterval: time.Second, StaleAfter: 5 * time.Second, EndAfter: time.Minute,
		Now: func() time.Time { return now }, Persistence: reopened,
		NewSessionID: func(string, time.Time) string { return "unexpected-new-match" },
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	second.poll(ctx)
	events, err := reopened.ListMatchEvents(ctx, "outage-match", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	assertEventKinds(t, events, domain.EventMatchStarted, domain.EventConnectionLost)

	now = now.Add(time.Second)
	second.poll(ctx)
	events, err = reopened.ListMatchEvents(ctx, "outage-match", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	assertEventKinds(t, events, domain.EventMatchStarted, domain.EventConnectionLost, domain.EventConnectionRestored)
}

func TestLifecycleRecoveryPersistsLatestFailureClassForUnavailableGrace(t *testing.T) {
	tests := []struct {
		name               string
		beforeRestart      []timedFailure
		restartAt          time.Duration
		wantEndedOnRestart bool
		endAfterRestartAt  time.Duration
	}{
		{
			name: "unavailable then transport starts a fresh unavailable interval",
			beforeRestart: []timedFailure{
				{at: time.Second, class: domain.FetchGameUnavailable},
				{at: 20 * time.Second, class: domain.FetchTransport},
			},
			restartAt:         40 * time.Second,
			endAfterRestartAt: 70 * time.Second,
		},
		{
			name: "unavailable then timeout starts a fresh unavailable interval",
			beforeRestart: []timedFailure{
				{at: time.Second, class: domain.FetchGameUnavailable},
				{at: 20 * time.Second, class: domain.FetchTimeout},
			},
			restartAt:         40 * time.Second,
			endAfterRestartAt: 70 * time.Second,
		},
		{
			name: "unavailable then invalid payload starts a fresh unavailable interval",
			beforeRestart: []timedFailure{
				{at: time.Second, class: domain.FetchGameUnavailable},
				{at: 20 * time.Second, class: domain.FetchInvalidPayload},
			},
			restartAt:         40 * time.Second,
			endAfterRestartAt: 70 * time.Second,
		},
		{
			name: "transport then unavailable resumes the continuous unavailable interval",
			beforeRestart: []timedFailure{
				{at: time.Second, class: domain.FetchTransport},
				{at: 20 * time.Second, class: domain.FetchGameUnavailable},
			},
			restartAt:          55 * time.Second,
			wantEndedOnRestart: true,
		},
		{
			name: "timeout then unavailable resumes the continuous unavailable interval",
			beforeRestart: []timedFailure{
				{at: time.Second, class: domain.FetchTimeout},
				{at: 20 * time.Second, class: domain.FetchGameUnavailable},
			},
			restartAt:          55 * time.Second,
			wantEndedOnRestart: true,
		},
		{
			name: "invalid payload then unavailable resumes the continuous unavailable interval",
			beforeRestart: []timedFailure{
				{at: time.Second, class: domain.FetchInvalidPayload},
				{at: 20 * time.Second, class: domain.FetchGameUnavailable},
			},
			restartAt:          55 * time.Second,
			wantEndedOnRestart: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "mixed-outage.db")
			database, err := storage.Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			origin := time.Date(2026, 9, 19, 7, 15, 0, 0, time.UTC)
			now := origin
			game := detectorGame(100, 500)
			results := []fetchResult{{game: game}}
			for _, failure := range test.beforeRestart {
				results = append(results, fetchResult{err: classifiedTestError{class: failure.class, err: errors.New(string(failure.class))}})
			}
			firstStore := state.NewStore(domain.Snapshot{Status: domain.StatusWaitingForGame, Connection: domain.ConnectionWaiting})
			first, err := New(&sequenceFetcher{results: results}, firstStore, Config{
				PollInterval: time.Second, StaleAfter: 5 * time.Second, EndAfter: 30 * time.Second,
				Now: func() time.Time { return now }, Persistence: database,
				NewSessionID: func(string, time.Time) string { return "mixed-outage-match" },
			})
			if err != nil {
				t.Fatal(err)
			}
			first.poll(ctx)
			for _, failure := range test.beforeRestart {
				now = origin.Add(failure.at)
				first.poll(ctx)
			}
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}

			reopened, err := storage.Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			now = origin.Add(test.restartAt)
			secondStore := state.NewStore(domain.Snapshot{Status: domain.StatusWaitingForGame, Connection: domain.ConnectionWaiting})
			second, err := New(&sequenceFetcher{results: []fetchResult{
				{err: classifiedTestError{class: domain.FetchGameUnavailable, err: errors.New("game unavailable after restart")}},
				{err: classifiedTestError{class: domain.FetchGameUnavailable, err: errors.New("game still unavailable")}},
			}}, secondStore, Config{
				PollInterval: time.Second, StaleAfter: 5 * time.Second, EndAfter: 30 * time.Second,
				Now: func() time.Time { return now }, Persistence: reopened,
				NewSessionID: func(string, time.Time) string { return "unexpected-new-match" },
			})
			if err != nil {
				t.Fatal(err)
			}
			second.poll(ctx)
			got := secondStore.Current()
			if got.Session == nil {
				t.Fatalf("recovered session missing: %#v", got)
			}
			if ended := got.Session.State == domain.MatchEnded; ended != test.wantEndedOnRestart {
				t.Fatalf("ended on first post-restart unavailable = %v, want %v; snapshot %#v", ended, test.wantEndedOnRestart, got)
			}

			if test.endAfterRestartAt > 0 {
				now = origin.Add(test.endAfterRestartAt)
				second.poll(ctx)
				got = secondStore.Current()
				if got.Session == nil || got.Session.State != domain.MatchEnded || got.Session.EndReason != "unavailable_grace_elapsed" {
					t.Fatalf("fresh unavailable interval did not end after its own grace: %#v", got)
				}
			}
		})
	}
}

func TestLifecycleRecoveryResetsUnavailableGraceOnReverseMixedFailureAfterRestart(t *testing.T) {
	for _, failureClass := range []domain.FetchErrorClass{
		domain.FetchTransport,
		domain.FetchTimeout,
		domain.FetchInvalidPayload,
	} {
		t.Run(string(failureClass), func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "reverse-mixed-outage.db")
			database, err := storage.Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			origin := time.Date(2026, 9, 19, 7, 25, 0, 0, time.UTC)
			now := origin
			first, err := New(&sequenceFetcher{results: []fetchResult{
				{game: detectorGame(100, 500)},
				{err: classifiedTestError{class: failureClass, err: errors.New(string(failureClass))}},
				{err: classifiedTestError{class: domain.FetchGameUnavailable, err: errors.New("game unavailable")}},
			}}, state.NewStore(domain.Snapshot{Status: domain.StatusWaitingForGame, Connection: domain.ConnectionWaiting}), Config{
				PollInterval: time.Second, StaleAfter: 5 * time.Second, EndAfter: 30 * time.Second,
				Now: func() time.Time { return now }, Persistence: database,
				NewSessionID: func(string, time.Time) string { return "reverse-mixed-match" },
			})
			if err != nil {
				t.Fatal(err)
			}
			first.poll(ctx)
			now = origin.Add(time.Second)
			first.poll(ctx)
			now = origin.Add(20 * time.Second)
			first.poll(ctx)
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}

			reopened, err := storage.Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			secondStore := state.NewStore(domain.Snapshot{Status: domain.StatusWaitingForGame, Connection: domain.ConnectionWaiting})
			second, err := New(&sequenceFetcher{results: []fetchResult{
				{err: classifiedTestError{class: failureClass, err: errors.New("non-game failure after restart")}},
				{err: classifiedTestError{class: domain.FetchGameUnavailable, err: errors.New("fresh unavailable")}},
				{err: classifiedTestError{class: domain.FetchGameUnavailable, err: errors.New("fresh unavailable elapsed")}},
			}}, secondStore, Config{
				PollInterval: time.Second, StaleAfter: 5 * time.Second, EndAfter: 30 * time.Second,
				Now: func() time.Time { return now }, Persistence: reopened,
				NewSessionID: func(string, time.Time) string { return "unexpected-new-match" },
			})
			if err != nil {
				t.Fatal(err)
			}

			now = origin.Add(55 * time.Second)
			second.poll(ctx)
			if got := secondStore.Current(); got.Session == nil || got.Session.State != domain.MatchActive {
				t.Fatalf("reverse failure class did not clear recovered unavailable grace: %#v", got)
			}
			now = origin.Add(56 * time.Second)
			second.poll(ctx)
			if got := secondStore.Current(); got.Session == nil || got.Session.State != domain.MatchActive {
				t.Fatalf("fresh unavailable interval ended immediately: %#v", got)
			}
			now = origin.Add(86 * time.Second)
			second.poll(ctx)
			if got := secondStore.Current(); got.Session == nil || got.Session.State != domain.MatchEnded || got.Session.EndReason != "unavailable_grace_elapsed" {
				t.Fatalf("fresh unavailable interval did not end after its own grace: %#v", got)
			}
		})
	}
}

type timedFailure struct {
	at    time.Duration
	class domain.FetchErrorClass
}

func TestLifecycleRestartDoesNotReplayAlreadyEndedTerminalPayload(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ended-replay.db")
	database, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 19, 7, 30, 0, 0, time.UTC)
	terminal := detectorGame(100, 500)
	terminal.SourceEvents = []domain.SourceEvent{
		{ID: 1, Name: "GameStart", GameTimeSeconds: 0, DedupeKey: "source:1"},
		{ID: 99, Name: "GameEnd", GameTimeSeconds: 100, DedupeKey: "source:99"},
	}
	firstStore := state.NewStore(domain.Snapshot{Status: domain.StatusWaitingForGame, Connection: domain.ConnectionWaiting})
	first, err := New(&sequenceFetcher{results: []fetchResult{{game: terminal}}}, firstStore, Config{
		PollInterval: time.Second, StaleAfter: 5 * time.Second, EndAfter: time.Minute,
		Now: func() time.Time { return now }, Persistence: database,
		NewSessionID: func(string, time.Time) string { return "ended-match" },
	})
	if err != nil {
		t.Fatal(err)
	}
	first.poll(ctx)
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	now = now.Add(time.Second)
	secondStore := state.NewStore(domain.Snapshot{Status: domain.StatusWaitingForGame, Connection: domain.ConnectionWaiting})
	second, err := New(&sequenceFetcher{results: []fetchResult{{game: terminal}}}, secondStore, Config{
		PollInterval: time.Second, StaleAfter: 5 * time.Second, EndAfter: time.Minute,
		Now: func() time.Time { return now }, Persistence: reopened,
		NewSessionID: func(string, time.Time) string { return "duplicate-ended-match" },
	})
	if err != nil {
		t.Fatal(err)
	}
	second.poll(ctx)
	matches, err := reopened.ListMatches(ctx, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].ID != "ended-match" {
		t.Fatalf("replayed terminal payload created duplicate sessions: %#v", matches)
	}
	events, err := reopened.ListMatchEvents(ctx, "ended-match", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	assertEventKinds(t, events, domain.EventMatchStarted, domain.EventMatchEnded)
}

func TestRepeatedPurchaseOccurrencesPersistWithContiguousSequence(t *testing.T) {
	ctx := context.Background()
	database, err := storage.Open(ctx, filepath.Join(t.TempDir(), "repeat-purchase.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	now := time.Date(2026, 9, 19, 8, 0, 0, 0, time.UTC)
	base := detectorGame(10, 500)
	firstPurchase := cloneDetectorGame(base)
	firstPurchase.GameTimeSeconds = 11
	firstPurchase.ActivePlayer.CurrentGold = 450
	firstPurchase.ActivePlayer.Items = []domain.Item{{ID: 2003, Count: 1}}
	firstPurchase.Players[0].Items = append([]domain.Item(nil), firstPurchase.ActivePlayer.Items...)
	consumed := cloneDetectorGame(firstPurchase)
	consumed.GameTimeSeconds = 12
	consumed.ActivePlayer.Items = nil
	consumed.Players[0].Items = nil
	secondPurchase := cloneDetectorGame(consumed)
	secondPurchase.GameTimeSeconds = 13
	secondPurchase.ActivePlayer.CurrentGold = 400
	secondPurchase.ActivePlayer.Items = []domain.Item{{ID: 2003, Count: 1}}
	secondPurchase.Players[0].Items = append([]domain.Item(nil), secondPurchase.ActivePlayer.Items...)

	store := state.NewStore(domain.Snapshot{Status: domain.StatusWaitingForGame, Connection: domain.ConnectionWaiting})
	tracker, err := New(&sequenceFetcher{results: []fetchResult{
		{game: base}, {game: firstPurchase}, {game: consumed}, {game: secondPurchase},
	}}, store, Config{
		PollInterval: time.Second, StaleAfter: 5 * time.Second, EndAfter: time.Minute,
		Now: func() time.Time { return now }, Persistence: database,
		NewSessionID: func(string, time.Time) string { return "repeat-purchase-match" },
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 4 {
		tracker.poll(ctx)
		now = now.Add(time.Second)
	}
	events, err := database.ListMatchEvents(ctx, "repeat-purchase-match", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	assertEventKinds(t, events,
		domain.EventMatchStarted,
		domain.EventInventoryChanged, domain.EventItemPurchased, domain.EventReturnedToBase,
		domain.EventInventoryChanged,
		domain.EventInventoryChanged, domain.EventItemPurchased, domain.EventReturnedToBase,
	)
	for index, event := range events {
		if event.Sequence != uint64(index+1) {
			t.Fatalf("event sequence gap at index %d: %#v", index, events)
		}
	}
	if events[2].DedupeKey == events[6].DedupeKey {
		t.Fatalf("repeated purchases shared durable key %q", events[2].DedupeKey)
	}
	matches, err := database.ListMatches(ctx, 10, 0)
	if err != nil || len(matches) != 1 || matches[0].LastEventSequence != uint64(len(events)) {
		t.Fatalf("session sequence does not match durable history: %#v, %v", matches, err)
	}
}

type classifiedTestError struct {
	class domain.FetchErrorClass
	err   error
}

func (err classifiedTestError) Error() string                           { return err.err.Error() }
func (err classifiedTestError) Unwrap() error                           { return err.err }
func (err classifiedTestError) FetchErrorClass() domain.FetchErrorClass { return err.class }
