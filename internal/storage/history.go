package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"lolengine/internal/catalog"
	"lolengine/internal/domain"
)

var (
	ErrSummarySourceChanged = errors.New("match summary source changed")
	ErrSummaryCorrupt       = errors.New("materialized match summary is corrupt")
	ErrActiveMatchHistory   = errors.New("active match history cannot be deleted")
	ErrHistoryDeleteChanged = errors.New("completed history changed before deletion")
)

// LoadMatchSummarySource reads every authoritative input from one SQLite snapshot.
func (store *DB) LoadMatchSummarySource(ctx context.Context, matchID string) (domain.MatchSummarySource, error) {
	matchID = strings.TrimSpace(matchID)
	if matchID == "" {
		return domain.MatchSummarySource{}, errors.New("match id is required")
	}
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.MatchSummarySource{}, fmt.Errorf("begin match summary source read: %w", err)
	}
	defer tx.Rollback()

	session, err := scanMatchSession(tx.QueryRowContext(ctx, matchSessionSelect+" WHERE id = ?", matchID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.MatchSummarySource{}, catalog.ErrNotFound
	}
	if err != nil {
		return domain.MatchSummarySource{}, fmt.Errorf("load match summary session: %w", err)
	}

	checkpoint, err := loadPlayerCheckpoint(ctx, tx, matchID)
	if err != nil {
		return domain.MatchSummarySource{}, err
	}
	lifecycle, err := loadAllMatchEvents(ctx, tx, matchID)
	if err != nil {
		return domain.MatchSummarySource{}, err
	}
	decisions, err := loadAllDecisionEvents(ctx, tx, matchID)
	if err != nil {
		return domain.MatchSummarySource{}, err
	}
	decisionEvidence, checkpointUpdatedAt, err := loadDecisionSummaryCheckpoint(ctx, tx, matchID)
	if err != nil {
		return domain.MatchSummarySource{}, err
	}
	revision, err := currentSummarySourceRevision(ctx, tx, matchID)
	if err != nil {
		return domain.MatchSummarySource{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.MatchSummarySource{}, fmt.Errorf("commit match summary source read: %w", err)
	}
	return domain.MatchSummarySource{
		Session: session, PlayerCheckpoint: checkpoint, LifecycleEvents: lifecycle, DecisionEvents: decisions,
		FinalRecommendationEvidence: decisionEvidence, DecisionCheckpointUpdatedAt: checkpointUpdatedAt,
		Revision: revision,
	}, nil
}

// SaveMatchSummaryIfCurrent materializes a summary only when its source vector
// still matches the authoritative rows.
func (store *DB) SaveMatchSummaryIfCurrent(ctx context.Context, summary domain.MatchSummary, expected domain.SummarySourceRevision) error {
	if strings.TrimSpace(summary.MatchID) == "" || summary.SchemaVersion <= 0 || strings.TrimSpace(summary.Digest) == "" {
		return errors.New("match summary id, schema version, and digest are required")
	}
	if summary.SourceRevision != expected {
		return errors.New("match summary expected revision does not match payload revision")
	}
	if summary.BuiltAt.IsZero() {
		summary.BuiltAt = time.Now().UTC()
	}
	payload, err := json.Marshal(summary)
	if err != nil {
		return fmt.Errorf("encode match summary: %w", err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin match summary write: %w", err)
	}
	defer tx.Rollback()
	current, err := currentSummarySourceRevision(ctx, tx, summary.MatchID)
	if err != nil {
		return err
	}
	if current != expected {
		return ErrSummarySourceChanged
	}
	var endedAt any
	if summary.EndedAt != nil {
		endedAt = summary.EndedAt.UTC().UnixNano()
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO match_summaries(
			match_id, schema_version, source_session_updated_at_ns, source_player_observed_at_ns,
			source_lifecycle_sequence, source_decision_sequence, source_decision_checkpoint_at_ns,
			started_at_ns, ended_at_ns, completion, champion_name, role, payload, digest, built_at_ns
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(match_id) DO UPDATE SET
			schema_version=excluded.schema_version,
			source_session_updated_at_ns=excluded.source_session_updated_at_ns,
			source_player_observed_at_ns=excluded.source_player_observed_at_ns,
			source_lifecycle_sequence=excluded.source_lifecycle_sequence,
			source_decision_sequence=excluded.source_decision_sequence,
			source_decision_checkpoint_at_ns=excluded.source_decision_checkpoint_at_ns,
			started_at_ns=excluded.started_at_ns, ended_at_ns=excluded.ended_at_ns,
			completion=excluded.completion, champion_name=excluded.champion_name, role=excluded.role,
			payload=excluded.payload, digest=excluded.digest, built_at_ns=excluded.built_at_ns`,
		summary.MatchID, summary.SchemaVersion, expected.SessionUpdatedAtNS, expected.PlayerObservedAtNS,
		expected.LifecycleSequence, expected.DecisionSequence, expected.DecisionCheckpointAtNS,
		summary.StartedAt.UTC().UnixNano(), endedAt, summary.Completion, summary.ChampionName, summary.Role,
		payload, summary.Digest, summary.BuiltAt.UTC().UnixNano(),
	)
	if err != nil {
		return fmt.Errorf("save match summary %s: %w", summary.MatchID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit match summary %s: %w", summary.MatchID, err)
	}
	return nil
}

func (store *DB) LoadMatchSummary(ctx context.Context, matchID string) (domain.MatchSummary, error) {
	matchID = strings.TrimSpace(matchID)
	if matchID == "" {
		return domain.MatchSummary{}, errors.New("match id is required")
	}
	var envelope storedSummaryEnvelope
	var payload []byte
	err := store.db.QueryRowContext(ctx, `
		SELECT s.match_id, s.schema_version, s.source_session_updated_at_ns,
		       s.source_player_observed_at_ns, s.source_lifecycle_sequence,
		       s.source_decision_sequence, s.source_decision_checkpoint_at_ns,
		       s.started_at_ns, s.ended_at_ns, s.completion, s.champion_name,
		       s.role, s.payload, s.digest, s.built_at_ns,
		       m.state, m.started_at_ns, m.ended_at_ns
		FROM match_summaries s
		JOIN match_sessions m ON m.id = s.match_id
		WHERE s.match_id = ?`, matchID).Scan(
		&envelope.MatchID, &envelope.SchemaVersion, &envelope.SourceSessionUpdatedAtNS,
		&envelope.SourcePlayerObservedAtNS, &envelope.SourceLifecycleSequence,
		&envelope.SourceDecisionSequence, &envelope.SourceDecisionCheckpointAtNS,
		&envelope.StartedAtNS, &envelope.EndedAtNS, &envelope.Completion, &envelope.ChampionName,
		&envelope.Role, &payload, &envelope.Digest, &envelope.BuiltAtNS,
		&envelope.SessionState, &envelope.SessionStartedAtNS, &envelope.SessionEndedAtNS,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.MatchSummary{}, catalog.ErrNotFound
	}
	if err != nil {
		return domain.MatchSummary{}, fmt.Errorf("load match summary %s: %w", matchID, err)
	}
	return decodeStoredSummary(payload, envelope)
}

// ListMatchSummaries returns a stable newest-first keyset page.
func (store *DB) ListMatchSummaries(ctx context.Context, before *domain.HistoryCursor, limit int) ([]domain.MatchSummary, *domain.HistoryCursor, error) {
	limit = boundedLimit(limit)
	// Filtering and ordering are authoritative match-session properties. The
	// materialized envelope is deliberately not trusted for either operation.
	query := `SELECT id, started_at_ns FROM match_sessions WHERE state = 'ended'`
	args := make([]any, 0, 3)
	if before != nil {
		query += ` AND (started_at_ns < ? OR (started_at_ns = ? AND id < ?))`
		args = append(args, before.StartedAtNS, before.StartedAtNS, before.MatchID)
	}
	query += ` ORDER BY started_at_ns DESC, id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("list match summaries: %w", err)
	}
	defer rows.Close()
	values := make([]domain.HistoryCursor, 0, limit+1)
	for rows.Next() {
		var value domain.HistoryCursor
		if err := rows.Scan(&value.MatchID, &value.StartedAtNS); err != nil {
			return nil, nil, fmt.Errorf("scan match summary page: %w", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("read match summary page: %w", err)
	}
	var next *domain.HistoryCursor
	if len(values) > limit {
		boundary := values[limit-1]
		next = &domain.HistoryCursor{StartedAtNS: boundary.StartedAtNS, MatchID: boundary.MatchID}
		values = values[:limit]
	}
	result := make([]domain.MatchSummary, len(values))
	for index := range values {
		result[index], err = store.LoadMatchSummary(ctx, values[index].MatchID)
		if errors.Is(err, catalog.ErrNotFound) || errors.Is(err, ErrSummaryCorrupt) {
			// Preserve the authoritative key so the history service can rebuild a
			// missing/corrupt cache row from source before returning it.
			result[index] = domain.MatchSummary{MatchID: values[index].MatchID}
			continue
		}
		if err != nil {
			return nil, nil, err
		}
	}
	return result, next, nil
}

// ListStaleMatchSummaryIDs returns ended matches whose materialization is missing,
// corrupt, outdated, or built with an older summary schema.
func (store *DB) ListStaleMatchSummaryIDs(ctx context.Context, limit int) ([]string, error) {
	limit = boundedLimit(limit)
	rows, err := store.db.QueryContext(ctx, `
		SELECT m.id
		FROM match_sessions m
		LEFT JOIN match_player_checkpoints pc ON pc.match_id = m.id
		LEFT JOIN decision_checkpoints dc ON dc.match_id = m.id
		LEFT JOIN match_summaries s ON s.match_id = m.id
		WHERE m.state = 'ended' AND (
			s.match_id IS NULL OR
			s.schema_version != ? OR
			s.source_session_updated_at_ns != m.updated_at_ns OR
			s.source_player_observed_at_ns != COALESCE(pc.observed_at_ns, 0) OR
			s.source_lifecycle_sequence != m.last_event_sequence OR
			s.source_decision_sequence != COALESCE(dc.last_sequence, 0) OR
			s.source_decision_checkpoint_at_ns != COALESCE(dc.updated_at_ns, 0) OR
			s.started_at_ns != m.started_at_ns OR
			COALESCE(s.ended_at_ns, -1) != COALESCE(m.ended_at_ns, -1) OR
			s.completion NOT IN (?, ?) OR
			json_valid(CAST(s.payload AS TEXT)) = 0 OR
			COALESCE(json_extract(CAST(s.payload AS TEXT), '$.match_id'), '') != m.id OR
			COALESCE(json_extract(CAST(s.payload AS TEXT), '$.schema_version'), -1) != s.schema_version OR
			COALESCE(json_extract(CAST(s.payload AS TEXT), '$.source_revision.session_updated_at_ns'), -1) != s.source_session_updated_at_ns OR
			COALESCE(json_extract(CAST(s.payload AS TEXT), '$.source_revision.player_observed_at_ns'), -1) != s.source_player_observed_at_ns OR
			COALESCE(json_extract(CAST(s.payload AS TEXT), '$.source_revision.lifecycle_sequence'), -1) != s.source_lifecycle_sequence OR
			COALESCE(json_extract(CAST(s.payload AS TEXT), '$.source_revision.decision_sequence'), -1) != s.source_decision_sequence OR
			COALESCE(json_extract(CAST(s.payload AS TEXT), '$.source_revision.decision_checkpoint_at_ns'), -1) != s.source_decision_checkpoint_at_ns OR
			COALESCE(json_extract(CAST(s.payload AS TEXT), '$.completion'), '') != s.completion OR
			COALESCE(json_extract(CAST(s.payload AS TEXT), '$.champion_name'), '') != s.champion_name OR
			COALESCE(json_extract(CAST(s.payload AS TEXT), '$.role'), '') != s.role OR
			COALESCE(json_extract(CAST(s.payload AS TEXT), '$.digest'), '') != s.digest
		)
		ORDER BY m.updated_at_ns ASC, m.id ASC
		LIMIT ?`, domain.MatchSummarySchemaVersion, domain.SummaryCompleteTerminal, domain.SummaryIncompleteInferred, limit)
	if err != nil {
		return nil, fmt.Errorf("list stale match summaries: %w", err)
	}
	defer rows.Close()
	result := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan stale match summary: %w", err)
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

func (store *DB) LoadHistoryPreferences(ctx context.Context) (domain.HistoryPreferences, error) {
	var preferences domain.HistoryPreferences
	var days, maximum sql.NullInt64
	var updatedAt int64
	err := store.db.QueryRowContext(ctx, `
		SELECT retention_days, max_completed_matches, updated_at_ns FROM history_preferences WHERE id = 1`,
	).Scan(&days, &maximum, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.HistoryPreferences{}, nil
	}
	if err != nil {
		return domain.HistoryPreferences{}, fmt.Errorf("load history preferences: %w", err)
	}
	if days.Valid {
		value := int(days.Int64)
		preferences.RetentionDays = &value
	}
	if maximum.Valid {
		value := int(maximum.Int64)
		preferences.MaxCompletedMatches = &value
	}
	preferences.UpdatedAt = time.Unix(0, updatedAt).UTC()
	return preferences, nil
}

func (store *DB) SaveHistoryPreferences(ctx context.Context, preferences domain.HistoryPreferences) error {
	if preferences.RetentionDays != nil && *preferences.RetentionDays <= 0 {
		return errors.New("retention days must be positive")
	}
	if preferences.MaxCompletedMatches != nil && *preferences.MaxCompletedMatches <= 0 {
		return errors.New("maximum completed matches must be positive")
	}
	if preferences.UpdatedAt.IsZero() {
		preferences.UpdatedAt = time.Now().UTC()
	}
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO history_preferences(id, retention_days, max_completed_matches, updated_at_ns)
		VALUES(1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET retention_days=excluded.retention_days,
			max_completed_matches=excluded.max_completed_matches, updated_at_ns=excluded.updated_at_ns`,
		nullableInt(preferences.RetentionDays), nullableInt(preferences.MaxCompletedMatches), preferences.UpdatedAt.UTC().UnixNano(),
	)
	if err != nil {
		return fmt.Errorf("save history preferences: %w", err)
	}
	return nil
}

// ListRetentionCandidates returns oldest ended sessions eligible under either
// configured retention bound. A nil policy is disabled.
func (store *DB) ListRetentionCandidates(ctx context.Context, preferences domain.HistoryPreferences, now time.Time, limit int) ([]string, error) {
	limit = boundedLimit(limit)
	selected := make(map[string]struct{})
	result := make([]string, 0, limit)
	appendID := func(id string) {
		if len(result) >= limit {
			return
		}
		if _, exists := selected[id]; exists {
			return
		}
		selected[id] = struct{}{}
		result = append(result, id)
	}
	if preferences.RetentionDays != nil {
		cutoff := now.UTC().Add(-time.Duration(*preferences.RetentionDays) * 24 * time.Hour).UnixNano()
		rows, err := store.db.QueryContext(ctx, `
			SELECT id FROM match_sessions
			WHERE state = 'ended' AND COALESCE(ended_at_ns, updated_at_ns) < ?
			ORDER BY COALESCE(ended_at_ns, updated_at_ns) ASC, id ASC LIMIT ?`, cutoff, limit)
		if err != nil {
			return nil, fmt.Errorf("list age retention candidates: %w", err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			appendID(id)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	if preferences.MaxCompletedMatches != nil && len(result) < limit {
		rows, err := store.db.QueryContext(ctx, `
			SELECT id FROM match_sessions WHERE state = 'ended'
			ORDER BY started_at_ns DESC, id DESC LIMIT -1 OFFSET ?`, *preferences.MaxCompletedMatches)
		if err != nil {
			return nil, fmt.Errorf("list count retention candidates: %w", err)
		}
		for rows.Next() && len(result) < limit {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			appendID(id)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (store *DB) DeleteMatchHistory(ctx context.Context, matchID string) error {
	matchID = strings.TrimSpace(matchID)
	if matchID == "" {
		return errors.New("match id is required")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin match history deletion: %w", err)
	}
	defer tx.Rollback()
	var state domain.MatchState
	if err := tx.QueryRowContext(ctx, `SELECT state FROM match_sessions WHERE id = ?`, matchID).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return catalog.ErrNotFound
		}
		return fmt.Errorf("read match history state: %w", err)
	}
	if state == domain.MatchActive {
		return ErrActiveMatchHistory
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM latest_recommendation WHERE match_id = ?`, matchID); err != nil {
		return fmt.Errorf("delete associated latest recommendation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM match_sessions WHERE id = ?`, matchID); err != nil {
		return fmt.Errorf("delete match history: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit match history deletion: %w", err)
	}
	return nil
}

func (store *DB) DeleteCompletedHistory(ctx context.Context) (int64, error) {
	ids, err := store.ListCompletedMatchIDs(ctx)
	if err != nil {
		return 0, err
	}
	return store.DeleteCompletedMatchHistory(ctx, ids)
}

// DeleteCompletedMatchHistory deletes only the caller's settled snapshot. The
// exact set is authoritatively revalidated and removed in one transaction, so
// a different match ending concurrently cannot be swept into the deletion.
func (store *DB) DeleteCompletedMatchHistory(ctx context.Context, ids []string) (int64, error) {
	unique := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			return 0, errors.New("completed history deletion contains an empty match id")
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return 0, nil
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin completed history deletion: %w", err)
	}
	defer tx.Rollback()
	for _, id := range unique {
		var state domain.MatchState
		if err := tx.QueryRowContext(ctx, `SELECT state FROM match_sessions WHERE id = ?`, id).Scan(&state); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, fmt.Errorf("%w: match %s no longer exists", ErrHistoryDeleteChanged, id)
			}
			return 0, fmt.Errorf("revalidate completed match %s: %w", id, err)
		}
		if state != domain.MatchEnded {
			return 0, fmt.Errorf("%w: %s", ErrActiveMatchHistory, id)
		}
	}
	for _, id := range unique {
		if _, err := tx.ExecContext(ctx, `DELETE FROM latest_recommendation WHERE match_id = ?`, id); err != nil {
			return 0, fmt.Errorf("delete completed recommendation %s: %w", id, err)
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM match_sessions WHERE id = ? AND state = 'ended'`, id)
		if err != nil {
			return 0, fmt.Errorf("delete completed history %s: %w", id, err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("read completed history deletion %s: %w", id, err)
		}
		if rows != 1 {
			return 0, fmt.Errorf("%w: match %s changed during transaction", ErrHistoryDeleteChanged, id)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit completed history deletion: %w", err)
	}
	return int64(len(unique)), nil
}

func (store *DB) ListCompletedMatchIDs(ctx context.Context) ([]string, error) {
	rows, err := store.db.QueryContext(ctx, `
		SELECT id FROM match_sessions WHERE state = 'ended' ORDER BY started_at_ns DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list completed match ids: %w", err)
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

// CheckpointHistoryWAL truncates committed WAL frames after bulk erasure. It is
// best-effort privacy maintenance, not a cryptographic deletion guarantee.
func (store *DB) CheckpointHistoryWAL(ctx context.Context) error {
	var busy, logFrames, checkpointed int
	if err := store.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointed); err != nil {
		return fmt.Errorf("checkpoint history WAL: %w", err)
	}
	if busy != 0 {
		return fmt.Errorf("checkpoint history WAL: database remained busy")
	}
	return nil
}

func loadPlayerCheckpoint(ctx context.Context, tx *sql.Tx, matchID string) (*domain.MatchPlayerCheckpoint, error) {
	var checkpoint domain.MatchPlayerCheckpoint
	var observedAt int64
	var roleKnown int
	var inventory []byte
	err := tx.QueryRowContext(ctx, `
		SELECT match_id, observed_at_ns, game_time, champion_name, role, role_known,
		       level, kills, deaths, assists, creep_score, inventory
		FROM match_player_checkpoints WHERE match_id = ?`, matchID).Scan(
		&checkpoint.MatchID, &observedAt, &checkpoint.GameTimeSeconds, &checkpoint.ChampionName,
		&checkpoint.Role, &roleKnown, &checkpoint.Level, &checkpoint.Kills, &checkpoint.Deaths,
		&checkpoint.Assists, &checkpoint.CreepScore, &inventory,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load player checkpoint %s: %w", matchID, err)
	}
	if err := json.Unmarshal(inventory, &checkpoint.Inventory); err != nil {
		return nil, fmt.Errorf("decode player checkpoint %s inventory: %w", matchID, err)
	}
	checkpoint.Inventory = canonicalInventory(checkpoint.Inventory)
	checkpoint.ObservedAt = time.Unix(0, observedAt).UTC()
	checkpoint.RoleKnown = roleKnown == 1 && checkpoint.Role.Valid()
	return &checkpoint, nil
}

func loadAllMatchEvents(ctx context.Context, tx *sql.Tx, matchID string) ([]domain.SemanticEvent, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, match_id, sequence, dedupe_key, kind, occurred_at_ns, observed_at_ns,
		       game_time, source, confidence, payload
		FROM match_events WHERE match_id = ? ORDER BY sequence ASC`, matchID)
	if err != nil {
		return nil, fmt.Errorf("load match summary lifecycle events: %w", err)
	}
	defer rows.Close()
	var result []domain.SemanticEvent
	for rows.Next() {
		event, err := scanMatchEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan match summary lifecycle event: %w", err)
		}
		result = append(result, event)
	}
	return result, rows.Err()
}

func loadAllDecisionEvents(ctx context.Context, tx *sql.Tx, matchID string) ([]domain.DecisionEvent, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, match_id, sequence, dedupe_key, kind, importance, occurred_at_ns,
		       game_time, previous_summary, current_summary, reason_codes, related_lifecycle_event_id, evidence
		FROM decision_events WHERE match_id = ? ORDER BY sequence ASC`, matchID)
	if err != nil {
		return nil, fmt.Errorf("load match summary decision events: %w", err)
	}
	defer rows.Close()
	var result []domain.DecisionEvent
	for rows.Next() {
		event, err := scanDecisionEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan match summary decision event: %w", err)
		}
		result = append(result, event)
	}
	return result, rows.Err()
}

func loadDecisionSummaryCheckpoint(ctx context.Context, tx *sql.Tx, matchID string) (*domain.RecommendationEvidence, time.Time, error) {
	var evidence []byte
	var updatedAt int64
	err := tx.QueryRowContext(ctx, `SELECT evidence, updated_at_ns FROM decision_checkpoints WHERE match_id = ?`, matchID).Scan(&evidence, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, time.Time{}, nil
	}
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("load match summary decision checkpoint: %w", err)
	}
	var decoded *domain.RecommendationEvidence
	if len(evidence) > 0 {
		if err := json.Unmarshal(evidence, &decoded); err != nil {
			return nil, time.Time{}, fmt.Errorf("decode match summary decision evidence: %w", err)
		}
	}
	return decoded, time.Unix(0, updatedAt).UTC(), nil
}

func currentSummarySourceRevision(ctx context.Context, query rowQueryer, matchID string) (domain.SummarySourceRevision, error) {
	var revision domain.SummarySourceRevision
	err := query.QueryRowContext(ctx, `
		SELECT m.updated_at_ns, m.last_event_sequence,
		       COALESCE(pc.observed_at_ns, 0), COALESCE(dc.last_sequence, 0), COALESCE(dc.updated_at_ns, 0)
		FROM match_sessions m
		LEFT JOIN match_player_checkpoints pc ON pc.match_id = m.id
		LEFT JOIN decision_checkpoints dc ON dc.match_id = m.id
		WHERE m.id = ?`, matchID).Scan(
		&revision.SessionUpdatedAtNS, &revision.LifecycleSequence, &revision.PlayerObservedAtNS,
		&revision.DecisionSequence, &revision.DecisionCheckpointAtNS,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SummarySourceRevision{}, catalog.ErrNotFound
	}
	if err != nil {
		return domain.SummarySourceRevision{}, fmt.Errorf("load match summary source revision: %w", err)
	}
	return revision, nil
}

type rowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type storedSummaryEnvelope struct {
	MatchID                      string
	SchemaVersion                int
	SourceSessionUpdatedAtNS     int64
	SourcePlayerObservedAtNS     int64
	SourceLifecycleSequence      int64
	SourceDecisionSequence       int64
	SourceDecisionCheckpointAtNS int64
	StartedAtNS                  int64
	EndedAtNS                    sql.NullInt64
	Completion                   domain.SummaryCompletion
	ChampionName                 string
	Role                         domain.Role
	Digest                       string
	BuiltAtNS                    int64
	SessionState                 domain.MatchState
	SessionStartedAtNS           int64
	SessionEndedAtNS             sql.NullInt64
}

func decodeStoredSummary(payload []byte, envelope storedSummaryEnvelope) (domain.MatchSummary, error) {
	if !json.Valid(payload) {
		return domain.MatchSummary{}, ErrSummaryCorrupt
	}
	var summary domain.MatchSummary
	if err := json.Unmarshal(payload, &summary); err != nil {
		return domain.MatchSummary{}, fmt.Errorf("%w: %v", ErrSummaryCorrupt, err)
	}
	revision, validRevision := envelope.sourceRevision()
	if summary.MatchID == "" || summary.Digest == "" || summary.MatchID != envelope.MatchID ||
		!validRevision || summary.SchemaVersion != envelope.SchemaVersion || summary.SourceRevision != revision ||
		summary.StartedAt.UTC().UnixNano() != envelope.StartedAtNS ||
		!nullableSummaryTimeMatches(summary.EndedAt, envelope.EndedAtNS) ||
		summary.Completion != envelope.Completion || summary.ChampionName != envelope.ChampionName ||
		summary.Role != envelope.Role || summary.Digest != envelope.Digest ||
		summary.BuiltAt.UTC().UnixNano() != envelope.BuiltAtNS ||
		summary.StartedAt.UTC().UnixNano() != envelope.SessionStartedAtNS ||
		!nullableSummaryTimeMatches(summary.EndedAt, envelope.SessionEndedAtNS) ||
		!completionMatchesSessionState(summary.Completion, envelope.SessionState) {
		return domain.MatchSummary{}, ErrSummaryCorrupt
	}
	return summary, nil
}

func (envelope storedSummaryEnvelope) sourceRevision() (domain.SummarySourceRevision, bool) {
	if envelope.SourceLifecycleSequence < 0 || envelope.SourceDecisionSequence < 0 {
		return domain.SummarySourceRevision{}, false
	}
	return domain.SummarySourceRevision{
		SessionUpdatedAtNS:     envelope.SourceSessionUpdatedAtNS,
		PlayerObservedAtNS:     envelope.SourcePlayerObservedAtNS,
		LifecycleSequence:      uint64(envelope.SourceLifecycleSequence),
		DecisionSequence:       uint64(envelope.SourceDecisionSequence),
		DecisionCheckpointAtNS: envelope.SourceDecisionCheckpointAtNS,
	}, true
}

func nullableSummaryTimeMatches(value *time.Time, stored sql.NullInt64) bool {
	if value == nil {
		return !stored.Valid
	}
	return stored.Valid && value.UTC().UnixNano() == stored.Int64
}

func completionMatchesSessionState(completion domain.SummaryCompletion, state domain.MatchState) bool {
	switch state {
	case domain.MatchActive:
		return completion == domain.SummaryActive
	case domain.MatchEnded:
		return completion == domain.SummaryCompleteTerminal || completion == domain.SummaryIncompleteInferred
	default:
		return false
	}
}

func playerCheckpointsExactlyEqual(left, right domain.MatchPlayerCheckpoint) bool {
	if left.MatchID != right.MatchID || !left.ObservedAt.Equal(right.ObservedAt) ||
		left.GameTimeSeconds != right.GameTimeSeconds || left.ChampionName != right.ChampionName ||
		left.Role != right.Role || left.RoleKnown != right.RoleKnown || left.Level != right.Level ||
		left.Kills != right.Kills || left.Deaths != right.Deaths || left.Assists != right.Assists ||
		left.CreepScore != right.CreepScore || len(left.Inventory) != len(right.Inventory) {
		return false
	}
	for index := range left.Inventory {
		if left.Inventory[index] != right.Inventory[index] {
			return false
		}
	}
	return true
}

func nullableInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}
