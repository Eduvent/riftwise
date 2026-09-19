package storage

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"lolengine/internal/catalog"
	"lolengine/internal/domain"
	"lolengine/internal/personalization"
)

func TestPersonalizationSchemaV9ReopensAndKeepsTypedRecords(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "personalization.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 19, 13, 0, 0, 0, time.UTC)
	preferences := personalization.Preferences{
		Style: personalization.StyleSafe, PreferredItemIDs: []int{3001, 3002}, AvoidedItemIDs: []int{3003},
		FeedbackEnabled: true, Version: 4, UpdatedAt: now, ResetAt: now.Add(-time.Hour),
	}
	if err := store.SavePersonalizationPreferences(ctx, preferences); err != nil {
		t.Fatal(err)
	}
	record := personalization.FeedbackRecord{
		ClientFeedbackID: "client-v9", DecisionEventID: "match-v9:decision:000001", SessionID: "match-v9", DecisionSequence: 1,
		ProfileID: "garen-top", ProfileVersion: "generic-v1", RulesVersion: "garen-v2", RulesSource: "external_exact",
		Patch: "16.18.1", ChampionID: "Garen", Role: domain.RoleTop, Stage: domain.PhaseMid,
		Strategy: domain.StrategyBalanced, ItemID: 3001, Value: personalization.FeedbackUseful, CreatedAt: now,
	}
	seedFeedbackDecision(t, store, record, now)
	inserted, err := store.SaveRecommendationFeedback(ctx, record)
	if err != nil || !inserted {
		t.Fatalf("SaveRecommendationFeedback() = %v, %v", inserted, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	version, err := reopened.SchemaVersion(ctx)
	if err != nil || version != schemaVersion {
		t.Fatalf("SchemaVersion() = %d, %v", version, err)
	}
	got, err := reopened.LoadPersonalizationPreferences(ctx)
	if err != nil || got.Style != preferences.Style || got.Version != preferences.Version || len(got.PreferredItemIDs) != 2 {
		t.Fatalf("LoadPersonalizationPreferences() = %+v, %v", got, err)
	}
	records, err := reopened.LoadRecommendationFeedback(ctx)
	if err != nil || len(records) != 1 || records[0].SessionID != "match-v9" || records[0].ChampionID != "Garen" {
		t.Fatalf("LoadRecommendationFeedback() = %+v, %v", records, err)
	}
	if _, err := reopened.db.ExecContext(ctx, `DELETE FROM match_sessions WHERE id=?`, record.SessionID); err != nil {
		t.Fatal(err)
	}
	records, err = reopened.LoadRecommendationFeedback(ctx)
	if err != nil || len(records) != 1 {
		t.Fatalf("feedback should remain separate from match-history deletion: %+v, %v", records, err)
	}
}

func TestMigrationFromSchemaV8AddsPersonalizationWithoutChangingExistingData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v8-personalization.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetMetadata(ctx, "v8-proof", "preserved"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{`DROP TABLE recommendation_feedback`, `DROP TABLE personalization_preferences`, `DELETE FROM schema_migrations WHERE version>=9`} {
		if _, err := store.db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if value, err := reopened.Metadata(ctx, "v8-proof"); err != nil || value != "preserved" {
		t.Fatalf("existing v8 data = %q, %v", value, err)
	}
	preferences, err := reopened.LoadPersonalizationPreferences(ctx)
	if err != nil || preferences.Style != personalization.StyleBalanced || preferences.Version != 1 || preferences.FeedbackEnabled {
		t.Fatalf("migrated defaults = %+v, %v", preferences, err)
	}
}

func TestSchemaV9MigrationRollsBackOnFailure(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v9-rollback.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{`DROP TABLE recommendation_feedback`, `DROP TABLE personalization_preferences`, `DELETE FROM schema_migrations WHERE version>=9`, `CREATE TABLE recommendation_feedback(broken TEXT)`} {
		if _, err := store.db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(ctx, path); err == nil {
		reopened.Close()
		t.Fatal("Open() succeeded with a conflicting v9 table")
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var version, preferenceTables int
	if err := database.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='personalization_preferences'`).Scan(&preferenceTables); err != nil {
		t.Fatal(err)
	}
	if version != 8 || preferenceTables != 0 {
		t.Fatalf("failed migration committed partial state: version=%d preference_tables=%d", version, preferenceTables)
	}
}

func TestRecommendationFeedbackExactRetryConflictAndRollback(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "feedback.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 9, 19, 13, 30, 0, 0, time.UTC)
	record := personalization.FeedbackRecord{
		ClientFeedbackID: "same-client", DecisionEventID: "match-feedback:decision:000001", SessionID: "match-feedback", DecisionSequence: 1,
		RulesVersion: "garen-v2", RulesSource: "external_exact",
		Patch: "16.18.1", ChampionID: "Garen", Role: domain.RoleTop, Stage: domain.PhaseMid,
		Strategy: domain.StrategyBalanced, ItemID: 3001, Value: personalization.FeedbackUseful, CreatedAt: now,
	}
	seedFeedbackDecision(t, store, record, now)
	inserted, err := store.SaveRecommendationFeedback(ctx, record)
	if err != nil || !inserted {
		t.Fatalf("first insert = %v, %v", inserted, err)
	}
	inserted, err = store.SaveRecommendationFeedback(ctx, record)
	if err != nil || inserted {
		t.Fatalf("exact retry = %v, %v", inserted, err)
	}
	conflict := record
	conflict.Value = personalization.FeedbackNotUseful
	if _, err := store.SaveRecommendationFeedback(ctx, conflict); !errors.Is(err, personalization.ErrFeedbackConflict) {
		t.Fatalf("conflict error = %v", err)
	}
	records, err := store.LoadRecommendationFeedback(ctx)
	if err != nil || len(records) != 1 || records[0].Value != personalization.FeedbackUseful {
		t.Fatalf("records after conflict = %+v, %v", records, err)
	}

	logicalRetry := record
	logicalRetry.ClientFeedbackID = "different-client-same-choice"
	logicalRetry.CreatedAt = logicalRetry.CreatedAt.Add(time.Second)
	inserted, err = store.SaveRecommendationFeedback(ctx, logicalRetry)
	if err != nil || inserted {
		t.Fatalf("logical retry = %v, %v", inserted, err)
	}
	logicalConflict := logicalRetry
	logicalConflict.ClientFeedbackID = "different-client-different-value"
	logicalConflict.Value = personalization.FeedbackNotUseful
	if _, err := store.SaveRecommendationFeedback(ctx, logicalConflict); !errors.Is(err, personalization.ErrFeedbackConflict) {
		t.Fatalf("logical conflict error = %v", err)
	}
	records, err = store.LoadRecommendationFeedback(ctx)
	if err != nil || len(records) != 1 {
		t.Fatalf("logical uniqueness records = %+v, %v", records, err)
	}
}

func TestSchemaV10MigrationDeduplicatesDecisionChoice(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v9-logical-feedback.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 19, 14, 15, 0, 0, time.UTC)
	record := personalization.FeedbackRecord{
		ClientFeedbackID: "earliest-client", DecisionEventID: "match-v10:decision:000001", SessionID: "match-v10", DecisionSequence: 1,
		RulesVersion: "garen-v2", RulesSource: "external_exact", Patch: "16.18.1", ChampionID: "Garen",
		Role: domain.RoleTop, Stage: domain.PhaseMid, Strategy: domain.StrategyBalanced,
		ItemID: 3001, Value: personalization.FeedbackUseful, CreatedAt: now,
	}
	seedFeedbackDecision(t, store, record, now)
	if inserted, err := store.SaveRecommendationFeedback(ctx, record); err != nil || !inserted {
		t.Fatalf("seed feedback = %v, %v", inserted, err)
	}
	for _, statement := range []string{
		`DROP INDEX recommendation_feedback_decision_item`,
		`DELETE FROM schema_migrations WHERE version=10`,
	} {
		if _, err := store.db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	duplicate := record
	duplicate.ClientFeedbackID = "later-client"
	duplicate.Value = personalization.FeedbackNotUseful
	duplicate.CreatedAt = now.Add(time.Second)
	if inserted, err := store.SaveRecommendationFeedback(ctx, duplicate); err != nil || !inserted {
		t.Fatalf("seed v9 duplicate = %v, %v", inserted, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	records, err := reopened.LoadRecommendationFeedback(ctx)
	if err != nil || len(records) != 1 || records[0].ClientFeedbackID != record.ClientFeedbackID || records[0].Value != record.Value {
		t.Fatalf("migrated logical records = %+v, %v", records, err)
	}
	if version, err := reopened.SchemaVersion(ctx); err != nil || version != schemaVersion {
		t.Fatalf("schema version = %d, %v", version, err)
	}
}

func TestRecommendationFeedbackDeletionRetriesWALPrivacyMaintenance(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	path := filepath.Join(directory, "feedback-privacy.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 9, 19, 15, 0, 0, 0, time.UTC)
	canary := "feedback-canary-must-disappear-19c77b"
	record := personalization.FeedbackRecord{
		ClientFeedbackID: canary, DecisionEventID: "match-privacy:decision:000001", SessionID: "match-privacy", DecisionSequence: 1,
		RulesVersion: "garen-v2", RulesSource: "external_exact", Patch: "16.18.1", ChampionID: "Garen",
		Role: domain.RoleTop, Stage: domain.PhaseMid, Strategy: domain.StrategyBalanced,
		ItemID: 3001, Value: personalization.FeedbackUseful, CreatedAt: now,
	}
	seedFeedbackDecision(t, store, record, now)
	if inserted, err := store.SaveRecommendationFeedback(ctx, record); err != nil || !inserted {
		t.Fatalf("seed feedback = %v, %v", inserted, err)
	}

	reader, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	readTx, err := reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	var observed int
	if err := readTx.QueryRowContext(ctx, `SELECT COUNT(*) FROM recommendation_feedback`).Scan(&observed); err != nil || observed != 1 {
		t.Fatalf("hold read snapshot = %d, %v", observed, err)
	}

	deleted, err := store.DeleteRecommendationFeedback(ctx)
	if err != nil || deleted != 1 {
		t.Fatalf("committed deletion = %d, %v", deleted, err)
	}
	if value, err := store.Metadata(ctx, personalizationPrivacyMaintenanceMetadata); err != nil || value != "pending" {
		t.Fatalf("pending maintenance marker = %q, %v", value, err)
	}
	if !fileContainsAny(path, []byte(canary)) && !fileContainsAny(path+"-wal", []byte(canary)) && !fileContainsAny(path+"-shm", []byte(canary)) {
		t.Fatal("test canary was not observable before retry")
	}

	if err := readTx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.Metadata(ctx, personalizationPrivacyMaintenanceMetadata); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatalf("maintenance marker remained: %v", err)
	}
	for _, artifact := range []string{path, path + "-wal", path + "-shm"} {
		if fileContainsAny(artifact, []byte(canary)) {
			t.Fatalf("deleted feedback canary remained in %s", artifact)
		}
	}
}

func fileContainsAny(path string, needle []byte) bool {
	payload, err := os.ReadFile(path)
	return err == nil && bytes.Contains(payload, needle)
}

func TestPersonalizationPreferenceShapeFailureDoesNotAdvanceSingleton(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "preference-shape.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 9, 19, 14, 0, 0, 0, time.UTC)
	valid := personalization.Preferences{
		Style: personalization.StyleBalanced, PreferredItemIDs: []int{3001}, AvoidedItemIDs: []int{3002},
		Version: 2, UpdatedAt: now,
	}
	if err := store.SavePersonalizationPreferences(ctx, valid); err != nil {
		t.Fatal(err)
	}
	invalidCases := []personalization.Preferences{
		{Style: personalization.StyleSafe, PreferredItemIDs: []int{3002, 3001}, Version: 3, UpdatedAt: now},
		{Style: personalization.StyleSafe, PreferredItemIDs: []int{3001}, AvoidedItemIDs: []int{3001}, Version: 3, UpdatedAt: now},
	}
	for _, invalid := range invalidCases {
		if err := store.SavePersonalizationPreferences(ctx, invalid); err == nil {
			t.Fatalf("SavePersonalizationPreferences(%+v) succeeded", invalid)
		}
	}
	stored, err := store.LoadPersonalizationPreferences(ctx)
	if err != nil || stored.Version != valid.Version || stored.Style != valid.Style {
		t.Fatalf("singleton after invalid writes = %+v, %v", stored, err)
	}
	defaults := personalization.Preferences{Style: personalization.StyleBalanced, Version: 3, UpdatedAt: now.Add(time.Minute), ResetAt: now.Add(time.Minute)}
	if err := store.SavePersonalizationPreferences(ctx, defaults); err != nil {
		t.Fatalf("save empty/default arrays: %v", err)
	}
	stored, err = store.LoadPersonalizationPreferences(ctx)
	if err != nil || len(stored.PreferredItemIDs) != 0 || len(stored.AvoidedItemIDs) != 0 || stored.Version != 3 {
		t.Fatalf("empty/default singleton = %+v, %v", stored, err)
	}
}

func seedFeedbackDecision(t *testing.T, store *DB, record personalization.FeedbackRecord, now time.Time) {
	t.Helper()
	session := decisionSession(now)
	session.ID = record.SessionID
	if err := store.SaveMatchUpdates(context.Background(), []domain.MatchSession{session}, nil); err != nil {
		t.Fatal(err)
	}
	evidence := &domain.RecommendationEvidence{
		SchemaVersion: 1, Patch: record.Patch, Phase: record.Stage, Strategy: record.Strategy,
		Rules:         domain.RuleProvenance{Version: record.RulesVersion, Source: record.RulesSource},
		RankedChoices: []domain.EvidenceChoice{{Rank: 1, Item: domain.EvidenceItem{ID: record.ItemID}}},
	}
	if record.ProfileID != "" || record.ProfileVersion != "" {
		evidence.Profile = &domain.ProfileProvenance{ProfileID: record.ProfileID, RulesetVersion: record.ProfileVersion}
	}
	event := decisionEvent(record.SessionID, record.DecisionSequence, domain.DecisionTargetChanged, now)
	event.ID = record.DecisionEventID
	event.Evidence = evidence
	checkpoint := decisionCheckpoint(record.SessionID, record.DecisionSequence, now)
	checkpoint.Evidence = evidence
	if err := store.SaveDecisionUpdates(context.Background(), checkpoint, []domain.DecisionEvent{event}); err != nil {
		t.Fatal(err)
	}
}
