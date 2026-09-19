package state

import (
	"reflect"
	"sync"
	"time"

	"lolengine/internal/domain"
)

// Store owns the latest snapshot and fans meaningful changes out to subscribers.
type Store struct {
	mu          sync.RWMutex
	current     domain.Snapshot
	subscribers map[uint64]chan domain.Snapshot
	nextID      uint64
}

// NewStore creates a store with an initial observable snapshot.
func NewStore(initial domain.Snapshot) *Store {
	if initial.Revision == 0 {
		initial.Revision = 1
	}
	if initial.UpdatedAt.IsZero() {
		initial.UpdatedAt = time.Now().UTC()
	}
	return &Store{
		current:     cloneSnapshot(initial),
		subscribers: make(map[uint64]chan domain.Snapshot),
	}
}

// Current returns an isolated copy safe for callers to inspect or serialize.
func (s *Store) Current() domain.Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneSnapshot(s.current)
}

// Update replaces the current snapshot. It notifies subscribers only when status,
// error, or normalized game data changes. A newer successful-poll timestamp is kept
// even when no notification is necessary.
func (s *Store) Update(next domain.Snapshot) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if snapshotsEquivalent(s.current, next) {
		if next.LastSuccessAt != nil {
			s.current.LastSuccessAt = cloneTime(next.LastSuccessAt)
		}
		return false
	}

	next.Revision = s.current.Revision + 1
	if next.UpdatedAt.IsZero() {
		next.UpdatedAt = time.Now().UTC()
	}
	s.current = cloneSnapshot(next)
	for _, subscriber := range s.subscribers {
		offerLatest(subscriber, cloneSnapshot(s.current))
	}
	return true
}

// Subscribe atomically captures the initial state and registers for later changes.
// The cancellation function removes and closes the subscriber exactly once.
func (s *Store) Subscribe(buffer int) (domain.Snapshot, <-chan domain.Snapshot, func()) {
	if buffer < 1 {
		buffer = 1
	}

	s.mu.Lock()
	id := s.nextID
	s.nextID++
	updates := make(chan domain.Snapshot, buffer)
	s.subscribers[id] = updates
	initial := cloneSnapshot(s.current)
	s.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			s.mu.Lock()
			if subscriber, exists := s.subscribers[id]; exists {
				delete(s.subscribers, id)
				close(subscriber)
			}
			s.mu.Unlock()
		})
	}

	return initial, updates, cancel
}

func snapshotsEquivalent(left, right domain.Snapshot) bool {
	return left.Status == right.Status &&
		left.LastError == right.LastError &&
		left.DecisionRevision == right.DecisionRevision &&
		left.Connection == right.Connection &&
		sessionsEquivalent(left.Session, right.Session) &&
		reflect.DeepEqual(left.RecentEvents, right.RecentEvents) &&
		reflect.DeepEqual(left.Game, right.Game)
}

func sessionsEquivalent(left, right *domain.MatchSession) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	leftCopy, rightCopy := *left, *right
	leftCopy.UpdatedAt, rightCopy.UpdatedAt = time.Time{}, time.Time{}
	leftCopy.LastSeenAt, rightCopy.LastSeenAt = time.Time{}, time.Time{}
	leftCopy.ConnectionState, rightCopy.ConnectionState = "", ""
	leftCopy.OutageStartedAt, rightCopy.OutageStartedAt = time.Time{}, time.Time{}
	leftCopy.LastFailureClass, rightCopy.LastFailureClass = "", ""
	leftCopy.LastFailureAt, rightCopy.LastFailureAt = time.Time{}, time.Time{}
	leftCopy.UnavailableSince, rightCopy.UnavailableSince = time.Time{}, time.Time{}
	return reflect.DeepEqual(leftCopy, rightCopy)
}

func offerLatest(updates chan domain.Snapshot, snapshot domain.Snapshot) {
	select {
	case updates <- snapshot:
		return
	default:
	}

	select {
	case <-updates:
	default:
	}

	select {
	case updates <- snapshot:
	default:
	}
}

func cloneSnapshot(snapshot domain.Snapshot) domain.Snapshot {
	clone := snapshot
	clone.LastSuccessAt = cloneTime(snapshot.LastSuccessAt)
	if snapshot.Session != nil {
		session := *snapshot.Session
		session.EndedAt = cloneTime(snapshot.Session.EndedAt)
		clone.Session = &session
	}
	clone.RecentEvents = cloneSemanticEvents(snapshot.RecentEvents)
	if snapshot.Game == nil {
		return clone
	}

	game := *snapshot.Game
	game.ActivePlayer.Items = append([]domain.Item(nil), snapshot.Game.ActivePlayer.Items...)
	if snapshot.Game.SourceEvents != nil {
		game.SourceEvents = make([]domain.SourceEvent, len(snapshot.Game.SourceEvents))
		for index, event := range snapshot.Game.SourceEvents {
			game.SourceEvents[index] = event
			game.SourceEvents[index].Payload = append([]byte(nil), event.Payload...)
		}
	}
	if snapshot.Game.Players != nil {
		game.Players = make([]domain.Player, len(snapshot.Game.Players))
		for index, player := range snapshot.Game.Players {
			game.Players[index] = player
			game.Players[index].Items = append([]domain.Item(nil), player.Items...)
		}
	}
	clone.Game = &game
	return clone
}

func cloneSemanticEvents(events []domain.SemanticEvent) []domain.SemanticEvent {
	if events == nil {
		return nil
	}
	clone := make([]domain.SemanticEvent, len(events))
	for index, event := range events {
		clone[index] = event
		clone[index].Payload = append([]byte(nil), event.Payload...)
	}
	return clone
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
