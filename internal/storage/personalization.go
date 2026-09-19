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
	"lolengine/internal/personalization"
)

const personalizationPrivacyMaintenanceMetadata = "personalization_feedback_privacy_maintenance_pending"

func (store *DB) LoadPersonalizationPreferences(ctx context.Context) (personalization.Preferences, error) {
	var value personalization.Preferences
	var preferred, avoided []byte
	var feedback int
	var updated int64
	var reset sql.NullInt64
	err := store.db.QueryRowContext(ctx, `
		SELECT style, preferred_item_ids, avoided_item_ids, feedback_enabled, version, updated_at_ns, reset_at_ns
		FROM personalization_preferences WHERE id = 1`,
	).Scan(&value.Style, &preferred, &avoided, &feedback, &value.Version, &updated, &reset)
	if err != nil {
		return personalization.Preferences{}, fmt.Errorf("load personalization preferences: %w", err)
	}
	if err := json.Unmarshal(preferred, &value.PreferredItemIDs); err != nil {
		return personalization.Preferences{}, fmt.Errorf("decode preferred item ids: %w", err)
	}
	if err := json.Unmarshal(avoided, &value.AvoidedItemIDs); err != nil {
		return personalization.Preferences{}, fmt.Errorf("decode avoided item ids: %w", err)
	}
	value.FeedbackEnabled = feedback == 1
	if updated > 0 {
		value.UpdatedAt = time.Unix(0, updated).UTC()
	}
	if reset.Valid && reset.Int64 > 0 {
		value.ResetAt = time.Unix(0, reset.Int64).UTC()
	}
	if err := validatePreferenceItemShape(value.PreferredItemIDs, value.AvoidedItemIDs); err != nil {
		return personalization.Preferences{}, fmt.Errorf("load personalization preferences: %w", err)
	}
	return value, nil
}

func (store *DB) SavePersonalizationPreferences(ctx context.Context, value personalization.Preferences) error {
	if !value.Style.Valid() || value.Version == 0 || value.UpdatedAt.IsZero() {
		return errors.New("personalization style, version, and update timestamp are required")
	}
	if err := validatePreferenceItemShape(value.PreferredItemIDs, value.AvoidedItemIDs); err != nil {
		return err
	}
	preferredValues := append([]int{}, value.PreferredItemIDs...)
	avoidedValues := append([]int{}, value.AvoidedItemIDs...)
	preferred, err := json.Marshal(preferredValues)
	if err != nil {
		return err
	}
	avoided, err := json.Marshal(avoidedValues)
	if err != nil {
		return err
	}
	feedback := 0
	if value.FeedbackEnabled {
		feedback = 1
	}
	var reset any
	if !value.ResetAt.IsZero() {
		reset = value.ResetAt.UTC().UnixNano()
	}
	result, err := store.db.ExecContext(ctx, `
		UPDATE personalization_preferences SET style=?, preferred_item_ids=?, avoided_item_ids=?,
			feedback_enabled=?, version=?, updated_at_ns=?, reset_at_ns=?
		WHERE id=1 AND version < ?`, value.Style, preferred, avoided, feedback, value.Version,
		value.UpdatedAt.UTC().UnixNano(), reset, value.Version)
	if err != nil {
		return fmt.Errorf("save personalization preferences: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return errors.New("personalization preference version must advance")
	}
	return nil
}

func (store *DB) LoadRecommendationFeedback(ctx context.Context) ([]personalization.FeedbackRecord, error) {
	rows, err := store.db.QueryContext(ctx, feedbackSelect+` ORDER BY created_at_ns ASC, client_feedback_id ASC`)
	if err != nil {
		return nil, fmt.Errorf("load recommendation feedback: %w", err)
	}
	defer rows.Close()
	result := make([]personalization.FeedbackRecord, 0)
	for rows.Next() {
		record, err := scanFeedback(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

func (store *DB) SaveRecommendationFeedback(ctx context.Context, record personalization.FeedbackRecord) (bool, error) {
	if err := validateFeedbackRecord(record); err != nil {
		return false, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin recommendation feedback: %w", err)
	}
	defer tx.Rollback()
	if err := validateFeedbackDecision(ctx, tx, record); err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO recommendation_feedback(
			client_feedback_id, decision_event_id, session_id, decision_sequence,
			profile_id, profile_version, rules_version, rules_source, patch, champion_id,
			role, stage, strategy, item_id, value, created_at_ns
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT DO NOTHING`,
		record.ClientFeedbackID, record.DecisionEventID, record.SessionID, record.DecisionSequence,
		record.ProfileID, record.ProfileVersion, record.RulesVersion, record.RulesSource,
		record.Patch, record.ChampionID, record.Role, record.Stage, record.Strategy,
		record.ItemID, record.Value, record.CreatedAt.UTC().UnixNano())
	if err != nil {
		return false, fmt.Errorf("save recommendation feedback: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if rows == 0 {
		stored, err := loadFeedbackByID(ctx, tx, record.ClientFeedbackID)
		if errors.Is(err, sql.ErrNoRows) {
			stored, err = loadFeedbackByDecisionItem(ctx, tx, record.DecisionEventID, record.ItemID)
			if err != nil {
				return false, err
			}
			if !feedbackLogicalEqual(stored, record) {
				return false, personalization.ErrFeedbackConflict
			}
		} else if err != nil {
			return false, err
		} else if !feedbackEqual(stored, record) {
			return false, personalization.ErrFeedbackConflict
		}
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit recommendation feedback: %w", err)
	}
	return true, nil
}

func (store *DB) LoadFeedbackDecision(ctx context.Context, id string) (personalization.FeedbackDecision, error) {
	id = strings.TrimSpace(id)
	row := store.db.QueryRowContext(ctx, `
		SELECT d.id, d.match_id, d.sequence, d.evidence, m.champion_name, m.position
		FROM decision_events d JOIN match_sessions m ON m.id=d.match_id WHERE d.id=?`, id)
	var result personalization.FeedbackDecision
	var evidence []byte
	var roleText string
	if err := row.Scan(&result.EventID, &result.SessionID, &result.Sequence, &evidence, &result.ChampionID, &roleText); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return personalization.FeedbackDecision{}, personalization.ErrDecisionNotFound
		}
		return personalization.FeedbackDecision{}, fmt.Errorf("load feedback decision: %w", err)
	}
	if len(evidence) == 0 || json.Unmarshal(evidence, &result.Evidence) != nil {
		return personalization.FeedbackDecision{}, errors.New("feedback decision has no valid recommendation evidence")
	}
	result.Role, _ = domain.ParseRole(roleText)
	return result, nil
}

func (store *DB) DeleteRecommendationFeedback(ctx context.Context) (int64, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin recommendation feedback deletion: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM recommendation_feedback`)
	if err != nil {
		return 0, fmt.Errorf("delete recommendation feedback: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if count > 0 {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO metadata(key, value, updated_at) VALUES(?, 'pending', ?)
			ON CONFLICT(key) DO UPDATE SET value='pending', updated_at=excluded.updated_at`,
			personalizationPrivacyMaintenanceMetadata, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return 0, fmt.Errorf("schedule recommendation feedback privacy maintenance: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit recommendation feedback deletion: %w", err)
	}
	if count > 0 {
		// The deletion is committed and authoritative even when a reader keeps
		// the WAL busy. The marker remains durable for a later retry.
		_ = store.RetryPersonalizationPrivacyMaintenance(context.Background())
	}
	return count, nil
}

// RetryPersonalizationPrivacyMaintenance truncates WAL frames after a
// committed feedback erasure. A busy checkpoint leaves the durable marker.
func (store *DB) RetryPersonalizationPrivacyMaintenance(ctx context.Context) error {
	value, err := store.Metadata(ctx, personalizationPrivacyMaintenanceMetadata)
	if errors.Is(err, catalog.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if value != "pending" {
		return fmt.Errorf("invalid personalization privacy maintenance state %q", value)
	}
	var busy, logFrames, checkpointed int
	if err := store.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointed); err != nil {
		return fmt.Errorf("checkpoint personalization feedback WAL: %w", err)
	}
	if busy != 0 {
		return errors.New("checkpoint personalization feedback WAL: database remained busy")
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM metadata WHERE key=?`, personalizationPrivacyMaintenanceMetadata); err != nil {
		return fmt.Errorf("clear personalization privacy maintenance marker: %w", err)
	}
	// The marker cleanup itself is non-sensitive, but leave the WAL tidy when
	// possible. A failure here cannot resurrect the deleted feedback.
	_, _ = store.db.ExecContext(context.Background(), `PRAGMA wal_checkpoint(TRUNCATE)`)
	return nil
}

const feedbackSelect = `SELECT client_feedback_id, decision_event_id, session_id, decision_sequence,
	profile_id, profile_version, rules_version, rules_source, patch, champion_id,
	role, stage, strategy, item_id, value, created_at_ns FROM recommendation_feedback`

func scanFeedback(row rowScanner) (personalization.FeedbackRecord, error) {
	var record personalization.FeedbackRecord
	var created int64
	err := row.Scan(&record.ClientFeedbackID, &record.DecisionEventID, &record.SessionID, &record.DecisionSequence,
		&record.ProfileID, &record.ProfileVersion, &record.RulesVersion, &record.RulesSource,
		&record.Patch, &record.ChampionID, &record.Role, &record.Stage, &record.Strategy,
		&record.ItemID, &record.Value, &created)
	if err != nil {
		return personalization.FeedbackRecord{}, fmt.Errorf("scan recommendation feedback: %w", err)
	}
	record.CreatedAt = time.Unix(0, created).UTC()
	return record, nil
}

func loadFeedbackByID(ctx context.Context, tx *sql.Tx, id string) (personalization.FeedbackRecord, error) {
	return scanFeedback(tx.QueryRowContext(ctx, feedbackSelect+` WHERE client_feedback_id=?`, id))
}

func loadFeedbackByDecisionItem(ctx context.Context, tx *sql.Tx, decisionEventID string, itemID int) (personalization.FeedbackRecord, error) {
	return scanFeedback(tx.QueryRowContext(ctx, feedbackSelect+` WHERE decision_event_id=? AND item_id=?`, decisionEventID, itemID))
}

func validateFeedbackRecord(record personalization.FeedbackRecord) error {
	if strings.TrimSpace(record.ClientFeedbackID) == "" || len(record.ClientFeedbackID) > personalization.MaximumClientIDLength ||
		strings.TrimSpace(record.DecisionEventID) == "" || len(record.DecisionEventID) > 192 ||
		strings.TrimSpace(record.SessionID) == "" || len(record.SessionID) > 192 || record.DecisionSequence == 0 ||
		strings.TrimSpace(record.Patch) == "" || strings.TrimSpace(record.ChampionID) == "" ||
		strings.TrimSpace(record.RulesVersion) == "" || strings.TrimSpace(record.RulesSource) == "" || record.ItemID <= 0 ||
		!record.Value.Valid() || record.CreatedAt.IsZero() || !record.Strategy.Valid() {
		return errors.New("recommendation feedback has invalid required fields")
	}
	if len(record.ProfileID) > 128 || len(record.ProfileVersion) > 128 || len(record.RulesVersion) > 128 || len(record.RulesSource) > 64 ||
		len(record.Patch) > 64 || len(record.ChampionID) > 96 || !record.Role.Valid() {
		return errors.New("recommendation feedback has invalid bounded context fields")
	}
	if record.Stage != domain.PhaseEarly && record.Stage != domain.PhaseMid && record.Stage != domain.PhaseLate {
		return errors.New("recommendation feedback has invalid stage")
	}
	return nil
}

func validatePreferenceItemShape(preferred, avoided []int) error {
	if len(preferred) > personalization.MaximumPreferredItems || len(avoided) > personalization.MaximumAvoidedItems {
		return errors.New("personalization item preference count exceeds bounds")
	}
	validateOrdered := func(label string, values []int) error {
		for index, itemID := range values {
			if itemID <= 0 {
				return fmt.Errorf("personalization %s item ids must be positive", label)
			}
			if index > 0 && values[index-1] >= itemID {
				return fmt.Errorf("personalization %s item ids must be unique and sorted", label)
			}
		}
		return nil
	}
	if err := validateOrdered("preferred", preferred); err != nil {
		return err
	}
	if err := validateOrdered("avoided", avoided); err != nil {
		return err
	}
	preferredIndex, avoidedIndex := 0, 0
	for preferredIndex < len(preferred) && avoidedIndex < len(avoided) {
		switch {
		case preferred[preferredIndex] == avoided[avoidedIndex]:
			return errors.New("personalization preferred and avoided item ids overlap")
		case preferred[preferredIndex] < avoided[avoidedIndex]:
			preferredIndex++
		default:
			avoidedIndex++
		}
	}
	return nil
}

func validateFeedbackDecision(ctx context.Context, tx *sql.Tx, record personalization.FeedbackRecord) error {
	var sessionID string
	var sequence uint64
	var evidenceJSON []byte
	err := tx.QueryRowContext(ctx, `SELECT match_id, sequence, evidence FROM decision_events WHERE id=?`, record.DecisionEventID).Scan(&sessionID, &sequence, &evidenceJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return personalization.ErrDecisionNotFound
	}
	if err != nil {
		return err
	}
	if sessionID != record.SessionID || sequence != record.DecisionSequence {
		return personalization.ErrFeedbackConflict
	}
	var evidence domain.RecommendationEvidence
	if len(evidenceJSON) == 0 || json.Unmarshal(evidenceJSON, &evidence) != nil {
		return errors.New("feedback decision has invalid recommendation evidence")
	}
	visible := false
	for _, choice := range evidence.RankedChoices {
		if choice.Item.ID == record.ItemID {
			visible = true
			break
		}
	}
	if !visible {
		return personalization.ErrChoiceNotVisible
	}
	if evidence.Patch != record.Patch || evidence.Phase != record.Stage || evidence.Strategy != record.Strategy ||
		evidence.Rules.Version != record.RulesVersion || evidence.Rules.Source != record.RulesSource {
		return personalization.ErrFeedbackConflict
	}
	if evidence.Profile == nil {
		if record.ProfileID != "" || record.ProfileVersion != "" {
			return personalization.ErrFeedbackConflict
		}
	} else if evidence.Profile.ProfileID != record.ProfileID || evidence.Profile.RulesetVersion != record.ProfileVersion {
		return personalization.ErrFeedbackConflict
	}
	return nil
}

func feedbackEqual(left, right personalization.FeedbackRecord) bool {
	return left.ClientFeedbackID == right.ClientFeedbackID && left.DecisionEventID == right.DecisionEventID && left.SessionID == right.SessionID &&
		left.DecisionSequence == right.DecisionSequence && left.ProfileID == right.ProfileID && left.ProfileVersion == right.ProfileVersion &&
		left.RulesVersion == right.RulesVersion && left.RulesSource == right.RulesSource && left.Patch == right.Patch &&
		left.ChampionID == right.ChampionID && left.Role == right.Role && left.Stage == right.Stage && left.Strategy == right.Strategy &&
		left.ItemID == right.ItemID && left.Value == right.Value && left.CreatedAt.Equal(right.CreatedAt)
}

func feedbackLogicalEqual(left, right personalization.FeedbackRecord) bool {
	return left.DecisionEventID == right.DecisionEventID && left.SessionID == right.SessionID &&
		left.DecisionSequence == right.DecisionSequence && left.ProfileID == right.ProfileID && left.ProfileVersion == right.ProfileVersion &&
		left.RulesVersion == right.RulesVersion && left.RulesSource == right.RulesSource && left.Patch == right.Patch &&
		left.ChampionID == right.ChampionID && left.Role == right.Role && left.Stage == right.Stage && left.Strategy == right.Strategy &&
		left.ItemID == right.ItemID && left.Value == right.Value
}
