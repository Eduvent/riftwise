package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"lolengine/internal/catalog"
	"lolengine/internal/domain"
)

func TestSchemaV5PersistsSummarySourcesAndRebuildRevision(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	endedAt := now.Add(30 * time.Minute)
	session := domain.MatchSession{
		ID: "history-match", Fingerprint: "private-fingerprint", State: domain.MatchEnded,
		StartedAt: now, UpdatedAt: endedAt, LastSeenAt: endedAt, EndedAt: &endedAt,
		EndReason: "terminal_event", EndConfidence: 1, LastGameTime: 1800,
		PlayerRiotID: "Private#ID", ChampionName: "Garen", Position: "TOP", LastEventSequence: 2,
		PlayerCheckpoint: &domain.MatchPlayerCheckpoint{
			MatchID: "history-match", ObservedAt: endedAt, GameTimeSeconds: 1800,
			ChampionName: "Garen", Role: domain.RoleTop, RoleKnown: true,
			Level: 16, Kills: 8, Deaths: 2, Assists: 5, CreepScore: 220,
			Inventory: []domain.InventoryCount{{ItemID: 2003, Count: 2}, {ItemID: 1001, Count: 1}, {ItemID: 2003, Count: 1}},
		},
	}
	events := []domain.SemanticEvent{
		{ID: "history-match:000001", SessionID: session.ID, Sequence: 1, DedupeKey: "start", Kind: domain.EventMatchStarted, OccurredAt: now, ObservedAt: now, Source: domain.EventSourceObserved, Confidence: 1, Payload: json.RawMessage(`{}`)},
		{ID: "history-match:000002", SessionID: session.ID, Sequence: 2, DedupeKey: "end", Kind: domain.EventMatchEnded, OccurredAt: endedAt, ObservedAt: endedAt, GameTimeSeconds: 1800, Source: domain.EventSourceObserved, Confidence: 1, Payload: json.RawMessage(`{"reason":"terminal_event"}`)},
	}
	if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{session}, events); err != nil {
		t.Fatal(err)
	}
	evidence := historyEvidence(4401)
	decision := decisionEvent(session.ID, 1, domain.DecisionTargetChanged, now.Add(10*time.Minute))
	decision.Evidence = evidence
	checkpoint := decisionCheckpoint(session.ID, 1, endedAt)
	checkpoint.LastLifecycleSequence = 2
	checkpoint.Evidence = evidence
	if err := store.SaveDecisionUpdates(ctx, checkpoint, []domain.DecisionEvent{decision}); err != nil {
		t.Fatal(err)
	}

	source, err := store.LoadMatchSummarySource(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if source.PlayerCheckpoint == nil || source.PlayerCheckpoint.Kills != 8 {
		t.Fatalf("player checkpoint = %+v", source.PlayerCheckpoint)
	}
	wantInventory := []domain.InventoryCount{{ItemID: 1001, Count: 1}, {ItemID: 2003, Count: 3}}
	if !reflect.DeepEqual(source.PlayerCheckpoint.Inventory, wantInventory) {
		t.Fatalf("canonical inventory = %+v", source.PlayerCheckpoint.Inventory)
	}
	if source.FinalRecommendationEvidence == nil || source.FinalRecommendationEvidence.Target.ID != 4401 || source.DecisionEvents[0].Evidence.Target.ID != 4401 {
		t.Fatalf("recommendation evidence = checkpoint:%+v events:%+v", source.FinalRecommendationEvidence, source.DecisionEvents)
	}

	summary := domain.MatchSummary{
		SchemaVersion: domain.MatchSummarySchemaVersion, MatchID: session.ID, SourceRevision: source.Revision,
		Digest: "stable-digest", ChampionName: "Garen", Role: domain.RoleTop, RoleKnown: true,
		Completion: domain.SummaryCompleteTerminal, StartedAt: now, EndedAt: &endedAt,
		Outcome: domain.OutcomeSummary{Value: domain.MatchOutcomeUnknown, Source: "unavailable_locally"},
	}
	summary.BuiltAt = endedAt.Add(time.Second)
	if err := store.SaveMatchSummaryIfCurrent(ctx, summary, source.Revision); err != nil {
		t.Fatal(err)
	}
	stale, err := store.ListStaleMatchSummaryIDs(ctx, 10)
	if err != nil || len(stale) != 0 {
		t.Fatalf("stale summaries = %v, %v", stale, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	stored, err := reopened.LoadMatchSummary(ctx, session.ID)
	if err != nil || stored.Digest != summary.Digest {
		t.Fatalf("reopened summary = %+v, %v", stored, err)
	}
	checkpoint.UpdatedAt = endedAt.Add(2 * time.Second)
	checkpoint.Evidence = historyEvidence(2504)
	if err := reopened.SaveDecisionUpdates(ctx, checkpoint, []domain.DecisionEvent{decision}); err != nil {
		t.Fatal(err)
	}
	stale, err = reopened.ListStaleMatchSummaryIDs(ctx, 10)
	if err != nil || !reflect.DeepEqual(stale, []string{session.ID}) {
		t.Fatalf("stale after late checkpoint = %v, %v", stale, err)
	}
}

func TestDecisionEvidenceConflictFailsLoudly(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "conflict.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 9, 19, 11, 0, 0, 0, time.UTC)
	session := decisionSession(now)
	if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{session}, nil); err != nil {
		t.Fatal(err)
	}
	event := decisionEvent(session.ID, 1, domain.DecisionTargetChanged, now)
	event.Evidence = historyEvidence(4401)
	checkpoint := decisionCheckpoint(session.ID, 1, now)
	checkpoint.Evidence = event.Evidence
	if err := store.SaveDecisionUpdates(ctx, checkpoint, []domain.DecisionEvent{event}); err != nil {
		t.Fatal(err)
	}
	conflict := event
	conflict.Evidence = historyEvidence(2504)
	checkpoint.Evidence = conflict.Evidence
	if err := store.SaveDecisionUpdates(ctx, checkpoint, []domain.DecisionEvent{conflict}); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("mismatched evidence conflict error = %v", err)
	}
	stored, err := store.ListDecisionEvents(ctx, session.ID, 0, 10)
	if err != nil || len(stored) != 1 || stored[0].Evidence.Target.ID != 4401 {
		t.Fatalf("stored after conflict = %+v, %v", stored, err)
	}
}

func TestDecisionCheckpointEvidenceConflictAtSameRevisionFailsLoudly(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "checkpoint-evidence-conflict.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 9, 19, 11, 30, 0, 0, time.UTC)
	session := decisionSession(now)
	if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{session}, nil); err != nil {
		t.Fatal(err)
	}
	checkpoint := decisionCheckpoint(session.ID, 0, now)
	checkpoint.Evidence = historyEvidence(4401)
	if err := store.SaveDecisionUpdates(ctx, checkpoint, nil); err != nil {
		t.Fatal(err)
	}
	conflict := checkpoint
	conflict.Evidence = historyEvidence(2504)
	if err := store.SaveDecisionUpdates(ctx, conflict, nil); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("mismatched same-revision checkpoint error = %v", err)
	}
	loaded, err := store.LoadDecisionCheckpoint(ctx, session.ID)
	if err != nil || loaded.Evidence == nil || loaded.Evidence.Target == nil || loaded.Evidence.Target.ID != 4401 {
		t.Fatalf("checkpoint after conflict = %+v, %v", loaded, err)
	}
}

func TestPlayerCheckpointSameObservationConflictRollsBackSession(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "player-checkpoint-conflict.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 9, 19, 11, 45, 0, 0, time.UTC)
	session := decisionSession(now)
	session.PlayerCheckpoint = &domain.MatchPlayerCheckpoint{
		MatchID: session.ID, ObservedAt: now, GameTimeSeconds: 100, ChampionName: "Garen",
		Role: domain.RoleTop, RoleKnown: true, Level: 6, Kills: 1, Deaths: 0, Assists: 1,
		Inventory: []domain.InventoryCount{{ItemID: 1001, Count: 1}},
	}
	if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{session}, nil); err != nil {
		t.Fatal(err)
	}
	conflict := session
	conflict.UpdatedAt = now.Add(time.Second)
	conflict.PlayerCheckpoint = clonePlayerCheckpointForTest(session.PlayerCheckpoint)
	conflict.PlayerCheckpoint.Deaths = 1
	if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{conflict}, nil); err == nil || !strings.Contains(err.Error(), "checkpoint conflicts") {
		t.Fatalf("same-observation checkpoint conflict error = %v", err)
	}
	source, err := store.LoadMatchSummarySource(ctx, session.ID)
	if err != nil || source.PlayerCheckpoint == nil || source.PlayerCheckpoint.Deaths != 0 || source.Session.UpdatedAt != now {
		t.Fatalf("source after checkpoint conflict = %+v, %v", source, err)
	}
}

func clonePlayerCheckpointForTest(value *domain.MatchPlayerCheckpoint) *domain.MatchPlayerCheckpoint {
	if value == nil {
		return nil
	}
	clone := *value
	clone.Inventory = append([]domain.InventoryCount(nil), value.Inventory...)
	return &clone
}

func TestHistoryPreferencesDeletionAndCascade(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "privacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	days, maximum := 30, 50
	preferences := domain.HistoryPreferences{RetentionDays: &days, MaxCompletedMatches: &maximum, UpdatedAt: now}
	if err := store.SaveHistoryPreferences(ctx, preferences); err != nil {
		t.Fatal(err)
	}
	active := decisionSession(now)
	active.ID = "active"
	ended := decisionSession(now.Add(-time.Hour))
	ended.ID = "ended"
	ended.State = domain.MatchEnded
	endedAt := now.Add(-30 * time.Minute)
	ended.EndedAt, ended.UpdatedAt = &endedAt, endedAt
	if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{active, ended}, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteMatchHistory(ctx, active.ID); !errors.Is(err, ErrActiveMatchHistory) {
		t.Fatalf("delete active error = %v", err)
	}
	if err := store.DeleteMatchHistory(ctx, ended.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadMatchSummarySource(ctx, ended.ID); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatalf("deleted source error = %v", err)
	}
	loaded, err := store.LoadHistoryPreferences(ctx)
	if err != nil || loaded.RetentionDays == nil || *loaded.RetentionDays != days {
		t.Fatalf("preferences after deletion = %+v, %v", loaded, err)
	}
}

func TestDeleteCompletedMatchHistoryRevalidatesExactSetAndPreservesPrivacyData(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "exact-delete.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 9, 19, 12, 30, 0, 0, time.UTC)
	first := decisionSession(now.Add(-2 * time.Hour))
	first.ID, first.State = "ended-first", domain.MatchEnded
	firstEnd := now.Add(-90 * time.Minute)
	first.EndedAt, first.UpdatedAt, first.LastSeenAt = &firstEnd, firstEnd, firstEnd
	second := decisionSession(now.Add(-time.Hour))
	second.ID, second.State = "ended-second", domain.MatchEnded
	secondEnd := now.Add(-30 * time.Minute)
	second.EndedAt, second.UpdatedAt, second.LastSeenAt = &secondEnd, secondEnd, secondEnd
	active := decisionSession(now)
	active.ID = "active"
	if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{first, second, active}, nil); err != nil {
		t.Fatal(err)
	}
	days := 30
	if err := store.SaveHistoryPreferences(ctx, domain.HistoryPreferences{RetentionDays: &days, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	cached := testCatalog("16.18.1", now)
	if err := store.SaveCatalog(ctx, cached); err != nil {
		t.Fatal(err)
	}

	// Any non-ended member invalidates and rolls back the whole exact set.
	if _, err := store.DeleteCompletedMatchHistory(ctx, []string{first.ID, active.ID}); !errors.Is(err, ErrActiveMatchHistory) {
		t.Fatalf("mixed-state exact deletion error = %v", err)
	}
	if _, err := store.LoadMatchSummarySource(ctx, first.ID); err != nil {
		t.Fatalf("transaction did not roll back first match: %v", err)
	}

	deleted, err := store.DeleteCompletedMatchHistory(ctx, []string{first.ID})
	if err != nil || deleted != 1 {
		t.Fatalf("exact deletion = %d, %v", deleted, err)
	}
	if _, err := store.LoadMatchSummarySource(ctx, first.ID); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatalf("selected match remains: %v", err)
	}
	for _, id := range []string{second.ID, active.ID} {
		if _, err := store.LoadMatchSummarySource(ctx, id); err != nil {
			t.Fatalf("unselected match %q removed: %v", id, err)
		}
	}
	preferences, err := store.LoadHistoryPreferences(ctx)
	if err != nil || preferences.RetentionDays == nil || *preferences.RetentionDays != days {
		t.Fatalf("preferences changed by deletion: %+v, %v", preferences, err)
	}
	catalogAfter, err := store.LoadLatestCatalog(ctx, cached.Locale)
	if err != nil || catalogAfter.Patch != cached.Patch {
		t.Fatalf("catalog changed by deletion: %+v, %v", catalogAfter, err)
	}
}

func TestMigrationFromSchemaV4AddsHistoryWithoutBackfill(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v4.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`,
		`INSERT INTO schema_migrations(version, applied_at) VALUES(1,'a'),(2,'b'),(3,'c'),(4,'d')`,
		`CREATE TABLE latest_recommendation (id INTEGER PRIMARY KEY, signature TEXT NOT NULL, payload BLOB NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE match_sessions (
			id TEXT PRIMARY KEY, fingerprint TEXT NOT NULL, state TEXT NOT NULL,
			started_at_ns INTEGER NOT NULL, updated_at_ns INTEGER NOT NULL, last_seen_at_ns INTEGER NOT NULL,
			ended_at_ns INTEGER, end_reason TEXT NOT NULL DEFAULT '', end_confidence REAL NOT NULL DEFAULT 0,
			start_game_time REAL NOT NULL DEFAULT 0, last_game_time REAL NOT NULL DEFAULT 0,
			player_riot_id TEXT NOT NULL DEFAULT '', champion_name TEXT NOT NULL DEFAULT '', team TEXT NOT NULL DEFAULT '',
			position TEXT NOT NULL DEFAULT '', map_number INTEGER NOT NULL DEFAULT 0, map_name TEXT NOT NULL DEFAULT '',
			game_mode TEXT NOT NULL DEFAULT '', game_version TEXT NOT NULL DEFAULT '', last_event_sequence INTEGER NOT NULL DEFAULT 0,
			connection_state TEXT NOT NULL DEFAULT '', outage_started_at_ns INTEGER,
			last_failure_class TEXT NOT NULL DEFAULT '', last_failure_at_ns INTEGER, unavailable_since_ns INTEGER
		)`,
		`CREATE TABLE match_events (
			id TEXT PRIMARY KEY, match_id TEXT NOT NULL REFERENCES match_sessions(id) ON DELETE CASCADE,
			sequence INTEGER NOT NULL, dedupe_key TEXT NOT NULL, kind TEXT NOT NULL,
			occurred_at_ns INTEGER NOT NULL, observed_at_ns INTEGER NOT NULL, game_time REAL NOT NULL DEFAULT 0,
			source TEXT NOT NULL, confidence REAL NOT NULL, payload BLOB NOT NULL,
			UNIQUE(match_id, sequence), UNIQUE(match_id, dedupe_key)
		)`,
		`CREATE TABLE decision_events (
			id TEXT PRIMARY KEY, match_id TEXT NOT NULL REFERENCES match_sessions(id) ON DELETE CASCADE,
			sequence INTEGER NOT NULL, dedupe_key TEXT NOT NULL, kind TEXT NOT NULL, importance TEXT NOT NULL,
			occurred_at_ns INTEGER NOT NULL, game_time REAL NOT NULL DEFAULT 0, previous_summary TEXT NOT NULL DEFAULT '',
			current_summary TEXT NOT NULL, reason_codes BLOB NOT NULL, related_lifecycle_event_id TEXT NOT NULL DEFAULT '',
			UNIQUE(match_id, sequence), UNIQUE(match_id, dedupe_key)
		)`,
		`CREATE TABLE decision_checkpoints (
			match_id TEXT PRIMARY KEY REFERENCES match_sessions(id) ON DELETE CASCADE,
			last_sequence INTEGER NOT NULL DEFAULT 0, last_lifecycle_sequence INTEGER NOT NULL DEFAULT 0,
			state BLOB NOT NULL, updated_at_ns INTEGER NOT NULL
		)`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("prepare v4: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open(v4) error = %v", err)
	}
	defer store.Close()
	version, err := store.SchemaVersion(ctx)
	if err != nil || version != schemaVersion {
		t.Fatalf("schema version = %d, %v", version, err)
	}
	rows, err := store.ListStaleMatchSummaryIDs(ctx, 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("migration eagerly backfilled summaries: %v, %v", rows, err)
	}
}

func TestListStaleMatchSummaryIDsDoesNotLetFreshRowsStarveMissingRows(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "stale-scan.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	base := time.Date(2026, 9, 19, 16, 0, 0, 0, time.UTC)
	for index, id := range []string{"fresh-oldest", "fresh-middle", "missing-newest"} {
		started := base.Add(time.Duration(index) * time.Hour)
		ended := started.Add(20 * time.Minute)
		session := domain.MatchSession{
			ID: id, Fingerprint: "private-" + id, State: domain.MatchEnded,
			StartedAt: started, UpdatedAt: ended, LastSeenAt: ended, EndedAt: &ended,
			EndReason: "terminal_event", EndConfidence: 1, LastGameTime: 1200,
		}
		if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{session}, nil); err != nil {
			t.Fatal(err)
		}
		if id == "missing-newest" {
			continue
		}
		source, err := store.LoadMatchSummarySource(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		summary := domain.MatchSummary{
			SchemaVersion: domain.MatchSummarySchemaVersion, MatchID: id, SourceRevision: source.Revision,
			Digest: "digest-" + id, Completion: domain.SummaryCompleteTerminal, StartedAt: started, EndedAt: &ended,
		}
		if err := store.SaveMatchSummaryIfCurrent(ctx, summary, source.Revision); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := store.ListStaleMatchSummaryIDs(ctx, 1)
	if err != nil || !reflect.DeepEqual(ids, []string{"missing-newest"}) {
		t.Fatalf("bounded stale scan = %v, %v; want missing row beyond fresh prefix", ids, err)
	}
}

func historyEvidence(target int) *domain.RecommendationEvidence {
	return &domain.RecommendationEvidence{
		SchemaVersion: domain.RecommendationEvidenceSchemaVersion, Status: "ready", Patch: "16.18.1",
		Target:        &domain.EvidenceItem{ID: target, Name: "Target"},
		RankedChoices: []domain.EvidenceChoice{{Rank: 1, Item: domain.EvidenceItem{ID: target, Name: "Target"}}},
		Confidence:    .8, Rules: domain.RuleProvenance{Version: "v2", Patch: "16.18", Source: "external_exact"},
	}
}
