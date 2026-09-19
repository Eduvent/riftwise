package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"lolengine/internal/catalog"
	"lolengine/internal/domain"

	_ "modernc.org/sqlite"
)

const schemaVersion = 10

// DB is the local SQLite catalog and recommendation repository.
type DB struct {
	db             *sql.DB
	path           string
	riotStorageKey []byte
}

// RecommendationRecord is an opaque persisted recommendation snapshot.
type RecommendationRecord struct {
	MatchID   string
	Signature string
	Payload   []byte
	UpdatedAt time.Time
}

// DecisionCheckpoint is the opaque recommendation-semantic state committed
// atomically with decision events for restart-safe deduplication.
type DecisionCheckpoint struct {
	SessionID             string
	LastSequence          uint64
	LastLifecycleSequence uint64
	State                 json.RawMessage
	Evidence              *domain.RecommendationEvidence
	UpdatedAt             time.Time
}

// Open creates the parent directory, opens SQLite, applies safe local pragmas, and migrates the schema.
func Open(ctx context.Context, path string) (*DB, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("database path must not be empty")
	}
	memory := path == ":memory:" || strings.HasPrefix(path, "file::memory:")
	var riotStorageKey []byte
	if !memory {
		if err := ensurePrivateStorageDirectory(filepath.Dir(path)); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
		var err error
		riotStorageKey, err = loadOrCreateRiotStorageKey(path + ".riot-key")
		if err != nil {
			return nil, err
		}
		if err := ensurePrivateDatabaseFile(path); err != nil {
			return nil, err
		}
	} else {
		var err error
		riotStorageKey, err = newRiotStorageKey()
		if err != nil {
			return nil, err
		}
	}

	database, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open SQLite: %w", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	store := &DB{db: database, path: path, riotStorageKey: riotStorageKey}
	if err := store.configure(ctx); err != nil {
		database.Close()
		return nil, err
	}
	if !memory {
		if err := secureSQLiteArtifacts(path); err != nil {
			database.Close()
			return nil, err
		}
	}
	if err := store.migrate(ctx); err != nil {
		database.Close()
		return nil, err
	}
	if err := store.validateRiotStorageKey(ctx); err != nil {
		database.Close()
		return nil, err
	}
	privacyMaintenancePending, err := store.riotPrivacyMaintenancePending(ctx)
	if err != nil {
		database.Close()
		return nil, err
	}
	if privacyMaintenancePending && !memory {
		if err := store.compactSensitiveMigration(ctx); err != nil {
			database.Close()
			return nil, err
		}
	}
	if !memory {
		// Feedback deletion is already authoritative if this best-effort privacy
		// cleanup remains busy. The durable marker is retried on the next open.
		_ = store.RetryPersonalizationPrivacyMaintenance(ctx)
	}
	if !memory {
		if err := secureSQLiteArtifacts(path); err != nil {
			database.Close()
			return nil, err
		}
	}
	return store, nil
}

// Close releases the SQLite connection.
func (store *DB) Close() error {
	err := store.db.Close()
	for index := range store.riotStorageKey {
		store.riotStorageKey[index] = 0
	}
	store.riotStorageKey = nil
	return err
}

func (store *DB) configure(ctx context.Context) error {
	pragmas := []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA secure_delete = ON",
	}
	for _, statement := range pragmas {
		if _, err := store.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("configure SQLite with %q: %w", statement, err)
		}
	}
	if err := store.db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping SQLite: %w", err)
	}
	return nil
}

func (store *DB) migrate(ctx context.Context) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin schema migration: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL
		)`); err != nil {
		return fmt.Errorf("create schema migrations table: %w", err)
	}

	var current int
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if current > schemaVersion {
		return fmt.Errorf("database schema %d is newer than supported version %d", current, schemaVersion)
	}
	if current < 1 {
		statements := []string{
			`CREATE TABLE metadata (
				key TEXT PRIMARY KEY,
				value TEXT NOT NULL,
				updated_at TEXT NOT NULL
			)`,
			`CREATE TABLE catalogs (
				patch TEXT NOT NULL,
				locale TEXT NOT NULL,
				payload BLOB NOT NULL,
				synced_at_ns INTEGER NOT NULL,
				complete INTEGER NOT NULL CHECK (complete IN (0, 1)),
				PRIMARY KEY (patch, locale)
			)`,
			`CREATE INDEX catalogs_complete_synced_at ON catalogs(complete, synced_at_ns DESC)`,
			`CREATE TABLE latest_recommendation (
				id INTEGER PRIMARY KEY CHECK (id = 1),
				signature TEXT NOT NULL,
				payload BLOB NOT NULL,
				updated_at TEXT NOT NULL
			)`,
		}
		for _, statement := range statements {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply schema version 1: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO schema_migrations(version, applied_at) VALUES(1, ?)",
			time.Now().UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("record schema version 1: %w", err)
		}
		current = 1
	}
	if current < 2 {
		statements := []string{
			`CREATE TABLE match_sessions (
				id TEXT PRIMARY KEY,
				fingerprint TEXT NOT NULL,
				state TEXT NOT NULL CHECK (state IN ('active', 'ended')),
				started_at_ns INTEGER NOT NULL,
				updated_at_ns INTEGER NOT NULL,
				last_seen_at_ns INTEGER NOT NULL,
				ended_at_ns INTEGER,
				end_reason TEXT NOT NULL DEFAULT '',
				end_confidence REAL NOT NULL DEFAULT 0,
				start_game_time REAL NOT NULL DEFAULT 0,
				last_game_time REAL NOT NULL DEFAULT 0,
				player_riot_id TEXT NOT NULL DEFAULT '',
				champion_name TEXT NOT NULL DEFAULT '',
				team TEXT NOT NULL DEFAULT '',
				position TEXT NOT NULL DEFAULT '',
				map_number INTEGER NOT NULL DEFAULT 0,
				map_name TEXT NOT NULL DEFAULT '',
				game_mode TEXT NOT NULL DEFAULT '',
				game_version TEXT NOT NULL DEFAULT '',
				last_event_sequence INTEGER NOT NULL DEFAULT 0
			)`,
			`CREATE INDEX match_sessions_started ON match_sessions(started_at_ns DESC, id DESC)`,
			`CREATE INDEX match_sessions_state_updated ON match_sessions(state, updated_at_ns DESC)`,
			`CREATE TABLE match_events (
				id TEXT PRIMARY KEY,
				match_id TEXT NOT NULL REFERENCES match_sessions(id) ON DELETE CASCADE,
				sequence INTEGER NOT NULL,
				dedupe_key TEXT NOT NULL,
				kind TEXT NOT NULL,
				occurred_at_ns INTEGER NOT NULL,
				observed_at_ns INTEGER NOT NULL,
				game_time REAL NOT NULL DEFAULT 0,
				source TEXT NOT NULL,
				confidence REAL NOT NULL,
				payload BLOB NOT NULL,
				UNIQUE(match_id, sequence),
				UNIQUE(match_id, dedupe_key)
			)`,
			`CREATE INDEX match_events_match_sequence ON match_events(match_id, sequence)`,
			`CREATE INDEX match_events_kind_observed ON match_events(kind, observed_at_ns DESC)`,
		}
		for _, statement := range statements {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply schema version 2: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO schema_migrations(version, applied_at) VALUES(2, ?)",
			time.Now().UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("record schema version 2: %w", err)
		}
		current = 2
	}
	if current < 3 {
		statements := []string{
			`ALTER TABLE match_sessions ADD COLUMN connection_state TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE match_sessions ADD COLUMN outage_started_at_ns INTEGER`,
			`ALTER TABLE match_sessions ADD COLUMN last_failure_class TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE match_sessions ADD COLUMN last_failure_at_ns INTEGER`,
			`ALTER TABLE match_sessions ADD COLUMN unavailable_since_ns INTEGER`,
		}
		for _, statement := range statements {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply schema version 3: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO schema_migrations(version, applied_at) VALUES(3, ?)",
			time.Now().UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("record schema version 3: %w", err)
		}
		current = 3
	}
	if current < 4 {
		statements := []string{
			`CREATE TABLE decision_events (
				id TEXT PRIMARY KEY,
				match_id TEXT NOT NULL REFERENCES match_sessions(id) ON DELETE CASCADE,
				sequence INTEGER NOT NULL,
				dedupe_key TEXT NOT NULL,
				kind TEXT NOT NULL,
				importance TEXT NOT NULL,
				occurred_at_ns INTEGER NOT NULL,
				game_time REAL NOT NULL DEFAULT 0,
				previous_summary TEXT NOT NULL DEFAULT '',
				current_summary TEXT NOT NULL,
				reason_codes BLOB NOT NULL,
				related_lifecycle_event_id TEXT NOT NULL DEFAULT '',
				UNIQUE(match_id, sequence),
				UNIQUE(match_id, dedupe_key)
			)`,
			`CREATE INDEX decision_events_match_sequence ON decision_events(match_id, sequence)`,
			`CREATE INDEX decision_events_kind_occurred ON decision_events(kind, occurred_at_ns DESC)`,
			`CREATE TABLE decision_checkpoints (
				match_id TEXT PRIMARY KEY REFERENCES match_sessions(id) ON DELETE CASCADE,
				last_sequence INTEGER NOT NULL DEFAULT 0,
				last_lifecycle_sequence INTEGER NOT NULL DEFAULT 0,
				state BLOB NOT NULL,
				updated_at_ns INTEGER NOT NULL
			)`,
		}
		for _, statement := range statements {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply schema version 4: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO schema_migrations(version, applied_at) VALUES(4, ?)",
			time.Now().UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("record schema version 4: %w", err)
		}
	}
	if current < 5 {
		statements := []string{
			`CREATE TABLE match_player_checkpoints (
				match_id TEXT PRIMARY KEY REFERENCES match_sessions(id) ON DELETE CASCADE,
				observed_at_ns INTEGER NOT NULL,
				game_time REAL NOT NULL DEFAULT 0,
				champion_name TEXT NOT NULL DEFAULT '',
				role TEXT NOT NULL DEFAULT '',
				role_known INTEGER NOT NULL DEFAULT 0 CHECK(role_known IN (0, 1)),
				level INTEGER NOT NULL DEFAULT 0,
				kills INTEGER NOT NULL DEFAULT 0,
				deaths INTEGER NOT NULL DEFAULT 0,
				assists INTEGER NOT NULL DEFAULT 0,
				creep_score INTEGER NOT NULL DEFAULT 0,
				inventory BLOB NOT NULL
			)`,
			`ALTER TABLE decision_events ADD COLUMN evidence BLOB`,
			`ALTER TABLE decision_checkpoints ADD COLUMN evidence BLOB`,
			`CREATE TABLE match_summaries (
				match_id TEXT PRIMARY KEY REFERENCES match_sessions(id) ON DELETE CASCADE,
				schema_version INTEGER NOT NULL,
				source_session_updated_at_ns INTEGER NOT NULL,
				source_player_observed_at_ns INTEGER NOT NULL DEFAULT 0,
				source_lifecycle_sequence INTEGER NOT NULL DEFAULT 0,
				source_decision_sequence INTEGER NOT NULL DEFAULT 0,
				source_decision_checkpoint_at_ns INTEGER NOT NULL DEFAULT 0,
				started_at_ns INTEGER NOT NULL,
				ended_at_ns INTEGER,
				completion TEXT NOT NULL,
				champion_name TEXT NOT NULL DEFAULT '',
				role TEXT NOT NULL DEFAULT '',
				payload BLOB NOT NULL,
				digest TEXT NOT NULL,
				built_at_ns INTEGER NOT NULL
			)`,
			`CREATE INDEX match_summaries_started ON match_summaries(started_at_ns DESC, match_id DESC)`,
			`CREATE INDEX match_summaries_completion_ended ON match_summaries(completion, ended_at_ns DESC)`,
			`CREATE TABLE history_preferences (
				id INTEGER PRIMARY KEY CHECK(id = 1),
				retention_days INTEGER,
				max_completed_matches INTEGER,
				updated_at_ns INTEGER NOT NULL,
				CHECK(retention_days IS NULL OR retention_days > 0),
				CHECK(max_completed_matches IS NULL OR max_completed_matches > 0)
			)`,
		}
		for _, statement := range statements {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply schema version 5: %w", err)
			}
		}
		var recommendationTable int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'latest_recommendation'`,
		).Scan(&recommendationTable); err != nil {
			return fmt.Errorf("inspect latest recommendation table for schema version 5: %w", err)
		}
		if recommendationTable > 0 {
			if _, err := tx.ExecContext(ctx, `ALTER TABLE latest_recommendation ADD COLUMN match_id TEXT`); err != nil {
				return fmt.Errorf("apply schema version 5 recommendation association: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `CREATE INDEX latest_recommendation_match ON latest_recommendation(match_id)`); err != nil {
				return fmt.Errorf("apply schema version 5 recommendation index: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO schema_migrations(version, applied_at) VALUES(5, ?)",
			time.Now().UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("record schema version 5: %w", err)
		}
		current = 5
	}
	if current < 6 {
		statements := []string{
			`CREATE TABLE riot_accounts (
				id TEXT PRIMARY KEY,
				puuid TEXT NOT NULL UNIQUE,
				region TEXT NOT NULL CHECK(region IN ('americas', 'asia', 'europe', 'sea')),
				platform TEXT NOT NULL,
				consent_at_ns INTEGER NOT NULL,
				linked_at_ns INTEGER NOT NULL,
				updated_at_ns INTEGER NOT NULL,
				auto_sync INTEGER NOT NULL DEFAULT 0 CHECK(auto_sync IN (0, 1))
			)`,
			`CREATE INDEX riot_accounts_updated ON riot_accounts(updated_at_ns DESC, id DESC)`,
			`CREATE TABLE riot_matches (
				match_id TEXT PRIMARY KEY,
				platform TEXT NOT NULL,
				queue_id INTEGER NOT NULL,
				map_id INTEGER NOT NULL,
				patch TEXT NOT NULL,
				started_at_ns INTEGER NOT NULL,
				ended_at_ns INTEGER NOT NULL,
				duration_seconds INTEGER NOT NULL,
				content_hash TEXT NOT NULL UNIQUE,
				fetched_at_ns INTEGER NOT NULL
			)`,
			`CREATE INDEX riot_matches_end ON riot_matches(ended_at_ns DESC, match_id DESC)`,
			`CREATE TABLE riot_account_matches (
				account_id TEXT NOT NULL REFERENCES riot_accounts(id) ON DELETE CASCADE,
				match_id TEXT NOT NULL REFERENCES riot_matches(match_id) ON DELETE CASCADE,
				champion_name TEXT NOT NULL,
				role TEXT NOT NULL DEFAULT '',
				role_provenance TEXT NOT NULL,
				team_id INTEGER NOT NULL,
				win INTEGER NOT NULL CHECK(win IN (0, 1)),
				kills INTEGER NOT NULL,
				deaths INTEGER NOT NULL,
				assists INTEGER NOT NULL,
				creep_score INTEGER NOT NULL,
				final_items BLOB NOT NULL,
				facts_hash TEXT NOT NULL,
				opponent_champion TEXT NOT NULL DEFAULT '',
				opponent_provenance TEXT NOT NULL DEFAULT 'unknown',
				rank_bucket TEXT NOT NULL DEFAULT 'unknown',
				rank_provenance TEXT NOT NULL DEFAULT 'unavailable',
				PRIMARY KEY(account_id, match_id),
				UNIQUE(account_id, facts_hash)
			)`,
			`CREATE INDEX riot_account_matches_match ON riot_account_matches(match_id, account_id)`,
			`CREATE TABLE riot_sync_checkpoints (
				account_id TEXT NOT NULL REFERENCES riot_accounts(id) ON DELETE CASCADE,
				filter_key TEXT NOT NULL,
				run_started_at_ns INTEGER NOT NULL,
				window_start_unix INTEGER NOT NULL DEFAULT 0,
				window_end_unix INTEGER NOT NULL DEFAULT 0,
				next_start INTEGER NOT NULL DEFAULT 0,
				coverage_start_unix INTEGER NOT NULL DEFAULT 0,
				coverage_end_unix INTEGER NOT NULL DEFAULT 0,
				status TEXT NOT NULL,
				last_error_kind TEXT NOT NULL DEFAULT '',
				updated_at_ns INTEGER NOT NULL,
				PRIMARY KEY(account_id, filter_key)
			)`,
			`CREATE TABLE riot_pending_fetches (
				account_id TEXT NOT NULL REFERENCES riot_accounts(id) ON DELETE CASCADE,
				match_id TEXT NOT NULL,
				attempts INTEGER NOT NULL DEFAULT 0,
				next_retry_at_ns INTEGER NOT NULL,
				last_error_kind TEXT NOT NULL,
				updated_at_ns INTEGER NOT NULL,
				PRIMARY KEY(account_id, match_id)
			)`,
			`CREATE INDEX riot_pending_fetches_due ON riot_pending_fetches(account_id, next_retry_at_ns, match_id)`,
			`CREATE TABLE session_match_links (
				local_session_id TEXT NOT NULL REFERENCES match_sessions(id) ON DELETE CASCADE,
				account_id TEXT NOT NULL REFERENCES riot_accounts(id) ON DELETE CASCADE,
				official_match_id TEXT REFERENCES riot_matches(match_id) ON DELETE CASCADE,
				state TEXT NOT NULL CHECK(state IN ('linked', 'ambiguous', 'pending')),
				reason TEXT NOT NULL,
				confidence REAL NOT NULL,
				updated_at_ns INTEGER NOT NULL,
				PRIMARY KEY(local_session_id, account_id)
			)`,
			`CREATE INDEX session_match_links_official ON session_match_links(official_match_id, local_session_id)`,
		}
		for _, statement := range statements {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply schema version 6: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO schema_migrations(version, applied_at) VALUES(6, ?)",
			time.Now().UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("record schema version 6: %w", err)
		}
		current = 6
	}
	if current < 7 {
		if err := store.migrateRiotSchemaV7(ctx, tx); err != nil {
			return fmt.Errorf("apply schema version 7: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO schema_migrations(version, applied_at) VALUES(7, ?)",
			time.Now().UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("record schema version 7: %w", err)
		}
		current = 7
	}
	if current < 8 {
		for _, statement := range []string{
			`CREATE TABLE riot_reconciliation_states (
				account_id TEXT PRIMARY KEY REFERENCES riot_accounts(id) ON DELETE CASCADE,
				status TEXT NOT NULL CHECK(status IN ('pending', 'complete')),
				filter_key TEXT NOT NULL,
				run_started_at_ns INTEGER NOT NULL,
				window_end_unix INTEGER NOT NULL,
				last_error_kind TEXT NOT NULL DEFAULT '',
				updated_at_ns INTEGER NOT NULL
			)`,
			`CREATE INDEX riot_reconciliation_states_status ON riot_reconciliation_states(status, updated_at_ns, account_id)`,
			`INSERT INTO riot_reconciliation_states(account_id, status, filter_key, run_started_at_ns, window_end_unix, last_error_kind, updated_at_ns)
			 SELECT account_id, 'pending', filter_key, run_started_at_ns, window_end_unix, 'migration_pending', updated_at_ns
			 FROM (
				 SELECT account_id, filter_key, run_started_at_ns, window_end_unix, updated_at_ns,
					ROW_NUMBER() OVER (PARTITION BY account_id ORDER BY updated_at_ns DESC, filter_key ASC) AS row_number
				 FROM riot_sync_checkpoints WHERE status = 'complete'
			 ) WHERE row_number = 1`,
		} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply schema version 8: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO schema_migrations(version, applied_at) VALUES(8, ?)",
			time.Now().UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("record schema version 8: %w", err)
		}
		current = 8
	}
	if current < 9 {
		statements := []string{
			`CREATE TABLE personalization_preferences (
				id INTEGER PRIMARY KEY CHECK(id = 1),
				style TEXT NOT NULL CHECK(style IN ('safe', 'balanced', 'aggressive')),
				preferred_item_ids TEXT NOT NULL CHECK(
					json_valid(preferred_item_ids) AND json_type(preferred_item_ids) = 'array' AND
					json_array_length(preferred_item_ids) <= 12
				),
				avoided_item_ids TEXT NOT NULL CHECK(
					json_valid(avoided_item_ids) AND json_type(avoided_item_ids) = 'array' AND
					json_array_length(avoided_item_ids) <= 12
				),
				feedback_enabled INTEGER NOT NULL CHECK(feedback_enabled IN (0, 1)),
				version INTEGER NOT NULL CHECK(version > 0),
				updated_at_ns INTEGER NOT NULL CHECK(updated_at_ns > 0),
				reset_at_ns INTEGER CHECK(reset_at_ns IS NULL OR reset_at_ns > 0)
			)`,
			`INSERT INTO personalization_preferences(
				id, style, preferred_item_ids, avoided_item_ids, feedback_enabled, version, updated_at_ns
			) VALUES(1, 'balanced', '[]', '[]', 0, 1, CAST(strftime('%s', 'now') AS INTEGER) * 1000000000)`,
			`CREATE TABLE recommendation_feedback (
				client_feedback_id TEXT PRIMARY KEY CHECK(length(client_feedback_id) BETWEEN 1 AND 96),
				decision_event_id TEXT NOT NULL CHECK(length(decision_event_id) BETWEEN 1 AND 192),
				session_id TEXT NOT NULL CHECK(length(session_id) BETWEEN 1 AND 192),
				decision_sequence INTEGER NOT NULL CHECK(decision_sequence > 0),
				profile_id TEXT NOT NULL DEFAULT '' CHECK(length(profile_id) <= 128),
				profile_version TEXT NOT NULL DEFAULT '' CHECK(length(profile_version) <= 128),
				rules_version TEXT NOT NULL CHECK(length(rules_version) BETWEEN 1 AND 128),
				rules_source TEXT NOT NULL CHECK(length(rules_source) BETWEEN 1 AND 64),
				patch TEXT NOT NULL CHECK(length(patch) BETWEEN 1 AND 64),
				champion_id TEXT NOT NULL CHECK(length(champion_id) BETWEEN 1 AND 96),
				role TEXT NOT NULL CHECK(role IN ('top', 'jungle', 'middle', 'bottom', 'support')),
				stage TEXT NOT NULL CHECK(stage IN ('early', 'mid', 'late')),
				strategy TEXT NOT NULL CHECK(strategy IN ('balanced', 'split_push', 'teamfight')),
				item_id INTEGER NOT NULL CHECK(item_id > 0),
				value TEXT NOT NULL CHECK(value IN ('useful', 'not_useful')),
				created_at_ns INTEGER NOT NULL CHECK(created_at_ns > 0)
			)`,
			`CREATE INDEX recommendation_feedback_context ON recommendation_feedback(
				champion_id, role, stage, strategy, patch, profile_id, rules_version, item_id
			)`,
			`CREATE INDEX recommendation_feedback_created ON recommendation_feedback(created_at_ns, client_feedback_id)`,
		}
		for _, statement := range statements {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply schema version 9: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO schema_migrations(version, applied_at) VALUES(9, ?)",
			time.Now().UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("record schema version 9: %w", err)
		}
		current = 9
	}
	if current < 10 {
		for _, statement := range []string{
			`DELETE FROM recommendation_feedback WHERE rowid IN (
				SELECT rowid FROM (
					SELECT rowid, ROW_NUMBER() OVER (
						PARTITION BY decision_event_id, item_id
						ORDER BY created_at_ns ASC, client_feedback_id ASC
					) AS duplicate_rank
					FROM recommendation_feedback
				) WHERE duplicate_rank > 1
			)`,
			`CREATE UNIQUE INDEX recommendation_feedback_decision_item
				ON recommendation_feedback(decision_event_id, item_id)`,
		} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply schema version 10: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO schema_migrations(version, applied_at) VALUES(10, ?)",
			time.Now().UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("record schema version 10: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit schema migration: %w", err)
	}
	return nil
}

// SchemaVersion reports the latest applied migration.
func (store *DB) SchemaVersion(ctx context.Context) (int, error) {
	var version int
	err := store.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&version)
	return version, err
}

// SetMetadata stores a small process value.
func (store *DB) SetMetadata(ctx context.Context, key, value string) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("metadata key must not be empty")
	}
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO metadata(key, value, updated_at) VALUES(?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, time.Now().UTC().Format(time.RFC3339Nano),
	)
	return err
}

// Metadata returns a stored process value.
func (store *DB) Metadata(ctx context.Context, key string) (string, error) {
	var value string
	err := store.db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", catalog.ErrNotFound
	}
	return value, err
}

// SaveCatalog atomically marks a fully serialized patch catalog complete.
func (store *DB) SaveCatalog(ctx context.Context, value catalog.Catalog) error {
	if err := value.Validate(); err != nil {
		return err
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode catalog: %w", err)
	}
	syncedAt := value.SyncedAt
	if syncedAt.IsZero() {
		syncedAt = time.Now().UTC()
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin catalog transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO catalogs(patch, locale, payload, synced_at_ns, complete) VALUES(?, ?, ?, ?, 0)
		ON CONFLICT(patch, locale) DO UPDATE SET
			payload = excluded.payload,
			synced_at_ns = excluded.synced_at_ns,
			complete = 0`,
		value.Patch, value.Locale, payload, syncedAt.UnixNano(),
	); err != nil {
		return fmt.Errorf("write incomplete catalog: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		"UPDATE catalogs SET complete = 1 WHERE patch = ? AND locale = ?",
		value.Patch, value.Locale,
	); err != nil {
		return fmt.Errorf("mark catalog complete: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO metadata(key, value, updated_at) VALUES('active_catalog_patch', ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		value.Patch, time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		return fmt.Errorf("update active catalog metadata: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit catalog transaction: %w", err)
	}
	return nil
}

// LoadLatestCatalog returns the newest complete cached catalog for one locale.
func (store *DB) LoadLatestCatalog(ctx context.Context, locale string) (catalog.Catalog, error) {
	if strings.TrimSpace(locale) == "" {
		return catalog.Catalog{}, errors.New("catalog locale must not be empty")
	}
	var payload []byte
	err := store.db.QueryRowContext(ctx, `
		SELECT payload FROM catalogs
		WHERE complete = 1 AND locale = ?
		ORDER BY synced_at_ns DESC, rowid DESC
		LIMIT 1`, locale).Scan(&payload)
	return decodeCatalog(payload, err, "load latest catalog")
}

// LoadCatalogForVersion prefers an exact complete patch and otherwise returns
// the newest complete catalog with the same major.minor version and locale.
func (store *DB) LoadCatalogForVersion(ctx context.Context, locale, version string) (catalog.Catalog, error) {
	locale = strings.TrimSpace(locale)
	version = strings.TrimSpace(version)
	majorMinor := catalog.MajorMinor(version)
	if locale == "" {
		return catalog.Catalog{}, errors.New("catalog locale must not be empty")
	}
	if majorMinor == "" {
		return catalog.Catalog{}, fmt.Errorf("invalid game version %q", version)
	}
	var payload []byte
	err := store.db.QueryRowContext(ctx, `
		SELECT payload FROM catalogs
		WHERE complete = 1 AND locale = ? AND (patch = ? OR patch LIKE ?)
		ORDER BY CASE WHEN patch = ? THEN 0 ELSE 1 END, synced_at_ns DESC, rowid DESC
		LIMIT 1`, locale, version, majorMinor+".%", version).Scan(&payload)
	return decodeCatalog(payload, err, "load catalog for version")
}

func decodeCatalog(payload []byte, queryErr error, operation string) (catalog.Catalog, error) {
	if errors.Is(queryErr, sql.ErrNoRows) {
		return catalog.Catalog{}, catalog.ErrNotFound
	}
	if queryErr != nil {
		return catalog.Catalog{}, fmt.Errorf("%s: %w", operation, queryErr)
	}
	var value catalog.Catalog
	if err := json.Unmarshal(payload, &value); err != nil {
		return catalog.Catalog{}, fmt.Errorf("decode cached catalog: %w", err)
	}
	if err := value.Validate(); err != nil {
		return catalog.Catalog{}, fmt.Errorf("validate cached catalog: %w", err)
	}
	return value, nil
}

// SaveRecommendation updates the singleton only when its semantic signature changes.
func (store *DB) SaveRecommendation(ctx context.Context, record RecommendationRecord) (bool, error) {
	if record.Signature == "" {
		return false, errors.New("recommendation signature must not be empty")
	}
	if !json.Valid(record.Payload) {
		return false, errors.New("recommendation payload must be valid JSON")
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = time.Now().UTC()
	}
	result, err := store.db.ExecContext(ctx, `
		INSERT INTO latest_recommendation(id, signature, payload, updated_at, match_id) VALUES(1, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			signature = excluded.signature,
			payload = excluded.payload,
			updated_at = excluded.updated_at,
			match_id = excluded.match_id
		WHERE latest_recommendation.signature <> excluded.signature
		   OR COALESCE(latest_recommendation.match_id, '') <> COALESCE(excluded.match_id, '')`,
		record.Signature, record.Payload, record.UpdatedAt.Format(time.RFC3339Nano), strings.TrimSpace(record.MatchID),
	)
	if err != nil {
		return false, fmt.Errorf("save recommendation: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read recommendation write count: %w", err)
	}
	return rows > 0, nil
}

// LoadRecommendation loads the latest opaque recommendation payload.
func (store *DB) LoadRecommendation(ctx context.Context) (RecommendationRecord, error) {
	var record RecommendationRecord
	var updated string
	var matchID sql.NullString
	err := store.db.QueryRowContext(ctx,
		"SELECT signature, payload, updated_at, match_id FROM latest_recommendation WHERE id = 1",
	).Scan(&record.Signature, &record.Payload, &updated, &matchID)
	if errors.Is(err, sql.ErrNoRows) {
		return RecommendationRecord{}, catalog.ErrNotFound
	}
	if err != nil {
		return RecommendationRecord{}, err
	}
	if matchID.Valid {
		record.MatchID = matchID.String
	}
	record.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return RecommendationRecord{}, fmt.Errorf("parse recommendation timestamp: %w", err)
	}
	return record, nil
}

// SaveDecisionUpdates atomically persists a semantic checkpoint and any newly
// derived decision events. Exact retries are idempotent; an identity collision
// with different content rolls the entire transaction back.
func (store *DB) SaveDecisionUpdates(ctx context.Context, checkpoint DecisionCheckpoint, events []domain.DecisionEvent) error {
	checkpoint.SessionID = strings.TrimSpace(checkpoint.SessionID)
	if checkpoint.SessionID == "" {
		return errors.New("decision checkpoint session is required")
	}
	if len(checkpoint.State) == 0 || !json.Valid(checkpoint.State) {
		return errors.New("decision checkpoint state must be valid JSON")
	}
	if checkpoint.UpdatedAt.IsZero() {
		checkpoint.UpdatedAt = time.Now().UTC()
	}
	checkpointEvidence, err := encodeRecommendationEvidence(checkpoint.Evidence)
	if err != nil {
		return fmt.Errorf("encode decision checkpoint %s evidence: %w", checkpoint.SessionID, err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin decision update: %w", err)
	}
	defer tx.Rollback()

	var storedSequence, storedLifecycle uint64
	var storedState, storedEvidence []byte
	var storedUpdatedAt int64
	err = tx.QueryRowContext(ctx, `
		SELECT last_sequence, last_lifecycle_sequence, state, updated_at_ns, evidence
		FROM decision_checkpoints WHERE match_id = ?`, checkpoint.SessionID).Scan(
		&storedSequence, &storedLifecycle, &storedState, &storedUpdatedAt, &storedEvidence,
	)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read decision checkpoint %s: %w", checkpoint.SessionID, err)
	}
	if err == nil {
		incomingUpdatedAt := checkpoint.UpdatedAt.UTC().UnixNano()
		if checkpoint.LastSequence < storedSequence || checkpoint.LastLifecycleSequence < storedLifecycle || incomingUpdatedAt < storedUpdatedAt {
			return fmt.Errorf("decision checkpoint %s is older than stored state", checkpoint.SessionID)
		}
		if incomingUpdatedAt == storedUpdatedAt && (checkpoint.LastSequence != storedSequence ||
			checkpoint.LastLifecycleSequence != storedLifecycle || !bytes.Equal(checkpoint.State, storedState) ||
			!bytes.Equal(checkpointEvidence, storedEvidence)) {
			return fmt.Errorf("decision checkpoint %s conflicts with a different checkpoint at the same update time", checkpoint.SessionID)
		}
	}
	var sessionLifecycleSequence uint64
	if err := tx.QueryRowContext(ctx, `
		SELECT last_event_sequence FROM match_sessions WHERE id = ?`, checkpoint.SessionID).Scan(&sessionLifecycleSequence); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("decision checkpoint session %s does not exist", checkpoint.SessionID)
		}
		return fmt.Errorf("read match lifecycle cursor %s: %w", checkpoint.SessionID, err)
	}
	if checkpoint.LastLifecycleSequence > sessionLifecycleSequence {
		return fmt.Errorf("decision checkpoint %s has future lifecycle sequence %d beyond match sequence %d",
			checkpoint.SessionID, checkpoint.LastLifecycleSequence, sessionLifecycleSequence)
	}
	var storedEventSequence uint64
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(sequence), 0) FROM decision_events WHERE match_id = ?`, checkpoint.SessionID).Scan(&storedEventSequence); err != nil {
		return fmt.Errorf("read highest decision event sequence %s: %w", checkpoint.SessionID, err)
	}
	if storedEventSequence != storedSequence {
		return fmt.Errorf("decision checkpoint %s sequence %d does not match highest stored event %d",
			checkpoint.SessionID, storedSequence, storedEventSequence)
	}

	highestCommitted := storedSequence
	for _, event := range events {
		if err := validateDecisionEvent(checkpoint.SessionID, event); err != nil {
			return err
		}
		if err := validateDecisionLifecycleLink(ctx, tx, checkpoint, event); err != nil {
			return err
		}
		if event.Sequence <= storedSequence {
			exact, err := exactStoredDecisionEventConflict(ctx, tx, event)
			if err != nil {
				return err
			}
			if !exact {
				return fmt.Errorf("decision event %s conflicts with a different stored decision event", event.ID)
			}
			continue
		}
		expected := highestCommitted + 1
		if event.Sequence != expected {
			return fmt.Errorf("new decision events must be sorted and contiguous from stored checkpoint: got sequence %d, want %d", event.Sequence, expected)
		}
		reasonCodes, err := json.Marshal(event.ReasonCodes)
		if err != nil {
			return fmt.Errorf("encode decision event %s reason codes: %w", event.ID, err)
		}
		evidence, err := encodeRecommendationEvidence(event.Evidence)
		if err != nil {
			return fmt.Errorf("encode decision event %s evidence: %w", event.ID, err)
		}
		result, err := tx.ExecContext(ctx, `
			INSERT INTO decision_events(
				id, match_id, sequence, dedupe_key, kind, importance, occurred_at_ns,
				game_time, previous_summary, current_summary, reason_codes, related_lifecycle_event_id, evidence
			) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT DO NOTHING`,
			event.ID, event.SessionID, event.Sequence, event.DedupeKey, event.Kind, event.Importance,
			event.OccurredAt.UTC().UnixNano(), event.GameTimeSeconds, event.PreviousSummary,
			event.CurrentSummary, reasonCodes, event.RelatedLifecycleEventID, evidence,
		)
		if err != nil {
			return fmt.Errorf("save decision event %s: %w", event.ID, err)
		}
		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("read decision event %s write count: %w", event.ID, err)
		}
		if rowsAffected == 0 {
			exact, err := exactStoredDecisionEventConflict(ctx, tx, event)
			if err != nil {
				return err
			}
			if !exact {
				return fmt.Errorf("decision event %s conflicts with a different stored decision event", event.ID)
			}
			return fmt.Errorf("decision event %s exists beyond stored checkpoint sequence %d", event.ID, storedSequence)
		}
		highestCommitted = event.Sequence
	}
	if checkpoint.LastSequence != highestCommitted {
		return fmt.Errorf("decision checkpoint sequence %d must equal highest committed decision sequence %d",
			checkpoint.LastSequence, highestCommitted)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO decision_checkpoints(match_id, last_sequence, last_lifecycle_sequence, state, updated_at_ns, evidence)
		VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(match_id) DO UPDATE SET
			last_sequence=excluded.last_sequence,
			last_lifecycle_sequence=excluded.last_lifecycle_sequence,
			state=excluded.state,
			updated_at_ns=excluded.updated_at_ns,
			evidence=excluded.evidence`,
		checkpoint.SessionID, checkpoint.LastSequence, checkpoint.LastLifecycleSequence,
		[]byte(checkpoint.State), checkpoint.UpdatedAt.UTC().UnixNano(), checkpointEvidence,
	)
	if err != nil {
		return fmt.Errorf("save decision checkpoint %s: %w", checkpoint.SessionID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit decision update: %w", err)
	}
	return nil
}

func validateDecisionLifecycleLink(ctx context.Context, tx *sql.Tx, checkpoint DecisionCheckpoint, event domain.DecisionEvent) error {
	if strings.TrimSpace(event.RelatedLifecycleEventID) == "" {
		return nil
	}
	var sessionID string
	var sequence uint64
	err := tx.QueryRowContext(ctx, `
		SELECT match_id, sequence FROM match_events WHERE id = ?`, event.RelatedLifecycleEventID).Scan(&sessionID, &sequence)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("decision event %s related lifecycle event %s does not exist", event.ID, event.RelatedLifecycleEventID)
	}
	if err != nil {
		return fmt.Errorf("read decision event %s related lifecycle event: %w", event.ID, err)
	}
	if sessionID != checkpoint.SessionID {
		return fmt.Errorf("decision event %s related lifecycle event belongs to a different session", event.ID)
	}
	if sequence > checkpoint.LastLifecycleSequence {
		return fmt.Errorf("decision event %s related lifecycle event sequence %d is beyond decision lifecycle cursor %d",
			event.ID, sequence, checkpoint.LastLifecycleSequence)
	}
	return nil
}

// LoadDecisionCheckpoint returns restart-safe semantic state for one session.
func (store *DB) LoadDecisionCheckpoint(ctx context.Context, sessionID string) (DecisionCheckpoint, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return DecisionCheckpoint{}, errors.New("decision checkpoint session is required")
	}
	var checkpoint DecisionCheckpoint
	var state []byte
	var updatedAt int64
	var evidence []byte
	err := store.db.QueryRowContext(ctx, `
		SELECT match_id, last_sequence, last_lifecycle_sequence, state, updated_at_ns, evidence
		FROM decision_checkpoints WHERE match_id = ?`, sessionID).Scan(
		&checkpoint.SessionID, &checkpoint.LastSequence, &checkpoint.LastLifecycleSequence, &state, &updatedAt, &evidence,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return DecisionCheckpoint{}, catalog.ErrNotFound
	}
	if err != nil {
		return DecisionCheckpoint{}, fmt.Errorf("load decision checkpoint %s: %w", sessionID, err)
	}
	if !json.Valid(state) {
		return DecisionCheckpoint{}, fmt.Errorf("decision checkpoint %s contains invalid JSON", sessionID)
	}
	checkpoint.State = append(json.RawMessage(nil), state...)
	if len(evidence) > 0 {
		if err := json.Unmarshal(evidence, &checkpoint.Evidence); err != nil {
			return DecisionCheckpoint{}, fmt.Errorf("decode decision checkpoint %s evidence: %w", sessionID, err)
		}
	}
	checkpoint.UpdatedAt = time.Unix(0, updatedAt).UTC()
	return checkpoint, nil
}

// ListDecisionEvents returns durable decision events in ascending sequence order.
func (store *DB) ListDecisionEvents(ctx context.Context, sessionID string, after uint64, limit int) ([]domain.DecisionEvent, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("decision event session is required")
	}
	limit = boundedLimit(limit)
	rows, err := store.db.QueryContext(ctx, `
		SELECT id, match_id, sequence, dedupe_key, kind, importance, occurred_at_ns,
		       game_time, previous_summary, current_summary, reason_codes, related_lifecycle_event_id, evidence
		FROM decision_events
		WHERE match_id = ? AND sequence > ?
		ORDER BY sequence ASC
		LIMIT ?`, sessionID, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list decision events: %w", err)
	}
	defer rows.Close()
	result := make([]domain.DecisionEvent, 0, limit)
	for rows.Next() {
		event, err := scanDecisionEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan decision event: %w", err)
		}
		result = append(result, event)
	}
	return result, rows.Err()
}

func validateDecisionEvent(sessionID string, event domain.DecisionEvent) error {
	if strings.TrimSpace(event.ID) == "" || strings.TrimSpace(event.SessionID) == "" ||
		event.SessionID != sessionID || event.Sequence == 0 || strings.TrimSpace(event.DedupeKey) == "" {
		return errors.New("decision event id, matching session, sequence, and dedupe key are required")
	}
	if !validDecisionKind(event.Kind) {
		return fmt.Errorf("decision event %s has invalid kind %q", event.ID, event.Kind)
	}
	if event.Importance != domain.DecisionInformational && event.Importance != domain.DecisionImportant && event.Importance != domain.DecisionCritical {
		return fmt.Errorf("decision event %s has invalid importance %q", event.ID, event.Importance)
	}
	if event.OccurredAt.IsZero() {
		return fmt.Errorf("decision event %s occurrence time is required", event.ID)
	}
	if strings.TrimSpace(event.CurrentSummary) == "" {
		return fmt.Errorf("decision event %s current summary is required", event.ID)
	}
	if event.RelatedLifecycleEventID != strings.TrimSpace(event.RelatedLifecycleEventID) {
		return fmt.Errorf("decision event %s related lifecycle event ID must not contain surrounding whitespace", event.ID)
	}
	return nil
}

func validDecisionKind(kind domain.DecisionKind) bool {
	switch kind {
	case domain.DecisionTargetChanged, domain.DecisionComponentChanged, domain.DecisionComponentAffordable,
		domain.DecisionCriticalCounterActivated, domain.DecisionInventoryFull, domain.DecisionDataStale,
		domain.DecisionRulesUnavailable, domain.DecisionConnectionRestored, domain.DecisionReturnToBaseOptions:
		return true
	default:
		return false
	}
}

func exactStoredDecisionEventConflict(ctx context.Context, tx *sql.Tx, event domain.DecisionEvent) (bool, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, match_id, sequence, dedupe_key, kind, importance, occurred_at_ns,
		       game_time, previous_summary, current_summary, reason_codes, related_lifecycle_event_id, evidence
		FROM decision_events
		WHERE id = ? OR (match_id = ? AND sequence = ?) OR (match_id = ? AND dedupe_key = ?)`,
		event.ID, event.SessionID, event.Sequence, event.SessionID, event.DedupeKey)
	if err != nil {
		return false, fmt.Errorf("read decision event %s conflict: %w", event.ID, err)
	}
	defer rows.Close()
	matches := 0
	exact := false
	for rows.Next() {
		matches++
		stored, err := scanDecisionEvent(rows)
		if err != nil {
			return false, fmt.Errorf("scan decision event %s conflict: %w", event.ID, err)
		}
		exact = exact || decisionEventsExactlyEqual(stored, event)
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("read decision event %s conflicts: %w", event.ID, err)
	}
	return matches == 1 && exact, nil
}

func decisionEventsExactlyEqual(left, right domain.DecisionEvent) bool {
	return left.ID == right.ID && left.SessionID == right.SessionID && left.Sequence == right.Sequence &&
		left.DedupeKey == right.DedupeKey && left.Kind == right.Kind && left.Importance == right.Importance &&
		left.OccurredAt.Equal(right.OccurredAt) && left.GameTimeSeconds == right.GameTimeSeconds &&
		left.PreviousSummary == right.PreviousSummary && left.CurrentSummary == right.CurrentSummary &&
		left.RelatedLifecycleEventID == right.RelatedLifecycleEventID && stringSliceEqual(left.ReasonCodes, right.ReasonCodes) &&
		recommendationEvidenceEqual(left.Evidence, right.Evidence)
}

func scanDecisionEvent(row rowScanner) (domain.DecisionEvent, error) {
	var event domain.DecisionEvent
	var occurredAt int64
	var reasonCodes []byte
	var evidence []byte
	if err := row.Scan(
		&event.ID, &event.SessionID, &event.Sequence, &event.DedupeKey, &event.Kind, &event.Importance,
		&occurredAt, &event.GameTimeSeconds, &event.PreviousSummary, &event.CurrentSummary,
		&reasonCodes, &event.RelatedLifecycleEventID, &evidence,
	); err != nil {
		return domain.DecisionEvent{}, err
	}
	if err := json.Unmarshal(reasonCodes, &event.ReasonCodes); err != nil {
		return domain.DecisionEvent{}, fmt.Errorf("decode reason codes: %w", err)
	}
	if len(evidence) > 0 {
		if err := json.Unmarshal(evidence, &event.Evidence); err != nil {
			return domain.DecisionEvent{}, fmt.Errorf("decode recommendation evidence: %w", err)
		}
	}
	event.OccurredAt = time.Unix(0, occurredAt).UTC()
	return event, nil
}

func encodeRecommendationEvidence(value *domain.RecommendationEvidence) ([]byte, error) {
	if value == nil {
		return nil, nil
	}
	if value.SchemaVersion <= 0 {
		return nil, errors.New("recommendation evidence schema version must be positive")
	}
	return json.Marshal(value)
}

func recommendationEvidenceEqual(left, right *domain.RecommendationEvidence) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return bytes.Equal(leftJSON, rightJSON)
}

func stringSliceEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// SaveMatchUpdates atomically persists session summaries before their events can be published.
func (store *DB) SaveMatchUpdates(ctx context.Context, sessions []domain.MatchSession, events []domain.SemanticEvent) error {
	if len(sessions) == 0 && len(events) == 0 {
		return nil
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin match update: %w", err)
	}
	defer tx.Rollback()
	for _, session := range sessions {
		if strings.TrimSpace(session.ID) == "" || strings.TrimSpace(session.Fingerprint) == "" {
			return errors.New("match session id and fingerprint are required")
		}
		if session.State != domain.MatchActive && session.State != domain.MatchEnded {
			return fmt.Errorf("invalid match state %q", session.State)
		}
		if session.StartedAt.IsZero() {
			return errors.New("match start time is required")
		}
		if session.UpdatedAt.IsZero() {
			session.UpdatedAt = session.StartedAt
		}
		if session.LastSeenAt.IsZero() {
			session.LastSeenAt = session.UpdatedAt
		}
		var endedAt any
		if session.EndedAt != nil {
			endedAt = session.EndedAt.UTC().UnixNano()
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO match_sessions(
				id, fingerprint, state, started_at_ns, updated_at_ns, last_seen_at_ns, ended_at_ns,
				end_reason, end_confidence, start_game_time, last_game_time, player_riot_id,
					champion_name, team, position, map_number, queue_id, map_name, game_mode, game_version, last_event_sequence,
				connection_state, outage_started_at_ns, last_failure_class, last_failure_at_ns, unavailable_since_ns
				) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				fingerprint=excluded.fingerprint, state=excluded.state, updated_at_ns=excluded.updated_at_ns,
				last_seen_at_ns=excluded.last_seen_at_ns, ended_at_ns=excluded.ended_at_ns,
				end_reason=excluded.end_reason, end_confidence=excluded.end_confidence,
				last_game_time=excluded.last_game_time, player_riot_id=excluded.player_riot_id,
				champion_name=excluded.champion_name, team=excluded.team, position=excluded.position,
					map_number=excluded.map_number, queue_id=excluded.queue_id, map_name=excluded.map_name, game_mode=excluded.game_mode,
				game_version=excluded.game_version,
				last_event_sequence=MAX(match_sessions.last_event_sequence, excluded.last_event_sequence),
				connection_state=excluded.connection_state, outage_started_at_ns=excluded.outage_started_at_ns,
				last_failure_class=excluded.last_failure_class, last_failure_at_ns=excluded.last_failure_at_ns,
				unavailable_since_ns=excluded.unavailable_since_ns`,
			session.ID, session.Fingerprint, session.State, session.StartedAt.UTC().UnixNano(),
			session.UpdatedAt.UTC().UnixNano(), session.LastSeenAt.UTC().UnixNano(), endedAt,
			session.EndReason, session.EndConfidence, session.StartGameTime, session.LastGameTime,
			session.PlayerRiotID, session.ChampionName, session.Team, session.Position, session.MapNumber, session.QueueID,
			session.MapName, session.GameMode, session.GameVersion, session.LastEventSequence,
			session.ConnectionState, nullableUnixNano(session.OutageStartedAt), session.LastFailureClass,
			nullableUnixNano(session.LastFailureAt), nullableUnixNano(session.UnavailableSince),
		)
		if err != nil {
			return fmt.Errorf("save match %s: %w", session.ID, err)
		}
		if session.PlayerCheckpoint != nil {
			checkpoint := *session.PlayerCheckpoint
			if checkpoint.MatchID == "" {
				checkpoint.MatchID = session.ID
			}
			if checkpoint.MatchID != session.ID || checkpoint.ObservedAt.IsZero() {
				return fmt.Errorf("match %s player checkpoint requires matching session and observed time", session.ID)
			}
			checkpoint.Inventory = canonicalInventory(checkpoint.Inventory)
			inventory, marshalErr := json.Marshal(checkpoint.Inventory)
			if marshalErr != nil {
				return fmt.Errorf("encode match %s player inventory: %w", session.ID, marshalErr)
			}
			roleKnown := 0
			if checkpoint.RoleKnown && checkpoint.Role.Valid() {
				roleKnown = 1
			} else {
				checkpoint.Role = ""
				checkpoint.RoleKnown = false
			}
			result, err := tx.ExecContext(ctx, `
				INSERT INTO match_player_checkpoints(
					match_id, observed_at_ns, game_time, champion_name, role, role_known,
					level, kills, deaths, assists, creep_score, inventory
				) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(match_id) DO UPDATE SET
					observed_at_ns=excluded.observed_at_ns, game_time=excluded.game_time,
					champion_name=excluded.champion_name, role=excluded.role, role_known=excluded.role_known,
					level=excluded.level, kills=excluded.kills, deaths=excluded.deaths,
					assists=excluded.assists, creep_score=excluded.creep_score, inventory=excluded.inventory
				WHERE excluded.observed_at_ns > match_player_checkpoints.observed_at_ns`,
				checkpoint.MatchID, checkpoint.ObservedAt.UTC().UnixNano(), checkpoint.GameTimeSeconds,
				checkpoint.ChampionName, checkpoint.Role, roleKnown, checkpoint.Level, checkpoint.Kills,
				checkpoint.Deaths, checkpoint.Assists, checkpoint.CreepScore, inventory,
			)
			if err != nil {
				return fmt.Errorf("save match %s player checkpoint: %w", session.ID, err)
			}
			rowsAffected, err := result.RowsAffected()
			if err != nil {
				return fmt.Errorf("read match %s player checkpoint write count: %w", session.ID, err)
			}
			if rowsAffected == 0 {
				stored, err := loadPlayerCheckpoint(ctx, tx, checkpoint.MatchID)
				if err != nil {
					return err
				}
				if stored == nil || !playerCheckpointsExactlyEqual(*stored, checkpoint) {
					return fmt.Errorf("match %s player checkpoint conflicts with stored checkpoint at the same or newer observation time", session.ID)
				}
			}
		}
	}
	for _, event := range events {
		if strings.TrimSpace(event.ID) == "" || strings.TrimSpace(event.SessionID) == "" || event.Sequence == 0 || strings.TrimSpace(event.DedupeKey) == "" {
			return errors.New("match event id, session, sequence, and dedupe key are required")
		}
		payload := event.Payload
		if len(payload) == 0 {
			payload = json.RawMessage(`{}`)
		}
		if !json.Valid(payload) {
			return fmt.Errorf("match event %s payload must be valid JSON", event.ID)
		}
		if event.ObservedAt.IsZero() {
			event.ObservedAt = event.OccurredAt
		}
		if event.OccurredAt.IsZero() {
			event.OccurredAt = event.ObservedAt
		}
		if event.OccurredAt.IsZero() || event.ObservedAt.IsZero() {
			return fmt.Errorf("match event %s timestamps are required", event.ID)
		}
		result, err := tx.ExecContext(ctx, `
			INSERT INTO match_events(
				id, match_id, sequence, dedupe_key, kind, occurred_at_ns, observed_at_ns,
				game_time, source, confidence, payload
			) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT DO NOTHING`,
			event.ID, event.SessionID, event.Sequence, event.DedupeKey, event.Kind,
			event.OccurredAt.UTC().UnixNano(), event.ObservedAt.UTC().UnixNano(), event.GameTimeSeconds,
			event.Source, event.Confidence, []byte(payload),
		)
		if err != nil {
			return fmt.Errorf("save match event %s: %w", event.ID, err)
		}
		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("read match event %s write count: %w", event.ID, err)
		}
		if rowsAffected == 0 {
			event.Payload = append(json.RawMessage(nil), payload...)
			exact, err := exactStoredEventConflict(ctx, tx, event)
			if err != nil {
				return err
			}
			if !exact {
				return fmt.Errorf("match event %s conflicts with a different stored event", event.ID)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit match update: %w", err)
	}
	return nil
}

func exactStoredEventConflict(ctx context.Context, tx *sql.Tx, event domain.SemanticEvent) (bool, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, match_id, sequence, dedupe_key, kind, occurred_at_ns, observed_at_ns,
		       game_time, source, confidence, payload
		FROM match_events
		WHERE id = ? OR (match_id = ? AND sequence = ?) OR (match_id = ? AND dedupe_key = ?)`,
		event.ID, event.SessionID, event.Sequence, event.SessionID, event.DedupeKey)
	if err != nil {
		return false, fmt.Errorf("read match event %s conflict: %w", event.ID, err)
	}
	defer rows.Close()
	matches := 0
	exact := false
	for rows.Next() {
		matches++
		var stored domain.SemanticEvent
		var occurred, observed int64
		var payload []byte
		if err := rows.Scan(&stored.ID, &stored.SessionID, &stored.Sequence, &stored.DedupeKey, &stored.Kind,
			&occurred, &observed, &stored.GameTimeSeconds, &stored.Source, &stored.Confidence, &payload); err != nil {
			return false, fmt.Errorf("scan match event %s conflict: %w", event.ID, err)
		}
		stored.OccurredAt = time.Unix(0, occurred).UTC()
		stored.ObservedAt = time.Unix(0, observed).UTC()
		stored.Payload = payload
		exact = exact || semanticEventsExactlyEqual(stored, event)
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("read match event %s conflicts: %w", event.ID, err)
	}
	return matches == 1 && exact, nil
}

func semanticEventsExactlyEqual(left, right domain.SemanticEvent) bool {
	return left.ID == right.ID && left.SessionID == right.SessionID && left.Sequence == right.Sequence &&
		left.DedupeKey == right.DedupeKey && left.Kind == right.Kind && left.OccurredAt.Equal(right.OccurredAt) &&
		left.ObservedAt.Equal(right.ObservedAt) && left.GameTimeSeconds == right.GameTimeSeconds &&
		left.Source == right.Source && left.Confidence == right.Confidence && bytes.Equal(left.Payload, right.Payload)
}

// LoadActiveMatch returns the most recently updated incomplete match, if any.
func (store *DB) LoadActiveMatch(ctx context.Context) (*domain.MatchSession, error) {
	row := store.db.QueryRowContext(ctx, matchSessionSelect+`
		WHERE state = 'active'
		ORDER BY updated_at_ns DESC, id DESC
		LIMIT 1`)
	session, err := scanMatchSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load active match: %w", err)
	}
	return &session, nil
}

// LoadLifecycleRecovery returns one active session, or one recently ended
// session, together with a bounded event tail needed for restart-safe recovery.
func (store *DB) LoadLifecycleRecovery(ctx context.Context, endedAfter time.Time, eventLimit int) (domain.LifecycleRecovery, error) {
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.LifecycleRecovery{}, fmt.Errorf("begin lifecycle recovery: %w", err)
	}
	defer tx.Rollback()
	endedAfterNS := int64(-1 << 63)
	if !endedAfter.IsZero() {
		endedAfterNS = endedAfter.UTC().UnixNano()
	}
	row := tx.QueryRowContext(ctx, matchSessionSelect+`
		WHERE state = 'active' OR (state = 'ended' AND updated_at_ns >= ?)
		ORDER BY CASE WHEN state = 'active' THEN 0 ELSE 1 END, updated_at_ns DESC, id DESC
		LIMIT 1`, endedAfterNS)
	session, err := scanMatchSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return domain.LifecycleRecovery{}, fmt.Errorf("commit empty lifecycle recovery: %w", err)
		}
		return domain.LifecycleRecovery{}, nil
	}
	if err != nil {
		return domain.LifecycleRecovery{}, fmt.Errorf("load lifecycle recovery session: %w", err)
	}

	eventLimit = boundedLimit(eventLimit)
	rows, err := tx.QueryContext(ctx, `
		SELECT id, match_id, sequence, dedupe_key, kind, occurred_at_ns, observed_at_ns,
		       game_time, source, confidence, payload
		FROM match_events
		WHERE match_id = ?
		ORDER BY sequence DESC
		LIMIT ?`, session.ID, eventLimit)
	if err != nil {
		return domain.LifecycleRecovery{}, fmt.Errorf("load lifecycle recovery events: %w", err)
	}
	events := make([]domain.SemanticEvent, 0, eventLimit)
	for rows.Next() {
		event, err := scanMatchEvent(rows)
		if err != nil {
			rows.Close()
			return domain.LifecycleRecovery{}, fmt.Errorf("scan lifecycle recovery event: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return domain.LifecycleRecovery{}, fmt.Errorf("read lifecycle recovery events: %w", err)
	}
	if err := rows.Close(); err != nil {
		return domain.LifecycleRecovery{}, fmt.Errorf("close lifecycle recovery events: %w", err)
	}
	for left, right := 0, len(events)-1; left < right; left, right = left+1, right-1 {
		events[left], events[right] = events[right], events[left]
	}
	if err := tx.Commit(); err != nil {
		return domain.LifecycleRecovery{}, fmt.Errorf("commit lifecycle recovery: %w", err)
	}

	recovery := domain.LifecycleRecovery{Session: &session, RecentEvents: events}
	if session.State == domain.MatchEnded {
		recovery.Connection = domain.ConnectionWaiting
		return recovery, nil
	}
	// Version 3 checkpoints the latest outage class and interval on every
	// failure, including failures that do not emit a new semantic event. This
	// prevents restart recovery from mistaking the original connection_lost
	// class for the current failure class.
	if session.ConnectionState != "" || !session.OutageStartedAt.IsZero() || session.LastFailureClass != "" ||
		!session.LastFailureAt.IsZero() || !session.UnavailableSince.IsZero() {
		recovery.Connection = session.ConnectionState
		if recovery.Connection == "" {
			if session.OutageStartedAt.IsZero() {
				recovery.Connection = domain.ConnectionConnected
			} else {
				recovery.Connection = domain.ConnectionReconnecting
			}
		}
		recovery.OutageStartedAt = optionalTime(session.OutageStartedAt)
		recovery.LastFailureClass = session.LastFailureClass
		recovery.LastFailureAt = optionalTime(session.LastFailureAt)
		recovery.UnavailableSince = optionalTime(session.UnavailableSince)
		return recovery, nil
	}

	// Rows migrated from v2 have no checkpoint columns populated. Reconstruct
	// their best-effort connection state from events, but never carry unavailable
	// grace: later class transitions were not recorded in v2, so continuity
	// cannot be proven and restarting the interval is the conservative choice.
	recovery.Connection = domain.ConnectionConnected
	for _, event := range events {
		switch event.Kind {
		case domain.EventConnectionLost:
			recovery.Connection = domain.ConnectionReconnecting
			started := event.OccurredAt
			recovery.OutageStartedAt = &started
			recovery.LastFailureAt = &started
			recovery.UnavailableSince = nil
			var payload struct {
				Class domain.FetchErrorClass `json:"class"`
			}
			if json.Unmarshal(event.Payload, &payload) == nil {
				recovery.LastFailureClass = payload.Class
			}
		case domain.EventConnectionRestored:
			recovery.Connection = domain.ConnectionConnected
			recovery.OutageStartedAt = nil
			recovery.LastFailureClass = ""
			recovery.LastFailureAt = nil
			recovery.UnavailableSince = nil
		}
	}
	return recovery, nil
}

// ListMatches returns stable newest-first history with bounded pagination.
func (store *DB) ListMatches(ctx context.Context, limit, offset int) ([]domain.MatchSession, error) {
	limit = boundedLimit(limit)
	if offset < 0 {
		offset = 0
	}
	rows, err := store.db.QueryContext(ctx, matchSessionSelect+`
		ORDER BY started_at_ns DESC, id DESC
		LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list matches: %w", err)
	}
	defer rows.Close()
	result := make([]domain.MatchSession, 0, limit)
	for rows.Next() {
		session, err := scanMatchSession(rows)
		if err != nil {
			return nil, fmt.Errorf("scan match: %w", err)
		}
		result = append(result, session)
	}
	return result, rows.Err()
}

// ListMatchEvents returns durable events in ascending sequence order.
func (store *DB) ListMatchEvents(ctx context.Context, matchID string, after uint64, limit int) ([]domain.SemanticEvent, error) {
	if strings.TrimSpace(matchID) == "" {
		return nil, errors.New("match id is required")
	}
	limit = boundedLimit(limit)
	rows, err := store.db.QueryContext(ctx, `
		SELECT id, match_id, sequence, dedupe_key, kind, occurred_at_ns, observed_at_ns,
		       game_time, source, confidence, payload
		FROM match_events
		WHERE match_id = ? AND sequence > ?
		ORDER BY sequence ASC
		LIMIT ?`, matchID, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list match events: %w", err)
	}
	defer rows.Close()
	result := make([]domain.SemanticEvent, 0, limit)
	for rows.Next() {
		event, err := scanMatchEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan match event: %w", err)
		}
		result = append(result, event)
	}
	return result, rows.Err()
}

const matchSessionSelect = `
	SELECT id, fingerprint, state, started_at_ns, updated_at_ns, last_seen_at_ns, ended_at_ns,
	       end_reason, end_confidence, start_game_time, last_game_time, player_riot_id,
		       champion_name, team, position, map_number, queue_id, map_name, game_mode, game_version, last_event_sequence,
	       connection_state, outage_started_at_ns, last_failure_class, last_failure_at_ns, unavailable_since_ns
	FROM match_sessions`

type rowScanner interface {
	Scan(...any) error
}

func scanMatchEvent(row rowScanner) (domain.SemanticEvent, error) {
	var event domain.SemanticEvent
	var occurred, observed int64
	var payload []byte
	if err := row.Scan(&event.ID, &event.SessionID, &event.Sequence, &event.DedupeKey, &event.Kind,
		&occurred, &observed, &event.GameTimeSeconds, &event.Source, &event.Confidence, &payload); err != nil {
		return domain.SemanticEvent{}, err
	}
	event.OccurredAt = time.Unix(0, occurred).UTC()
	event.ObservedAt = time.Unix(0, observed).UTC()
	event.Payload = append(json.RawMessage(nil), payload...)
	return event, nil
}

func scanMatchSession(row rowScanner) (domain.MatchSession, error) {
	var session domain.MatchSession
	var started, updated, lastSeen int64
	var ended, outageStarted, lastFailureAt, unavailableSince sql.NullInt64
	err := row.Scan(
		&session.ID, &session.Fingerprint, &session.State, &started, &updated, &lastSeen, &ended,
		&session.EndReason, &session.EndConfidence, &session.StartGameTime, &session.LastGameTime,
		&session.PlayerRiotID, &session.ChampionName, &session.Team, &session.Position, &session.MapNumber, &session.QueueID,
		&session.MapName, &session.GameMode, &session.GameVersion, &session.LastEventSequence,
		&session.ConnectionState, &outageStarted, &session.LastFailureClass, &lastFailureAt, &unavailableSince,
	)
	if err != nil {
		return domain.MatchSession{}, err
	}
	session.StartedAt = time.Unix(0, started).UTC()
	session.UpdatedAt = time.Unix(0, updated).UTC()
	session.LastSeenAt = time.Unix(0, lastSeen).UTC()
	if ended.Valid {
		value := time.Unix(0, ended.Int64).UTC()
		session.EndedAt = &value
	}
	if outageStarted.Valid {
		session.OutageStartedAt = time.Unix(0, outageStarted.Int64).UTC()
	}
	if lastFailureAt.Valid {
		session.LastFailureAt = time.Unix(0, lastFailureAt.Int64).UTC()
	}
	if unavailableSince.Valid {
		session.UnavailableSince = time.Unix(0, unavailableSince.Int64).UTC()
	}
	return session, nil
}

func nullableUnixNano(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC().UnixNano()
}

func optionalTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	clone := value.UTC()
	return &clone
}

func boundedLimit(limit int) int {
	if limit <= 0 {
		return 20
	}
	if limit > 100 {
		return 100
	}
	return limit
}

func canonicalInventory(values []domain.InventoryCount) []domain.InventoryCount {
	counts := make(map[int]int, len(values))
	for _, value := range values {
		if value.ItemID > 0 && value.Count > 0 {
			counts[value.ItemID] += value.Count
		}
	}
	ids := make([]int, 0, len(counts))
	for id := range counts {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	result := make([]domain.InventoryCount, 0, len(ids))
	for _, id := range ids {
		result = append(result, domain.InventoryCount{ItemID: id, Count: counts[id]})
	}
	return result
}
