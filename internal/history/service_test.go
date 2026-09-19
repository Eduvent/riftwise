package history

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"lolengine/internal/catalog"
	"lolengine/internal/domain"
	"lolengine/internal/storage"
)

func TestServiceRebuildsAcrossRestartAndLateDecisionCommit(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rebuild.db")
	store, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 19, 13, 0, 0, 0, time.UTC)
	endedAt := now.Add(20 * time.Minute)
	session := endedHistorySession("restart-match", now, endedAt)
	if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{session}, historyLifecycle(session, endedAt)); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(store, Config{Now: func() time.Time { return endedAt.Add(time.Second) }, BatchSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	built, err := service.RebuildPending(ctx)
	if err != nil || built != 1 {
		t.Fatalf("RebuildPending() = %d, %v", built, err)
	}
	first, err := service.Get(ctx, session.ID)
	if err != nil || first.Digest == "" {
		t.Fatalf("Get(first) = %+v, %v", first, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restarted, err := NewService(reopened, Config{Now: func() time.Time { return endedAt.Add(2 * time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	second, err := restarted.Get(ctx, session.ID)
	if err != nil || second.Digest != first.Digest {
		t.Fatalf("restart digest = %q, %v; want %q", second.Digest, err, first.Digest)
	}

	evidence := &domain.RecommendationEvidence{
		SchemaVersion: domain.RecommendationEvidenceSchemaVersion, Status: "ready",
		Target: &domain.EvidenceItem{ID: 4401, Name: "Force of Nature"}, Confidence: .8,
	}
	decision := domain.DecisionEvent{
		ID: session.ID + ":decision:000001", SessionID: session.ID, Sequence: 1,
		DedupeKey: "decision:1", Kind: domain.DecisionTargetChanged, Importance: domain.DecisionImportant,
		OccurredAt: endedAt, GameTimeSeconds: 1200, CurrentSummary: "Opción principal actualizada", Evidence: evidence,
	}
	checkpoint := storage.DecisionCheckpoint{
		SessionID: session.ID, LastSequence: 1, LastLifecycleSequence: 2,
		State: json.RawMessage(`{"target_id":4401}`), Evidence: evidence, UpdatedAt: endedAt.Add(3 * time.Second),
	}
	if err := reopened.SaveDecisionUpdates(ctx, checkpoint, []domain.DecisionEvent{decision}); err != nil {
		t.Fatal(err)
	}
	built, err = restarted.RebuildPending(ctx)
	if err != nil || built != 1 {
		t.Fatalf("late RebuildPending() = %d, %v", built, err)
	}
	third, err := restarted.Get(ctx, session.ID)
	if err != nil || third.Digest == first.Digest || len(third.DecisionChanges) != 1 {
		t.Fatalf("late rebuilt summary = %+v, %v", third, err)
	}
}

func TestServicePaginationExportAndPrivacyAreStable(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "page.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 9, 19, 14, 0, 0, 0, time.UTC)
	for _, id := range []string{"match-c", "match-b", "match-a"} {
		ended := now.Add(10 * time.Minute)
		session := endedHistorySession(id, now, ended)
		session.PlayerRiotID = "Secret#ID"
		if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{session}, historyLifecycle(session, ended)); err != nil {
			t.Fatal(err)
		}
	}
	service, _ := NewService(store, Config{Now: func() time.Time { return now.Add(time.Hour) }})
	first, err := service.List(ctx, "", 2)
	if err != nil || len(first.Matches) != 2 || first.NextCursor == "" {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	second, err := service.List(ctx, first.NextCursor, 2)
	if err != nil || len(second.Matches) != 1 || second.NextCursor != "" {
		t.Fatalf("second page = %+v, %v", second, err)
	}
	seen := map[string]bool{}
	for _, summary := range append(first.Matches, second.Matches...) {
		if seen[summary.MatchID] {
			t.Fatalf("duplicate match %q across keyset pages", summary.MatchID)
		}
		seen[summary.MatchID] = true
	}
	exported, err := service.Export(ctx, ExportScopeFull)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(exported)
	for _, forbidden := range []string{"Secret#ID", "private-fingerprint", "Opponent#"} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("privacy-safe export contains %q: %s", forbidden, payload)
		}
	}
	if _, err := service.List(ctx, "not-a-cursor", 10); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("invalid cursor error = %v", err)
	}
}

func TestListAndExportTraverseEveryAuthoritativeSessionBeyondRebuildBatches(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "complete-history.db")
	base := time.Date(2026, 9, 19, 14, 15, 0, 0, time.UTC)
	wantNewestFirst, err := prepareLegacyV4History(ctx, path, base, 7)
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(store, Config{Now: func() time.Time { return base.Add(24 * time.Hour) }, BatchSize: 2})
	if err != nil {
		t.Fatal(err)
	}

	// Export must not stop after the first bounded materialization batch.
	exported, err := service.Export(ctx, ExportScopeFull)
	if err != nil {
		t.Fatal(err)
	}
	if got := summaryIDs(exported.Matches); !reflect.DeepEqual(got, wantNewestFirst) {
		t.Fatalf("exported matches = %v; want %v", got, wantNewestFirst)
	}

	// Remove all derived rows to prove interactive keyset pages also traverse
	// authoritative sessions rather than only already-materialized summaries.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `DELETE FROM match_summaries`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service, _ = NewService(store, Config{Now: func() time.Time { return base.Add(25 * time.Hour) }, BatchSize: 2})
	var listed []string
	for cursor := ""; ; {
		page, err := service.List(ctx, cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		listed = append(listed, summaryIDs(page.Matches)...)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if !reflect.DeepEqual(listed, wantNewestFirst) {
		t.Fatalf("listed matches = %v; want %v", listed, wantNewestFirst)
	}

	// A completed export remains complete even when the user subsequently
	// confirms deletion of the corresponding local sources.
	deleted, err := service.DeleteAll(ctx, DeleteAllConfirmation)
	if err != nil || deleted != int64(len(wantNewestFirst)) {
		t.Fatalf("DeleteAll() = %d, %v", deleted, err)
	}
	if got := summaryIDs(exported.Matches); !reflect.DeepEqual(got, wantNewestFirst) {
		t.Fatalf("previous export changed after deletion: %v", got)
	}
}

func TestListUsesAuthoritativeSessionEnvelopeAndRepairsCorruptIndexes(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "envelope-corruption.db")
	store, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 19, 14, 45, 0, 0, time.UTC)
	olderEnd := base.Add(20 * time.Minute)
	newerEnd := base.Add(time.Hour + 20*time.Minute)
	older := endedHistorySession("ended-older", base, olderEnd)
	newer := endedHistorySession("ended-newer", base.Add(time.Hour), newerEnd)
	active := domain.MatchSession{
		ID: "active-corrupt", Fingerprint: "private-active", State: domain.MatchActive,
		StartedAt: base.Add(2 * time.Hour), UpdatedAt: base.Add(2 * time.Hour), LastSeenAt: base.Add(2 * time.Hour),
		ChampionName: "Garen", Position: "TOP",
	}
	if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{older, newer, active}, append(historyLifecycle(older, olderEnd), historyLifecycle(newer, newerEnd)...)); err != nil {
		t.Fatal(err)
	}
	service, _ := NewService(store, Config{Now: func() time.Time { return base.Add(3 * time.Hour) }, BatchSize: 10})
	for _, id := range []string{older.ID, newer.ID, active.ID} {
		if _, err := service.Get(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`UPDATE match_summaries SET completion = 'complete_terminal' WHERE match_id = 'active-corrupt'`,
		`UPDATE match_summaries SET completion = 'active' WHERE match_id = 'ended-newer'`,
		`UPDATE match_summaries SET started_at_ns = started_at_ns + 999999999999999 WHERE match_id = 'ended-older'`,
	}
	for _, statement := range statements {
		if _, err := raw.ExecContext(ctx, statement); err != nil {
			raw.Close()
			t.Fatal(err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, id := range []string{older.ID, newer.ID, active.ID} {
		if _, err := store.LoadMatchSummary(ctx, id); !errors.Is(err, storage.ErrSummaryCorrupt) {
			t.Fatalf("LoadMatchSummary(%q) error = %v; want corrupt envelope", id, err)
		}
	}
	stale, err := store.ListStaleMatchSummaryIDs(ctx, 10)
	if err != nil || !reflect.DeepEqual(stale, []string{older.ID, newer.ID}) {
		t.Fatalf("envelope-only stale rows = %v, %v", stale, err)
	}

	service, _ = NewService(store, Config{Now: func() time.Time { return base.Add(4 * time.Hour) }, BatchSize: 1})
	page, err := service.List(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := summaryIDs(page.Matches), []string{newer.ID, older.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("completed history order/content = %v; want %v", got, want)
	}
	if detail, err := service.Get(ctx, active.ID); err != nil || detail.Completion != domain.SummaryActive {
		t.Fatalf("active detail repair = %+v, %v", detail, err)
	}
}

func TestActiveSummaryIsAvailableByDetailButExcludedFromCompletedHistoryList(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "active-detail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 9, 19, 14, 30, 0, 0, time.UTC)
	session := domain.MatchSession{
		ID: "active", Fingerprint: "private-active", State: domain.MatchActive,
		StartedAt: now.Add(-10 * time.Minute), UpdatedAt: now, LastSeenAt: now, LastGameTime: 600,
		ChampionName: "Garen", Position: "TOP",
	}
	if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{session}, nil); err != nil {
		t.Fatal(err)
	}
	service, _ := NewService(store, Config{Now: func() time.Time { return now }})
	detail, err := service.Get(ctx, session.ID)
	if err != nil || detail.Completion != domain.SummaryActive {
		t.Fatalf("active detail = %+v, %v", detail, err)
	}
	page, err := service.List(ctx, "", 20)
	if err != nil || len(page.Matches) != 0 {
		t.Fatalf("completed history includes active summary = %+v, %v", page, err)
	}
}

func TestServiceProtectsSettlingDeletionAndAppliesRetention(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "retention.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 9, 19, 15, 0, 0, 0, time.UTC)
	oldEnd := now.Add(-60 * 24 * time.Hour)
	old := endedHistorySession("old", oldEnd.Add(-20*time.Minute), oldEnd)
	recentEnd := now.Add(-time.Hour)
	recent := endedHistorySession("recent", recentEnd.Add(-20*time.Minute), recentEnd)
	if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{old, recent}, append(historyLifecycle(old, oldEnd), historyLifecycle(recent, recentEnd)...)); err != nil {
		t.Fatal(err)
	}
	fence := &testFence{unsettled: map[string]bool{"old": true}}
	service, _ := NewService(store, Config{Now: func() time.Time { return now }, Fence: fence})
	if err := service.Delete(ctx, "old", "wrong"); !errors.Is(err, ErrConfirmationRequired) {
		t.Fatalf("single-delete confirmation error = %v", err)
	}
	if err := service.Delete(ctx, "old", DeleteOneConfirmation); !errors.Is(err, ErrHistorySettling) {
		t.Fatalf("settling delete error = %v", err)
	}
	days := 30
	if _, err := service.SetPreferences(ctx, domain.HistoryPreferences{RetentionDays: &days}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadMatchSummarySource(ctx, "old"); err != nil {
		t.Fatalf("unsettled retention deleted match: %v", err)
	}
	fence.unsettled["old"] = false
	deleted, err := service.ApplyRetention(ctx)
	if err != nil || deleted != 1 {
		t.Fatalf("ApplyRetention() = %d, %v", deleted, err)
	}
	if _, err := store.LoadMatchSummarySource(ctx, "old"); err == nil {
		t.Fatal("settled old match was not deleted")
	}
	if _, err := store.LoadMatchSummarySource(ctx, "recent"); err != nil {
		t.Fatalf("recent match deleted: %v", err)
	}
	if _, err := service.DeleteAll(ctx, "wrong"); !errors.Is(err, ErrConfirmationRequired) {
		t.Fatalf("delete-all confirmation error = %v", err)
	}
	if _, err := service.Export(ctx, "raw"); !errors.Is(err, ErrInvalidExportScope) {
		t.Fatalf("invalid export scope error = %v", err)
	}
}

func TestCommittedDeletionIsNotReportedAsFailedWhenWALCheckpointIsBusy(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "checkpoint-busy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 9, 19, 16, 0, 0, 0, time.UTC)
	ended := endedHistorySession("delete-me", now.Add(-time.Hour), now)
	if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{ended}, historyLifecycle(ended, now)); err != nil {
		t.Fatal(err)
	}
	var reported error
	service, err := NewService(checkpointFailingRepository{DB: store}, Config{OnError: func(err error) { reported = err }})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(ctx, ended.ID, DeleteOneConfirmation); err != nil {
		t.Fatalf("Delete() reported committed deletion as failed: %v", err)
	}
	if reported == nil || !strings.Contains(reported.Error(), "busy") {
		t.Fatalf("checkpoint maintenance error was not reported: %v", reported)
	}
	if _, err := store.LoadMatchSummarySource(ctx, ended.ID); err == nil {
		t.Fatal("committed deletion did not remove source")
	}
}

func TestDeleteAllDeletesOnlyTheSettledCompletedSnapshot(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "delete-snapshot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	base := time.Date(2026, 9, 19, 16, 15, 0, 0, time.UTC)
	firstEnd := base.Add(20 * time.Minute)
	first := endedHistorySession("already-ended", base, firstEnd)
	pending := domain.MatchSession{
		ID: "ends-during-delete", Fingerprint: "private-pending", State: domain.MatchActive,
		StartedAt: base.Add(time.Hour), UpdatedAt: base.Add(time.Hour), LastSeenAt: base.Add(time.Hour),
		ChampionName: "Garen", Position: "TOP",
	}
	if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{first, pending}, historyLifecycle(first, firstEnd)); err != nil {
		t.Fatal(err)
	}
	repository := &endingDuringDeleteRepository{DB: store, pending: pending, endedAt: base.Add(80 * time.Minute)}
	fence := &recordingFence{}
	service, err := NewService(repository, Config{Fence: fence})
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := service.DeleteAll(ctx, DeleteAllConfirmation)
	if err != nil || deleted != 1 {
		t.Fatalf("DeleteAll() = %d, %v", deleted, err)
	}
	if !reflect.DeepEqual(repository.selected, []string{first.ID}) {
		t.Fatalf("exact delete set = %v; want [%s]", repository.selected, first.ID)
	}
	if !reflect.DeepEqual(fence.checked, []string{first.ID}) {
		t.Fatalf("settlement checks = %v; want [%s]", fence.checked, first.ID)
	}
	if _, err := store.LoadMatchSummarySource(ctx, first.ID); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatalf("initially completed match still exists: %v", err)
	}
	source, err := store.LoadMatchSummarySource(ctx, pending.ID)
	if err != nil || source.Session.State != domain.MatchEnded {
		t.Fatalf("newly ended match was broadly deleted: %+v, %v", source.Session, err)
	}
}

func TestPendingWALCheckpointRetriesOnMaintenanceUntilSuccess(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "checkpoint-retry.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 9, 19, 16, 30, 0, 0, time.UTC)
	ended := endedHistorySession("delete-and-retry", now.Add(-time.Hour), now)
	if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{ended}, historyLifecycle(ended, now)); err != nil {
		t.Fatal(err)
	}
	repository := &checkpointSequenceRepository{DB: store, failures: 1}
	var reported []error
	service, err := NewService(repository, Config{OnError: func(err error) { reported = append(reported, err) }})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(ctx, ended.ID, DeleteOneConfirmation); err != nil {
		t.Fatalf("Delete() reported committed deletion as failed: %v", err)
	}
	if repository.calls != 1 || len(reported) != 1 || !strings.Contains(reported[0].Error(), "busy") {
		t.Fatalf("initial checkpoint calls=%d reported=%v", repository.calls, reported)
	}
	service.maintain(ctx)
	if repository.calls != 2 {
		t.Fatalf("maintenance did not retry pending checkpoint: calls=%d", repository.calls)
	}
	service.maintain(ctx)
	if repository.calls != 2 {
		t.Fatalf("successful checkpoint was not cleared: calls=%d", repository.calls)
	}
}

func TestPartialRetentionFailureStillSchedulesCheckpointAndPreservesResult(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "partial-retention-checkpoint.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 9, 19, 16, 40, 0, 0, time.UTC)
	firstEnd := now.Add(-72 * time.Hour)
	first := endedHistorySession("retention-first", firstEnd.Add(-20*time.Minute), firstEnd)
	secondEnd := now.Add(-48 * time.Hour)
	second := endedHistorySession("retention-second", secondEnd.Add(-20*time.Minute), secondEnd)
	if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{first, second}, append(historyLifecycle(first, firstEnd), historyLifecycle(second, secondEnd)...)); err != nil {
		t.Fatal(err)
	}
	days := 1
	if err := store.SaveHistoryPreferences(ctx, domain.HistoryPreferences{RetentionDays: &days, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("injected second retention deletion failure")
	repository := &partialRetentionRepository{DB: store, failID: second.ID, failure: failure}
	var reported []error
	service, err := NewService(repository, Config{
		Now: func() time.Time { return now }, OnError: func(err error) { reported = append(reported, err) },
	})
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := service.ApplyRetention(ctx)
	if deleted != 1 || !errors.Is(err, failure) {
		t.Fatalf("ApplyRetention() = %d, %v; want 1 and injected error", deleted, err)
	}
	if repository.checkpointCalls != 1 || len(reported) != 1 || !strings.Contains(reported[0].Error(), "busy") {
		t.Fatalf("committed partial deletion checkpoint calls=%d reported=%v", repository.checkpointCalls, reported)
	}
	if _, err := store.LoadMatchSummarySource(ctx, first.ID); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatalf("first retention deletion did not commit: %v", err)
	}
	if _, err := store.LoadMatchSummarySource(ctx, second.ID); err != nil {
		t.Fatalf("failed second candidate was removed: %v", err)
	}

	// A new service's startup maintenance retries the checkpoint left busy by
	// the prior service, even though retention continues to return its own error.
	restarted, err := NewService(repository, Config{
		Now: func() time.Time { return now }, OnError: func(err error) { reported = append(reported, err) },
	})
	if err != nil {
		t.Fatal(err)
	}
	restarted.maintain(ctx)
	if repository.checkpointCalls != 2 {
		t.Fatalf("restart did not retry partial-delete checkpoint: calls=%d", repository.checkpointCalls)
	}
	restarted.maintain(ctx)
	if repository.checkpointCalls != 2 {
		t.Fatalf("successful checkpoint was scheduled twice: calls=%d", repository.checkpointCalls)
	}
}

func TestStartupMaintenanceRetriesCheckpointLeftByPriorService(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "checkpoint-restart.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 9, 19, 16, 45, 0, 0, time.UTC)
	ended := endedHistorySession("delete-before-restart", now.Add(-time.Hour), now)
	if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{ended}, historyLifecycle(ended, now)); err != nil {
		t.Fatal(err)
	}
	first, err := NewService(checkpointFailingRepository{DB: store}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Delete(ctx, ended.ID, DeleteOneConfirmation); err != nil {
		t.Fatal(err)
	}

	restartedRepository := &checkpointSequenceRepository{DB: store}
	restarted, err := NewService(restartedRepository, Config{})
	if err != nil {
		t.Fatal(err)
	}
	restarted.maintain(ctx)
	if restartedRepository.calls != 1 {
		t.Fatalf("startup maintenance checkpoint calls = %d; want 1", restartedRepository.calls)
	}
	restarted.maintain(ctx)
	if restartedRepository.calls != 1 {
		t.Fatalf("successful startup checkpoint was not cleared: calls=%d", restartedRepository.calls)
	}
}

func TestListRepairsCorruptSummaryBeyondPeriodicBatch(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "corrupt-page.db")
	store, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 19, 17, 0, 0, 0, time.UTC)
	for index, id := range []string{"older", "newer"} {
		endedAt := base.Add(time.Duration(index) * time.Hour)
		session := endedHistorySession(id, endedAt.Add(-20*time.Minute), endedAt)
		if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{session}, historyLifecycle(session, endedAt)); err != nil {
			t.Fatal(err)
		}
	}
	service, _ := NewService(store, Config{Now: func() time.Time { return base.Add(3 * time.Hour) }, BatchSize: 10})
	if built, err := service.RebuildPending(ctx); err != nil || built != 2 {
		t.Fatalf("initial build = %d, %v", built, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `UPDATE match_summaries SET payload = 'not-json'`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restarted, _ := NewService(reopened, Config{Now: func() time.Time { return base.Add(4 * time.Hour) }, BatchSize: 1})
	page, err := restarted.List(ctx, "", 10)
	if err != nil || len(page.Matches) != 2 {
		t.Fatalf("List() after corruption = %+v, %v", page, err)
	}
	for _, summary := range page.Matches {
		if !VerifyDigest(summary) {
			t.Fatalf("summary %q was not rebuilt: %+v", summary.MatchID, summary)
		}
	}
}

func TestRebuildRetriesWhenAuthoritativeSourceChangesBeforeMaterialization(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "optimistic-retry.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	started := time.Date(2026, 9, 19, 18, 0, 0, 0, time.UTC)
	endedAt := started.Add(20 * time.Minute)
	session := endedHistorySession("source-race", started, endedAt)
	if err := store.SaveMatchUpdates(ctx, []domain.MatchSession{session}, historyLifecycle(session, endedAt)); err != nil {
		t.Fatal(err)
	}
	tracing := &sourceChangingRepository{DB: store}
	tracing.change = func(ctx context.Context) error {
		updated := session
		updated.UpdatedAt = endedAt.Add(time.Second)
		updated.PlayerCheckpoint = cloneHistoryCheckpoint(session.PlayerCheckpoint)
		updated.PlayerCheckpoint.ObservedAt = endedAt.Add(time.Second)
		updated.PlayerCheckpoint.Kills = 9
		return store.SaveMatchUpdates(ctx, []domain.MatchSession{updated}, nil)
	}
	service, err := NewService(tracing, Config{Now: func() time.Time { return endedAt.Add(2 * time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	summary, err := service.Get(ctx, session.ID)
	if err != nil || !tracing.changed || summary.Stats == nil || summary.Stats.Kills != 9 {
		t.Fatalf("optimistic rebuild = %+v, changed=%v, err=%v", summary, tracing.changed, err)
	}
}

type sourceChangingRepository struct {
	*storage.DB
	changed bool
	change  func(context.Context) error
}

func (repository *sourceChangingRepository) SaveMatchSummaryIfCurrent(ctx context.Context, summary domain.MatchSummary, revision domain.SummarySourceRevision) error {
	if !repository.changed {
		repository.changed = true
		if err := repository.change(ctx); err != nil {
			return err
		}
		return storage.ErrSummarySourceChanged
	}
	return repository.DB.SaveMatchSummaryIfCurrent(ctx, summary, revision)
}

func cloneHistoryCheckpoint(value *domain.MatchPlayerCheckpoint) *domain.MatchPlayerCheckpoint {
	if value == nil {
		return nil
	}
	clone := *value
	clone.Inventory = append([]domain.InventoryCount(nil), value.Inventory...)
	return &clone
}

type checkpointFailingRepository struct{ *storage.DB }

func (checkpointFailingRepository) CheckpointHistoryWAL(context.Context) error {
	return errors.New("database remained busy")
}

type checkpointSequenceRepository struct {
	*storage.DB
	failures int
	calls    int
}

type partialRetentionRepository struct {
	*storage.DB
	failID          string
	failure         error
	checkpointCalls int
}

func (repository *partialRetentionRepository) DeleteMatchHistory(ctx context.Context, matchID string) error {
	if matchID == repository.failID {
		return repository.failure
	}
	return repository.DB.DeleteMatchHistory(ctx, matchID)
}

func (repository *partialRetentionRepository) CheckpointHistoryWAL(context.Context) error {
	repository.checkpointCalls++
	if repository.checkpointCalls == 1 {
		return errors.New("database remained busy")
	}
	return nil
}

func (repository *checkpointSequenceRepository) CheckpointHistoryWAL(context.Context) error {
	repository.calls++
	if repository.calls <= repository.failures {
		return errors.New("database remained busy")
	}
	return nil
}

type endingDuringDeleteRepository struct {
	*storage.DB
	pending  domain.MatchSession
	endedAt  time.Time
	selected []string
}

func (repository *endingDuringDeleteRepository) DeleteCompletedMatchHistory(ctx context.Context, ids []string) (int64, error) {
	repository.selected = append([]string(nil), ids...)
	ended := repository.pending
	ended.State = domain.MatchEnded
	ended.EndedAt = &repository.endedAt
	ended.UpdatedAt = repository.endedAt
	ended.LastSeenAt = repository.endedAt
	ended.LastGameTime = repository.endedAt.Sub(ended.StartedAt).Seconds()
	if err := repository.DB.SaveMatchUpdates(ctx, []domain.MatchSession{ended}, nil); err != nil {
		return 0, err
	}
	return repository.DB.DeleteCompletedMatchHistory(ctx, ids)
}

type recordingFence struct{ checked []string }

func (fence *recordingFence) HistorySessionSettled(id string) bool {
	fence.checked = append(fence.checked, id)
	return true
}

type testFence struct{ unsettled map[string]bool }

func (fence *testFence) HistorySessionSettled(id string) bool { return !fence.unsettled[id] }

func endedHistorySession(id string, started, ended time.Time) domain.MatchSession {
	return domain.MatchSession{
		ID: id, Fingerprint: "private-fingerprint", State: domain.MatchEnded,
		StartedAt: started, UpdatedAt: ended, LastSeenAt: ended, EndedAt: &ended,
		EndReason: "terminal_event", EndConfidence: 1, LastGameTime: ended.Sub(started).Seconds(),
		ChampionName: "Garen", Position: "TOP", LastEventSequence: 2,
		PlayerCheckpoint: &domain.MatchPlayerCheckpoint{
			MatchID: id, ObservedAt: ended, GameTimeSeconds: ended.Sub(started).Seconds(),
			ChampionName: "Garen", Role: domain.RoleTop, RoleKnown: true,
			Level: 12, Kills: 4, Deaths: 2, Assists: 3, CreepScore: 150,
			Inventory: []domain.InventoryCount{{ItemID: 1001, Count: 1}},
		},
	}
}

func historyLifecycle(session domain.MatchSession, ended time.Time) []domain.SemanticEvent {
	return []domain.SemanticEvent{
		{ID: session.ID + ":000001", SessionID: session.ID, Sequence: 1, DedupeKey: session.ID + ":start", Kind: domain.EventMatchStarted, OccurredAt: session.StartedAt, ObservedAt: session.StartedAt, Source: domain.EventSourceObserved, Confidence: 1, Payload: json.RawMessage(`{}`)},
		{ID: session.ID + ":000002", SessionID: session.ID, Sequence: 2, DedupeKey: session.ID + ":end", Kind: domain.EventMatchEnded, OccurredAt: ended, ObservedAt: ended, GameTimeSeconds: session.LastGameTime, Source: domain.EventSourceObserved, Confidence: 1, Payload: json.RawMessage(`{"reason":"terminal_event"}`)},
	}
}

func summaryIDs(summaries []domain.MatchSummary) []string {
	ids := make([]string, len(summaries))
	for index := range summaries {
		ids[index] = summaries[index].MatchID
	}
	return ids
}

func prepareLegacyV4History(ctx context.Context, path string, base time.Time, count int) ([]string, error) {
	database, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	defer database.Close()
	statements := []string{
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`,
		`INSERT INTO schema_migrations(version, applied_at) VALUES(1,'a'),(2,'b'),(3,'c'),(4,'d')`,
		`CREATE TABLE metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE catalogs (patch TEXT NOT NULL, locale TEXT NOT NULL, payload BLOB NOT NULL, synced_at_ns INTEGER NOT NULL, complete INTEGER NOT NULL, PRIMARY KEY(patch, locale))`,
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
		if _, err := database.ExecContext(ctx, statement); err != nil {
			return nil, fmt.Errorf("prepare legacy v4 history: %w", err)
		}
	}
	wantNewestFirst := make([]string, 0, count)
	for index := 0; index < count; index++ {
		started := base.Add(time.Duration(index) * time.Hour)
		endedAt := started.Add(20 * time.Minute)
		id := fmt.Sprintf("legacy-source-%02d", index)
		if _, err := database.ExecContext(ctx, `
			INSERT INTO match_sessions(
				id, fingerprint, state, started_at_ns, updated_at_ns, last_seen_at_ns, ended_at_ns,
				end_reason, end_confidence, last_game_time, champion_name, position
			) VALUES(?, ?, 'ended', ?, ?, ?, ?, 'legacy_boundary', 0.5, 1200, 'Garen', 'TOP')`,
			id, "private-"+id, started.UnixNano(), endedAt.UnixNano(), endedAt.UnixNano(), endedAt.UnixNano(),
		); err != nil {
			return nil, fmt.Errorf("insert legacy v4 history %s: %w", id, err)
		}
		wantNewestFirst = append([]string{id}, wantNewestFirst...)
	}
	return wantNewestFirst, nil
}
