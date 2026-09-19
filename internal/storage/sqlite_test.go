package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"lolengine/internal/catalog"
	"lolengine/internal/domain"
)

func TestMigrationMetadataAndCatalogRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runtime", "lol-engine.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	version, err := store.SchemaVersion(ctx)
	if err != nil || version != schemaVersion {
		t.Fatalf("SchemaVersion() = %d, %v", version, err)
	}
	if err := store.SetMetadata(ctx, "test_key", "test_value"); err != nil {
		t.Fatalf("SetMetadata() error = %v", err)
	}
	value, err := store.Metadata(ctx, "test_key")
	if err != nil || value != "test_value" {
		t.Fatalf("Metadata() = %q, %v", value, err)
	}

	older := testCatalog("16.17.1", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	newer := testCatalog("16.18.1", time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC))
	if err := store.SaveCatalog(ctx, older); err != nil {
		t.Fatalf("SaveCatalog(older) error = %v", err)
	}
	if err := store.SaveCatalog(ctx, newer); err != nil {
		t.Fatalf("SaveCatalog(newer) error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open(restart) error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	cached, err := reopened.LoadLatestCatalog(ctx, "es_MX")
	if err != nil {
		t.Fatalf("LoadLatestCatalog() error = %v", err)
	}
	if cached.Patch != newer.Patch || cached.Items[1001].Name != "Boots" {
		t.Fatalf("cached catalog = %#v", cached)
	}
}

func TestCatalogLookupPrefersExactThenMajorMinorAndLocale(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "catalog-lookup.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	catalogs := []catalog.Catalog{
		testCatalog("16.18.1", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)),
		testCatalog("16.18.2", time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)),
		testCatalog("16.19.1", time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)),
	}
	english := testCatalog("16.18.9", time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC))
	english.Locale = "en_US"
	catalogs = append(catalogs, english)
	for _, value := range catalogs {
		if err := store.SaveCatalog(ctx, value); err != nil {
			t.Fatalf("SaveCatalog(%s/%s) error = %v", value.Locale, value.Patch, err)
		}
	}

	exact, err := store.LoadCatalogForVersion(ctx, "es_MX", "16.18.1")
	if err != nil || exact.Patch != "16.18.1" {
		t.Fatalf("exact lookup = %q, %v", exact.Patch, err)
	}
	majorMinor, err := store.LoadCatalogForVersion(ctx, "es_MX", "16.18.99.7")
	if err != nil || majorMinor.Patch != "16.18.2" {
		t.Fatalf("major/minor lookup = %q, %v", majorMinor.Patch, err)
	}
	latest, err := store.LoadLatestCatalog(ctx, "es_MX")
	if err != nil || latest.Patch != "16.19.1" {
		t.Fatalf("locale latest = %q, %v", latest.Patch, err)
	}
}

func TestRecommendationWritesOnlySemanticChanges(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "recommendations.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	first := RecommendationRecord{Signature: "same", Payload: []byte(`{"status":"ready"}`)}
	changed, err := store.SaveRecommendation(ctx, first)
	if err != nil || !changed {
		t.Fatalf("first SaveRecommendation() = %v, %v", changed, err)
	}
	changed, err = store.SaveRecommendation(ctx, RecommendationRecord{
		Signature: "same",
		Payload:   []byte(`{"status":"ready","generated_at":"later"}`),
	})
	if err != nil || changed {
		t.Fatalf("duplicate SaveRecommendation() = %v, %v", changed, err)
	}
	changed, err = store.SaveRecommendation(ctx, RecommendationRecord{
		Signature: "different",
		Payload:   []byte(`{"status":"save_for_component"}`),
	})
	if err != nil || !changed {
		t.Fatalf("changed SaveRecommendation() = %v, %v", changed, err)
	}
	record, err := store.LoadRecommendation(ctx)
	if err != nil || record.Signature != "different" {
		t.Fatalf("LoadRecommendation() = %#v, %v", record, err)
	}
}

func TestSaveCatalogRejectsIncompleteRecipeClosure(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "invalid-catalog.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	broken := testCatalog("16.18.1", time.Now().UTC())
	item := broken.Items[1001]
	item.From = []int{9999}
	broken.Items[1001] = item
	if err := store.SaveCatalog(ctx, broken); err == nil {
		t.Fatal("SaveCatalog() unexpectedly accepted a dangling recipe")
	}
	if _, err := store.LoadLatestCatalog(ctx, "es_MX"); err != catalog.ErrNotFound {
		t.Fatalf("LoadLatestCatalog() error = %v, want ErrNotFound", err)
	}
}

func TestMatchHistoryFreshReopenAndIdempotency(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	startedAt := time.Date(2026, 9, 19, 1, 0, 0, 0, time.UTC)
	session := domain.MatchSession{
		ID: "match-1", Fingerprint: "fingerprint", State: domain.MatchActive,
		StartedAt: startedAt, UpdatedAt: startedAt, LastSeenAt: startedAt,
		StartGameTime: 10, LastGameTime: 10, PlayerRiotID: "Self#LAS", ChampionName: "Garen",
		Team: "ORDER", Position: "TOP", MapNumber: 11, MapName: "Summoner's Rift",
		GameMode: "CLASSIC", GameVersion: "16.18.1", LastEventSequence: 1,
	}
	startEvent := domain.SemanticEvent{
		ID: "match-1:000001", SessionID: session.ID, Sequence: 1, DedupeKey: "lifecycle:start",
		Kind: domain.EventMatchStarted, OccurredAt: startedAt, ObservedAt: startedAt,
		GameTimeSeconds: 10, Source: domain.EventSourceObserved, Confidence: 1, Payload: json.RawMessage(`{"reason":"first_observation"}`),
	}
	if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{session}, []domain.SemanticEvent{startEvent}); err != nil {
		t.Fatalf("SaveMatchUpdates(start) error = %v", err)
	}
	if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{session}, []domain.SemanticEvent{startEvent}); err != nil {
		t.Fatalf("SaveMatchUpdates(duplicate) error = %v", err)
	}
	events, err := store.ListMatchEvents(ctx, session.ID, 0, 50)
	if err != nil || len(events) != 1 || events[0].Sequence != 1 {
		t.Fatalf("events after duplicate = %#v, %v", events, err)
	}
	active, err := store.LoadActiveMatch(ctx)
	if err != nil || active == nil || active.ID != session.ID {
		t.Fatalf("LoadActiveMatch() = %#v, %v", active, err)
	}

	endedAt := startedAt.Add(30 * time.Minute)
	session.State = domain.MatchEnded
	session.UpdatedAt = endedAt
	session.LastSeenAt = endedAt
	session.EndedAt = &endedAt
	session.EndReason = "terminal_event"
	session.EndConfidence = 1
	session.LastGameTime = 1810
	session.LastEventSequence = 2
	endEvent := domain.SemanticEvent{
		ID: "match-1:000002", SessionID: session.ID, Sequence: 2, DedupeKey: "lifecycle:end:terminal",
		Kind: domain.EventMatchEnded, OccurredAt: endedAt, ObservedAt: endedAt,
		GameTimeSeconds: 1810, Source: domain.EventSourceObserved, Confidence: 1, Payload: json.RawMessage(`{"reason":"terminal_event"}`),
	}
	if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{session}, []domain.SemanticEvent{endEvent}); err != nil {
		t.Fatalf("SaveMatchUpdates(end) error = %v", err)
	}
	if active, err = store.LoadActiveMatch(ctx); err != nil || active != nil {
		t.Fatalf("active after end = %#v, %v", active, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open(reopen) error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	matches, err := reopened.ListMatches(ctx, 10, 0)
	if err != nil || len(matches) != 1 || matches[0].State != domain.MatchEnded || matches[0].PlayerRiotID != "Self#LAS" {
		t.Fatalf("matches after reopen = %#v, %v", matches, err)
	}
	events, err = reopened.ListMatchEvents(ctx, session.ID, 1, 10)
	if err != nil || len(events) != 1 || events[0].Kind != domain.EventMatchEnded {
		t.Fatalf("paged events after reopen = %#v, %v", events, err)
	}
}

func TestMatchEventConflictRequiresExactStoredEvent(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "event-conflict.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 9, 19, 6, 0, 0, 0, time.UTC)
	session := domain.MatchSession{
		ID: "match-conflict", Fingerprint: "fingerprint", State: domain.MatchActive,
		StartedAt: now, UpdatedAt: now, LastSeenAt: now, LastEventSequence: 1,
		ConnectionState: domain.ConnectionReconnecting, OutageStartedAt: now,
		LastFailureClass: domain.FetchTransport, LastFailureAt: now,
	}
	event := domain.SemanticEvent{
		ID: "match-conflict:000001", SessionID: session.ID, Sequence: 1, DedupeKey: "inventory:000001:digest",
		Kind: domain.EventInventoryChanged, OccurredAt: now, ObservedAt: now, GameTimeSeconds: 10,
		Source: domain.EventSourceObserved, Confidence: 1, Payload: json.RawMessage(`{"added":[{"id":2003,"count":1}]}`),
	}
	if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{session}, []domain.SemanticEvent{event}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{session}, []domain.SemanticEvent{event}); err != nil {
		t.Fatalf("exact retry was not idempotent: %v", err)
	}

	mismatched := event
	mismatched.Payload = json.RawMessage(`{"added":[{"id":2003,"count":2}]}`)
	advanced := session
	advanced.LastEventSequence = 2
	advanced.UpdatedAt = now.Add(time.Second)
	advanced.LastFailureClass = domain.FetchGameUnavailable
	advanced.LastFailureAt = now.Add(time.Second)
	advanced.UnavailableSince = now.Add(time.Second)
	if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{advanced}, []domain.SemanticEvent{mismatched}); err == nil {
		t.Fatal("mismatched same-key event was silently accepted")
	}
	matches, err := store.ListMatches(ctx, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].LastEventSequence != 1 || matches[0].LastFailureClass != domain.FetchTransport ||
		!matches[0].LastFailureAt.Equal(now) || !matches[0].UnavailableSince.IsZero() {
		t.Fatalf("failed conflict advanced durable session sequence: %#v", matches)
	}
	events, err := store.ListMatchEvents(ctx, session.ID, 0, 10)
	if err != nil || len(events) != 1 || string(events[0].Payload) != string(event.Payload) {
		t.Fatalf("stored event changed after conflict: %#v, %v", events, err)
	}
}

func TestLifecycleRecoveryLoadsOutstandingOutageAndBoundedEventTail(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "lifecycle-recovery.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 19, 6, 30, 0, 0, time.UTC)
	session := domain.MatchSession{
		ID: "outage", Fingerprint: "fingerprint", State: domain.MatchActive,
		StartedAt: now, UpdatedAt: now.Add(time.Second), LastSeenAt: now, LastEventSequence: 2,
	}
	events := []domain.SemanticEvent{
		{
			ID: "outage:000001", SessionID: session.ID, Sequence: 1, DedupeKey: "lifecycle:start",
			Kind: domain.EventMatchStarted, OccurredAt: now, ObservedAt: now,
			Source: domain.EventSourceObserved, Confidence: 1, Payload: json.RawMessage(`{"reason":"first_observation"}`),
		},
		{
			ID: "outage:000002", SessionID: session.ID, Sequence: 2, DedupeKey: "connection:lost:2",
			Kind: domain.EventConnectionLost, OccurredAt: now.Add(time.Second), ObservedAt: now.Add(time.Second),
			Source: domain.EventSourceObserved, Confidence: 1, Payload: json.RawMessage(`{"class":"transport"}`),
		},
	}
	if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{session}, events); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	recovery, err := reopened.LoadLifecycleRecovery(ctx, now.Add(-time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	if recovery.Session == nil || recovery.Session.ID != session.ID || recovery.Connection != domain.ConnectionReconnecting ||
		recovery.OutageStartedAt == nil || !recovery.OutageStartedAt.Equal(now.Add(time.Second)) || len(recovery.RecentEvents) != 2 {
		t.Fatalf("lifecycle recovery = %#v", recovery)
	}
}

func TestLifecycleRecoveryReopensLatestOutageCheckpoint(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "latest-outage-checkpoint.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 19, 6, 40, 0, 0, time.UTC)
	outageStarted := now.Add(time.Second)
	failureAt := now.Add(3 * time.Second)
	unavailableSince := now.Add(2 * time.Second)
	session := domain.MatchSession{
		ID: "checkpoint", Fingerprint: "fingerprint", State: domain.MatchActive,
		StartedAt: now, UpdatedAt: failureAt, LastSeenAt: now, LastEventSequence: 1,
		ConnectionState: domain.ConnectionStale, OutageStartedAt: outageStarted,
		LastFailureClass: domain.FetchGameUnavailable, LastFailureAt: failureAt,
		UnavailableSince: unavailableSince,
	}
	event := domain.SemanticEvent{
		ID: "checkpoint:000001", SessionID: session.ID, Sequence: 1, DedupeKey: "connection:lost:1",
		Kind: domain.EventConnectionLost, OccurredAt: outageStarted, ObservedAt: outageStarted,
		Source: domain.EventSourceObserved, Confidence: 1, Payload: json.RawMessage(`{"class":"transport"}`),
	}
	if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{session}, []domain.SemanticEvent{event}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	recovery, err := reopened.LoadLifecycleRecovery(ctx, now.Add(-time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	if recovery.Connection != domain.ConnectionStale || recovery.LastFailureClass != domain.FetchGameUnavailable ||
		recovery.OutageStartedAt == nil || !recovery.OutageStartedAt.Equal(outageStarted) ||
		recovery.LastFailureAt == nil || !recovery.LastFailureAt.Equal(failureAt) ||
		recovery.UnavailableSince == nil || !recovery.UnavailableSince.Equal(unavailableSince) {
		t.Fatalf("reopened checkpoint = %#v", recovery)
	}
}

func TestLifecycleRecoveryExcludesExpiredEndedSession(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "expired-ended.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 9, 19, 6, 45, 0, 0, time.UTC)
	endedAt := now.Add(-time.Hour)
	session := domain.MatchSession{
		ID: "expired", Fingerprint: "fingerprint", State: domain.MatchEnded,
		StartedAt: endedAt.Add(-20 * time.Minute), UpdatedAt: endedAt, LastSeenAt: endedAt,
		EndedAt: &endedAt, EndReason: "terminal_event", EndConfidence: 1,
	}
	if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{session}, nil); err != nil {
		t.Fatal(err)
	}
	recovery, err := store.LoadLifecycleRecovery(ctx, now.Add(-10*time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	if recovery.Session != nil {
		t.Fatalf("expired ended session was recovered: %#v", recovery.Session)
	}
}

func TestMigrationFromSchemaV1AddsMatchHistory(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v1.db")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`,
		`INSERT INTO schema_migrations(version, applied_at) VALUES(1, '2026-09-19T00:00:00Z')`,
		`CREATE TABLE metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE catalogs (patch TEXT NOT NULL, locale TEXT NOT NULL, payload BLOB NOT NULL, synced_at_ns INTEGER NOT NULL, complete INTEGER NOT NULL, PRIMARY KEY (patch, locale))`,
		`CREATE TABLE latest_recommendation (id INTEGER PRIMARY KEY, signature TEXT NOT NULL, payload BLOB NOT NULL, updated_at TEXT NOT NULL)`,
	}
	for _, statement := range statements {
		if _, err := database.ExecContext(ctx, statement); err != nil {
			t.Fatalf("prepare v1: %v", err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open(v1) error = %v", err)
	}
	defer store.Close()
	version, err := store.SchemaVersion(ctx)
	if err != nil || version != schemaVersion {
		t.Fatalf("SchemaVersion() = %d, %v", version, err)
	}
	session := domain.MatchSession{
		ID: "incomplete", Fingerprint: "fp", State: domain.MatchActive,
		StartedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), LastSeenAt: time.Now().UTC(),
	}
	if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{session}, nil); err != nil {
		t.Fatalf("SaveMatchUpdates() after migration error = %v", err)
	}
	active, err := store.LoadActiveMatch(ctx)
	if err != nil || active == nil || active.ID != "incomplete" {
		t.Fatalf("incomplete session = %#v, %v", active, err)
	}
}

func TestMigrationFromSchemaV2AddsOutageCheckpointWithLegacyRecovery(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v2.db")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`,
		`INSERT INTO schema_migrations(version, applied_at) VALUES(1, '2026-09-19T00:00:00Z'), (2, '2026-09-19T00:01:00Z')`,
		`CREATE TABLE match_sessions (
			id TEXT PRIMARY KEY, fingerprint TEXT NOT NULL, state TEXT NOT NULL,
			started_at_ns INTEGER NOT NULL, updated_at_ns INTEGER NOT NULL, last_seen_at_ns INTEGER NOT NULL,
			ended_at_ns INTEGER, end_reason TEXT NOT NULL DEFAULT '', end_confidence REAL NOT NULL DEFAULT 0,
			start_game_time REAL NOT NULL DEFAULT 0, last_game_time REAL NOT NULL DEFAULT 0,
			player_riot_id TEXT NOT NULL DEFAULT '', champion_name TEXT NOT NULL DEFAULT '', team TEXT NOT NULL DEFAULT '',
			position TEXT NOT NULL DEFAULT '', map_number INTEGER NOT NULL DEFAULT 0, map_name TEXT NOT NULL DEFAULT '',
			game_mode TEXT NOT NULL DEFAULT '', game_version TEXT NOT NULL DEFAULT '', last_event_sequence INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE match_events (
			id TEXT PRIMARY KEY, match_id TEXT NOT NULL REFERENCES match_sessions(id) ON DELETE CASCADE,
			sequence INTEGER NOT NULL, dedupe_key TEXT NOT NULL, kind TEXT NOT NULL,
			occurred_at_ns INTEGER NOT NULL, observed_at_ns INTEGER NOT NULL, game_time REAL NOT NULL DEFAULT 0,
			source TEXT NOT NULL, confidence REAL NOT NULL, payload BLOB NOT NULL,
			UNIQUE(match_id, sequence), UNIQUE(match_id, dedupe_key)
		)`,
	}
	for _, statement := range statements {
		if _, err := database.ExecContext(ctx, statement); err != nil {
			t.Fatalf("prepare v2: %v", err)
		}
	}
	now := time.Date(2026, 9, 19, 6, 50, 0, 0, time.UTC)
	if _, err := database.ExecContext(ctx, `
		INSERT INTO match_sessions(id, fingerprint, state, started_at_ns, updated_at_ns, last_seen_at_ns, last_event_sequence)
		VALUES('legacy-v2', 'fingerprint', 'active', ?, ?, ?, 1)`, now.UnixNano(), now.Add(time.Second).UnixNano(), now.UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO match_events(id, match_id, sequence, dedupe_key, kind, occurred_at_ns, observed_at_ns, source, confidence, payload)
		VALUES('legacy-v2:000001', 'legacy-v2', 1, 'connection:lost:1', 'connection_lost', ?, ?, 'observed', 1, ?)`,
		now.Add(time.Second).UnixNano(), now.Add(time.Second).UnixNano(), []byte(`{"class":"game_unavailable"}`)); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open(v2) error = %v", err)
	}
	defer store.Close()
	version, err := store.SchemaVersion(ctx)
	if err != nil || version != schemaVersion {
		t.Fatalf("SchemaVersion() = %d, %v, want %d", version, err, schemaVersion)
	}
	recovery, err := store.LoadLifecycleRecovery(ctx, now.Add(-time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	if recovery.Session == nil || recovery.Session.ID != "legacy-v2" || recovery.Connection != domain.ConnectionReconnecting ||
		recovery.LastFailureClass != domain.FetchGameUnavailable || recovery.OutageStartedAt == nil || !recovery.OutageStartedAt.Equal(now.Add(time.Second)) ||
		recovery.UnavailableSince != nil {
		t.Fatalf("legacy v2 recovery = %#v", recovery)
	}

	updated := *recovery.Session
	updated.UpdatedAt = now.Add(2 * time.Second)
	updated.ConnectionState = domain.ConnectionStale
	updated.OutageStartedAt = now.Add(time.Second)
	updated.LastFailureClass = domain.FetchInvalidPayload
	updated.LastFailureAt = now.Add(2 * time.Second)
	if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{updated}, nil); err != nil {
		t.Fatalf("SaveMatchUpdates() after v2 migration error = %v", err)
	}
	recovery, err = store.LoadLifecycleRecovery(ctx, now.Add(-time.Minute), 10)
	if err != nil || recovery.LastFailureClass != domain.FetchInvalidPayload || recovery.LastFailureAt == nil ||
		!recovery.LastFailureAt.Equal(now.Add(2*time.Second)) || recovery.Connection != domain.ConnectionStale {
		t.Fatalf("v3 checkpoint after migration = %#v, %v", recovery, err)
	}
}

func testCatalog(patch string, syncedAt time.Time) catalog.Catalog {
	return catalog.Catalog{
		Patch:    patch,
		Locale:   "es_MX",
		SyncedAt: syncedAt,
		Items: map[int]catalog.Item{
			1001: {ID: 1001, Name: "Boots", Purchasable: true, AvailableMap11: true},
		},
		Champions: map[string]catalog.Champion{
			"Garen": {ID: "Garen", Key: "86", Name: "Garen"},
		},
	}
}
