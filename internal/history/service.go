package history

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"lolengine/internal/catalog"
	"lolengine/internal/domain"
	"lolengine/internal/storage"
)

const (
	DeleteOneConfirmation = "DELETE_LOCAL_MATCH_HISTORY"
	DeleteAllConfirmation = "DELETE_LOCAL_HISTORY"
	ExportScopeSummaries  = "summaries"
	ExportScopeFull       = "full"
)

var (
	ErrHistorySettling      = errors.New("match history is still settling")
	ErrConfirmationRequired = errors.New("explicit history deletion confirmation is required")
	ErrInvalidCursor        = errors.New("invalid history cursor")
	ErrInvalidExportScope   = errors.New("history export scope must be summaries or full")
	ErrInvalidPreferences   = errors.New("invalid history retention preferences")
)

type Repository interface {
	LoadMatchSummarySource(context.Context, string) (domain.MatchSummarySource, error)
	SaveMatchSummaryIfCurrent(context.Context, domain.MatchSummary, domain.SummarySourceRevision) error
	LoadMatchSummary(context.Context, string) (domain.MatchSummary, error)
	ListMatchSummaries(context.Context, *domain.HistoryCursor, int) ([]domain.MatchSummary, *domain.HistoryCursor, error)
	ListStaleMatchSummaryIDs(context.Context, int) ([]string, error)
	LoadHistoryPreferences(context.Context) (domain.HistoryPreferences, error)
	SaveHistoryPreferences(context.Context, domain.HistoryPreferences) error
	ListRetentionCandidates(context.Context, domain.HistoryPreferences, time.Time, int) ([]string, error)
	DeleteMatchHistory(context.Context, string) error
	DeleteCompletedMatchHistory(context.Context, []string) (int64, error)
	ListCompletedMatchIDs(context.Context) ([]string, error)
	CheckpointHistoryWAL(context.Context) error
}

// SettlementFence prevents deletion while the recommendation coordinator owns
// an uncertain persistence batch for the session.
type SettlementFence interface {
	HistorySessionSettled(string) bool
}

type Config struct {
	Now             func() time.Time
	BatchSize       int
	RefreshInterval time.Duration
	Fence           SettlementFence
	OnError         func(error)
}

type Service struct {
	repository      Repository
	now             func() time.Time
	batchSize       int
	refreshInterval time.Duration
	fence           SettlementFence
	onError         func(error)
	checkpointMu    sync.Mutex
	checkpointState checkpointRetryState
}

type checkpointRetryState struct {
	generation uint64
	running    bool
}

func NewService(repository Repository, cfg Config) (*Service, error) {
	if repository == nil {
		return nil, errors.New("history repository is required")
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 25
	}
	if cfg.BatchSize > 100 {
		cfg.BatchSize = 100
	}
	if cfg.RefreshInterval <= 0 {
		cfg.RefreshInterval = time.Minute
	}
	return &Service{
		repository: repository, now: cfg.Now, batchSize: cfg.BatchSize,
		refreshInterval: cfg.RefreshInterval, fence: cfg.Fence, onError: cfg.OnError,
		// A startup sweep safely covers a busy checkpoint left by a prior
		// process without writing another durable privacy marker into the WAL.
		checkpointState: checkpointRetryState{generation: 1},
	}, nil
}

// Run performs bounded startup recovery and periodic stale/retention work.
func (service *Service) Run(ctx context.Context) {
	service.maintain(ctx)
	ticker := time.NewTicker(service.refreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			service.maintain(ctx)
		}
	}
}

func (service *Service) maintain(ctx context.Context) {
	if _, err := service.RebuildPending(ctx); err != nil {
		service.report(err)
	}
	if _, err := service.ApplyRetention(ctx); err != nil {
		service.report(err)
	}
	service.retryPendingCheckpoint(ctx)
}

func (service *Service) RebuildPending(ctx context.Context) (int, error) {
	ids, err := service.repository.ListStaleMatchSummaryIDs(ctx, service.batchSize)
	if err != nil {
		return 0, err
	}
	built := 0
	for _, id := range ids {
		if _, err := service.rebuild(ctx, id, 2); err != nil {
			return built, err
		}
		built++
	}
	return built, nil
}

func (service *Service) Get(ctx context.Context, matchID string) (domain.MatchSummary, error) {
	matchID = strings.TrimSpace(matchID)
	if matchID == "" {
		return domain.MatchSummary{}, errors.New("match id is required")
	}
	source, err := service.repository.LoadMatchSummarySource(ctx, matchID)
	if err != nil {
		return domain.MatchSummary{}, err
	}
	stored, loadErr := service.repository.LoadMatchSummary(ctx, matchID)
	if loadErr == nil && stored.SchemaVersion == domain.MatchSummarySchemaVersion &&
		stored.SourceRevision == source.Revision && VerifyDigest(stored) {
		return stored, nil
	}
	if loadErr != nil && !errors.Is(loadErr, catalog.ErrNotFound) && !errors.Is(loadErr, storage.ErrSummaryCorrupt) {
		return domain.MatchSummary{}, loadErr
	}
	return service.rebuildFromSource(ctx, source, 2)
}

func (service *Service) rebuild(ctx context.Context, matchID string, attempts int) (domain.MatchSummary, error) {
	source, err := service.repository.LoadMatchSummarySource(ctx, matchID)
	if err != nil {
		return domain.MatchSummary{}, err
	}
	return service.rebuildFromSource(ctx, source, attempts)
}

func (service *Service) rebuildFromSource(ctx context.Context, source domain.MatchSummarySource, attempts int) (domain.MatchSummary, error) {
	if attempts < 1 {
		attempts = 1
	}
	for attempt := 0; attempt < attempts; attempt++ {
		summary, err := Build(source)
		if err != nil {
			return domain.MatchSummary{}, err
		}
		summary.BuiltAt = service.now().UTC()
		if err := service.repository.SaveMatchSummaryIfCurrent(ctx, summary, source.Revision); err == nil {
			return summary, nil
		} else if !errors.Is(err, storage.ErrSummarySourceChanged) {
			return domain.MatchSummary{}, err
		}
		source, err = service.repository.LoadMatchSummarySource(ctx, source.Session.ID)
		if err != nil {
			return domain.MatchSummary{}, err
		}
	}
	return domain.MatchSummary{}, storage.ErrSummarySourceChanged
}

func (service *Service) List(ctx context.Context, cursor string, limit int) (domain.HistoryPage, error) {
	before, err := DecodeCursor(cursor)
	if err != nil {
		return domain.HistoryPage{}, err
	}
	matches, next, err := service.repository.ListMatchSummaries(ctx, before, limit)
	if err != nil {
		return domain.HistoryPage{}, err
	}
	for index := range matches {
		// Materialized summaries are a cache. Always reconcile every returned
		// authoritative match ID so a validly encoded but stale row cannot escape
		// a bounded background rebuild batch.
		matches[index], err = service.Get(ctx, matches[index].MatchID)
		if err != nil {
			return domain.HistoryPage{}, err
		}
	}
	page := domain.HistoryPage{Matches: matches}
	if next != nil {
		page.NextCursor, err = EncodeCursor(*next)
	}
	return page, err
}

func EncodeCursor(cursor domain.HistoryCursor) (string, error) {
	if cursor.StartedAtNS <= 0 || strings.TrimSpace(cursor.MatchID) == "" {
		return "", ErrInvalidCursor
	}
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func DecodeCursor(value string) (*domain.HistoryCursor, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	if len(value) > 512 {
		return nil, ErrInvalidCursor
	}
	payload, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, ErrInvalidCursor
	}
	var cursor domain.HistoryCursor
	if err := json.Unmarshal(payload, &cursor); err != nil || cursor.StartedAtNS <= 0 || strings.TrimSpace(cursor.MatchID) == "" {
		return nil, ErrInvalidCursor
	}
	return &cursor, nil
}

func (service *Service) Export(ctx context.Context, scope string) (domain.HistoryExport, error) {
	if scope == "" {
		scope = ExportScopeSummaries
	}
	if scope != ExportScopeSummaries && scope != ExportScopeFull {
		return domain.HistoryExport{}, ErrInvalidExportScope
	}
	result := domain.HistoryExport{
		SchemaVersion: 1, ExportedAt: service.now().UTC(), Scope: scope,
		Matches: make([]domain.MatchSummary, 0),
	}
	// Snapshot the authoritative completed IDs before traversing derived rows.
	// This makes export independent of the materialization batch size and gives
	// concurrent next-match boundaries an explicit inclusion point.
	ids, err := service.repository.ListCompletedMatchIDs(ctx)
	if err != nil {
		return domain.HistoryExport{}, err
	}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return domain.HistoryExport{}, err
		}
		summary, err := service.Get(ctx, id)
		if err != nil {
			return domain.HistoryExport{}, err
		}
		if scope == ExportScopeSummaries {
			summary.InventoryChanges = nil
			summary.InferredPurchases = nil
			summary.InferredBaseReturns = nil
			summary.DecisionChanges = nil
		}
		result.Matches = append(result.Matches, summary)
	}
	return result, nil
}

func (service *Service) Preferences(ctx context.Context) (domain.HistoryPreferences, error) {
	return service.repository.LoadHistoryPreferences(ctx)
}

func (service *Service) SetPreferences(ctx context.Context, preferences domain.HistoryPreferences) (domain.HistoryPreferences, error) {
	if preferences.RetentionDays != nil && (*preferences.RetentionDays < 1 || *preferences.RetentionDays > 3650) {
		return domain.HistoryPreferences{}, fmt.Errorf("%w: retention days must be between 1 and 3650", ErrInvalidPreferences)
	}
	if preferences.MaxCompletedMatches != nil && (*preferences.MaxCompletedMatches < 1 || *preferences.MaxCompletedMatches > 10000) {
		return domain.HistoryPreferences{}, fmt.Errorf("%w: maximum completed matches must be between 1 and 10000", ErrInvalidPreferences)
	}
	preferences.UpdatedAt = service.now().UTC()
	if err := service.repository.SaveHistoryPreferences(ctx, preferences); err != nil {
		return domain.HistoryPreferences{}, err
	}
	if _, err := service.ApplyRetention(ctx); err != nil {
		return domain.HistoryPreferences{}, err
	}
	return service.repository.LoadHistoryPreferences(ctx)
}

func (service *Service) Delete(ctx context.Context, matchID, confirmation string) error {
	if confirmation != DeleteOneConfirmation {
		return ErrConfirmationRequired
	}
	if !service.settled(matchID) {
		return ErrHistorySettling
	}
	if err := service.repository.DeleteMatchHistory(ctx, matchID); err != nil {
		return err
	}
	service.scheduleCheckpoint(ctx)
	return nil
}

func (service *Service) DeleteAll(ctx context.Context, confirmation string) (int64, error) {
	if confirmation != DeleteAllConfirmation {
		return 0, ErrConfirmationRequired
	}
	ids, err := service.repository.ListCompletedMatchIDs(ctx)
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if !service.settled(id) {
			return 0, fmt.Errorf("%w: %s", ErrHistorySettling, id)
		}
	}
	count, err := service.repository.DeleteCompletedMatchHistory(ctx, ids)
	if err != nil {
		return 0, err
	}
	if count > 0 {
		service.scheduleCheckpoint(ctx)
	}
	return count, nil
}

func (service *Service) ApplyRetention(ctx context.Context) (deleted int, err error) {
	// Each candidate deletion commits independently. Schedule privacy
	// maintenance exactly once whenever this invocation committed at least one,
	// even if a later candidate fails and the partial result is returned.
	defer func() {
		if deleted > 0 {
			service.scheduleCheckpoint(ctx)
		}
	}()
	preferences, err := service.repository.LoadHistoryPreferences(ctx)
	if err != nil {
		return 0, err
	}
	if preferences.RetentionDays == nil && preferences.MaxCompletedMatches == nil {
		return 0, nil
	}
	ids, err := service.repository.ListRetentionCandidates(ctx, preferences, service.now(), service.batchSize)
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if !service.settled(id) {
			continue
		}
		if err := service.repository.DeleteMatchHistory(ctx, id); err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}

func (service *Service) settled(matchID string) bool {
	return service.fence == nil || service.fence.HistorySessionSettled(strings.TrimSpace(matchID))
}

func (service *Service) report(err error) {
	if err != nil && service.onError != nil {
		service.onError(err)
	}
}

// scheduleCheckpoint records privacy maintenance after an authoritative
// deletion has committed. The generation prevents a successful older attempt
// from clearing a newer deletion that arrived while the checkpoint was busy.
func (service *Service) scheduleCheckpoint(ctx context.Context) {
	service.checkpointMu.Lock()
	service.checkpointState.generation++
	service.checkpointMu.Unlock()
	service.retryPendingCheckpoint(ctx)
}

// retryPendingCheckpoint is best effort: a busy WAL checkpoint never turns a
// committed deletion into an ambiguous API failure. Pending work remains
// retained in service state and every maintenance cycle retries it.
func (service *Service) retryPendingCheckpoint(ctx context.Context) {
	service.checkpointMu.Lock()
	if service.checkpointState.generation == 0 || service.checkpointState.running {
		service.checkpointMu.Unlock()
		return
	}
	generation := service.checkpointState.generation
	service.checkpointState.running = true
	service.checkpointMu.Unlock()

	err := service.repository.CheckpointHistoryWAL(ctx)

	service.checkpointMu.Lock()
	service.checkpointState.running = false
	if err == nil && service.checkpointState.generation == generation {
		service.checkpointState.generation = 0
	}
	service.checkpointMu.Unlock()
	service.report(err)
}
