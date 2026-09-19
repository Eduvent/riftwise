package matchsync

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"lolengine/internal/domain"
	"lolengine/internal/riotapi"
	"lolengine/internal/storage"
)

func TestLinkRequiresConsentAndStoresNoDisplayIdentity(t *testing.T) {
	repository := openStore(t)
	now := time.Date(2026, 9, 19, 15, 0, 0, 0, time.UTC)
	client := &fakeClient{account: riotapi.Account{PUUID: "private-puuid", GameName: "José", TagLine: "LAS"}}
	service, err := New(client, repository, Config{Mode: riotapi.ModeDirect, Region: riotapi.RegionAmericas, Now: func() time.Time { return now }, NewID: func() string { return "account-1" }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Link(context.Background(), LinkRequest{GameName: "José", TagLine: "LAS", Platform: riotapi.PlatformNA1}); !errors.Is(err, ErrConsentRequired) {
		t.Fatalf("link without consent = %v", err)
	}
	linked, err := service.Link(context.Background(), LinkRequest{GameName: "José", TagLine: "LAS", Platform: riotapi.PlatformNA1, Consent: true})
	if err != nil {
		t.Fatal(err)
	}
	if linked.ID != "account-1" || linked.LinkedAt != now || linked.Region != riotapi.RegionAmericas || linked.Platform != riotapi.PlatformNA1 {
		t.Fatalf("linked = %#v", linked)
	}
	stored, err := repository.LoadRiotAccount(context.Background(), linked.ID)
	if err != nil || stored.PUUID != "private-puuid" {
		t.Fatalf("stored = %#v, %v", stored, err)
	}
}

func TestSyncFreezesWindowPaginatesResumesAndDedupes(t *testing.T) {
	repository := openStore(t)
	base := time.Date(2026, 9, 19, 16, 0, 0, 0, time.UTC)
	seedAccount(t, repository, base)
	client := &fakeClient{
		pages: map[int][]string{0: {"NA1_3", "NA1_2"}, 2: {"NA1_2", "NA1_1"}, 4: {}},
		matches: map[string]riotapi.Match{
			"NA1_3": syncMatch("NA1_3", base.Add(-time.Hour)),
			"NA1_2": syncMatch("NA1_2", base.Add(-2*time.Hour)),
			"NA1_1": syncMatch("NA1_1", base.Add(-3*time.Hour)),
		},
	}
	service, err := New(client, repository, Config{Mode: riotapi.ModeDirect, Region: riotapi.RegionAmericas, PageSize: 2, MaxMatches: 3, Concurrency: 2, Now: func() time.Time { return base }})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Sync(context.Background(), "account-1", Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Fetched != 3 || result.Duplicates != 1 || !result.HasMore || result.WindowEndUnix != base.Unix() {
		t.Fatalf("first result = %#v", result)
	}
	for _, query := range client.queries {
		if query.EndTime != base.Unix() {
			t.Fatalf("pagination window moved: %#v", client.queries)
		}
	}
	base = base.Add(6 * time.Hour) // resume must retain the original frozen end.
	result, err = service.Sync(context.Background(), "account-1", Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if result.WindowEndUnix != time.Date(2026, 9, 19, 16, 0, 0, 0, time.UTC).Unix() || result.Status != storage.RiotSyncComplete {
		t.Fatalf("resume result = %#v", result)
	}
	rows, err := repository.ListRiotMatches(context.Background(), "account-1", storage.RiotMatchFilter{Limit: 10})
	if err != nil || len(rows) != 3 {
		t.Fatalf("stored matches = %d, %v", len(rows), err)
	}
}

func TestSyncPersists404PendingAndPartialProgressForRestart(t *testing.T) {
	repository := openStore(t)
	now := time.Date(2026, 9, 19, 17, 0, 0, 0, time.UTC)
	seedAccount(t, repository, now)
	client := &fakeClient{
		pages:   map[int][]string{0: {"NA1_1", "NA1_2"}},
		matches: map[string]riotapi.Match{"NA1_1": syncMatch("NA1_1", now.Add(-time.Hour))},
		errors: map[string]error{
			"NA1_2": &riotapi.Error{Kind: riotapi.ErrorNotFound, Operation: "get match", StatusCode: 404},
		},
	}
	service, _ := New(client, repository, Config{Mode: riotapi.ModeDirect, Region: riotapi.RegionAmericas, PageSize: 2, MaxMatches: 10, Concurrency: 2, PendingRetry: time.Hour, Now: func() time.Time { return now }})
	result, err := service.Sync(context.Background(), "account-1", Filter{})
	if err != nil || result.Pending != 1 {
		t.Fatalf("result = %#v, %v", result, err)
	}
	pending, err := repository.ListDueRiotPendingFetches(context.Background(), "account-1", now.Add(2*time.Hour), 10)
	if err != nil || len(pending) != 1 || pending[0].MatchID != "NA1_2" {
		t.Fatalf("pending = %#v, %v", pending, err)
	}
	beforeRetry := client.callsFor("NA1_2")
	now = now.Add(30 * time.Minute)
	if _, err := service.Sync(context.Background(), "account-1", Filter{}); err != nil {
		t.Fatal(err)
	}
	if afterRetry := client.callsFor("NA1_2"); afterRetry != beforeRetry {
		t.Fatalf("pending 404 retried before next_retry_at: before=%d after=%d", beforeRetry, afterRetry)
	}

	// A server failure does not advance the durable page cursor, but successful
	// siblings on the same page are committed idempotently.
	client.pages = map[int][]string{0: {"NA1_3", "NA1_4"}}
	client.matches["NA1_3"] = syncMatch("NA1_3", now.Add(-2*time.Hour))
	client.errors["NA1_4"] = &riotapi.Error{Kind: riotapi.ErrorServer, Operation: "get match", StatusCode: 503}
	// Complete checkpoint starts a new run at offset zero.
	result, err = service.Sync(context.Background(), "account-1", Filter{Queue: 430})
	if err == nil || result.Status != storage.RiotSyncPartial {
		t.Fatalf("partial result = %#v, %v", result, err)
	}
	checkpoint, loadErr := repository.LoadRiotSyncCheckpoint(context.Background(), "account-1", Filter{Queue: 430}.Key())
	if loadErr != nil || checkpoint.NextStart != 0 || checkpoint.WindowEndUnix != now.Unix() {
		t.Fatalf("partial checkpoint = %#v, %v", checkpoint, loadErr)
	}
	delete(client.errors, "NA1_4")
	client.matches["NA1_4"] = syncMatch("NA1_4", now.Add(-3*time.Hour))
	now = now.Add(12 * time.Hour)
	result, err = service.Sync(context.Background(), "account-1", Filter{Queue: 430})
	if err != nil || result.WindowEndUnix != checkpoint.WindowEndUnix {
		t.Fatalf("restart result = %#v, %v", result, err)
	}
}

func TestSyncResumesFrozenWindowAfterDatabaseReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "restart.db")
	store, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 9, 19, 17, 30, 0, 0, time.UTC)
	seedAccount(t, store, started)
	client := &fakeClient{
		pages:   map[int][]string{0: {"NA1_1", "NA1_2"}},
		matches: map[string]riotapi.Match{"NA1_1": syncMatch("NA1_1", started.Add(-time.Hour))},
		errors:  map[string]error{"NA1_2": &riotapi.Error{Kind: riotapi.ErrorServer, Operation: "get match", StatusCode: 503}},
	}
	now := started
	service, _ := New(client, store, Config{Mode: riotapi.ModeDirect, Region: riotapi.RegionAmericas, PageSize: 2, MaxMatches: 10, Now: func() time.Time { return now }})
	first, err := service.Sync(ctx, "account-1", Filter{})
	if err == nil || first.Status != storage.RiotSyncPartial {
		t.Fatalf("first sync = %#v, %v", first, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	delete(client.errors, "NA1_2")
	client.matches["NA1_2"] = syncMatch("NA1_2", started.Add(-2*time.Hour))
	client.pages[2] = nil
	now = started.Add(24 * time.Hour)
	service, _ = New(client, reopened, Config{Mode: riotapi.ModeDirect, Region: riotapi.RegionAmericas, PageSize: 2, MaxMatches: 10, Now: func() time.Time { return now }})
	resumed, err := service.Sync(ctx, "account-1", Filter{})
	if err != nil || resumed.Status != storage.RiotSyncComplete || resumed.WindowEndUnix != started.Unix() {
		t.Fatalf("resumed sync = %#v, %v", resumed, err)
	}
	rows, err := reopened.ListRiotMatches(ctx, "account-1", storage.RiotMatchFilter{Limit: 10})
	if err != nil || len(rows) != 2 {
		t.Fatalf("reopened facts = %#v, %v", rows, err)
	}
}

func TestSyncBoundsWorkerConcurrencyAndSingleflights(t *testing.T) {
	repository := openStore(t)
	now := time.Date(2026, 9, 19, 18, 0, 0, 0, time.UTC)
	seedAccount(t, repository, now)
	client := &fakeClient{pages: map[int][]string{0: {"NA1_1", "NA1_2", "NA1_3", "NA1_4"}}, matches: map[string]riotapi.Match{}}
	for index, id := range client.pages[0] {
		client.matches[id] = syncMatch(id, now.Add(-time.Duration(index+1)*time.Hour))
	}
	release := make(chan struct{})
	client.block = release
	service, _ := New(client, repository, Config{Mode: riotapi.ModeDirect, Region: riotapi.RegionAmericas, PageSize: 4, MaxMatches: 4, Concurrency: 2, Now: func() time.Time { return now }})
	results := make(chan SyncResult, 2)
	errorsCh := make(chan error, 2)
	for index := 0; index < 2; index++ {
		go func() {
			result, err := service.Sync(context.Background(), "account-1", Filter{})
			results <- result
			errorsCh <- err
		}()
	}
	deadline := time.After(2 * time.Second)
	for atomic.LoadInt32(&client.maxActive) < 2 {
		select {
		case <-deadline:
			t.Fatal("workers did not start")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if got := atomic.LoadInt32(&client.maxActive); got > 2 {
		t.Fatalf("max concurrency = %d", got)
	}
	close(release)
	for index := 0; index < 2; index++ {
		if err := <-errorsCh; err != nil {
			t.Fatal(err)
		}
		<-results
	}
	if client.listCalls != 1 {
		t.Fatalf("singleflight list calls = %d", client.listCalls)
	}
}

func TestSyncCancellationPersistsRestartableCursor(t *testing.T) {
	repository := openStore(t)
	now := time.Date(2026, 9, 19, 19, 0, 0, 0, time.UTC)
	seedAccount(t, repository, now)
	block := make(chan struct{})
	client := &fakeClient{pages: map[int][]string{0: {"NA1_1"}}, matches: map[string]riotapi.Match{"NA1_1": syncMatch("NA1_1", now)}, block: block}
	service, _ := New(client, repository, Config{Mode: riotapi.ModeDirect, Region: riotapi.RegionAmericas, PageSize: 1, MaxMatches: 1, Concurrency: 1, Now: func() time.Time { return now }})
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { _, err := service.Sync(ctx, "account-1", Filter{}); finished <- err }()
	deadline := time.After(2 * time.Second)
	for atomic.LoadInt32(&client.active) == 0 {
		select {
		case <-deadline:
			t.Fatal("fetch did not start")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("Sync() cancellation = %v", err)
	}
	checkpoint, err := repository.LoadRiotSyncCheckpoint(context.Background(), "account-1", Filter{}.Key())
	if err != nil || checkpoint.Status != storage.RiotSyncCancelled || checkpoint.NextStart != 0 || checkpoint.WindowEndUnix != now.Unix() {
		t.Fatalf("cancelled checkpoint = %#v, %v", checkpoint, err)
	}
}

func TestTransportDeadlineDoesNotBecomeCallerCancellation(t *testing.T) {
	repository := openStore(t)
	now := time.Date(2026, 9, 19, 19, 5, 0, 0, time.UTC)
	seedAccount(t, repository, now)
	client := &fakeClient{
		pages:  map[int][]string{0: {"NA1_1"}},
		errors: map[string]error{"NA1_1": &riotapi.Error{Kind: riotapi.ErrorTransport, Operation: "get match", Cause: context.DeadlineExceeded}},
	}
	service, _ := New(client, repository, Config{Mode: riotapi.ModeDirect, Region: riotapi.RegionAmericas, PageSize: 1, MaxMatches: 1, Now: func() time.Time { return now }})
	result, err := service.Sync(context.Background(), "account-1", Filter{})
	if err == nil || result.Status != storage.RiotSyncPartial || result.ErrorKind != string(riotapi.ErrorTransport) {
		t.Fatalf("transport timeout result = %#v, %v", result, err)
	}
	checkpoint, loadErr := repository.LoadRiotSyncCheckpoint(context.Background(), "account-1", Filter{}.Key())
	if loadErr != nil || checkpoint.Status != storage.RiotSyncPartial || checkpoint.LastErrorKind != string(riotapi.ErrorTransport) {
		t.Fatalf("transport timeout checkpoint = %#v, %v", checkpoint, loadErr)
	}
}

func TestDeleteSettlesInflightSyncBeforePrivacyCommit(t *testing.T) {
	repository := openStore(t)
	now := time.Date(2026, 9, 19, 19, 30, 0, 0, time.UTC)
	seedAccount(t, repository, now)
	block := make(chan struct{})
	client := &fakeClient{pages: map[int][]string{0: {"NA1_1"}}, matches: map[string]riotapi.Match{"NA1_1": syncMatch("NA1_1", now)}, block: block}
	service, _ := New(client, repository, Config{Mode: riotapi.ModeDirect, Region: riotapi.RegionAmericas, PageSize: 1, MaxMatches: 1, Concurrency: 1, Now: func() time.Time { return now }})
	syncDone := make(chan error, 1)
	go func() { _, err := service.Sync(context.Background(), "account-1", Filter{}); syncDone <- err }()
	deadline := time.After(2 * time.Second)
	for atomic.LoadInt32(&client.active) == 0 {
		select {
		case <-deadline:
			t.Fatal("fetch did not start")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if err := service.Delete(context.Background(), "account-1", DeleteConfirmation); err != nil {
		t.Fatal(err)
	}
	if err := <-syncDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("settled sync error = %v", err)
	}
	if _, err := repository.LoadRiotAccount(context.Background(), "account-1"); !errors.Is(err, storage.ErrRiotAccountNotFound) {
		t.Fatalf("deleted account remains: %v", err)
	}
	if _, err := service.Sync(context.Background(), "account-1", Filter{}); !errors.Is(err, storage.ErrRiotAccountNotFound) {
		t.Fatalf("post-delete sync = %v", err)
	}
}

func TestDeleteFencesLinksBeforeDuringAndAfterResolution(t *testing.T) {
	t.Run("inflight resolver cannot recreate deleted account", func(t *testing.T) {
		store := openStore(t)
		now := time.Date(2026, 9, 19, 19, 35, 0, 0, time.UTC)
		seedAccount(t, store, now)
		started, release := make(chan struct{}), make(chan struct{})
		client := &fakeClient{account: riotapi.Account{PUUID: "self"}, resolveStarted: started, resolveBlock: release, ignoreResolveCancel: true}
		service, _ := New(client, store, Config{Mode: riotapi.ModeDirect, Region: riotapi.RegionAmericas, Now: func() time.Time { return now }, NewID: func() string { return "replacement" }})
		linkDone := make(chan error, 1)
		go func() {
			_, err := service.Link(context.Background(), LinkRequest{GameName: "name", TagLine: "tag", Platform: riotapi.PlatformNA1, Consent: true})
			linkDone <- err
		}()
		<-started
		deleteDone := make(chan error, 1)
		go func() { deleteDone <- service.Delete(context.Background(), "account-1", DeleteConfirmation) }()
		deadline := time.After(2 * time.Second)
		for {
			service.mu.Lock()
			settling := service.deleting["account-1"]
			service.mu.Unlock()
			if settling {
				break
			}
			select {
			case <-deadline:
				t.Fatal("delete did not establish privacy fence")
			default:
				time.Sleep(time.Millisecond)
			}
		}
		close(release)
		if err := <-linkDone; !errors.Is(err, ErrAccountSettling) {
			t.Fatalf("link result = %v", err)
		}
		if err := <-deleteDone; err != nil {
			t.Fatal(err)
		}
		if _, err := store.LoadRiotAccount(context.Background(), "account-1"); !errors.Is(err, storage.ErrRiotAccountNotFound) {
			t.Fatalf("account recreated: %v", err)
		}
	})

	t.Run("new link is rejected throughout privacy transaction", func(t *testing.T) {
		base := openStore(t)
		now := time.Date(2026, 9, 19, 19, 36, 0, 0, time.UTC)
		seedAccount(t, base, now)
		repository := &blockingRepository{DB: base, deleteStarted: make(chan struct{}), releaseDelete: make(chan struct{})}
		client := &fakeClient{account: riotapi.Account{PUUID: "other"}}
		service, _ := New(client, repository, Config{Mode: riotapi.ModeDirect, Region: riotapi.RegionAmericas, Now: func() time.Time { return now }})
		deleteDone := make(chan error, 1)
		go func() { deleteDone <- service.Delete(context.Background(), "account-1", DeleteConfirmation) }()
		<-repository.deleteStarted
		if _, err := service.Link(context.Background(), LinkRequest{GameName: "other", TagLine: "tag", Platform: riotapi.PlatformNA1, Consent: true}); !errors.Is(err, ErrAccountSettling) {
			t.Fatalf("link during delete = %v", err)
		}
		close(repository.releaseDelete)
		if err := <-deleteDone; err != nil {
			t.Fatal(err)
		}
	})
}

func TestSyncUsesStoredAccountRegionAndGlobalDetailLimit(t *testing.T) {
	t.Run("stored region overrides current process config", func(t *testing.T) {
		store := openStore(t)
		now := time.Date(2026, 9, 19, 19, 37, 0, 0, time.UTC)
		account := storage.RiotAccount{ID: "kr-account", PUUID: "self-kr", Region: riotapi.RegionAsia, Platform: riotapi.PlatformKR, ConsentAt: now, LinkedAt: now, UpdatedAt: now}
		if err := store.UpsertRiotAccount(context.Background(), account); err != nil {
			t.Fatal(err)
		}
		match := syncMatch("KR_1", now)
		match.Platform = riotapi.PlatformKR
		client := &fakeClient{pages: map[int][]string{0: {"KR_1"}}, matches: map[string]riotapi.Match{"KR_1": match}}
		service, _ := New(client, store, Config{Mode: riotapi.ModeDirect, Region: riotapi.RegionAmericas, PageSize: 2, MaxMatches: 2, Now: func() time.Time { return now }})
		if _, err := service.Sync(context.Background(), account.ID, Filter{}); err != nil {
			t.Fatal(err)
		}
		client.mu.Lock()
		defer client.mu.Unlock()
		if len(client.listRegions) == 0 || client.listRegions[0] != riotapi.RegionAsia || len(client.getRegions) == 0 || client.getRegions[0] != riotapi.RegionAsia {
			t.Fatalf("regions list=%v detail=%v", client.listRegions, client.getRegions)
		}
		checkpoint, err := store.LoadRiotSyncCheckpoint(context.Background(), account.ID, Filter{}.Key())
		if err != nil || checkpoint.Region != riotapi.RegionAsia || checkpoint.CoverageEndUnix == 0 {
			t.Fatalf("stored route coverage = %#v, %v", checkpoint, err)
		}
	})

	t.Run("limit is shared across accounts and filters", func(t *testing.T) {
		store := openStore(t)
		now := time.Date(2026, 9, 19, 19, 38, 0, 0, time.UTC)
		seedAccount(t, store, now)
		if err := store.UpsertRiotAccount(context.Background(), storage.RiotAccount{ID: "account-2", PUUID: "self-2", Region: riotapi.RegionAmericas, Platform: riotapi.PlatformNA1, ConsentAt: now, LinkedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		ids := []string{"NA1_1", "NA1_2", "NA1_3", "NA1_4"}
		release := make(chan struct{})
		client := &fakeClient{repeatPage: ids, matches: map[string]riotapi.Match{}, block: release}
		for _, id := range ids {
			client.matches[id] = syncMatch(id, now)
		}
		service, _ := New(client, store, Config{Mode: riotapi.ModeDirect, Region: riotapi.RegionAmericas, PageSize: 4, MaxMatches: 4, Concurrency: 2, Now: func() time.Time { return now }})
		done := make(chan error, 2)
		go func() { _, err := service.Sync(context.Background(), "account-1", Filter{}); done <- err }()
		go func() { _, err := service.Sync(context.Background(), "account-2", Filter{Queue: 420}); done <- err }()
		deadline := time.After(2 * time.Second)
		for atomic.LoadInt32(&client.maxActive) < 2 {
			select {
			case <-deadline:
				t.Fatal("global detail workers did not start")
			default:
				time.Sleep(time.Millisecond)
			}
		}
		if got := atomic.LoadInt32(&client.maxActive); got > 2 {
			t.Fatalf("global detail concurrency = %d", got)
		}
		close(release)
		for range 2 {
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
		if got := atomic.LoadInt32(&client.maxActive); got > 2 {
			t.Fatalf("global detail concurrency = %d", got)
		}
	})
}

func TestSyncDiscardsCheckpointCoverageFromAnotherRegion(t *testing.T) {
	store := openStore(t)
	now := time.Date(2026, 9, 19, 19, 38, 30, 0, time.UTC)
	account := storage.RiotAccount{ID: "kr-account", PUUID: "self-kr", Region: riotapi.RegionAsia, Platform: riotapi.PlatformKR, ConsentAt: now, LinkedAt: now, UpdatedAt: now}
	if err := store.UpsertRiotAccount(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	legacy := storage.RiotSyncCheckpoint{
		AccountID: account.ID, Region: riotapi.RegionAmericas, FilterKey: Filter{}.Key(),
		RunStartedAt: now.Add(-48 * time.Hour), WindowStartUnix: now.Add(-72 * time.Hour).Unix(), WindowEndUnix: now.Add(-48 * time.Hour).Unix(),
		NextStart: 75, CoverageStartUnix: now.Add(-30 * 24 * time.Hour).Unix(), CoverageEndUnix: now.Add(-48 * time.Hour).Unix(),
		Status: storage.RiotSyncPartial, UpdatedAt: now.Add(-48 * time.Hour),
	}
	repository := &checkpointOverrideRepository{DB: store, checkpoint: legacy}
	client := &fakeClient{pages: map[int][]string{}}
	service, _ := New(client, repository, Config{
		Mode: riotapi.ModeDirect, Region: riotapi.RegionAmericas, Lookback: 7 * 24 * time.Hour,
		PageSize: 2, MaxMatches: 2, Now: func() time.Time { return now },
	})
	if _, err := service.Sync(context.Background(), account.ID, Filter{}); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	if len(client.queries) != 1 || client.queries[0].Start != 0 || client.queries[0].StartTime != now.Add(-7*24*time.Hour).Unix() || len(client.listRegions) != 1 || client.listRegions[0] != riotapi.RegionAsia {
		t.Fatalf("wrong-region checkpoint was resumed: queries=%#v regions=%v", client.queries, client.listRegions)
	}
	client.mu.Unlock()
	checkpoint, err := store.LoadRiotSyncCheckpoint(context.Background(), account.ID, Filter{}.Key())
	if err != nil || checkpoint.Region != riotapi.RegionAsia || checkpoint.CoverageStartUnix != now.Add(-7*24*time.Hour).Unix() || checkpoint.CoverageEndUnix != now.Unix() {
		t.Fatalf("replacement checkpoint = %#v, %v", checkpoint, err)
	}
}

func TestSyncRejectsStoredPlatformRegionMismatchBeforeCheckpoint(t *testing.T) {
	base := openStore(t)
	now := time.Date(2026, 9, 19, 19, 39, 0, 0, time.UTC)
	repository := &accountOverrideRepository{DB: base, account: storage.RiotAccount{
		ID: "corrupt", PUUID: "self", Region: riotapi.RegionAsia, Platform: riotapi.PlatformNA1,
		ConsentAt: now, LinkedAt: now, UpdatedAt: now,
	}}
	service, _ := New(&fakeClient{}, repository, Config{Mode: riotapi.ModeDirect, Region: riotapi.RegionAmericas, Now: func() time.Time { return now }})
	if _, err := service.Sync(context.Background(), "corrupt", Filter{}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("mismatched stored route = %v", err)
	}
	if _, err := base.LoadRiotSyncCheckpoint(context.Background(), "corrupt", Filter{}.Key()); !errors.Is(err, storage.ErrRiotCheckpointMissing) {
		t.Fatalf("wrong-region checkpoint was recorded: %v", err)
	}
}

func TestSyncAutomaticallyReconcilesPersistedEndedSessions(t *testing.T) {
	store := openStore(t)
	ended := time.Date(2026, 9, 19, 20, 30, 0, 0, time.UTC)
	started := ended.Add(-30 * time.Minute)
	seedAccount(t, store, ended)
	session := domain.MatchSession{
		ID: "local-ended", Fingerprint: "local-only", State: domain.MatchEnded,
		StartedAt: started, UpdatedAt: ended, LastSeenAt: ended, EndedAt: &ended,
		ChampionName: "Garen", MapNumber: 11, QueueID: 420,
	}
	if err := store.SaveMatchUpdates(context.Background(), []domain.MatchSession{session}, nil); err != nil {
		t.Fatal(err)
	}
	match := syncMatch("NA1_777", ended)
	client := &fakeClient{pages: map[int][]string{0: {match.ID}}, matches: map[string]riotapi.Match{match.ID: match}}
	service, _ := New(client, store, Config{Mode: riotapi.ModeDirect, Region: riotapi.RegionAmericas, PageSize: 2, MaxMatches: 2, Now: func() time.Time { return ended }})
	if _, err := service.Sync(context.Background(), "account-1", Filter{}); err != nil {
		t.Fatal(err)
	}
	link, err := store.LoadSessionMatchLink(context.Background(), session.ID, "account-1")
	if err != nil || link.State != storage.SessionLinkLinked || link.OfficialMatchID == match.ID || len(link.OfficialMatchID) != 64 {
		t.Fatalf("link = %#v, %v", link, err)
	}
}

func TestCompletedIngestionRetriesPendingReconciliationAfterRestartWithoutRefetch(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "reconciliation-restart.db")
	store, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	ended := time.Date(2026, 9, 19, 20, 45, 0, 0, time.UTC)
	started := ended.Add(-30 * time.Minute)
	seedAccount(t, store, ended)
	session := domain.MatchSession{
		ID: "local-retry", Fingerprint: "local-retry", State: domain.MatchEnded,
		StartedAt: started, UpdatedAt: ended, LastSeenAt: ended, EndedAt: &ended,
		ChampionName: "Garen", MapNumber: 11, QueueID: 420,
	}
	if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{session}, nil); err != nil {
		t.Fatal(err)
	}
	match := syncMatch("NA1_888", ended)
	client := &fakeClient{pages: map[int][]string{0: {match.ID}}, matches: map[string]riotapi.Match{match.ID: match}}
	failing := &failSessionLinkRepository{DB: store, failures: 1}
	service, _ := New(client, failing, Config{Mode: riotapi.ModeDirect, Region: riotapi.RegionAmericas, PageSize: 2, MaxMatches: 2, Now: func() time.Time { return ended }})
	result, err := service.Sync(ctx, "account-1", Filter{})
	if err != nil || result.Status != storage.RiotSyncComplete || result.ReconciliationStatus != storage.RiotReconciliationPending || result.ReconciliationErrorKind != reconciliationErrorStorage {
		t.Fatalf("completed ingestion outcome = %#v, %v", result, err)
	}
	if client.listCalls != 1 {
		t.Fatalf("initial list calls = %d", client.listCalls)
	}
	checkpoint, err := store.LoadRiotSyncCheckpoint(ctx, "account-1", Filter{}.Key())
	if err != nil || checkpoint.Status != storage.RiotSyncComplete {
		t.Fatalf("ingestion checkpoint = %#v, %v", checkpoint, err)
	}
	reconciliation, err := store.LoadRiotReconciliationState(ctx, "account-1")
	if err != nil || reconciliation.Status != storage.RiotReconciliationPending || reconciliation.LastErrorKind != reconciliationErrorStorage {
		t.Fatalf("pending reconciliation = %#v, %v", reconciliation, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, _ = New(client, store, Config{Mode: riotapi.ModeDirect, Region: riotapi.RegionAmericas, PageSize: 2, MaxMatches: 2, Now: func() time.Time { return ended.Add(time.Minute) }})
	status, err := service.Status(ctx)
	if err != nil || len(status.Reconciliations) != 1 || status.Reconciliations[0].Status != storage.RiotReconciliationPending {
		t.Fatalf("restart status = %#v, %v", status, err)
	}
	result, err = service.Sync(ctx, "account-1", Filter{})
	if err != nil || result.Status != storage.RiotSyncComplete || result.ReconciliationStatus != storage.RiotReconciliationComplete || !result.ReconciliationRetried {
		t.Fatalf("restart reconciliation outcome = %#v, %v", result, err)
	}
	if client.listCalls != 1 {
		t.Fatalf("pending reconciliation refetched Match-V5 window: list calls=%d", client.listCalls)
	}
	link, err := store.LoadSessionMatchLink(ctx, session.ID, "account-1")
	if err != nil || link.State != storage.SessionLinkLinked {
		t.Fatalf("reconciled link = %#v, %v", link, err)
	}
	reconciliation, err = store.LoadRiotReconciliationState(ctx, "account-1")
	if err != nil || reconciliation.Status != storage.RiotReconciliationComplete || reconciliation.LastErrorKind != "" {
		t.Fatalf("completed reconciliation = %#v, %v", reconciliation, err)
	}
}

func TestCompletedIngestionKeepsPendingWhenReconciliationCompletionWriteFails(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	now := time.Date(2026, 9, 19, 20, 46, 0, 0, time.UTC)
	seedAccount(t, store, now)
	client := &fakeClient{pages: map[int][]string{}}
	repository := &failSessionLinkRepository{DB: store, stateFailures: 1}
	service, _ := New(client, repository, Config{Mode: riotapi.ModeDirect, Region: riotapi.RegionAmericas, Now: func() time.Time { return now }})
	result, err := service.Sync(ctx, "account-1", Filter{})
	if err != nil || result.Status != storage.RiotSyncComplete || result.ReconciliationStatus != storage.RiotReconciliationPending || result.ReconciliationErrorKind != reconciliationErrorStorage {
		t.Fatalf("completion-write failure = %#v, %v", result, err)
	}
	state, err := store.LoadRiotReconciliationState(ctx, "account-1")
	if err != nil || state.Status != storage.RiotReconciliationPending || state.LastErrorKind != reconciliationErrorStorage {
		t.Fatalf("durable pending state = %#v, %v", state, err)
	}
	if client.listCalls != 1 {
		t.Fatalf("initial list calls = %d", client.listCalls)
	}
	result, err = service.Sync(ctx, "account-1", Filter{})
	if err != nil || result.ReconciliationStatus != storage.RiotReconciliationComplete || !result.ReconciliationRetried || client.listCalls != 1 {
		t.Fatalf("completion retry = %#v, calls=%d, err=%v", result, client.listCalls, err)
	}
}

func TestSyncBoundsDuplicateOnlyPagination(t *testing.T) {
	repository := openStore(t)
	now := time.Date(2026, 9, 19, 19, 45, 0, 0, time.UTC)
	seedAccount(t, repository, now)
	client := &fakeClient{repeatPage: []string{"NA1_1"}, matches: map[string]riotapi.Match{"NA1_1": syncMatch("NA1_1", now)}}
	service, _ := New(client, repository, Config{Mode: riotapi.ModeDirect, Region: riotapi.RegionAmericas, PageSize: 1, MaxMatches: 3, Concurrency: 1, Now: func() time.Time { return now }})
	result, err := service.Sync(context.Background(), "account-1", Filter{})
	if err != nil || !result.HasMore || result.Fetched != 1 || client.listCalls != 4 {
		t.Fatalf("bounded duplicate pagination = %#v, calls=%d, err=%v", result, client.listCalls, err)
	}
}

func TestMalformedMatchIDPageDoesNotAdvanceCursor(t *testing.T) {
	repository := openStore(t)
	now := time.Date(2026, 9, 19, 19, 46, 0, 0, time.UTC)
	seedAccount(t, repository, now)
	client := &fakeClient{listErr: &riotapi.Error{Kind: riotapi.ErrorMalformed, Operation: "list matches"}}
	service, _ := New(client, repository, Config{Mode: riotapi.ModeDirect, Region: riotapi.RegionAmericas, PageSize: 2, MaxMatches: 2, Now: func() time.Time { return now }})
	if _, err := service.Sync(context.Background(), "account-1", Filter{}); riotapi.KindOf(err) != riotapi.ErrorMalformed {
		t.Fatalf("sync error = %v", err)
	}
	checkpoint, err := repository.LoadRiotSyncCheckpoint(context.Background(), "account-1", Filter{}.Key())
	if err != nil || checkpoint.NextStart != 0 || checkpoint.CoverageEndUnix != 0 {
		t.Fatalf("malformed page checkpoint = %#v, %v", checkpoint, err)
	}
}

func TestCompletedSyncUsesOverlapAndRetainsCumulativeCoverage(t *testing.T) {
	repository := openStore(t)
	firstRun := time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)
	seedAccount(t, repository, firstRun)
	now := firstRun
	client := &fakeClient{pages: map[int][]string{}}
	service, _ := New(client, repository, Config{
		Mode: riotapi.ModeDirect, Region: riotapi.RegionAmericas, Lookback: 7 * 24 * time.Hour,
		Overlap: 2 * time.Hour, Now: func() time.Time { return now },
	})
	if _, err := service.Sync(context.Background(), "account-1", Filter{}); err != nil {
		t.Fatal(err)
	}
	initialStart := firstRun.Add(-7 * 24 * time.Hour).Unix()
	now = firstRun.Add(6 * time.Hour)
	if _, err := service.Sync(context.Background(), "account-1", Filter{}); err != nil {
		t.Fatal(err)
	}
	if len(client.queries) != 2 || client.queries[1].StartTime != firstRun.Add(-2*time.Hour).Unix() || client.queries[1].EndTime != now.Unix() {
		t.Fatalf("overlap queries = %#v", client.queries)
	}
	checkpoint, err := repository.LoadRiotSyncCheckpoint(context.Background(), "account-1", Filter{}.Key())
	if err != nil || checkpoint.CoverageStartUnix != initialStart || checkpoint.CoverageEndUnix != now.Unix() {
		t.Fatalf("cumulative coverage = %#v, %v", checkpoint, err)
	}
}

type fakeClient struct {
	mu                  sync.Mutex
	account             riotapi.Account
	pages               map[int][]string
	matches             map[string]riotapi.Match
	errors              map[string]error
	queries             []riotapi.MatchListQuery
	listCalls           int
	listErr             error
	active              int32
	maxActive           int32
	block               <-chan struct{}
	repeatPage          []string
	getCalls            map[string]int
	listRegions         []riotapi.Region
	getRegions          []riotapi.Region
	resolveStarted      chan struct{}
	resolveBlock        <-chan struct{}
	ignoreResolveCancel bool
}

func (client *fakeClient) ResolveAccount(ctx context.Context, _ riotapi.Region, _ string, _ string) (riotapi.Account, error) {
	if client.resolveStarted != nil {
		close(client.resolveStarted)
		client.resolveStarted = nil
	}
	if client.resolveBlock != nil {
		if client.ignoreResolveCancel {
			<-client.resolveBlock
		} else {
			select {
			case <-ctx.Done():
				return riotapi.Account{}, ctx.Err()
			case <-client.resolveBlock:
			}
		}
	}
	return client.account, nil
}

func (client *fakeClient) ListMatchIDs(_ context.Context, region riotapi.Region, _ string, query riotapi.MatchListQuery) ([]string, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.listRegions = append(client.listRegions, region)
	client.queries = append(client.queries, query)
	client.listCalls++
	if client.listErr != nil {
		return nil, client.listErr
	}
	if client.repeatPage != nil {
		return append([]string(nil), client.repeatPage...), nil
	}
	return append([]string(nil), client.pages[query.Start]...), nil
}

func (client *fakeClient) GetMatch(ctx context.Context, region riotapi.Region, id, _ string) (riotapi.Match, error) {
	active := atomic.AddInt32(&client.active, 1)
	defer atomic.AddInt32(&client.active, -1)
	for {
		maximum := atomic.LoadInt32(&client.maxActive)
		if active <= maximum || atomic.CompareAndSwapInt32(&client.maxActive, maximum, active) {
			break
		}
	}
	if client.block != nil {
		select {
		case <-ctx.Done():
			return riotapi.Match{}, ctx.Err()
		case <-client.block:
		}
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	client.getRegions = append(client.getRegions, region)
	if client.getCalls == nil {
		client.getCalls = make(map[string]int)
	}
	client.getCalls[id]++
	if err := client.errors[id]; err != nil {
		return riotapi.Match{}, err
	}
	return client.matches[id], nil
}

type blockingRepository struct {
	*storage.DB
	deleteStarted chan struct{}
	releaseDelete chan struct{}
}

type accountOverrideRepository struct {
	*storage.DB
	account storage.RiotAccount
}

type checkpointOverrideRepository struct {
	*storage.DB
	checkpoint storage.RiotSyncCheckpoint
}

type failSessionLinkRepository struct {
	*storage.DB
	failures      int
	stateFailures int
}

func (repository *accountOverrideRepository) LoadRiotAccount(context.Context, string) (storage.RiotAccount, error) {
	return repository.account, nil
}

func (repository *checkpointOverrideRepository) LoadRiotSyncCheckpoint(context.Context, string, string) (storage.RiotSyncCheckpoint, error) {
	return repository.checkpoint, nil
}

func (repository *failSessionLinkRepository) SaveSessionMatchLink(ctx context.Context, link storage.SessionMatchLink) error {
	if repository.failures > 0 {
		repository.failures--
		return errors.New("injected reconciliation storage failure")
	}
	return repository.DB.SaveSessionMatchLink(ctx, link)
}

func (repository *failSessionLinkRepository) SaveRiotReconciliationState(ctx context.Context, state storage.RiotReconciliationState) error {
	if repository.stateFailures > 0 {
		repository.stateFailures--
		return errors.New("injected reconciliation state failure")
	}
	return repository.DB.SaveRiotReconciliationState(ctx, state)
}

func (repository *blockingRepository) DeleteRiotAccount(ctx context.Context, accountID string) error {
	close(repository.deleteStarted)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-repository.releaseDelete:
		return repository.DB.DeleteRiotAccount(ctx, accountID)
	}
}

func (client *fakeClient) callsFor(id string) int {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.getCalls[id]
}

func openStore(t *testing.T) *storage.DB {
	t.Helper()
	store, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func seedAccount(t *testing.T, store *storage.DB, now time.Time) {
	t.Helper()
	if err := store.UpsertRiotAccount(context.Background(), storage.RiotAccount{ID: "account-1", PUUID: "self", Region: riotapi.RegionAmericas, Platform: riotapi.PlatformNA1, ConsentAt: now, LinkedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
}

func syncMatch(id string, end time.Time) riotapi.Match {
	return riotapi.Match{
		ID: id, Platform: riotapi.PlatformNA1, QueueID: 420, MapID: 11, Patch: "16.18",
		StartedAt: end.Add(-30 * time.Minute), EndedAt: end, DurationSeconds: 1800, ContentHash: id + "-hash",
		Player: riotapi.LinkedPlayerFacts{ChampionName: "Garen", Role: domain.RoleTop, RoleProvenance: riotapi.RoleFromTeamPosition, Win: true},
	}
}
