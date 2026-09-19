package tracker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"lolengine/internal/domain"
	"lolengine/internal/state"
)

// Fetcher supplies normalized game state from the Live Client boundary.
type Fetcher interface {
	Fetch(context.Context) (domain.GameState, error)
}

// LifecyclePersistence is the durable boundary used before semantic events are published.
type LifecyclePersistence interface {
	SaveMatchUpdates(context.Context, []domain.MatchSession, []domain.SemanticEvent) error
	LoadActiveMatch(context.Context) (*domain.MatchSession, error)
}

type lifecycleRecoveryPersistence interface {
	LoadLifecycleRecovery(context.Context, time.Time, int) (domain.LifecycleRecovery, error)
}

const defaultEndedRecoveryWindow = 10 * time.Minute

// Config controls tracker polling and stale-state transitions.
type Config struct {
	PollInterval        time.Duration
	StaleAfter          time.Duration
	EndAfter            time.Duration
	Now                 func() time.Time
	NewSessionID        func(string, time.Time) string
	RecentEventLimit    int
	EndedRecoveryWindow time.Duration
	Persistence         LifecyclePersistence
}

// Tracker polls sequentially and publishes only meaningful state changes.
type Tracker struct {
	fetcher      Fetcher
	store        *state.Store
	pollInterval time.Duration
	staleAfter   time.Duration
	now          func() time.Time
	detector     *Detector
	persistence  LifecyclePersistence

	pollMu          sync.Mutex
	lastSuccess     time.Time
	pendingSessions map[string]domain.MatchSession
	pendingEvents   []domain.SemanticEvent
	pendingRelevant bool
	pendingSnapshot *domain.Snapshot
}

// New constructs a tracker with validated dependencies and timing.
func New(fetcher Fetcher, store *state.Store, cfg Config) (*Tracker, error) {
	if fetcher == nil {
		return nil, errors.New("fetcher must not be nil")
	}
	if store == nil {
		return nil, errors.New("state store must not be nil")
	}
	if cfg.PollInterval <= 0 {
		return nil, fmt.Errorf("poll interval must be greater than zero")
	}
	if cfg.StaleAfter <= 0 {
		return nil, fmt.Errorf("stale threshold must be greater than zero")
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.EndAfter <= 0 {
		cfg.EndAfter = maxDuration(6*cfg.StaleAfter, time.Minute)
	}
	if cfg.EndAfter <= cfg.StaleAfter {
		return nil, fmt.Errorf("end threshold must be greater than stale threshold")
	}
	if cfg.EndedRecoveryWindow <= 0 {
		cfg.EndedRecoveryWindow = defaultEndedRecoveryWindow
	}
	var recovery domain.LifecycleRecovery
	var initialSession *domain.MatchSession
	if cfg.Persistence != nil {
		var err error
		if recoverer, ok := cfg.Persistence.(lifecycleRecoveryPersistence); ok {
			eventLimit := cfg.RecentEventLimit
			if eventLimit <= 0 {
				eventLimit = defaultRecentEventLimit
			}
			recovery, err = recoverer.LoadLifecycleRecovery(
				context.Background(), cfg.Now().UTC().Add(-cfg.EndedRecoveryWindow), eventLimit,
			)
			initialSession = recovery.Session
		} else {
			initialSession, err = cfg.Persistence.LoadActiveMatch(context.Background())
			recovery.Session = initialSession
		}
		if err != nil {
			return nil, fmt.Errorf("load lifecycle recovery: %w", err)
		}
	}

	tracker := &Tracker{
		fetcher:         fetcher,
		store:           store,
		pollInterval:    cfg.PollInterval,
		staleAfter:      cfg.StaleAfter,
		now:             cfg.Now,
		persistence:     cfg.Persistence,
		pendingSessions: make(map[string]domain.MatchSession),
	}
	tracker.detector = NewDetector(DetectorConfig{
		Now: cfg.Now, NewSessionID: cfg.NewSessionID, StaleAfter: cfg.StaleAfter,
		EndAfter: cfg.EndAfter, RecentLimit: cfg.RecentEventLimit, InitialSession: initialSession,
		InitialConnection: recovery.Connection, InitialEvents: recovery.RecentEvents,
		InitialOutage: timeValue(recovery.OutageStartedAt), InitialFailureClass: recovery.LastFailureClass,
		InitialFailureAt: timeValue(recovery.LastFailureAt), InitialUnavailable: timeValue(recovery.UnavailableSince),
	})
	if lastSuccess := store.Current().LastSuccessAt; lastSuccess != nil {
		tracker.lastSuccess = *lastSuccess
	} else if initialSession != nil {
		tracker.lastSuccess = initialSession.LastSeenAt
	}
	return tracker, nil
}

// Run polls immediately and then at the configured interval until cancellation.
func (t *Tracker) Run(ctx context.Context) {
	t.poll(ctx)

	ticker := time.NewTicker(t.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.poll(ctx)
		}
	}
}

func (t *Tracker) poll(ctx context.Context) {
	t.pollMu.Lock()
	defer t.pollMu.Unlock()

	game, err := t.fetcher.Fetch(ctx)
	if ctx.Err() != nil {
		return
	}

	now := t.now().UTC()
	committed := t.store.Current()
	if err == nil {
		t.lastSuccess = now
		detection := t.detector.Observe(game)
		t.stageDetection(detection)
		status := domain.StatusInGame
		if detection.Session != nil && detection.Session.State == domain.MatchEnded {
			status = domain.StatusStale
		}
		next := domain.Snapshot{
			Status: status, Game: &game, UpdatedAt: now, LastSuccessAt: timePointer(now),
			DecisionRevision: committed.DecisionRevision, Connection: detection.Connection,
			Session: detection.Session, RecentEvents: t.detector.RecentEvents(),
		}
		if t.pendingRelevant {
			next.DecisionRevision++
		}
		t.pendingSnapshot = snapshotPointer(next)
		if persistenceErr := t.persistPending(ctx); persistenceErr != nil {
			failed := committed
			failed.UpdatedAt = now
			failed.LastError = persistenceErr.Error()
			t.store.Update(failed)
			return
		}
		t.publishPending()
		return
	}

	class := domain.FetchClassOf(err)
	if class == domain.FetchUnknown {
		class = domain.FetchTransport
	}
	detection := t.detector.ObserveFailure(class, err.Error())
	t.stageDetection(detection)
	base := committed
	if t.pendingSnapshot != nil {
		base = cloneTrackerSnapshot(*t.pendingSnapshot)
	}
	status := domain.StatusWaitingForGame
	if detection.Session != nil {
		status = domain.StatusInGame
		if detection.Connection == domain.ConnectionStale || detection.Session.State == domain.MatchEnded || now.Sub(t.lastSuccess) >= t.staleAfter {
			status = domain.StatusStale
		}
	}
	next := domain.Snapshot{
		Status:           status,
		Game:             base.Game,
		UpdatedAt:        now,
		LastSuccessAt:    timePointerOrNil(t.lastSuccess),
		LastError:        err.Error(),
		DecisionRevision: committed.DecisionRevision,
		Connection:       detection.Connection,
		Session:          detection.Session,
		RecentEvents:     t.detector.RecentEvents(),
	}
	if t.pendingRelevant {
		next.DecisionRevision++
	}
	t.pendingSnapshot = snapshotPointer(next)
	if persistenceErr := t.persistPending(ctx); persistenceErr != nil {
		failed := committed
		failed.UpdatedAt = now
		failed.LastError = err.Error() + "; " + persistenceErr.Error()
		t.store.Update(failed)
		return
	}
	t.publishPending()
}

func (t *Tracker) stageDetection(detection Detection) {
	for _, session := range detection.Sessions {
		t.pendingSessions[session.ID] = session
	}
	t.pendingEvents = append(t.pendingEvents, detection.Events...)
	t.pendingRelevant = t.pendingRelevant || detection.RecommendationRelevant
}

func (t *Tracker) persistPending(ctx context.Context) error {
	if t.persistence != nil && (len(t.pendingSessions) > 0 || len(t.pendingEvents) > 0) {
		sessions := make([]domain.MatchSession, 0, len(t.pendingSessions))
		for _, session := range t.pendingSessions {
			sessions = append(sessions, session)
		}
		if err := t.persistence.SaveMatchUpdates(ctx, sessions, t.pendingEvents); err != nil {
			return fmt.Errorf("persist match lifecycle: %w", err)
		}
	}
	return nil
}

func (t *Tracker) publishPending() {
	if t.pendingSnapshot == nil {
		return
	}
	next := cloneTrackerSnapshot(*t.pendingSnapshot)
	t.pendingSessions = make(map[string]domain.MatchSession)
	t.pendingEvents = nil
	t.pendingRelevant = false
	t.pendingSnapshot = nil
	t.store.Update(next)
}

func timePointer(value time.Time) *time.Time {
	return &value
}

func timePointerOrNil(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func snapshotPointer(snapshot domain.Snapshot) *domain.Snapshot {
	clone := cloneTrackerSnapshot(snapshot)
	return &clone
}

func cloneTrackerSnapshot(snapshot domain.Snapshot) domain.Snapshot {
	clone := snapshot
	clone.Game = cloneGame(snapshot.Game)
	clone.Session = cloneSession(snapshot.Session)
	clone.RecentEvents = cloneEvents(snapshot.RecentEvents)
	clone.LastSuccessAt = timePointerOrNil(timeValue(snapshot.LastSuccessAt))
	return clone
}

func maxDuration(left, right time.Duration) time.Duration {
	if left > right {
		return left
	}
	return right
}

func timeValue(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}
