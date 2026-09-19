package storage

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const riotStorageKeyBytes = 32
const riotStorageKeyMetadata = "riot_storage_key_verification_v1"
const riotPrivacyMaintenanceMetadata = "riot_v7_privacy_maintenance_pending"

func ensurePrivateStorageDirectory(directory string) error {
	if directory == "" {
		directory = "."
	}
	_, statErr := os.Stat(directory)
	created := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !created {
		return statErr
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	// Do not chmod a pre-existing directory: it may be a shared ancestor such
	// as /tmp or a repository root. Newly created storage directories are ours.
	if created {
		if err := os.Chmod(directory, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func newRiotStorageKey() ([]byte, error) {
	key := make([]byte, riotStorageKeyBytes)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generate Riot storage key: %w", err)
	}
	return key, nil
}

func ensurePrivateDatabaseFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("create private SQLite file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close private SQLite file: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("tighten SQLite file permissions: %w", err)
	}
	return nil
}

func loadOrCreateRiotStorageKey(path string) ([]byte, error) {
	if payload, err := os.ReadFile(path); err == nil {
		if len(payload) != riotStorageKeyBytes {
			return nil, errors.New("Riot storage key has an invalid length")
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return nil, fmt.Errorf("tighten Riot storage key permissions: %w", err)
		}
		return append([]byte(nil), payload...), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read Riot storage key: %w", err)
	}

	key, err := newRiotStorageKey()
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return loadOrCreateRiotStorageKey(path)
	}
	if err != nil {
		return nil, fmt.Errorf("create Riot storage key: %w", err)
	}
	complete := false
	defer func() {
		_ = file.Close()
		if !complete {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(key); err != nil {
		return nil, fmt.Errorf("write Riot storage key: %w", err)
	}
	if err := file.Sync(); err != nil {
		return nil, fmt.Errorf("sync Riot storage key: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close Riot storage key: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("set Riot storage key permissions: %w", err)
	}
	complete = true
	return key, nil
}

func secureSQLiteArtifacts(path string) error {
	for _, candidate := range []string{path, path + "-wal", path + "-shm", path + ".riot-key"} {
		info, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect private storage artifact %s: %w", filepath.Base(candidate), err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("private storage artifact %s is not a regular file", filepath.Base(candidate))
		}
		if err := os.Chmod(candidate, 0o600); err != nil {
			return fmt.Errorf("tighten private storage artifact %s: %w", filepath.Base(candidate), err)
		}
	}
	return nil
}

func (store *DB) RiotMatchKey(accountID, externalMatchID string) (string, error) {
	accountID = strings.TrimSpace(accountID)
	externalMatchID = strings.TrimSpace(externalMatchID)
	if accountID == "" || externalMatchID == "" || len(store.riotStorageKey) != riotStorageKeyBytes {
		return "", errors.New("invalid Riot match key input")
	}
	mac := hmac.New(sha256.New, store.riotStorageKey)
	_, _ = mac.Write([]byte("lol-engine/riot-match/v1\x00"))
	_, _ = mac.Write([]byte(accountID))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(externalMatchID))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func (store *DB) riotStorageKeyVerification() string {
	mac := hmac.New(sha256.New, store.riotStorageKey)
	_, _ = mac.Write([]byte("lol-engine/riot-storage-key/verification/v1"))
	return hex.EncodeToString(mac.Sum(nil))
}

func (store *DB) validateRiotStorageKey(ctx context.Context) error {
	var stored string
	if err := store.db.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key = ?`, riotStorageKeyMetadata).Scan(&stored); err != nil {
		return fmt.Errorf("read Riot storage key verification: %w", err)
	}
	if !hmac.Equal([]byte(stored), []byte(store.riotStorageKeyVerification())) {
		return errors.New("Riot storage sidecar key does not match this database")
	}
	return nil
}

func (store *DB) riotPrivacyMaintenancePending(ctx context.Context) (bool, error) {
	var value string
	if err := store.db.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key = ?`, riotPrivacyMaintenanceMetadata).Scan(&value); err != nil {
		return false, fmt.Errorf("read Riot privacy maintenance state: %w", err)
	}
	return value == "1", nil
}

func (store *DB) encryptRiotMatchID(accountID, externalMatchID string) (string, []byte, error) {
	key, err := store.RiotMatchKey(accountID, externalMatchID)
	if err != nil {
		return "", nil, err
	}
	block, err := aes.NewCipher(store.riotStorageKey)
	if err != nil {
		return "", nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", nil, err
	}
	additional := []byte(accountID + "\x00" + key)
	ciphertext := aead.Seal(nil, nonce, []byte(externalMatchID), additional)
	return key, append(nonce, ciphertext...), nil
}

func (store *DB) decryptRiotMatchID(accountID, key string, encrypted []byte) (string, error) {
	block, err := aes.NewCipher(store.riotStorageKey)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(encrypted) < aead.NonceSize()+aead.Overhead() {
		return "", errors.New("invalid encrypted Riot match ID")
	}
	nonce, ciphertext := encrypted[:aead.NonceSize()], encrypted[aead.NonceSize():]
	plaintext, err := aead.Open(nil, nonce, ciphertext, []byte(accountID+"\x00"+key))
	if err != nil {
		return "", errors.New("authenticate encrypted Riot match ID")
	}
	externalID := string(plaintext)
	calculated, err := store.RiotMatchKey(accountID, externalID)
	if err != nil || !hmac.Equal([]byte(calculated), []byte(key)) {
		return "", errors.New("encrypted Riot match ID key mismatch")
	}
	return externalID, nil
}

type riotV6MatchRow struct {
	accountID, rawID, platform, patch, contentHash                                   string
	queueID, mapID, durationSeconds, teamID, win, kills, deaths, assists, creepScore int64
	startedAt, endedAt, fetchedAt                                                    int64
	champion, role, roleProvenance, factsHash, opponent, opponentProvenance          string
	finalItems                                                                       []byte
	rankBucket, rankProvenance                                                       string
}

type riotV6PendingRow struct {
	accountID, rawID, lastError string
	attempts                    int
	nextRetryAt, updatedAt      int64
}

type riotV6SessionLinkRow struct {
	localID, accountID, state, reason string
	officialRaw                       sql.NullString
	confidence                        float64
	updatedAt                         int64
}

func (store *DB) migrateRiotSchemaV7(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TEXT NOT NULL)`); err != nil {
		return err
	}
	// Correct the legacy routing classification before any resumed sync reads
	// the consented account route. ME1 was not previously accepted, but the
	// update also repairs manually imported legacy rows.
	if _, err := tx.ExecContext(ctx, `UPDATE riot_accounts SET region = 'sea' WHERE platform = 'OC1' AND region = 'americas'`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE riot_accounts SET region = 'europe' WHERE platform = 'ME1' AND region != 'europe'`); err != nil {
		return err
	}
	hasQueueID, err := sqliteColumnExists(ctx, tx, "match_sessions", "queue_id")
	if err != nil {
		return err
	}
	if !hasQueueID {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE match_sessions ADD COLUMN queue_id INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	hasCheckpointRegion, err := sqliteColumnExists(ctx, tx, "riot_sync_checkpoints", "region")
	if err != nil {
		return err
	}
	if !hasCheckpointRegion {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE riot_sync_checkpoints ADD COLUMN region TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}

	matchRows, err := tx.QueryContext(ctx, `
		SELECT am.account_id, m.match_id, m.platform, m.queue_id, m.map_id, m.patch,
			m.started_at_ns, m.ended_at_ns, m.duration_seconds, m.content_hash, m.fetched_at_ns,
			am.champion_name, am.role, am.role_provenance, am.team_id, am.win, am.kills, am.deaths,
			am.assists, am.creep_score, am.final_items, am.facts_hash, am.opponent_champion,
			am.opponent_provenance, am.rank_bucket, am.rank_provenance
		FROM riot_account_matches am JOIN riot_matches m ON m.match_id = am.match_id`)
	if err != nil {
		return err
	}
	var matches []riotV6MatchRow
	for matchRows.Next() {
		var value riotV6MatchRow
		if err := matchRows.Scan(&value.accountID, &value.rawID, &value.platform, &value.queueID, &value.mapID, &value.patch,
			&value.startedAt, &value.endedAt, &value.durationSeconds, &value.contentHash, &value.fetchedAt,
			&value.champion, &value.role, &value.roleProvenance, &value.teamID, &value.win, &value.kills,
			&value.deaths, &value.assists, &value.creepScore, &value.finalItems, &value.factsHash,
			&value.opponent, &value.opponentProvenance, &value.rankBucket, &value.rankProvenance); err != nil {
			matchRows.Close()
			return err
		}
		value.finalItems = append([]byte(nil), value.finalItems...)
		matches = append(matches, value)
	}
	if err := matchRows.Err(); err != nil {
		matchRows.Close()
		return err
	}
	if err := matchRows.Close(); err != nil {
		return err
	}

	pendingRows, err := tx.QueryContext(ctx, `SELECT account_id, match_id, attempts, next_retry_at_ns, last_error_kind, updated_at_ns FROM riot_pending_fetches`)
	if err != nil {
		return err
	}
	var pending []riotV6PendingRow
	for pendingRows.Next() {
		var value riotV6PendingRow
		if err := pendingRows.Scan(&value.accountID, &value.rawID, &value.attempts, &value.nextRetryAt, &value.lastError, &value.updatedAt); err != nil {
			pendingRows.Close()
			return err
		}
		pending = append(pending, value)
	}
	if err := pendingRows.Err(); err != nil {
		pendingRows.Close()
		return err
	}
	if err := pendingRows.Close(); err != nil {
		return err
	}

	linkRows, err := tx.QueryContext(ctx, `SELECT local_session_id, account_id, official_match_id, state, reason, confidence, updated_at_ns FROM session_match_links`)
	if err != nil {
		return err
	}
	var links []riotV6SessionLinkRow
	for linkRows.Next() {
		var value riotV6SessionLinkRow
		if err := linkRows.Scan(&value.localID, &value.accountID, &value.officialRaw, &value.state, &value.reason, &value.confidence, &value.updatedAt); err != nil {
			linkRows.Close()
			return err
		}
		links = append(links, value)
	}
	if err := linkRows.Err(); err != nil {
		linkRows.Close()
		return err
	}
	if err := linkRows.Close(); err != nil {
		return err
	}

	for _, statement := range []string{
		`DROP TABLE session_match_links`,
		`DROP TABLE riot_pending_fetches`,
		`DROP TABLE riot_account_matches`,
		`DROP TABLE riot_matches`,
		`CREATE TABLE riot_matches (
			match_key TEXT PRIMARY KEY,
			platform TEXT NOT NULL,
			queue_id INTEGER NOT NULL,
			map_id INTEGER NOT NULL,
			patch TEXT NOT NULL,
			started_at_ns INTEGER NOT NULL,
			ended_at_ns INTEGER NOT NULL,
			duration_seconds INTEGER NOT NULL,
			content_hash TEXT NOT NULL,
			fetched_at_ns INTEGER NOT NULL
		)`,
		`CREATE INDEX riot_matches_end ON riot_matches(ended_at_ns DESC, match_key DESC)`,
		`CREATE TABLE riot_account_matches (
			account_id TEXT NOT NULL REFERENCES riot_accounts(id) ON DELETE CASCADE,
			match_key TEXT NOT NULL REFERENCES riot_matches(match_key) ON DELETE CASCADE,
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
			PRIMARY KEY(account_id, match_key)
		)`,
		`CREATE INDEX riot_account_matches_match ON riot_account_matches(match_key, account_id)`,
		`CREATE TABLE riot_pending_fetches (
			account_id TEXT NOT NULL REFERENCES riot_accounts(id) ON DELETE CASCADE,
			match_key TEXT NOT NULL,
			match_id_ciphertext BLOB NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			next_retry_at_ns INTEGER NOT NULL,
			last_error_kind TEXT NOT NULL,
			updated_at_ns INTEGER NOT NULL,
			PRIMARY KEY(account_id, match_key)
		)`,
		`CREATE INDEX riot_pending_fetches_due ON riot_pending_fetches(account_id, next_retry_at_ns, match_key)`,
		`CREATE TABLE session_match_links (
			local_session_id TEXT NOT NULL REFERENCES match_sessions(id) ON DELETE CASCADE,
			account_id TEXT NOT NULL REFERENCES riot_accounts(id) ON DELETE CASCADE,
			official_match_key TEXT REFERENCES riot_matches(match_key) ON DELETE CASCADE,
			state TEXT NOT NULL CHECK(state IN ('linked', 'ambiguous', 'pending')),
			reason TEXT NOT NULL,
			confidence REAL NOT NULL,
			updated_at_ns INTEGER NOT NULL,
			PRIMARY KEY(local_session_id, account_id)
		)`,
		`CREATE INDEX session_match_links_official ON session_match_links(official_match_key, local_session_id)`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}

	knownKeys := make(map[string]struct{}, len(matches))
	for _, value := range matches {
		matchKey, err := store.RiotMatchKey(value.accountID, value.rawID)
		if err != nil {
			return err
		}
		generalHash := semanticHash(struct {
			Platform, Patch                    string
			QueueID, MapID, StartedAt, EndedAt int64
			DurationSeconds                    int64
		}{value.platform, value.patch, value.queueID, value.mapID, value.startedAt, value.endedAt, value.durationSeconds})
		factsHash := semanticHash(struct {
			Champion, Role, RoleProvenance, Opponent, OpponentProvenance, RankBucket, RankProvenance string
			TeamID, Win, Kills, Deaths, Assists, CreepScore                                          int64
			FinalItems                                                                               json.RawMessage
		}{value.champion, value.role, value.roleProvenance, value.opponent, value.opponentProvenance, value.rankBucket, value.rankProvenance,
			value.teamID, value.win, value.kills, value.deaths, value.assists, value.creepScore, value.finalItems})
		if _, err := tx.ExecContext(ctx, `INSERT INTO riot_matches(match_key, platform, queue_id, map_id, patch, started_at_ns, ended_at_ns, duration_seconds, content_hash, fetched_at_ns)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, matchKey, value.platform, value.queueID, value.mapID, value.patch, value.startedAt, value.endedAt, value.durationSeconds, generalHash, value.fetchedAt); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO riot_account_matches(account_id, match_key, champion_name, role, role_provenance, team_id, win, kills, deaths, assists, creep_score, final_items, facts_hash, opponent_champion, opponent_provenance, rank_bucket, rank_provenance)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, value.accountID, matchKey, value.champion, value.role, value.roleProvenance,
			value.teamID, value.win, value.kills, value.deaths, value.assists, value.creepScore, value.finalItems, factsHash, value.opponent, value.opponentProvenance, value.rankBucket, value.rankProvenance); err != nil {
			return err
		}
		knownKeys[value.accountID+"\x00"+value.rawID] = struct{}{}
	}
	for _, value := range pending {
		matchKey, encrypted, err := store.encryptRiotMatchID(value.accountID, value.rawID)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO riot_pending_fetches(account_id, match_key, match_id_ciphertext, attempts, next_retry_at_ns, last_error_kind, updated_at_ns) VALUES(?, ?, ?, ?, ?, ?, ?)`,
			value.accountID, matchKey, encrypted, value.attempts, value.nextRetryAt, value.lastError, value.updatedAt); err != nil {
			return err
		}
	}
	for _, value := range links {
		var official any
		state, reason, confidence := value.state, value.reason, value.confidence
		if value.officialRaw.Valid {
			if _, ok := knownKeys[value.accountID+"\x00"+value.officialRaw.String]; ok {
				matchKey, err := store.RiotMatchKey(value.accountID, value.officialRaw.String)
				if err != nil {
					return err
				}
				official = matchKey
			} else {
				state, reason, confidence = string(SessionLinkPending), "migration_missing_official_match", 0
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO session_match_links(local_session_id, account_id, official_match_key, state, reason, confidence, updated_at_ns) VALUES(?, ?, ?, ?, ?, ?, ?)`,
			value.localID, value.accountID, official, state, reason, confidence, value.updatedAt); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO metadata(key, value, updated_at) VALUES(?, ?, datetime('now'))
		ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
		riotStorageKeyMetadata, store.riotStorageKeyVerification()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO metadata(key, value, updated_at) VALUES(?, '1', datetime('now'))
		ON CONFLICT(key) DO UPDATE SET value='1', updated_at=excluded.updated_at`, riotPrivacyMaintenanceMetadata); err != nil {
		return err
	}
	return nil
}

func sqliteColumnExists(ctx context.Context, tx *sql.Tx, table, column string) (bool, error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func semanticHash(value any) string {
	payload, _ := json.Marshal(value)
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func (store *DB) compactSensitiveMigration(ctx context.Context) error {
	if err := store.checkpointSensitiveMigrationWAL(ctx); err != nil {
		return fmt.Errorf("checkpoint schema-v7 privacy migration: %w", err)
	}
	if _, err := store.db.ExecContext(ctx, `VACUUM`); err != nil {
		return fmt.Errorf("compact schema-v7 privacy migration: %w", err)
	}
	// VACUUM can itself leave frames in a WAL database. Settle and truncate
	// those frames before clearing the durable retry marker.
	if err := store.checkpointSensitiveMigrationWAL(ctx); err != nil {
		return fmt.Errorf("checkpoint compacted schema-v7 database: %w", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE metadata SET value = '0', updated_at = datetime('now') WHERE key = ?`, riotPrivacyMaintenanceMetadata); err != nil {
		return fmt.Errorf("settle schema-v7 privacy migration: %w", err)
	}
	if err := store.checkpointSensitiveMigrationWAL(ctx); err != nil {
		// The sensitive pages were already compacted, but retain the marker so
		// startup retries the final maintenance rather than silently assuming
		// the WAL was truncated. This write is intentionally best-effort only
		// after the committed marker clear; report both failures if it cannot be
		// restored.
		_, restoreErr := store.db.ExecContext(context.Background(), `UPDATE metadata SET value = '1', updated_at = datetime('now') WHERE key = ?`, riotPrivacyMaintenanceMetadata)
		if restoreErr != nil {
			return errors.Join(fmt.Errorf("truncate schema-v7 privacy migration WAL: %w", err), fmt.Errorf("restore schema-v7 privacy maintenance marker: %w", restoreErr))
		}
		return fmt.Errorf("truncate schema-v7 privacy migration WAL: %w", err)
	}
	return secureSQLiteArtifacts(store.path)
}

func (store *DB) checkpointSensitiveMigrationWAL(ctx context.Context) error {
	var busy, logFrames, checkpointed int
	if err := store.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointed); err != nil {
		return err
	}
	if busy != 0 {
		return errors.New("database remained busy")
	}
	return nil
}
