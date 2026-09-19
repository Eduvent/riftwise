package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"lolengine/internal/domain"
	"lolengine/internal/riotapi"
)

var (
	ErrRiotAccountNotFound       = errors.New("Riot account not found")
	ErrRiotCheckpointMissing     = errors.New("Riot sync checkpoint not found")
	ErrRiotReconciliationMissing = errors.New("Riot reconciliation state not found")
)

type RiotAccount struct {
	ID        string           `json:"id"`
	PUUID     string           `json:"-"`
	Region    riotapi.Region   `json:"region"`
	Platform  riotapi.Platform `json:"platform"`
	ConsentAt time.Time        `json:"consent_at"`
	LinkedAt  time.Time        `json:"linked_at"`
	UpdatedAt time.Time        `json:"updated_at"`
	AutoSync  bool             `json:"auto_sync"`
}

type RiotSyncStatus string

const (
	RiotSyncIdle      RiotSyncStatus = "idle"
	RiotSyncRunning   RiotSyncStatus = "running"
	RiotSyncComplete  RiotSyncStatus = "complete"
	RiotSyncPartial   RiotSyncStatus = "partial"
	RiotSyncCancelled RiotSyncStatus = "cancelled"
	RiotSyncFailed    RiotSyncStatus = "failed"
)

type RiotSyncCheckpoint struct {
	AccountID         string         `json:"account_id"`
	Region            riotapi.Region `json:"region"`
	FilterKey         string         `json:"filter_key"`
	RunStartedAt      time.Time      `json:"run_started_at"`
	WindowStartUnix   int64          `json:"window_start_unix"`
	WindowEndUnix     int64          `json:"window_end_unix"`
	NextStart         int            `json:"next_start"`
	CoverageStartUnix int64          `json:"coverage_start_unix"`
	CoverageEndUnix   int64          `json:"coverage_end_unix"`
	Status            RiotSyncStatus `json:"status"`
	LastErrorKind     string         `json:"last_error_kind,omitempty"`
	UpdatedAt         time.Time      `json:"updated_at"`
}

type RiotReconciliationStatus string

const (
	RiotReconciliationPending  RiotReconciliationStatus = "pending"
	RiotReconciliationComplete RiotReconciliationStatus = "complete"
)

// RiotReconciliationState separates completed Match-V5 ingestion from the
// retryable local-session linking projection. The source fields preserve the
// completed ingestion identity so a restart can retry without opening a new
// Match-V5 window.
type RiotReconciliationState struct {
	AccountID     string                   `json:"account_id"`
	Status        RiotReconciliationStatus `json:"status"`
	FilterKey     string                   `json:"filter_key"`
	RunStartedAt  time.Time                `json:"run_started_at"`
	WindowEndUnix int64                    `json:"window_end_unix"`
	LastErrorKind string                   `json:"last_error_kind,omitempty"`
	UpdatedAt     time.Time                `json:"updated_at"`
}

type RiotPendingFetch struct {
	AccountID     string    `json:"account_id"`
	MatchID       string    `json:"match_id"`
	Attempts      int       `json:"attempts"`
	NextRetryAt   time.Time `json:"next_retry_at"`
	LastErrorKind string    `json:"last_error_kind"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type SessionLinkState string

const (
	SessionLinkLinked    SessionLinkState = "linked"
	SessionLinkAmbiguous SessionLinkState = "ambiguous"
	SessionLinkPending   SessionLinkState = "pending"
)

type SessionMatchLink struct {
	LocalSessionID string `json:"local_session_id"`
	AccountID      string `json:"account_id"`
	// OfficialMatchID is an installation/account-keyed opaque match key. The
	// historical field name is retained internally for source compatibility.
	OfficialMatchID string           `json:"official_match_key,omitempty"`
	State           SessionLinkState `json:"state"`
	Reason          string           `json:"reason"`
	Confidence      float64          `json:"confidence"`
	UpdatedAt       time.Time        `json:"updated_at"`
}

type RiotStoredMatch struct {
	AccountID      string        `json:"account_id"`
	Match          riotapi.Match `json:"match"`
	RankBucket     string        `json:"rank_bucket"`
	RankProvenance string        `json:"rank_provenance"`
	FetchedAt      time.Time     `json:"fetched_at"`
}

type RiotMatchFilter struct {
	Patch       string
	Champion    string
	Role        domain.Role
	QueueID     int
	MapID       int
	EndedBefore time.Time
	Limit       int
}

func (store *DB) UpsertRiotAccount(ctx context.Context, account RiotAccount) error {
	if strings.TrimSpace(account.ID) == "" || strings.TrimSpace(account.PUUID) == "" || !account.Region.Valid() {
		return errors.New("invalid Riot account")
	}
	if region, ok := account.Platform.Region(); !ok || region != account.Region {
		return errors.New("Riot platform does not belong to region")
	}
	if account.ConsentAt.IsZero() {
		return errors.New("explicit Riot account consent timestamp is required")
	}
	if account.LinkedAt.IsZero() {
		account.LinkedAt = account.ConsentAt
	}
	if account.UpdatedAt.IsZero() {
		account.UpdatedAt = account.LinkedAt
	}
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO riot_accounts(id, puuid, region, platform, consent_at_ns, linked_at_ns, updated_at_ns, auto_sync)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			puuid=excluded.puuid, region=excluded.region, platform=excluded.platform,
			consent_at_ns=excluded.consent_at_ns, updated_at_ns=excluded.updated_at_ns,
			auto_sync=excluded.auto_sync`,
		account.ID, account.PUUID, account.Region, account.Platform, account.ConsentAt.UTC().UnixNano(),
		account.LinkedAt.UTC().UnixNano(), account.UpdatedAt.UTC().UnixNano(), boolInt(account.AutoSync),
	)
	if err != nil {
		return fmt.Errorf("upsert Riot account: %w", err)
	}
	return nil
}

func (store *DB) LoadRiotAccount(ctx context.Context, accountID string) (RiotAccount, error) {
	var account RiotAccount
	var consentAt, linkedAt, updatedAt int64
	var autoSync int
	err := store.db.QueryRowContext(ctx, `
		SELECT id, puuid, region, platform, consent_at_ns, linked_at_ns, updated_at_ns, auto_sync
		FROM riot_accounts WHERE id = ?`, accountID).Scan(
		&account.ID, &account.PUUID, &account.Region, &account.Platform, &consentAt, &linkedAt, &updatedAt, &autoSync,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return RiotAccount{}, ErrRiotAccountNotFound
	}
	if err != nil {
		return RiotAccount{}, fmt.Errorf("load Riot account: %w", err)
	}
	account.ConsentAt = time.Unix(0, consentAt).UTC()
	account.LinkedAt = time.Unix(0, linkedAt).UTC()
	account.UpdatedAt = time.Unix(0, updatedAt).UTC()
	account.AutoSync = autoSync == 1
	return account, nil
}

func (store *DB) ListRiotAccounts(ctx context.Context) ([]RiotAccount, error) {
	rows, err := store.db.QueryContext(ctx, `
		SELECT id, puuid, region, platform, consent_at_ns, linked_at_ns, updated_at_ns, auto_sync
		FROM riot_accounts ORDER BY linked_at_ns ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list Riot accounts: %w", err)
	}
	defer rows.Close()
	var result []RiotAccount
	for rows.Next() {
		var value RiotAccount
		var consentAt, linkedAt, updatedAt int64
		var autoSync int
		if err := rows.Scan(&value.ID, &value.PUUID, &value.Region, &value.Platform, &consentAt, &linkedAt, &updatedAt, &autoSync); err != nil {
			return nil, err
		}
		value.ConsentAt, value.LinkedAt, value.UpdatedAt = time.Unix(0, consentAt).UTC(), time.Unix(0, linkedAt).UTC(), time.Unix(0, updatedAt).UTC()
		value.AutoSync = autoSync == 1
		result = append(result, value)
	}
	return result, rows.Err()
}

func (store *DB) DeleteRiotAccount(ctx context.Context, accountID string) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Riot account deletion: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM riot_accounts WHERE id = ?`, accountID)
	if err != nil {
		return fmt.Errorf("delete Riot account: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrRiotAccountNotFound
	}
	// Official rows contain no identifiers, but deleting orphans makes the
	// privacy guarantee easy to inspect and avoids retaining unwanted facts.
	if _, err := tx.ExecContext(ctx, `DELETE FROM riot_matches WHERE match_key NOT IN (SELECT match_key FROM riot_account_matches)`); err != nil {
		return fmt.Errorf("delete orphaned Riot matches: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Riot account deletion: %w", err)
	}
	// secure_delete is enabled at open; truncate the WAL best-effort after the
	// committed privacy operation. A checkpoint failure must not roll back or
	// misreport the already committed deletion.
	_, _ = store.db.ExecContext(context.Background(), `PRAGMA wal_checkpoint(TRUNCATE)`)
	return nil
}

func (store *DB) SaveRiotMatch(ctx context.Context, accountID string, match riotapi.Match, fetchedAt time.Time) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := store.saveRiotMatchTx(ctx, tx, accountID, match, fetchedAt); err != nil {
		return err
	}
	return tx.Commit()
}

func (store *DB) saveRiotMatchTx(ctx context.Context, tx *sql.Tx, accountID string, match riotapi.Match, fetchedAt time.Time) error {
	if strings.TrimSpace(accountID) == "" || strings.TrimSpace(match.ID) == "" || strings.TrimSpace(match.ContentHash) == "" ||
		match.StartedAt.IsZero() || match.EndedAt.IsZero() || match.EndedAt.Before(match.StartedAt) || match.DurationSeconds <= 0 ||
		strings.TrimSpace(match.Player.ChampionName) == "" || match.Patch == "" {
		return errors.New("invalid normalized Riot match")
	}
	if _, ok := match.Platform.Region(); !ok {
		return errors.New("invalid normalized Riot match platform")
	}
	items := append([]riotapi.FinalItem(nil), match.Player.FinalItems...)
	sort.Slice(items, func(i, j int) bool {
		if items[i].Slot != items[j].Slot {
			return items[i].Slot < items[j].Slot
		}
		return items[i].ItemID < items[j].ItemID
	})
	itemsJSON, err := json.Marshal(items)
	if err != nil {
		return err
	}
	if fetchedAt.IsZero() {
		fetchedAt = time.Now().UTC()
	}
	matchKey, err := store.RiotMatchKey(accountID, match.ID)
	if err != nil {
		return err
	}
	generalPayload, err := json.Marshal(struct {
		Platform        riotapi.Platform `json:"platform"`
		QueueID         int              `json:"queue_id"`
		MapID           int              `json:"map_id"`
		Patch           string           `json:"patch"`
		StartedAtNS     int64            `json:"started_at_ns"`
		EndedAtNS       int64            `json:"ended_at_ns"`
		DurationSeconds int64            `json:"duration_seconds"`
	}{match.Platform, match.QueueID, match.MapID, match.Patch, match.StartedAt.UTC().UnixNano(), match.EndedAt.UTC().UnixNano(), match.DurationSeconds})
	if err != nil {
		return err
	}
	generalHash := semanticHash(json.RawMessage(generalPayload))
	factsPlayer := match.Player
	factsPlayer.FinalItems = items
	factsHash := semanticHash(factsPlayer)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO riot_matches(match_key, platform, queue_id, map_id, patch, started_at_ns, ended_at_ns, duration_seconds, content_hash, fetched_at_ns)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(match_key) DO UPDATE SET
			platform=excluded.platform, queue_id=excluded.queue_id, map_id=excluded.map_id, patch=excluded.patch,
			started_at_ns=excluded.started_at_ns, ended_at_ns=excluded.ended_at_ns,
			duration_seconds=excluded.duration_seconds, content_hash=excluded.content_hash, fetched_at_ns=excluded.fetched_at_ns`,
		matchKey, match.Platform, match.QueueID, match.MapID, match.Patch, match.StartedAt.UTC().UnixNano(),
		match.EndedAt.UTC().UnixNano(), match.DurationSeconds, generalHash, fetchedAt.UTC().UnixNano(),
	); err != nil {
		return fmt.Errorf("upsert normalized Riot match: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO riot_account_matches(account_id, match_key, champion_name, role, role_provenance, team_id, win,
			kills, deaths, assists, creep_score, final_items, facts_hash, opponent_champion, opponent_provenance, rank_bucket, rank_provenance)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'unknown', 'unavailable')
		ON CONFLICT(account_id, match_key) DO UPDATE SET
			champion_name=excluded.champion_name, role=excluded.role, role_provenance=excluded.role_provenance,
			team_id=excluded.team_id, win=excluded.win, kills=excluded.kills, deaths=excluded.deaths,
			assists=excluded.assists, creep_score=excluded.creep_score, final_items=excluded.final_items, facts_hash=excluded.facts_hash,
			opponent_champion=excluded.opponent_champion, opponent_provenance=excluded.opponent_provenance`,
		accountID, matchKey, match.Player.ChampionName, match.Player.Role, match.Player.RoleProvenance,
		match.Player.TeamID, boolInt(match.Player.Win), match.Player.Kills, match.Player.Deaths, match.Player.Assists,
		match.Player.CreepScore, itemsJSON, factsHash, match.Player.OpponentChampion, match.Player.OpponentProvenance,
	); err != nil {
		return fmt.Errorf("upsert linked Riot player facts: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM riot_pending_fetches WHERE account_id = ? AND match_key = ?`, accountID, matchKey); err != nil {
		return fmt.Errorf("clear pending Riot fetch: %w", err)
	}
	return nil
}

func (store *DB) ListRiotMatches(ctx context.Context, accountID string, filter RiotMatchFilter) ([]RiotStoredMatch, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 10000 {
		limit = 10000
	}
	query := `SELECT am.account_id, m.match_key, m.platform, m.queue_id, m.map_id, m.patch,
		m.started_at_ns, m.ended_at_ns, m.duration_seconds, am.facts_hash, m.fetched_at_ns,
		am.champion_name, am.role, am.role_provenance, am.team_id, am.win, am.kills, am.deaths,
		am.assists, am.creep_score, am.final_items, am.opponent_champion, am.opponent_provenance,
		am.rank_bucket, am.rank_provenance
		FROM riot_account_matches am JOIN riot_matches m ON m.match_key = am.match_key WHERE am.account_id = ?`
	args := []any{accountID}
	if value := strings.TrimSpace(filter.Patch); value != "" {
		query += ` AND m.patch = ?`
		args = append(args, value)
	}
	if value := strings.TrimSpace(filter.Champion); value != "" {
		query += ` AND am.champion_name = ?`
		args = append(args, value)
	}
	if filter.Role.Valid() {
		query += ` AND am.role = ?`
		args = append(args, filter.Role)
	}
	if filter.QueueID > 0 {
		query += ` AND m.queue_id = ?`
		args = append(args, filter.QueueID)
	}
	if filter.MapID > 0 {
		query += ` AND m.map_id = ?`
		args = append(args, filter.MapID)
	}
	if !filter.EndedBefore.IsZero() {
		query += ` AND m.ended_at_ns < ?`
		args = append(args, filter.EndedBefore.UTC().UnixNano())
	}
	query += ` ORDER BY m.ended_at_ns DESC, m.match_key DESC LIMIT ?`
	args = append(args, limit)
	rows, err := store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list normalized Riot matches: %w", err)
	}
	defer rows.Close()
	var result []RiotStoredMatch
	for rows.Next() {
		value, err := scanRiotStoredMatch(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func scanRiotStoredMatch(scanner interface{ Scan(...any) error }) (RiotStoredMatch, error) {
	var value RiotStoredMatch
	var startedAt, endedAt, fetchedAt int64
	var win int
	var role domain.Role
	var roleSource riotapi.RoleProvenance
	var opponentSource riotapi.OpponentProvenance
	var items []byte
	err := scanner.Scan(&value.AccountID, &value.Match.ID, &value.Match.Platform, &value.Match.QueueID, &value.Match.MapID,
		&value.Match.Patch, &startedAt, &endedAt, &value.Match.DurationSeconds, &value.Match.ContentHash, &fetchedAt,
		&value.Match.Player.ChampionName, &role, &roleSource, &value.Match.Player.TeamID, &win,
		&value.Match.Player.Kills, &value.Match.Player.Deaths, &value.Match.Player.Assists, &value.Match.Player.CreepScore,
		&items, &value.Match.Player.OpponentChampion, &opponentSource, &value.RankBucket, &value.RankProvenance)
	if err != nil {
		return RiotStoredMatch{}, err
	}
	if err := json.Unmarshal(items, &value.Match.Player.FinalItems); err != nil {
		return RiotStoredMatch{}, fmt.Errorf("decode normalized final items: %w", err)
	}
	value.Match.StartedAt, value.Match.EndedAt, value.FetchedAt = time.Unix(0, startedAt).UTC(), time.Unix(0, endedAt).UTC(), time.Unix(0, fetchedAt).UTC()
	value.Match.Player.Role, value.Match.Player.RoleProvenance = role, roleSource
	value.Match.Player.Win = win == 1
	value.Match.Player.OpponentProvenance = opponentSource
	return value, nil
}

func (store *DB) SaveRiotSyncBatch(ctx context.Context, accountID string, matches []riotapi.Match, pending []RiotPendingFetch, checkpoint RiotSyncCheckpoint) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Riot sync batch: %w", err)
	}
	defer tx.Rollback()
	for _, match := range matches {
		if err := store.saveRiotMatchTx(ctx, tx, accountID, match, checkpoint.UpdatedAt); err != nil {
			return err
		}
	}
	for _, value := range pending {
		value.AccountID = accountID
		if err := store.saveRiotPendingTx(ctx, tx, value); err != nil {
			return err
		}
	}
	checkpoint.AccountID = accountID
	if err := saveRiotCheckpointTx(ctx, tx, checkpoint); err != nil {
		return err
	}
	if checkpoint.Status == RiotSyncComplete {
		state := RiotReconciliationState{
			AccountID: accountID, Status: RiotReconciliationPending, FilterKey: checkpoint.FilterKey,
			RunStartedAt: checkpoint.RunStartedAt, WindowEndUnix: checkpoint.WindowEndUnix, UpdatedAt: checkpoint.UpdatedAt,
		}
		if err := saveRiotReconciliationStateTx(ctx, tx, state); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Riot sync batch: %w", err)
	}
	return nil
}

func saveRiotCheckpointTx(ctx context.Context, tx *sql.Tx, checkpoint RiotSyncCheckpoint) error {
	if strings.TrimSpace(checkpoint.AccountID) == "" || strings.TrimSpace(checkpoint.FilterKey) == "" || checkpoint.RunStartedAt.IsZero() || checkpoint.UpdatedAt.IsZero() ||
		checkpoint.NextStart < 0 || !validRiotSyncStatus(checkpoint.Status) {
		return errors.New("invalid Riot sync checkpoint")
	}
	var accountRegion riotapi.Region
	if err := tx.QueryRowContext(ctx, `SELECT region FROM riot_accounts WHERE id = ?`, checkpoint.AccountID).Scan(&accountRegion); err != nil {
		return fmt.Errorf("load Riot account route for checkpoint: %w", err)
	}
	if checkpoint.Region == "" {
		checkpoint.Region = accountRegion
	}
	if !checkpoint.Region.Valid() || checkpoint.Region != accountRegion {
		return errors.New("Riot checkpoint region does not match linked account")
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO riot_sync_checkpoints(account_id, region, filter_key, run_started_at_ns, window_start_unix, window_end_unix,
			next_start, coverage_start_unix, coverage_end_unix, status, last_error_kind, updated_at_ns)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(account_id, filter_key) DO UPDATE SET
			region=excluded.region, run_started_at_ns=excluded.run_started_at_ns, window_start_unix=excluded.window_start_unix,
			window_end_unix=excluded.window_end_unix, next_start=excluded.next_start,
			coverage_start_unix=excluded.coverage_start_unix, coverage_end_unix=excluded.coverage_end_unix,
			status=excluded.status, last_error_kind=excluded.last_error_kind, updated_at_ns=excluded.updated_at_ns`,
		checkpoint.AccountID, checkpoint.Region, checkpoint.FilterKey, checkpoint.RunStartedAt.UTC().UnixNano(), checkpoint.WindowStartUnix,
		checkpoint.WindowEndUnix, checkpoint.NextStart, checkpoint.CoverageStartUnix, checkpoint.CoverageEndUnix,
		checkpoint.Status, checkpoint.LastErrorKind, checkpoint.UpdatedAt.UTC().UnixNano())
	if err != nil {
		return fmt.Errorf("save Riot sync checkpoint: %w", err)
	}
	return nil
}

func (store *DB) LoadRiotSyncCheckpoint(ctx context.Context, accountID, filterKey string) (RiotSyncCheckpoint, error) {
	var value RiotSyncCheckpoint
	var runStartedAt, updatedAt int64
	err := store.db.QueryRowContext(ctx, `
		SELECT account_id, region, filter_key, run_started_at_ns, window_start_unix, window_end_unix, next_start,
			coverage_start_unix, coverage_end_unix, status, last_error_kind, updated_at_ns
		FROM riot_sync_checkpoints WHERE account_id = ? AND filter_key = ?`, accountID, filterKey).Scan(
		&value.AccountID, &value.Region, &value.FilterKey, &runStartedAt, &value.WindowStartUnix, &value.WindowEndUnix, &value.NextStart,
		&value.CoverageStartUnix, &value.CoverageEndUnix, &value.Status, &value.LastErrorKind, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RiotSyncCheckpoint{}, ErrRiotCheckpointMissing
	}
	if err != nil {
		return RiotSyncCheckpoint{}, err
	}
	value.RunStartedAt, value.UpdatedAt = time.Unix(0, runStartedAt).UTC(), time.Unix(0, updatedAt).UTC()
	return value, nil
}

func (store *DB) ListRiotSyncCheckpoints(ctx context.Context) ([]RiotSyncCheckpoint, error) {
	rows, err := store.db.QueryContext(ctx, `
		SELECT account_id, region, filter_key, run_started_at_ns, window_start_unix, window_end_unix, next_start,
			coverage_start_unix, coverage_end_unix, status, last_error_kind, updated_at_ns
		FROM riot_sync_checkpoints ORDER BY updated_at_ns DESC, account_id ASC, filter_key ASC LIMIT 100`)
	if err != nil {
		return nil, fmt.Errorf("list Riot sync checkpoints: %w", err)
	}
	defer rows.Close()
	var result []RiotSyncCheckpoint
	for rows.Next() {
		var value RiotSyncCheckpoint
		var runStartedAt, updatedAt int64
		if err := rows.Scan(&value.AccountID, &value.Region, &value.FilterKey, &runStartedAt, &value.WindowStartUnix, &value.WindowEndUnix,
			&value.NextStart, &value.CoverageStartUnix, &value.CoverageEndUnix, &value.Status, &value.LastErrorKind, &updatedAt); err != nil {
			return nil, err
		}
		value.RunStartedAt, value.UpdatedAt = time.Unix(0, runStartedAt).UTC(), time.Unix(0, updatedAt).UTC()
		result = append(result, value)
	}
	return result, rows.Err()
}

func (store *DB) SaveRiotReconciliationState(ctx context.Context, state RiotReconciliationState) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Riot reconciliation state update: %w", err)
	}
	defer tx.Rollback()
	if err := saveRiotReconciliationStateTx(ctx, tx, state); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Riot reconciliation state update: %w", err)
	}
	return nil
}

func saveRiotReconciliationStateTx(ctx context.Context, tx *sql.Tx, state RiotReconciliationState) error {
	if strings.TrimSpace(state.AccountID) == "" || strings.TrimSpace(state.FilterKey) == "" || state.RunStartedAt.IsZero() || state.WindowEndUnix <= 0 || state.UpdatedAt.IsZero() ||
		(state.Status != RiotReconciliationPending && state.Status != RiotReconciliationComplete) {
		return errors.New("invalid Riot reconciliation state")
	}
	if state.Status == RiotReconciliationComplete {
		state.LastErrorKind = ""
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO riot_reconciliation_states(account_id, status, filter_key, run_started_at_ns, window_end_unix, last_error_kind, updated_at_ns)
		VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(account_id) DO UPDATE SET
			status=excluded.status, filter_key=excluded.filter_key, run_started_at_ns=excluded.run_started_at_ns,
			window_end_unix=excluded.window_end_unix, last_error_kind=excluded.last_error_kind, updated_at_ns=excluded.updated_at_ns`,
		state.AccountID, state.Status, state.FilterKey, state.RunStartedAt.UTC().UnixNano(), state.WindowEndUnix,
		state.LastErrorKind, state.UpdatedAt.UTC().UnixNano())
	if err != nil {
		return fmt.Errorf("save Riot reconciliation state: %w", err)
	}
	return nil
}

func (store *DB) LoadRiotReconciliationState(ctx context.Context, accountID string) (RiotReconciliationState, error) {
	var state RiotReconciliationState
	var runStartedAt, updatedAt int64
	err := store.db.QueryRowContext(ctx, `
		SELECT account_id, status, filter_key, run_started_at_ns, window_end_unix, last_error_kind, updated_at_ns
		FROM riot_reconciliation_states WHERE account_id = ?`, accountID).Scan(
		&state.AccountID, &state.Status, &state.FilterKey, &runStartedAt, &state.WindowEndUnix, &state.LastErrorKind, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RiotReconciliationState{}, ErrRiotReconciliationMissing
	}
	if err != nil {
		return RiotReconciliationState{}, fmt.Errorf("load Riot reconciliation state: %w", err)
	}
	state.RunStartedAt = time.Unix(0, runStartedAt).UTC()
	state.UpdatedAt = time.Unix(0, updatedAt).UTC()
	return state, nil
}

func (store *DB) ListRiotReconciliationStates(ctx context.Context) ([]RiotReconciliationState, error) {
	rows, err := store.db.QueryContext(ctx, `
		SELECT account_id, status, filter_key, run_started_at_ns, window_end_unix, last_error_kind, updated_at_ns
		FROM riot_reconciliation_states ORDER BY updated_at_ns DESC, account_id ASC LIMIT 100`)
	if err != nil {
		return nil, fmt.Errorf("list Riot reconciliation states: %w", err)
	}
	defer rows.Close()
	var result []RiotReconciliationState
	for rows.Next() {
		var state RiotReconciliationState
		var runStartedAt, updatedAt int64
		if err := rows.Scan(&state.AccountID, &state.Status, &state.FilterKey, &runStartedAt, &state.WindowEndUnix, &state.LastErrorKind, &updatedAt); err != nil {
			return nil, err
		}
		state.RunStartedAt = time.Unix(0, runStartedAt).UTC()
		state.UpdatedAt = time.Unix(0, updatedAt).UTC()
		result = append(result, state)
	}
	return result, rows.Err()
}

func (store *DB) saveRiotPendingTx(ctx context.Context, tx *sql.Tx, value RiotPendingFetch) error {
	if strings.TrimSpace(value.AccountID) == "" || strings.TrimSpace(value.MatchID) == "" || value.Attempts < 1 || value.NextRetryAt.IsZero() || value.UpdatedAt.IsZero() || strings.TrimSpace(value.LastErrorKind) == "" {
		return errors.New("invalid pending Riot fetch")
	}
	matchKey, encryptedID, err := store.encryptRiotMatchID(value.AccountID, value.MatchID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO riot_pending_fetches(account_id, match_key, match_id_ciphertext, attempts, next_retry_at_ns, last_error_kind, updated_at_ns)
		VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(account_id, match_key) DO UPDATE SET
			match_id_ciphertext=excluded.match_id_ciphertext, attempts=excluded.attempts, next_retry_at_ns=excluded.next_retry_at_ns,
			last_error_kind=excluded.last_error_kind, updated_at_ns=excluded.updated_at_ns`,
		value.AccountID, matchKey, encryptedID, value.Attempts, value.NextRetryAt.UTC().UnixNano(), value.LastErrorKind, value.UpdatedAt.UTC().UnixNano())
	if err != nil {
		return fmt.Errorf("save pending Riot fetch: %w", err)
	}
	return nil
}

func (store *DB) ListDueRiotPendingFetches(ctx context.Context, accountID string, now time.Time, limit int) ([]RiotPendingFetch, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := store.db.QueryContext(ctx, `
		SELECT account_id, match_key, match_id_ciphertext, attempts, next_retry_at_ns, last_error_kind, updated_at_ns
		FROM riot_pending_fetches WHERE account_id = ? AND next_retry_at_ns <= ?
		ORDER BY next_retry_at_ns ASC, match_key ASC LIMIT ?`, accountID, now.UTC().UnixNano(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []RiotPendingFetch
	for rows.Next() {
		var value RiotPendingFetch
		var matchKey string
		var encryptedID []byte
		var retryAt, updatedAt int64
		if err := rows.Scan(&value.AccountID, &matchKey, &encryptedID, &value.Attempts, &retryAt, &value.LastErrorKind, &updatedAt); err != nil {
			return nil, err
		}
		value.MatchID, err = store.decryptRiotMatchID(value.AccountID, matchKey, encryptedID)
		if err != nil {
			return nil, fmt.Errorf("decrypt pending Riot fetch: %w", err)
		}
		value.NextRetryAt, value.UpdatedAt = time.Unix(0, retryAt).UTC(), time.Unix(0, updatedAt).UTC()
		result = append(result, value)
	}
	return result, rows.Err()
}

func (store *DB) ListRiotPendingFetches(ctx context.Context, accountID string, limit int) ([]RiotPendingFetch, error) {
	if limit <= 0 || limit > 10000 {
		limit = 10000
	}
	rows, err := store.db.QueryContext(ctx, `
		SELECT account_id, match_key, match_id_ciphertext, attempts, next_retry_at_ns, last_error_kind, updated_at_ns
		FROM riot_pending_fetches WHERE account_id = ?
		ORDER BY next_retry_at_ns ASC, match_key ASC LIMIT ?`, accountID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []RiotPendingFetch
	for rows.Next() {
		var value RiotPendingFetch
		var matchKey string
		var encryptedID []byte
		var retryAt, updatedAt int64
		if err := rows.Scan(&value.AccountID, &matchKey, &encryptedID, &value.Attempts, &retryAt, &value.LastErrorKind, &updatedAt); err != nil {
			return nil, err
		}
		value.MatchID, err = store.decryptRiotMatchID(value.AccountID, matchKey, encryptedID)
		if err != nil {
			return nil, fmt.Errorf("decrypt pending Riot fetch: %w", err)
		}
		value.NextRetryAt, value.UpdatedAt = time.Unix(0, retryAt).UTC(), time.Unix(0, updatedAt).UTC()
		result = append(result, value)
	}
	return result, rows.Err()
}

func (store *DB) SaveSessionMatchLink(ctx context.Context, link SessionMatchLink) error {
	if strings.TrimSpace(link.LocalSessionID) == "" || strings.TrimSpace(link.AccountID) == "" || strings.TrimSpace(link.Reason) == "" || link.UpdatedAt.IsZero() ||
		link.Confidence < 0 || link.Confidence > 1 || (link.State != SessionLinkLinked && link.State != SessionLinkAmbiguous && link.State != SessionLinkPending) {
		return errors.New("invalid session match link")
	}
	if link.State == SessionLinkLinked && strings.TrimSpace(link.OfficialMatchID) == "" {
		return errors.New("linked session requires an official match")
	}
	var official any
	if link.OfficialMatchID != "" {
		officialKey := link.OfficialMatchID
		if !isOpaqueRiotMatchKey(officialKey) {
			var err error
			officialKey, err = store.RiotMatchKey(link.AccountID, officialKey)
			if err != nil {
				return err
			}
		}
		official = officialKey
	}
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO session_match_links(local_session_id, account_id, official_match_key, state, reason, confidence, updated_at_ns)
		VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(local_session_id, account_id) DO UPDATE SET
			official_match_key=excluded.official_match_key, state=excluded.state, reason=excluded.reason,
			confidence=excluded.confidence, updated_at_ns=excluded.updated_at_ns`,
		link.LocalSessionID, link.AccountID, official, link.State, link.Reason, link.Confidence, link.UpdatedAt.UTC().UnixNano())
	if err != nil {
		return fmt.Errorf("save session match link: %w", err)
	}
	return nil
}

func (store *DB) LoadSessionMatchLink(ctx context.Context, localSessionID, accountID string) (SessionMatchLink, error) {
	var link SessionMatchLink
	var official sql.NullString
	var updatedAt int64
	err := store.db.QueryRowContext(ctx, `
		SELECT local_session_id, account_id, official_match_key, state, reason, confidence, updated_at_ns
		FROM session_match_links WHERE local_session_id = ? AND account_id = ?`, localSessionID, accountID).Scan(
		&link.LocalSessionID, &link.AccountID, &official, &link.State, &link.Reason, &link.Confidence, &updatedAt)
	if err != nil {
		return SessionMatchLink{}, err
	}
	if official.Valid {
		link.OfficialMatchID = official.String
	}
	link.UpdatedAt = time.Unix(0, updatedAt).UTC()
	return link, nil
}

func isOpaqueRiotMatchKey(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func validRiotSyncStatus(value RiotSyncStatus) bool {
	switch value {
	case RiotSyncIdle, RiotSyncRunning, RiotSyncComplete, RiotSyncPartial, RiotSyncCancelled, RiotSyncFailed:
		return true
	default:
		return false
	}
}
