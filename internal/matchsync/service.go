// Package matchsync coordinates explicit, bounded, restart-safe Match-V5
// synchronization. Network responses are normalized by riotapi before this
// package sees them, and every durable batch is committed by one repository
// transaction.
package matchsync

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"lolengine/internal/domain"
	"lolengine/internal/riotapi"
	"lolengine/internal/storage"
)

var (
	ErrDisabled        = errors.New("Riot enrichment is disabled")
	ErrConsentRequired = errors.New("explicit Riot account consent is required")
	ErrInvalidRequest  = errors.New("invalid Riot synchronization request")
	ErrAccountSettling = errors.New("Riot account synchronization is settling")
)

const DeleteConfirmation = "DELETE_RIOT_ACCOUNT_DATA"

const reconciliationErrorStorage = "storage"

type Repository interface {
	UpsertRiotAccount(context.Context, storage.RiotAccount) error
	LoadRiotAccount(context.Context, string) (storage.RiotAccount, error)
	ListRiotAccounts(context.Context) ([]storage.RiotAccount, error)
	DeleteRiotAccount(context.Context, string) error
	LoadRiotSyncCheckpoint(context.Context, string, string) (storage.RiotSyncCheckpoint, error)
	SaveRiotSyncBatch(context.Context, string, []riotapi.Match, []storage.RiotPendingFetch, storage.RiotSyncCheckpoint) error
	ListRiotPendingFetches(context.Context, string, int) ([]storage.RiotPendingFetch, error)
	ListRiotSyncCheckpoints(context.Context) ([]storage.RiotSyncCheckpoint, error)
	LoadRiotReconciliationState(context.Context, string) (storage.RiotReconciliationState, error)
	ListRiotReconciliationStates(context.Context) ([]storage.RiotReconciliationState, error)
	SaveRiotReconciliationState(context.Context, storage.RiotReconciliationState) error
	ListRiotMatches(context.Context, string, storage.RiotMatchFilter) ([]storage.RiotStoredMatch, error)
	SaveSessionMatchLink(context.Context, storage.SessionMatchLink) error
	RiotMatchKey(string, string) (string, error)
	ListMatches(context.Context, int, int) ([]domain.MatchSession, error)
}

type Config struct {
	Mode         riotapi.Mode
	Region       riotapi.Region
	PageSize     int
	MaxMatches   int
	Concurrency  int
	Lookback     time.Duration
	Overlap      time.Duration
	PendingRetry time.Duration
	AutoSync     bool
	Now          func() time.Time
	NewID        func() string
}

type LinkRequest struct {
	GameName string           `json:"game_name"`
	TagLine  string           `json:"tag_line"`
	Platform riotapi.Platform `json:"platform"`
	Consent  bool             `json:"consent"`
	AutoSync bool             `json:"auto_sync"`
}

// LinkedAccount is safe for APIs: it deliberately excludes PUUID and Riot ID.
type LinkedAccount struct {
	ID        string           `json:"id"`
	Region    riotapi.Region   `json:"region"`
	Platform  riotapi.Platform `json:"platform"`
	LinkedAt  time.Time        `json:"linked_at"`
	UpdatedAt time.Time        `json:"updated_at"`
	AutoSync  bool             `json:"auto_sync"`
}

type Status struct {
	Mode            riotapi.Mode                      `json:"mode"`
	Enabled         bool                              `json:"enabled"`
	Configured      bool                              `json:"configured"`
	KeyRequired     bool                              `json:"key_required"`
	Region          riotapi.Region                    `json:"region,omitempty"`
	Accounts        []LinkedAccount                   `json:"accounts,omitempty"`
	Syncs           []storage.RiotSyncCheckpoint      `json:"syncs,omitempty"`
	Reconciliations []storage.RiotReconciliationState `json:"reconciliations,omitempty"`
}

type Filter struct {
	StartTime int64  `json:"start_time,omitempty"`
	Queue     int    `json:"queue,omitempty"`
	Type      string `json:"type,omitempty"`
}

func (filter Filter) Key() string {
	normalized := struct {
		StartTime int64  `json:"start_time"`
		Queue     int    `json:"queue"`
		Type      string `json:"type"`
	}{StartTime: filter.StartTime, Queue: filter.Queue, Type: strings.ToLower(strings.TrimSpace(filter.Type))}
	payload, _ := json.Marshal(normalized)
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:16])
}

type SyncResult struct {
	AccountID               string                           `json:"account_id"`
	FilterKey               string                           `json:"filter_key"`
	Status                  storage.RiotSyncStatus           `json:"status"`
	RunStartedAt            time.Time                        `json:"run_started_at"`
	WindowEndUnix           int64                            `json:"window_end_unix"`
	Fetched                 int                              `json:"fetched"`
	Pending                 int                              `json:"pending"`
	Duplicates              int                              `json:"duplicates"`
	HasMore                 bool                             `json:"has_more"`
	ErrorKind               string                           `json:"error_kind,omitempty"`
	ReconciliationStatus    storage.RiotReconciliationStatus `json:"reconciliation_status,omitempty"`
	ReconciliationErrorKind string                           `json:"reconciliation_error_kind,omitempty"`
	ReconciliationRetried   bool                             `json:"reconciliation_retried,omitempty"`
}

type Service struct {
	client      riotapi.Service
	repository  Repository
	config      Config
	mu          sync.Mutex
	accountMu   sync.Mutex
	inflight    map[string]*syncCall
	links       map[*linkCall]struct{}
	deleting    map[string]bool
	accounts    map[string]LinkedAccount
	detailSlots chan struct{}
}

type syncCall struct {
	done   chan struct{}
	cancel context.CancelFunc
	result SyncResult
	err    error
}

type linkCall struct {
	done   chan struct{}
	cancel context.CancelFunc
}

func New(client riotapi.Service, repository Repository, config Config) (*Service, error) {
	if repository == nil {
		return nil, errors.New("Riot sync repository is required")
	}
	if config.Mode == "" {
		config.Mode = riotapi.ModeDisabled
	}
	if !config.Mode.Valid() {
		return nil, errors.New("invalid Riot enrichment mode")
	}
	if config.Mode != riotapi.ModeDisabled && (client == nil || !config.Region.Valid()) {
		return nil, errors.New("enabled Riot synchronization requires a client and region")
	}
	if config.PageSize <= 0 {
		config.PageSize = 20
	}
	if config.PageSize > 100 {
		return nil, errors.New("Riot sync page size exceeds 100")
	}
	if config.MaxMatches <= 0 {
		config.MaxMatches = 100
	}
	if config.MaxMatches > 1000 {
		return nil, errors.New("Riot sync match limit exceeds 1000")
	}
	if config.Concurrency <= 0 {
		config.Concurrency = 4
	}
	if config.Concurrency > 8 {
		return nil, errors.New("Riot sync concurrency exceeds 8")
	}
	if config.Lookback <= 0 {
		config.Lookback = 30 * 24 * time.Hour
	}
	if config.Overlap <= 0 {
		config.Overlap = 24 * time.Hour
	}
	if config.PendingRetry <= 0 {
		config.PendingRetry = 6 * time.Hour
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.NewID == nil {
		config.NewID = randomID
	}
	return &Service{
		client: client, repository: repository, config: config,
		inflight: make(map[string]*syncCall), links: make(map[*linkCall]struct{}), deleting: make(map[string]bool),
		accounts: make(map[string]LinkedAccount), detailSlots: make(chan struct{}, config.Concurrency),
	}, nil
}

func (service *Service) Status(ctx context.Context) (Status, error) {
	status := Status{
		Mode: service.config.Mode, Enabled: service.config.Mode != riotapi.ModeDisabled,
		Configured:  service.config.Mode == riotapi.ModeDisabled || service.client != nil,
		KeyRequired: service.config.Mode == riotapi.ModeDirect && service.client == nil,
		Region:      service.config.Region,
	}
	accounts, err := service.repository.ListRiotAccounts(ctx)
	if err != nil {
		return Status{}, err
	}
	for _, account := range accounts {
		status.Accounts = append(status.Accounts, safeAccount(account))
	}
	status.Syncs, err = service.repository.ListRiotSyncCheckpoints(ctx)
	if err != nil {
		return Status{}, err
	}
	status.Reconciliations, err = service.repository.ListRiotReconciliationStates(ctx)
	if err != nil {
		return Status{}, err
	}
	return status, nil
}

func (service *Service) Link(ctx context.Context, request LinkRequest) (LinkedAccount, error) {
	if service.config.Mode == riotapi.ModeDisabled || service.client == nil {
		return LinkedAccount{}, ErrDisabled
	}
	if !request.Consent {
		return LinkedAccount{}, ErrConsentRequired
	}
	if len([]rune(request.GameName)) > 100 || len([]rune(request.TagLine)) > 30 {
		return LinkedAccount{}, ErrInvalidRequest
	}
	platform, ok := riotapi.ParsePlatform(string(request.Platform))
	if ok {
		request.Platform = platform
	}
	region, ok := request.Platform.Region()
	if !ok || region != service.config.Region || strings.TrimSpace(request.GameName) == "" || strings.TrimSpace(request.TagLine) == "" {
		return LinkedAccount{}, ErrInvalidRequest
	}
	linkContext, cancel := context.WithCancel(ctx)
	call := &linkCall{done: make(chan struct{}), cancel: cancel}
	service.mu.Lock()
	if len(service.deleting) > 0 {
		service.mu.Unlock()
		cancel()
		return LinkedAccount{}, ErrAccountSettling
	}
	service.links[call] = struct{}{}
	service.mu.Unlock()
	defer func() {
		cancel()
		service.mu.Lock()
		delete(service.links, call)
		close(call.done)
		service.mu.Unlock()
	}()

	resolved, err := service.client.ResolveAccount(linkContext, region, request.GameName, request.TagLine)
	if err != nil {
		return LinkedAccount{}, err
	}
	now := service.config.Now().UTC()
	service.accountMu.Lock()
	defer service.accountMu.Unlock()
	existing, err := service.repository.ListRiotAccounts(ctx)
	if err != nil {
		return LinkedAccount{}, err
	}
	accountID := service.config.NewID()
	linkedAt := now
	for _, candidate := range existing {
		if candidate.PUUID == resolved.PUUID {
			accountID = candidate.ID
			linkedAt = candidate.LinkedAt
			break
		}
	}
	service.mu.Lock()
	settling := len(service.deleting) > 0 || service.deleting[accountID]
	service.mu.Unlock()
	if settling {
		return LinkedAccount{}, ErrAccountSettling
	}
	account := storage.RiotAccount{
		ID: accountID, PUUID: resolved.PUUID, Region: region,
		Platform: request.Platform, ConsentAt: now, LinkedAt: linkedAt, UpdatedAt: now,
		AutoSync: request.AutoSync || service.config.AutoSync,
	}
	if strings.TrimSpace(account.ID) == "" {
		return LinkedAccount{}, errors.New("Riot account ID generator returned an empty ID")
	}
	if err := service.repository.UpsertRiotAccount(ctx, account); err != nil {
		return LinkedAccount{}, err
	}
	safe := safeAccount(account)
	service.mu.Lock()
	service.accounts[account.ID] = safe
	service.mu.Unlock()
	return safe, nil
}

func (service *Service) Delete(ctx context.Context, accountID, confirmation string) error {
	if confirmation != DeleteConfirmation {
		return ErrConsentRequired
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return ErrInvalidRequest
	}
	// Settle every filter-specific writer before the privacy transaction. This
	// prevents a page fetched before deletion from being committed afterward.
	service.mu.Lock()
	if service.deleting[accountID] {
		service.mu.Unlock()
		return ErrAccountSettling
	}
	service.deleting[accountID] = true
	var pending []<-chan struct{}
	for key, call := range service.inflight {
		if strings.HasPrefix(key, accountID+":") {
			call.cancel()
			pending = append(pending, call.done)
		}
	}
	for call := range service.links {
		call.cancel()
		pending = append(pending, call.done)
	}
	service.mu.Unlock()
	defer func() {
		service.mu.Lock()
		delete(service.deleting, accountID)
		service.mu.Unlock()
	}()
	for _, done := range pending {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
		}
	}
	service.accountMu.Lock()
	defer service.accountMu.Unlock()
	if err := service.repository.DeleteRiotAccount(ctx, accountID); err != nil {
		return err
	}
	service.mu.Lock()
	delete(service.accounts, accountID)
	service.mu.Unlock()
	return nil
}

func (service *Service) Sync(ctx context.Context, accountID string, filter Filter) (SyncResult, error) {
	if service.config.Mode == riotapi.ModeDisabled || service.client == nil {
		return SyncResult{}, ErrDisabled
	}
	if strings.TrimSpace(accountID) == "" || len(accountID) > 128 || filter.StartTime < 0 || filter.Queue < 0 || filter.Queue > 10000 || !riotapi.ValidMatchType(filter.Type) {
		return SyncResult{}, ErrInvalidRequest
	}
	if filter.StartTime > service.config.Now().UTC().Unix() {
		return SyncResult{}, ErrInvalidRequest
	}
	key := accountID + ":" + filter.Key()
	var runContext context.Context
	var cancel context.CancelFunc
	var call *syncCall
	for {
		service.mu.Lock()
		if service.deleting[accountID] {
			service.mu.Unlock()
			return SyncResult{}, ErrAccountSettling
		}
		if current := service.inflight[key]; current != nil {
			service.mu.Unlock()
			select {
			case <-ctx.Done():
				return SyncResult{}, ctx.Err()
			case <-current.done:
				return current.result, current.err
			}
		}
		// Reconciliation state is account-wide, so different filters for the
		// same account serialize. A different-filter caller waits and then runs
		// its own request; identical filters retain singleflight semantics.
		var blocker *syncCall
		for currentKey, current := range service.inflight {
			if strings.HasPrefix(currentKey, accountID+":") {
				blocker = current
				break
			}
		}
		if blocker != nil {
			service.mu.Unlock()
			select {
			case <-ctx.Done():
				return SyncResult{}, ctx.Err()
			case <-blocker.done:
				continue
			}
		}
		runContext, cancel = context.WithCancel(ctx)
		call = &syncCall{done: make(chan struct{}), cancel: cancel}
		service.inflight[key] = call
		service.mu.Unlock()
		break
	}

	call.result, call.err = service.runSync(runContext, accountID, filter)
	cancel()
	service.mu.Lock()
	delete(service.inflight, key)
	close(call.done)
	service.mu.Unlock()
	return call.result, call.err
}

func (service *Service) runSync(ctx context.Context, accountID string, filter Filter) (SyncResult, error) {
	if result, handled, err := service.retryPendingReconciliation(ctx, accountID); handled || err != nil {
		return result, err
	}
	result, err := service.sync(ctx, accountID, filter)
	if err != nil || result.Status != storage.RiotSyncComplete {
		return result, err
	}
	state, loadErr := service.repository.LoadRiotReconciliationState(ctx, accountID)
	if loadErr != nil {
		// Completion and the pending marker are committed in one storage
		// transaction. If the state cannot currently be read, ingestion remains
		// complete and reconciliation is truthfully exposed as pending rather
		// than masquerading as resumable Match-V5 ingestion.
		result.ReconciliationStatus = storage.RiotReconciliationPending
		result.ReconciliationErrorKind = reconciliationErrorStorage
		return result, nil
	}
	return service.attemptReconciliation(ctx, state, result)
}

func (service *Service) retryPendingReconciliation(ctx context.Context, accountID string) (SyncResult, bool, error) {
	state, err := service.repository.LoadRiotReconciliationState(ctx, accountID)
	if errors.Is(err, storage.ErrRiotReconciliationMissing) {
		return SyncResult{}, false, nil
	}
	if err != nil {
		return SyncResult{}, false, err
	}
	if state.Status != storage.RiotReconciliationPending {
		return SyncResult{}, false, nil
	}
	result := SyncResult{
		AccountID: accountID, FilterKey: state.FilterKey, Status: storage.RiotSyncComplete,
		RunStartedAt: state.RunStartedAt, WindowEndUnix: state.WindowEndUnix, ReconciliationRetried: true,
	}
	result, err = service.attemptReconciliation(ctx, state, result)
	return result, true, err
}

func (service *Service) attemptReconciliation(ctx context.Context, state storage.RiotReconciliationState, result SyncResult) (SyncResult, error) {
	result.ReconciliationStatus = storage.RiotReconciliationPending
	if err := service.reconcileStoredSessions(ctx, state.AccountID, 200); err != nil {
		errorKind := reconciliationErrorStorage
		if ctx.Err() != nil {
			errorKind = string(riotapi.ErrorCancelled)
		}
		state.Status = storage.RiotReconciliationPending
		state.LastErrorKind = errorKind
		state.UpdatedAt = service.config.Now().UTC()
		persistContext := ctx
		if ctx.Err() != nil {
			persistContext = context.Background()
		}
		_ = service.repository.SaveRiotReconciliationState(persistContext, state)
		result.ReconciliationErrorKind = errorKind
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, nil
	}

	state.Status = storage.RiotReconciliationComplete
	state.LastErrorKind = ""
	state.UpdatedAt = service.config.Now().UTC()
	if err := service.repository.SaveRiotReconciliationState(ctx, state); err != nil {
		// The original pending row remains durable. A later trigger repeats the
		// idempotent links and retries this final state transition without
		// opening a new ingestion window.
		state.Status = storage.RiotReconciliationPending
		state.LastErrorKind = reconciliationErrorStorage
		state.UpdatedAt = service.config.Now().UTC()
		_ = service.repository.SaveRiotReconciliationState(context.Background(), state)
		result.ReconciliationStatus = storage.RiotReconciliationPending
		result.ReconciliationErrorKind = reconciliationErrorStorage
		return result, nil
	}
	result.ReconciliationStatus = storage.RiotReconciliationComplete
	result.ReconciliationErrorKind = ""
	return result, nil
}

func (service *Service) sync(ctx context.Context, accountID string, filter Filter) (SyncResult, error) {
	account, err := service.repository.LoadRiotAccount(ctx, accountID)
	if err != nil {
		return SyncResult{}, err
	}
	platformRegion, platformOK := account.Platform.Region()
	if !account.Region.Valid() || !platformOK || platformRegion != account.Region {
		return SyncResult{}, ErrInvalidRequest
	}
	now := service.config.Now().UTC()
	filterKey := filter.Key()
	checkpoint, loadErr := service.repository.LoadRiotSyncCheckpoint(ctx, accountID, filterKey)
	resume := loadErr == nil && checkpoint.Region == account.Region && checkpoint.Status != storage.RiotSyncComplete && checkpoint.WindowEndUnix > 0 && !checkpoint.RunStartedAt.IsZero()
	if loadErr != nil && !errors.Is(loadErr, storage.ErrRiotCheckpointMissing) {
		return SyncResult{}, loadErr
	}
	if !resume {
		previous := checkpoint
		if previous.Region != account.Region {
			previous = storage.RiotSyncCheckpoint{}
		}
		windowStart := filter.StartTime
		if windowStart == 0 {
			windowStart = now.Add(-service.config.Lookback).Unix()
			if loadErr == nil && previous.CoverageEndUnix > 0 {
				windowStart = time.Unix(previous.CoverageEndUnix, 0).Add(-service.config.Overlap).Unix()
			}
		}
		checkpoint = storage.RiotSyncCheckpoint{
			AccountID: accountID, Region: account.Region, FilterKey: filterKey, RunStartedAt: now,
			WindowStartUnix: windowStart, WindowEndUnix: now.Unix(), Status: storage.RiotSyncRunning, UpdatedAt: now,
			CoverageStartUnix: previous.CoverageStartUnix, CoverageEndUnix: previous.CoverageEndUnix,
		}
	} else {
		checkpoint.Status = storage.RiotSyncRunning
		checkpoint.LastErrorKind = ""
		checkpoint.UpdatedAt = now
	}
	if err := service.repository.SaveRiotSyncBatch(ctx, accountID, nil, nil, checkpoint); err != nil {
		return SyncResult{}, err
	}
	result := SyncResult{AccountID: accountID, FilterKey: filterKey, Status: storage.RiotSyncRunning, RunStartedAt: checkpoint.RunStartedAt, WindowEndUnix: checkpoint.WindowEndUnix}
	seen := make(map[string]struct{})

	// Retry previously listed 404s without changing the frozen list cursor.
	allPending, err := service.repository.ListRiotPendingFetches(ctx, accountID, 10000)
	if err != nil {
		return result, err
	}
	var due []storage.RiotPendingFetch
	for _, pending := range allPending {
		seen[pending.MatchID] = struct{}{}
		if !pending.NextRetryAt.After(now) && len(due) < service.config.MaxMatches {
			due = append(due, pending)
		}
	}
	if len(due) > 0 {
		ids := make([]string, len(due))
		for index := range due {
			ids[index] = due[index].MatchID
		}
		outcomes := service.fetch(ctx, account, ids)
		matches, pending, firstErr := service.reduceOutcomes(ctx, outcomes, due, now)
		result.Fetched += len(matches)
		result.Pending += len(pending)
		if firstErr != nil {
			if ctx.Err() != nil || riotapi.KindOf(firstErr) == riotapi.ErrorCancelled {
				return service.persistCancellationBatch(accountID, checkpoint, result, matches, pending, firstErr)
			}
			return service.persistFailure(ctx, accountID, checkpoint, result, matches, pending, firstErr)
		}
		checkpoint.UpdatedAt = now
		if err := service.repository.SaveRiotSyncBatch(ctx, accountID, matches, pending, checkpoint); err != nil {
			return result, err
		}
	}

	pagesScanned := 0
	maximumPages := (service.config.MaxMatches+service.config.PageSize-1)/service.config.PageSize + 1
	for result.Fetched+result.Pending < service.config.MaxMatches && pagesScanned < maximumPages {
		if err := ctx.Err(); err != nil {
			return service.persistCancellation(accountID, checkpoint, result, err)
		}
		remaining := service.config.MaxMatches - result.Fetched - result.Pending
		count := service.config.PageSize
		if remaining < count {
			count = remaining
		}
		query := riotapi.MatchListQuery{
			Start: checkpoint.NextStart, Count: count, StartTime: checkpoint.WindowStartUnix,
			EndTime: checkpoint.WindowEndUnix, Queue: filter.Queue, Type: strings.TrimSpace(filter.Type),
		}
		ids, err := service.client.ListMatchIDs(ctx, account.Region, account.PUUID, query)
		pagesScanned++
		if err != nil {
			if ctx.Err() != nil || riotapi.KindOf(err) == riotapi.ErrorCancelled {
				return service.persistCancellation(accountID, checkpoint, result, err)
			}
			return service.persistFailure(ctx, accountID, checkpoint, result, nil, nil, err)
		}
		if len(ids) == 0 {
			checkpoint.Status = storage.RiotSyncComplete
			extendCoverage(&checkpoint)
			checkpoint.LastErrorKind = ""
			checkpoint.UpdatedAt = service.config.Now().UTC()
			if err := service.repository.SaveRiotSyncBatch(ctx, accountID, nil, nil, checkpoint); err != nil {
				return result, err
			}
			result.Status = storage.RiotSyncComplete
			return result, nil
		}
		checkpoint.NextStart += len(ids)
		unique := make([]string, 0, len(ids))
		for _, id := range ids {
			if _, ok := seen[id]; ok {
				result.Duplicates++
				continue
			}
			seen[id] = struct{}{}
			if len(unique)+result.Fetched+result.Pending >= service.config.MaxMatches {
				break
			}
			unique = append(unique, id)
		}
		outcomes := service.fetch(ctx, account, unique)
		matches, pending, firstErr := service.reduceOutcomes(ctx, outcomes, nil, now)
		result.Fetched += len(matches)
		result.Pending += len(pending)
		if firstErr != nil {
			checkpoint.NextStart -= len(ids) // retry the exact page after restart.
			if ctx.Err() != nil || riotapi.KindOf(firstErr) == riotapi.ErrorCancelled {
				return service.persistCancellationBatch(accountID, checkpoint, result, matches, pending, firstErr)
			}
			return service.persistFailure(ctx, accountID, checkpoint, result, matches, pending, firstErr)
		}
		checkpoint.UpdatedAt = service.config.Now().UTC()
		if err := service.repository.SaveRiotSyncBatch(ctx, accountID, matches, pending, checkpoint); err != nil {
			return result, err
		}
		if len(ids) < count {
			checkpoint.Status = storage.RiotSyncComplete
			extendCoverage(&checkpoint)
			checkpoint.UpdatedAt = service.config.Now().UTC()
			if err := service.repository.SaveRiotSyncBatch(ctx, accountID, nil, nil, checkpoint); err != nil {
				return result, err
			}
			result.Status = storage.RiotSyncComplete
			return result, nil
		}
	}
	checkpoint.Status = storage.RiotSyncPartial
	checkpoint.LastErrorKind = ""
	checkpoint.UpdatedAt = service.config.Now().UTC()
	if err := service.repository.SaveRiotSyncBatch(ctx, accountID, nil, nil, checkpoint); err != nil {
		return result, err
	}
	result.Status = storage.RiotSyncPartial
	result.HasMore = true
	return result, nil
}

type fetchOutcome struct {
	match riotapi.Match
	id    string
	err   error
}

func (service *Service) fetch(ctx context.Context, account storage.RiotAccount, ids []string) []fetchOutcome {
	result := make([]fetchOutcome, len(ids))
	jobs := make(chan int)
	var workers sync.WaitGroup
	workerCount := service.config.Concurrency
	if len(ids) < workerCount {
		workerCount = len(ids)
	}
	for worker := 0; worker < workerCount; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				id := ids[index]
				select {
				case <-ctx.Done():
					result[index] = fetchOutcome{id: id, err: ctx.Err()}
					continue
				case service.detailSlots <- struct{}{}:
				}
				match, err := service.client.GetMatch(ctx, account.Region, id, account.PUUID)
				<-service.detailSlots
				result[index] = fetchOutcome{id: id, match: match, err: err}
			}
		}()
	}
	for index := range ids {
		select {
		case <-ctx.Done():
			for remaining := index; remaining < len(ids); remaining++ {
				result[remaining] = fetchOutcome{id: ids[remaining], err: ctx.Err()}
			}
			close(jobs)
			workers.Wait()
			return result
		case jobs <- index:
		}
	}
	close(jobs)
	workers.Wait()
	return result
}

func (service *Service) reduceOutcomes(ctx context.Context, outcomes []fetchOutcome, previous []storage.RiotPendingFetch, now time.Time) ([]riotapi.Match, []storage.RiotPendingFetch, error) {
	previousByID := make(map[string]storage.RiotPendingFetch, len(previous))
	for _, value := range previous {
		previousByID[value.MatchID] = value
	}
	var matches []riotapi.Match
	var pending []storage.RiotPendingFetch
	var firstErr error
	for _, outcome := range outcomes {
		if outcome.err == nil {
			matches = append(matches, outcome.match)
			continue
		}
		kind := riotapi.KindOf(outcome.err)
		if kind == riotapi.ErrorCancelled || (ctx.Err() != nil && errors.Is(outcome.err, ctx.Err())) {
			if firstErr == nil {
				firstErr = outcome.err
			}
			continue
		}
		old := previousByID[outcome.id]
		if kind == riotapi.ErrorNotFound || old.MatchID != "" {
			attempts := old.Attempts + 1
			pending = append(pending, storage.RiotPendingFetch{
				MatchID: outcome.id, Attempts: attempts, NextRetryAt: now.Add(service.config.PendingRetry),
				LastErrorKind: string(kind), UpdatedAt: now,
			})
			if kind == riotapi.ErrorNotFound {
				continue
			}
		}
		if firstErr == nil {
			firstErr = outcome.err
		}
	}
	return matches, pending, firstErr
}

func (service *Service) persistFailure(ctx context.Context, accountID string, checkpoint storage.RiotSyncCheckpoint, result SyncResult, matches []riotapi.Match, pending []storage.RiotPendingFetch, failure error) (SyncResult, error) {
	kind := riotapi.KindOf(failure)
	if kind == "" {
		kind = riotapi.ErrorTransport
	}
	checkpoint.Status = storage.RiotSyncPartial
	checkpoint.LastErrorKind = string(kind)
	checkpoint.UpdatedAt = service.config.Now().UTC()
	persistContext := ctx
	if ctx.Err() != nil {
		persistContext = context.WithoutCancel(ctx)
	}
	if err := service.repository.SaveRiotSyncBatch(persistContext, accountID, matches, pending, checkpoint); err != nil {
		return result, err
	}
	result.Status = storage.RiotSyncPartial
	result.ErrorKind = string(kind)
	return result, fmt.Errorf("Riot synchronization stopped (%s): %w", kind, failure)
}

func (service *Service) persistCancellation(accountID string, checkpoint storage.RiotSyncCheckpoint, result SyncResult, failure error) (SyncResult, error) {
	return service.persistCancellationBatch(accountID, checkpoint, result, nil, nil, failure)
}

func (service *Service) persistCancellationBatch(accountID string, checkpoint storage.RiotSyncCheckpoint, result SyncResult, matches []riotapi.Match, pending []storage.RiotPendingFetch, failure error) (SyncResult, error) {
	checkpoint.Status = storage.RiotSyncCancelled
	checkpoint.LastErrorKind = string(riotapi.ErrorCancelled)
	checkpoint.UpdatedAt = service.config.Now().UTC()
	if err := service.repository.SaveRiotSyncBatch(context.Background(), accountID, matches, pending, checkpoint); err != nil {
		return result, err
	}
	result.Status = storage.RiotSyncCancelled
	result.ErrorKind = string(riotapi.ErrorCancelled)
	return result, failure
}

// Reconcile persists deterministic local-session links after callers provide
// all available queue/platform/external-ID evidence. Missing evidence remains
// pending rather than being guessed.
func (service *Service) Reconcile(ctx context.Context, accountID string, locals []LocalCandidate) ([]storage.SessionMatchLink, error) {
	if service.config.Mode == riotapi.ModeDisabled {
		return nil, ErrDisabled
	}
	account, err := service.repository.LoadRiotAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	for index := range locals {
		if strings.TrimSpace(locals[index].ExternalMatchID) != "" {
			key, keyErr := service.repository.RiotMatchKey(accountID, locals[index].ExternalMatchID)
			if keyErr != nil {
				return nil, keyErr
			}
			locals[index].ExternalMatchID = key
		}
		if locals[index].Platform == "" {
			locals[index].Platform = account.Platform
		}
	}
	officials, err := service.repository.ListRiotMatches(ctx, accountID, storage.RiotMatchFilter{Limit: 10000})
	if err != nil {
		return nil, err
	}
	links := ReconcileSessions(accountID, locals, officials, service.config.Now().UTC())
	for _, link := range links {
		if err := service.repository.SaveSessionMatchLink(ctx, link); err != nil {
			return nil, err
		}
	}
	return links, nil
}

func (service *Service) reconcileStoredSessions(ctx context.Context, accountID string, limit int) error {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	account, err := service.repository.LoadRiotAccount(ctx, accountID)
	if err != nil {
		return err
	}
	sessions, err := service.repository.ListMatches(ctx, limit, 0)
	if err != nil {
		return err
	}
	locals := make([]LocalCandidate, 0, len(sessions))
	for _, session := range sessions {
		if session.State != domain.MatchEnded {
			continue
		}
		locals = append(locals, LocalCandidate{Session: session, Platform: account.Platform, QueueID: session.QueueID})
	}
	if len(locals) == 0 {
		return nil
	}
	_, err = service.Reconcile(ctx, accountID, locals)
	return err
}

func safeAccount(account storage.RiotAccount) LinkedAccount {
	return LinkedAccount{ID: account.ID, Region: account.Region, Platform: account.Platform, LinkedAt: account.LinkedAt, UpdatedAt: account.UpdatedAt, AutoSync: account.AutoSync}
}

func extendCoverage(checkpoint *storage.RiotSyncCheckpoint) {
	if checkpoint.CoverageStartUnix == 0 || checkpoint.WindowStartUnix < checkpoint.CoverageStartUnix {
		checkpoint.CoverageStartUnix = checkpoint.WindowStartUnix
	}
	if checkpoint.WindowEndUnix > checkpoint.CoverageEndUnix {
		checkpoint.CoverageEndUnix = checkpoint.WindowEndUnix
	}
}

func randomID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return ""
	}
	return hex.EncodeToString(buffer)
}
