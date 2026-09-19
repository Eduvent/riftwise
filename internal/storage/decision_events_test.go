package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lolengine/internal/catalog"
	"lolengine/internal/domain"
)

func TestMigrationFromSchemaV3AddsDecisionEventTables(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v3-decisions.db")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`,
		`INSERT INTO schema_migrations(version, applied_at) VALUES
			(1, '2026-09-19T00:00:00Z'), (2, '2026-09-19T00:01:00Z'), (3, '2026-09-19T00:02:00Z')`,
		`CREATE TABLE match_sessions (id TEXT PRIMARY KEY, last_event_sequence INTEGER NOT NULL DEFAULT 0)`,
		`INSERT INTO match_sessions(id) VALUES('migrated-match')`,
	}
	for _, statement := range statements {
		if _, err := database.ExecContext(ctx, statement); err != nil {
			t.Fatalf("prepare v3: %v", err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open(v3) error = %v", err)
	}
	defer store.Close()
	version, err := store.SchemaVersion(ctx)
	if err != nil || version != schemaVersion {
		t.Fatalf("SchemaVersion() = %d, %v, want %d", version, err, schemaVersion)
	}
	checkpoint := DecisionCheckpoint{
		SessionID: "migrated-match", State: json.RawMessage(`{"target_id":4401}`),
		UpdatedAt: time.Date(2026, 9, 19, 9, 45, 0, 0, time.UTC),
	}
	if err := store.SaveDecisionUpdates(ctx, checkpoint, nil); err != nil {
		t.Fatalf("SaveDecisionUpdates() after v3 migration error = %v", err)
	}
}

func TestDecisionSequenceIntegrityAndTransactionalRollback(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 19, 11, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		seed       bool
		events     func(string) []domain.DecisionEvent
		checkpoint func(string) DecisionCheckpoint
		wantError  string
		wantCount  int
		wantLast   uint64
	}{
		{
			name: "initial sequence greater than one",
			events: func(sessionID string) []domain.DecisionEvent {
				return []domain.DecisionEvent{decisionEvent(sessionID, 2, domain.DecisionTargetChanged, now)}
			},
			checkpoint: func(sessionID string) DecisionCheckpoint { return decisionCheckpoint(sessionID, 2, now) },
			wantError:  "contiguous",
		},
		{
			name: "gap rolls back earlier insert",
			events: func(sessionID string) []domain.DecisionEvent {
				return []domain.DecisionEvent{
					decisionEvent(sessionID, 1, domain.DecisionTargetChanged, now),
					decisionEvent(sessionID, 3, domain.DecisionComponentAffordable, now.Add(time.Second)),
				}
			},
			checkpoint: func(sessionID string) DecisionCheckpoint { return decisionCheckpoint(sessionID, 3, now) },
			wantError:  "contiguous",
		},
		{
			name: "unsorted new events",
			events: func(sessionID string) []domain.DecisionEvent {
				return []domain.DecisionEvent{
					decisionEvent(sessionID, 2, domain.DecisionComponentAffordable, now.Add(time.Second)),
					decisionEvent(sessionID, 1, domain.DecisionTargetChanged, now),
				}
			},
			checkpoint: func(sessionID string) DecisionCheckpoint { return decisionCheckpoint(sessionID, 2, now) },
			wantError:  "contiguous",
		},
		{
			name: "duplicate new event",
			events: func(sessionID string) []domain.DecisionEvent {
				event := decisionEvent(sessionID, 1, domain.DecisionTargetChanged, now)
				return []domain.DecisionEvent{event, event}
			},
			checkpoint: func(sessionID string) DecisionCheckpoint { return decisionCheckpoint(sessionID, 1, now) },
			wantError:  "contiguous",
		},
		{
			name:       "checkpoint cannot advance without event",
			events:     func(string) []domain.DecisionEvent { return nil },
			checkpoint: func(sessionID string) DecisionCheckpoint { return decisionCheckpoint(sessionID, 1, now) },
			wantError:  "highest committed",
		},
		{
			name: "checkpoint cannot exceed supplied events",
			events: func(sessionID string) []domain.DecisionEvent {
				return []domain.DecisionEvent{decisionEvent(sessionID, 1, domain.DecisionTargetChanged, now)}
			},
			checkpoint: func(sessionID string) DecisionCheckpoint { return decisionCheckpoint(sessionID, 2, now) },
			wantError:  "highest committed",
		},
		{
			name: "checkpoint cannot trail supplied events",
			events: func(sessionID string) []domain.DecisionEvent {
				return []domain.DecisionEvent{
					decisionEvent(sessionID, 1, domain.DecisionTargetChanged, now),
					decisionEvent(sessionID, 2, domain.DecisionComponentAffordable, now.Add(time.Second)),
				}
			},
			checkpoint: func(sessionID string) DecisionCheckpoint { return decisionCheckpoint(sessionID, 1, now) },
			wantError:  "highest committed",
		},
		{
			name: "duplicate exact retry is accepted",
			seed: true,
			events: func(sessionID string) []domain.DecisionEvent {
				event := decisionEvent(sessionID, 1, domain.DecisionTargetChanged, now)
				return []domain.DecisionEvent{event, event}
			},
			checkpoint: func(sessionID string) DecisionCheckpoint { return decisionCheckpoint(sessionID, 1, now) },
			wantCount:  1,
			wantLast:   1,
		},
		{
			name: "exact retry followed by contiguous insert",
			seed: true,
			events: func(sessionID string) []domain.DecisionEvent {
				return []domain.DecisionEvent{
					decisionEvent(sessionID, 1, domain.DecisionTargetChanged, now),
					decisionEvent(sessionID, 2, domain.DecisionComponentAffordable, now.Add(time.Second)),
				}
			},
			checkpoint: func(sessionID string) DecisionCheckpoint {
				return decisionCheckpoint(sessionID, 2, now.Add(time.Second))
			},
			wantCount: 2,
			wantLast:  2,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := Open(ctx, filepath.Join(t.TempDir(), "sequence.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			session := decisionSession(now)
			session.LastEventSequence = 0
			if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{session}, nil); err != nil {
				t.Fatal(err)
			}
			if test.seed {
				seed := decisionEvent(session.ID, 1, domain.DecisionTargetChanged, now)
				if err := store.SaveDecisionUpdates(ctx, decisionCheckpoint(session.ID, 1, now), []domain.DecisionEvent{seed}); err != nil {
					t.Fatalf("seed decision: %v", err)
				}
			}

			err = store.SaveDecisionUpdates(ctx, test.checkpoint(session.ID), test.events(session.ID))
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("SaveDecisionUpdates() error = %v, want %q", err, test.wantError)
				}
			} else if err != nil {
				t.Fatalf("SaveDecisionUpdates() error = %v", err)
			}
			stored, listErr := store.ListDecisionEvents(ctx, session.ID, 0, 10)
			if listErr != nil || len(stored) != test.wantCount {
				t.Fatalf("stored events = %#v, %v; want count %d", stored, listErr, test.wantCount)
			}
			checkpoint, loadErr := store.LoadDecisionCheckpoint(ctx, session.ID)
			if test.wantLast == 0 {
				if !errors.Is(loadErr, catalog.ErrNotFound) {
					t.Fatalf("rolled-back checkpoint = %#v, %v", checkpoint, loadErr)
				}
			} else if loadErr != nil || checkpoint.LastSequence != test.wantLast {
				t.Fatalf("checkpoint = %#v, %v; want sequence %d", checkpoint, loadErr, test.wantLast)
			}
		})
	}
}

func TestDecisionLifecycleCursorAndLinksAreTransactionallyValidated(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 19, 11, 30, 0, 0, time.UTC)
	tests := []struct {
		name              string
		lastLifecycle     uint64
		relatedSession    string
		relatedSequence   uint64
		relatedIDOverride string
		wantError         string
	}{
		{name: "future lifecycle cursor", lastLifecycle: 3, wantError: "future lifecycle"},
		{name: "missing lifecycle link", lastLifecycle: 2, relatedIDOverride: "missing:event", wantError: "does not exist"},
		{name: "cross session lifecycle link", lastLifecycle: 2, relatedSession: "other-match", relatedSequence: 1, wantError: "different session"},
		{name: "link beyond processed cursor", lastLifecycle: 1, relatedSequence: 2, wantError: "beyond decision lifecycle cursor"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := Open(ctx, filepath.Join(t.TempDir(), "links.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			session := decisionSession(now)
			session.LastEventSequence = 2
			other := session
			other.ID = "other-match"
			other.Fingerprint = "other-fingerprint"
			lifecycle := []domain.SemanticEvent{
				lifecycleEvent(session.ID, 1, now), lifecycleEvent(session.ID, 2, now.Add(time.Second)),
				lifecycleEvent(other.ID, 1, now),
			}
			if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{session, other}, lifecycle); err != nil {
				t.Fatal(err)
			}
			event := decisionEvent(session.ID, 1, domain.DecisionReturnToBaseOptions, now)
			relatedSession := test.relatedSession
			if relatedSession == "" {
				relatedSession = session.ID
			}
			relatedSequence := test.relatedSequence
			if relatedSequence == 0 {
				relatedSequence = 1
			}
			event.RelatedLifecycleEventID = relatedSession + ":lifecycle:" + string(rune('0'+relatedSequence))
			if test.relatedIDOverride != "" {
				event.RelatedLifecycleEventID = test.relatedIDOverride
			}
			checkpoint := decisionCheckpoint(session.ID, 1, now)
			checkpoint.LastLifecycleSequence = test.lastLifecycle
			err = store.SaveDecisionUpdates(ctx, checkpoint, []domain.DecisionEvent{event})
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("SaveDecisionUpdates() error = %v, want %q", err, test.wantError)
			}
			stored, listErr := store.ListDecisionEvents(ctx, session.ID, 0, 10)
			if listErr != nil || len(stored) != 0 {
				t.Fatalf("invalid link committed events: %#v, %v", stored, listErr)
			}
			if _, loadErr := store.LoadDecisionCheckpoint(ctx, session.ID); !errors.Is(loadErr, catalog.ErrNotFound) {
				t.Fatalf("invalid link committed checkpoint: %v", loadErr)
			}
		})
	}
}

func TestDecisionLifecycleLinkWithinProcessedSessionIsAccepted(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 19, 11, 45, 0, 0, time.UTC)
	store, err := Open(ctx, filepath.Join(t.TempDir(), "valid-link.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	session := decisionSession(now)
	session.LastEventSequence = 1
	lifecycle := lifecycleEvent(session.ID, 1, now)
	if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{session}, []domain.SemanticEvent{lifecycle}); err != nil {
		t.Fatal(err)
	}
	event := decisionEvent(session.ID, 1, domain.DecisionReturnToBaseOptions, now)
	event.RelatedLifecycleEventID = lifecycle.ID
	checkpoint := decisionCheckpoint(session.ID, 1, now)
	checkpoint.LastLifecycleSequence = 1
	if err := store.SaveDecisionUpdates(ctx, checkpoint, []domain.DecisionEvent{event}); err != nil {
		t.Fatalf("valid lifecycle link rejected: %v", err)
	}
	stored, err := store.ListDecisionEvents(ctx, session.ID, 0, 10)
	if err != nil || len(stored) != 1 || stored[0].RelatedLifecycleEventID != lifecycle.ID {
		t.Fatalf("stored linked decision = %#v, %v", stored, err)
	}
}

func TestDecisionEventsPersistAcrossRestartWithStablePagination(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "decision-events.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC)
	session := decisionSession(now)
	if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{session}, nil); err != nil {
		t.Fatal(err)
	}
	events := []domain.DecisionEvent{
		decisionEvent(session.ID, 1, domain.DecisionTargetChanged, now),
		decisionEvent(session.ID, 2, domain.DecisionComponentAffordable, now.Add(time.Second)),
	}
	checkpoint := DecisionCheckpoint{
		SessionID: session.ID, LastSequence: 2, LastLifecycleSequence: 4,
		State: json.RawMessage(`{"target_id":2504,"component_id":1057}`), UpdatedAt: now.Add(time.Second),
	}
	if err := store.SaveDecisionUpdates(ctx, checkpoint, events); err != nil {
		t.Fatalf("SaveDecisionUpdates() error = %v", err)
	}
	if err := store.SaveDecisionUpdates(ctx, checkpoint, events); err != nil {
		t.Fatalf("exact retry was not idempotent: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	version, err := reopened.SchemaVersion(ctx)
	if err != nil || version != schemaVersion {
		t.Fatalf("schema version = %d, %v, want %d", version, err, schemaVersion)
	}
	loaded, err := reopened.LoadDecisionCheckpoint(ctx, session.ID)
	if err != nil || loaded.LastSequence != 2 || loaded.LastLifecycleSequence != 4 || string(loaded.State) != string(checkpoint.State) {
		t.Fatalf("checkpoint = %#v, %v", loaded, err)
	}
	page, err := reopened.ListDecisionEvents(ctx, session.ID, 1, 1)
	if err != nil || len(page) != 1 || page[0].Sequence != 2 || page[0].Kind != domain.DecisionComponentAffordable {
		t.Fatalf("paged decisions = %#v, %v", page, err)
	}
}

func TestDecisionEventConflictRollsBackCheckpoint(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "decision-conflict.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 9, 19, 9, 30, 0, 0, time.UTC)
	session := decisionSession(now)
	if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{session}, nil); err != nil {
		t.Fatal(err)
	}
	event := decisionEvent(session.ID, 1, domain.DecisionTargetChanged, now)
	first := DecisionCheckpoint{SessionID: session.ID, LastSequence: 1, State: json.RawMessage(`{"target_id":4401}`), UpdatedAt: now}
	if err := store.SaveDecisionUpdates(ctx, first, []domain.DecisionEvent{event}); err != nil {
		t.Fatal(err)
	}

	conflict := event
	conflict.CurrentSummary = "Different stored meaning"
	advanced := DecisionCheckpoint{
		SessionID: session.ID, LastSequence: 2, LastLifecycleSequence: 3,
		State: json.RawMessage(`{"target_id":2504}`), UpdatedAt: now.Add(time.Second),
	}
	err = store.SaveDecisionUpdates(ctx, advanced, []domain.DecisionEvent{conflict})
	if err == nil || !strings.Contains(err.Error(), "conflicts with a different stored decision event") {
		t.Fatalf("conflicting retry error = %v", err)
	}
	loaded, loadErr := store.LoadDecisionCheckpoint(ctx, session.ID)
	if loadErr != nil || loaded.LastSequence != 1 || string(loaded.State) != string(first.State) {
		t.Fatalf("failed conflict advanced checkpoint = %#v, %v", loaded, loadErr)
	}
	stored, listErr := store.ListDecisionEvents(ctx, session.ID, 0, 10)
	if listErr != nil || len(stored) != 1 || stored[0].CurrentSummary != event.CurrentSummary {
		t.Fatalf("stored events after conflict = %#v, %v", stored, listErr)
	}
}

func decisionSession(now time.Time) domain.MatchSession {
	return domain.MatchSession{
		ID: "decision-match", Fingerprint: "fingerprint", State: domain.MatchActive,
		StartedAt: now, UpdatedAt: now, LastSeenAt: now, LastEventSequence: 4,
	}
}

func decisionCheckpoint(sessionID string, sequence uint64, now time.Time) DecisionCheckpoint {
	return DecisionCheckpoint{
		SessionID: sessionID, LastSequence: sequence,
		State: json.RawMessage(`{"target_id":4401}`), UpdatedAt: now,
	}
}

func lifecycleEvent(sessionID string, sequence uint64, now time.Time) domain.SemanticEvent {
	return domain.SemanticEvent{
		ID: sessionID + ":lifecycle:" + string(rune('0'+sequence)), SessionID: sessionID,
		Sequence: sequence, DedupeKey: sessionID + ":lifecycle-key:" + string(rune('0'+sequence)),
		Kind: domain.EventInventoryChanged, OccurredAt: now, ObservedAt: now,
		GameTimeSeconds: float64(sequence), Source: domain.EventSourceObserved, Confidence: 1,
		Payload: json.RawMessage(`{}`),
	}
}

func decisionEvent(sessionID string, sequence uint64, kind domain.DecisionKind, now time.Time) domain.DecisionEvent {
	return domain.DecisionEvent{
		ID: sessionID + ":decision:" + string(rune('0'+sequence)), SessionID: sessionID,
		Sequence: sequence, DedupeKey: "decision-key-" + string(rune('0'+sequence)), Kind: kind,
		Importance: domain.DecisionImportant, OccurredAt: now, GameTimeSeconds: float64(sequence * 10),
		PreviousSummary: "Opciones anteriores disponibles.", CurrentSummary: "Las opciones disponibles cambiaron.",
		ReasonCodes: []string{"semantic_change"},
	}
}
