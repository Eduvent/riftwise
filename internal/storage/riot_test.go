package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"lolengine/internal/domain"
	"lolengine/internal/riotapi"
)

func scanDatabaseFiles(path string) (string, error) {
	var result strings.Builder
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		payload, err := os.ReadFile(candidate)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		result.Write(payload)
	}
	return result.String(), nil
}

func TestRiotSchemaV8PersistsOpaqueNormalizedFactsAndDeletesAccountData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "riot.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	account := RiotAccount{
		ID: "account-1", PUUID: "self-private-puuid", Region: riotapi.RegionAmericas,
		Platform: riotapi.PlatformNA1, ConsentAt: now, LinkedAt: now, UpdatedAt: now,
	}
	if err := store.UpsertRiotAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	match := testRiotMatch(now)
	if err := store.SaveRiotMatch(ctx, account.ID, match, now); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRiotMatch(ctx, account.ID, match, now); err != nil {
		t.Fatalf("exact retry: %v", err)
	}
	rows, err := store.ListRiotMatches(ctx, account.ID, RiotMatchFilter{Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].Match.ID == match.ID || len(rows[0].Match.ID) != 64 || rows[0].Match.Player.ChampionName != "Garen" || rows[0].Match.Player.OpponentChampion != "Darius" {
		t.Fatalf("matches = %#v, %v", rows, err)
	}
	opaqueID := rows[0].Match.ID
	contents, err := scanDatabaseFiles(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(contents, match.ID) {
		t.Fatalf("raw successful Match-V5 ID was persisted: %q", match.ID)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if version, err := reopened.SchemaVersion(ctx); err != nil || version != schemaVersion {
		t.Fatalf("version = %d, %v", version, err)
	}
	reopenedRows, err := reopened.ListRiotMatches(ctx, account.ID, RiotMatchFilter{Limit: 10})
	if err != nil || len(reopenedRows) != 1 || reopenedRows[0].Match.ID != opaqueID {
		t.Fatalf("opaque key changed after reopen: %#v, %v", reopenedRows, err)
	}
	if err := reopened.DeleteRiotAccount(ctx, account.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.LoadRiotAccount(ctx, account.ID); !errors.Is(err, ErrRiotAccountNotFound) {
		t.Fatalf("account remains: %v", err)
	}
	contents, err = scanDatabaseFiles(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"self-private-puuid", "Darius", "Garen"} {
		if strings.Contains(contents, forbidden) {
			t.Fatalf("deleted Riot account data remains in SQLite files: %q", forbidden)
		}
	}
}

func TestSharedOfficialMatchKeepsAccountScopedPlayerFacts(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "shared-match.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 19, 12, 30, 0, 0, time.UTC)
	for _, account := range []RiotAccount{
		{ID: "one", PUUID: "puuid-one", Region: riotapi.RegionAmericas, Platform: riotapi.PlatformNA1, ConsentAt: now, LinkedAt: now, UpdatedAt: now},
		{ID: "two", PUUID: "puuid-two", Region: riotapi.RegionAmericas, Platform: riotapi.PlatformNA1, ConsentAt: now, LinkedAt: now, UpdatedAt: now},
	} {
		if err := store.UpsertRiotAccount(ctx, account); err != nil {
			t.Fatal(err)
		}
	}
	first := testRiotMatch(now)
	second := first
	second.ContentHash = strings.Repeat("b", 64)
	second.Player.ChampionName = "Lux"
	second.Player.OpponentChampion = "Ahri"
	if err := store.SaveRiotMatch(ctx, "one", first, now); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRiotMatch(ctx, "two", second, now); err != nil {
		t.Fatal(err)
	}
	rowsOne, errOne := store.ListRiotMatches(ctx, "one", RiotMatchFilter{Limit: 10})
	rowsTwo, errTwo := store.ListRiotMatches(ctx, "two", RiotMatchFilter{Limit: 10})
	if errOne != nil || errTwo != nil || len(rowsOne) != 1 || len(rowsTwo) != 1 ||
		rowsOne[0].Match.Player.ChampionName != "Garen" || rowsTwo[0].Match.Player.ChampionName != "Lux" ||
		rowsOne[0].Match.ID == first.ID || rowsTwo[0].Match.ID == second.ID || rowsOne[0].Match.ID == rowsTwo[0].Match.ID {
		t.Fatalf("account-scoped facts: one=%#v/%v two=%#v/%v", rowsOne, errOne, rowsTwo, errTwo)
	}
}

func TestOpaqueMatchKeysAreInstallationAndAccountScoped(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 19, 12, 45, 0, 0, time.UTC)
	var installationKeys []string
	for installation := 0; installation < 2; installation++ {
		store, err := Open(ctx, filepath.Join(t.TempDir(), "install.db"))
		if err != nil {
			t.Fatal(err)
		}
		for _, accountID := range []string{"one", "two"} {
			account := RiotAccount{ID: accountID, PUUID: "puuid-" + accountID, Region: riotapi.RegionAmericas, Platform: riotapi.PlatformNA1, ConsentAt: now, LinkedAt: now, UpdatedAt: now}
			if err := store.UpsertRiotAccount(ctx, account); err != nil {
				t.Fatal(err)
			}
			key, err := store.RiotMatchKey(accountID, "NA1_42")
			if err != nil {
				t.Fatal(err)
			}
			installationKeys = append(installationKeys, key)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
	seen := make(map[string]struct{}, len(installationKeys))
	for _, key := range installationKeys {
		seen[key] = struct{}{}
	}
	if len(seen) != len(installationKeys) {
		t.Fatalf("opaque keys were not scoped by installation/account: %v", installationKeys)
	}
}

func TestRiotCheckpointPendingAndSessionLinkCascade(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 9, 19, 13, 0, 0, 0, time.UTC)
	account := RiotAccount{ID: "account-1", PUUID: "self", Region: riotapi.RegionAmericas, Platform: riotapi.PlatformNA1, ConsentAt: now, LinkedAt: now, UpdatedAt: now}
	if err := store.UpsertRiotAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	localEnd := now.Add(30 * time.Minute)
	session := domain.MatchSession{ID: "local-1", Fingerprint: "private", State: domain.MatchEnded, StartedAt: now, UpdatedAt: localEnd, LastSeenAt: localEnd, EndedAt: &localEnd, ChampionName: "Garen", MapNumber: 11}
	if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{session}, nil); err != nil {
		t.Fatal(err)
	}
	match := testRiotMatch(now)
	checkpoint := RiotSyncCheckpoint{AccountID: account.ID, FilterKey: "default", RunStartedAt: now, WindowStartUnix: now.Add(-24 * time.Hour).Unix(), WindowEndUnix: now.Unix(), NextStart: 20, CoverageEndUnix: now.Unix(), Status: RiotSyncComplete, UpdatedAt: now}
	pending := RiotPendingFetch{AccountID: account.ID, MatchID: "NA1_999", Attempts: 1, NextRetryAt: now.Add(time.Hour), LastErrorKind: string(riotapi.ErrorNotFound), UpdatedAt: now}
	link := SessionMatchLink{LocalSessionID: session.ID, AccountID: account.ID, OfficialMatchID: match.ID, State: SessionLinkLinked, Reason: "unique_interval_champion", Confidence: .95, UpdatedAt: now}
	if err := store.SaveRiotSyncBatch(ctx, account.ID, []riotapi.Match{match}, []RiotPendingFetch{pending}, checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSessionMatchLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadRiotSyncCheckpoint(ctx, account.ID, "default")
	if err != nil || loaded.NextStart != 20 {
		t.Fatalf("checkpoint = %#v, %v", loaded, err)
	}
	due, err := store.ListDueRiotPendingFetches(ctx, account.ID, now.Add(2*time.Hour), 10)
	if err != nil || len(due) != 1 || due[0].MatchID != pending.MatchID {
		t.Fatalf("pending = %#v, %v", due, err)
	}
	if err := store.DeleteRiotAccount(ctx, account.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadMatchSummarySource(ctx, session.ID); err != nil {
		t.Fatalf("local history was deleted with Riot account: %v", err)
	}
	if _, err := store.LoadRiotReconciliationState(ctx, account.ID); !errors.Is(err, ErrRiotReconciliationMissing) {
		t.Fatalf("reconciliation state survived account privacy deletion: %v", err)
	}
}

func TestCompletedIngestionPersistsReconciliationPendingAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "reconciliation.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 19, 13, 30, 0, 0, time.UTC)
	account := RiotAccount{ID: "account-1", PUUID: "self", Region: riotapi.RegionAmericas, Platform: riotapi.PlatformNA1, ConsentAt: now, LinkedAt: now, UpdatedAt: now}
	if err := store.UpsertRiotAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	checkpoint := RiotSyncCheckpoint{
		AccountID: account.ID, Region: account.Region, FilterKey: "filter-a", RunStartedAt: now.Add(-time.Minute),
		WindowStartUnix: now.Add(-24 * time.Hour).Unix(), WindowEndUnix: now.Unix(), Status: RiotSyncComplete, UpdatedAt: now,
	}
	if err := store.SaveRiotSyncBatch(ctx, account.ID, nil, nil, checkpoint); err != nil {
		t.Fatal(err)
	}
	state, err := store.LoadRiotReconciliationState(ctx, account.ID)
	if err != nil || state.Status != RiotReconciliationPending || state.FilterKey != checkpoint.FilterKey || state.RunStartedAt != checkpoint.RunStartedAt || state.WindowEndUnix != checkpoint.WindowEndUnix {
		t.Fatalf("pending reconciliation = %#v, %v", state, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state, err = store.LoadRiotReconciliationState(ctx, account.ID)
	if err != nil || state.Status != RiotReconciliationPending {
		t.Fatalf("reopened reconciliation = %#v, %v", state, err)
	}
	state.Status = RiotReconciliationComplete
	state.LastErrorKind = ""
	state.UpdatedAt = now.Add(time.Minute)
	if err := store.SaveRiotReconciliationState(ctx, state); err != nil {
		t.Fatal(err)
	}
	states, err := store.ListRiotReconciliationStates(ctx)
	if err != nil || len(states) != 1 || states[0].Status != RiotReconciliationComplete {
		t.Fatalf("reconciliation states = %#v, %v", states, err)
	}
}

func TestMigrationFromSchemaV7QueuesCompletedIngestionForReconciliation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v7-reconciliation.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 19, 13, 45, 0, 0, time.UTC)
	account := RiotAccount{ID: "account-v7", PUUID: "self-v7", Region: riotapi.RegionAmericas, Platform: riotapi.PlatformNA1, ConsentAt: now, LinkedAt: now, UpdatedAt: now}
	if err := store.UpsertRiotAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	checkpoint := RiotSyncCheckpoint{AccountID: account.ID, Region: account.Region, FilterKey: "legacy-complete", RunStartedAt: now.Add(-time.Hour), WindowStartUnix: now.Add(-24 * time.Hour).Unix(), WindowEndUnix: now.Unix(), Status: RiotSyncComplete, UpdatedAt: now}
	if err := store.SaveRiotSyncBatch(ctx, account.ID, nil, nil, checkpoint); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TABLE riot_reconciliation_states`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TABLE recommendation_feedback`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TABLE personalization_preferences`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version >= 8`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state, err := store.LoadRiotReconciliationState(ctx, account.ID)
	if err != nil || state.Status != RiotReconciliationPending || state.FilterKey != checkpoint.FilterKey || state.LastErrorKind != "migration_pending" {
		t.Fatalf("migrated reconciliation = %#v, %v", state, err)
	}
}

func TestMigrationFromSchemaV5AddsRiotTablesWithoutChangingLocalHistory(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v5.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 19, 14, 0, 0, 0, time.UTC)
	session := domain.MatchSession{ID: "local-history", Fingerprint: "private", State: domain.MatchActive, StartedAt: now, UpdatedAt: now, LastSeenAt: now}
	if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{session}, nil); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DROP TABLE recommendation_feedback`, `DROP TABLE personalization_preferences`,
		`DROP TABLE riot_reconciliation_states`, `DROP TABLE session_match_links`, `DROP TABLE riot_pending_fetches`, `DROP TABLE riot_sync_checkpoints`,
		`DROP TABLE riot_account_matches`, `DROP TABLE riot_matches`, `DROP TABLE riot_accounts`,
		`DELETE FROM schema_migrations WHERE version >= 6`,
	} {
		if _, err := store.db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("prepare v5: %v", err)
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
	if version, err := reopened.SchemaVersion(ctx); err != nil || version != schemaVersion {
		t.Fatalf("version = %d, %v", version, err)
	}
	active, err := reopened.LoadActiveMatch(ctx)
	if err != nil || active == nil || active.ID != session.ID {
		t.Fatalf("local history changed by v6 migration: %#v, %v", active, err)
	}
	if accounts, err := reopened.ListRiotAccounts(ctx); err != nil || len(accounts) != 0 {
		t.Fatalf("migration eagerly created account data: %#v, %v", accounts, err)
	}
}

func TestMigrationFromSchemaV6RemovesRawMatchIDsFromSQLiteArtifacts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v6-privacy.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 19, 14, 30, 0, 0, time.UTC)
	ended := now.Add(30 * time.Minute)
	if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{{ID: "local-v6", Fingerprint: "local", State: domain.MatchEnded, StartedAt: now, UpdatedAt: ended, LastSeenAt: ended, EndedAt: &ended, ChampionName: "Garen", MapNumber: 11, QueueID: 420}}, nil); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DROP TABLE recommendation_feedback`, `DROP TABLE personalization_preferences`,
		`DROP TABLE riot_reconciliation_states`, `DROP TABLE session_match_links`, `DROP TABLE riot_pending_fetches`, `DROP TABLE riot_account_matches`, `DROP TABLE riot_matches`,
		`CREATE TABLE riot_matches (match_id TEXT PRIMARY KEY, platform TEXT NOT NULL, queue_id INTEGER NOT NULL, map_id INTEGER NOT NULL, patch TEXT NOT NULL, started_at_ns INTEGER NOT NULL, ended_at_ns INTEGER NOT NULL, duration_seconds INTEGER NOT NULL, content_hash TEXT NOT NULL UNIQUE, fetched_at_ns INTEGER NOT NULL)`,
		`CREATE TABLE riot_account_matches (account_id TEXT NOT NULL REFERENCES riot_accounts(id) ON DELETE CASCADE, match_id TEXT NOT NULL REFERENCES riot_matches(match_id) ON DELETE CASCADE, champion_name TEXT NOT NULL, role TEXT NOT NULL DEFAULT '', role_provenance TEXT NOT NULL, team_id INTEGER NOT NULL, win INTEGER NOT NULL, kills INTEGER NOT NULL, deaths INTEGER NOT NULL, assists INTEGER NOT NULL, creep_score INTEGER NOT NULL, final_items BLOB NOT NULL, facts_hash TEXT NOT NULL, opponent_champion TEXT NOT NULL DEFAULT '', opponent_provenance TEXT NOT NULL DEFAULT 'unknown', rank_bucket TEXT NOT NULL DEFAULT 'unknown', rank_provenance TEXT NOT NULL DEFAULT 'unavailable', PRIMARY KEY(account_id, match_id), UNIQUE(account_id, facts_hash))`,
		`CREATE TABLE riot_pending_fetches (account_id TEXT NOT NULL REFERENCES riot_accounts(id) ON DELETE CASCADE, match_id TEXT NOT NULL, attempts INTEGER NOT NULL, next_retry_at_ns INTEGER NOT NULL, last_error_kind TEXT NOT NULL, updated_at_ns INTEGER NOT NULL, PRIMARY KEY(account_id, match_id))`,
		`CREATE TABLE session_match_links (local_session_id TEXT NOT NULL REFERENCES match_sessions(id) ON DELETE CASCADE, account_id TEXT NOT NULL REFERENCES riot_accounts(id) ON DELETE CASCADE, official_match_id TEXT REFERENCES riot_matches(match_id) ON DELETE CASCADE, state TEXT NOT NULL, reason TEXT NOT NULL, confidence REAL NOT NULL, updated_at_ns INTEGER NOT NULL, PRIMARY KEY(local_session_id, account_id))`,
		`DELETE FROM schema_migrations WHERE version >= 7`,
	} {
		if _, err := store.db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("prepare v6 schema with %q: %v", statement, err)
		}
	}
	account := RiotAccount{ID: "account-v6", PUUID: "self-v6", Region: riotapi.RegionAmericas, Platform: riotapi.PlatformNA1, ConsentAt: now, LinkedAt: now, UpdatedAt: now}
	if err := store.UpsertRiotAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO riot_accounts(id, puuid, region, platform, consent_at_ns, linked_at_ns, updated_at_ns, auto_sync) VALUES('legacy-oc1', 'legacy-oc1-puuid', 'americas', 'OC1', ?, ?, ?, 0)`, now.UnixNano(), now.UnixNano(), now.UnixNano()); err != nil {
		t.Fatal(err)
	}
	const successfulRawID = "NA1_987654321"
	const pendingRawID = "NA1_123456789"
	if _, err := store.db.ExecContext(ctx, `INSERT INTO riot_matches VALUES(?, 'NA1', 420, 11, '16.18', ?, ?, 1800, ?, ?)`, successfulRawID, now.UnixNano(), ended.UnixNano(), strings.Repeat("a", 64), now.UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO riot_account_matches VALUES(?, ?, 'Garen', 'top', 'team_position', 100, 1, 8, 2, 5, 220, '[]', ?, 'Darius', 'unique_same_role', 'unknown', 'unavailable')`, account.ID, successfulRawID, strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO riot_pending_fetches VALUES(?, ?, 1, ?, 'not_found', ?)`, account.ID, pendingRawID, now.Add(time.Hour).UnixNano(), now.UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO session_match_links VALUES('local-v6', ?, ?, 'linked', 'v6', 1, ?)`, account.ID, successfulRawID, now.UnixNano()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := scanDatabaseFiles(path)
	if err != nil || !strings.Contains(before, successfulRawID) || !strings.Contains(before, pendingRawID) {
		t.Fatalf("v6 fixture did not contain raw IDs: %v", err)
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := reopened.ListRiotMatches(ctx, account.ID, RiotMatchFilter{Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].Match.ID == successfulRawID || len(rows[0].Match.ID) != 64 {
		t.Fatalf("migrated matches = %#v, %v", rows, err)
	}
	legacyOC1, err := reopened.LoadRiotAccount(ctx, "legacy-oc1")
	if err != nil || legacyOC1.Region != riotapi.RegionSEA {
		t.Fatalf("legacy OC1 route = %#v, %v", legacyOC1, err)
	}
	pending, err := reopened.ListRiotPendingFetches(ctx, account.ID, 10)
	if err != nil || len(pending) != 1 || pending[0].MatchID != pendingRawID {
		t.Fatalf("encrypted pending retry = %#v, %v", pending, err)
	}
	link, err := reopened.LoadSessionMatchLink(ctx, "local-v6", account.ID)
	if err != nil || link.OfficialMatchID == successfulRawID || len(link.OfficialMatchID) != 64 {
		t.Fatalf("migrated link = %#v, %v", link, err)
	}
	if state, err := reopened.Metadata(ctx, riotPrivacyMaintenanceMetadata); err != nil || state != "0" {
		t.Fatalf("privacy maintenance state = %q, %v", state, err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := scanDatabaseFiles(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{successfulRawID, pendingRawID} {
		if strings.Contains(after, forbidden) {
			t.Fatalf("raw Match-V5 ID remains after migration maintenance: %q", forbidden)
		}
	}
}

func TestPrivateStorageModesIgnorePermissiveUmaskAndDoNotChmodSharedParent(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	shared := filepath.Join(root, "shared")
	if err := os.Mkdir(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(shared, "private", "engine.db")
	oldUmask := syscall.Umask(0)
	store, err := Open(ctx, path)
	syscall.Umask(oldUmask)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for candidate, want := range map[string]os.FileMode{
		shared:             0o755,
		filepath.Dir(path): 0o700,
		path:               0o600,
		path + "-wal":      0o600,
		path + "-shm":      0o600,
		path + ".riot-key": 0o600,
	} {
		info, err := os.Stat(candidate)
		if err != nil {
			t.Fatalf("stat %s: %v", candidate, err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("mode %s = %04o, want %04o", candidate, got, want)
		}
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path+".riot-key", 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, candidate := range []string{path, path + ".riot-key"} {
		info, statErr := os.Stat(candidate)
		if statErr != nil {
			t.Fatalf("stat tightened artifact %s: %v", candidate, statErr)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("existing artifact not tightened: %s mode=%v", candidate, info.Mode().Perm())
		}
	}
}

func TestRiotStorageSidecarMismatchFailsClosed(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sidecar.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path + ".riot-key")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path + ".riot-key"); err != nil {
		t.Fatal(err)
	}
	if replacement, err := Open(ctx, path); err == nil {
		replacement.Close()
		t.Fatal("database opened with a replacement Riot storage sidecar")
	}
	if err := os.WriteFile(path+".riot-key", original, 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("database did not recover with original sidecar: %v", err)
	}
	defer reopened.Close()
}

func testRiotMatch(start time.Time) riotapi.Match {
	return riotapi.Match{
		ID: "NA1_123", Platform: riotapi.PlatformNA1, QueueID: 420, MapID: 11, Patch: "16.18",
		StartedAt: start, EndedAt: start.Add(30 * time.Minute), DurationSeconds: 1800, ContentHash: strings.Repeat("a", 64),
		Player: riotapi.LinkedPlayerFacts{
			ChampionName: "Garen", Role: domain.RoleTop, RoleProvenance: riotapi.RoleFromTeamPosition,
			TeamID: 100, Win: true, Kills: 8, Deaths: 2, Assists: 5, CreepScore: 220,
			FinalItems:       []riotapi.FinalItem{{Slot: 0, ItemID: 3078}, {Slot: 6, ItemID: 3340}},
			OpponentChampion: "Darius", OpponentProvenance: riotapi.OpponentUniqueSameRole,
		},
	}
}
