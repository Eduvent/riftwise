package domain

import (
	"encoding/json"
	"errors"
	"time"
)

// FetchErrorClass keeps transport availability separate from payload validity.
type FetchErrorClass string

const (
	FetchUnknown         FetchErrorClass = "unknown"
	FetchGameUnavailable FetchErrorClass = "game_unavailable"
	FetchTransport       FetchErrorClass = "transport"
	FetchTimeout         FetchErrorClass = "timeout"
	FetchInvalidPayload  FetchErrorClass = "invalid_payload"
)

// ClassifiedFetchError is implemented by Live Client boundary errors.
type ClassifiedFetchError interface {
	error
	FetchErrorClass() FetchErrorClass
}

// FetchClassOf returns the stable class of an adapter error.
func FetchClassOf(err error) FetchErrorClass {
	if err == nil {
		return FetchUnknown
	}
	var classified ClassifiedFetchError
	if errors.As(err, &classified) {
		return classified.FetchErrorClass()
	}
	return FetchUnknown
}

// SourceEvent is one normalized entry from the cumulative Live Client event feed.
type SourceEvent struct {
	ID              int64           `json:"id"`
	Name            string          `json:"name"`
	GameTimeSeconds float64         `json:"game_time_seconds"`
	DedupeKey       string          `json:"dedupe_key"`
	Payload         json.RawMessage `json:"payload,omitempty"`
}

// ConnectionState describes observation health independently of the match phase.
type ConnectionState string

const (
	ConnectionWaiting      ConnectionState = "waiting"
	ConnectionConnected    ConnectionState = "connected"
	ConnectionReconnecting ConnectionState = "reconnecting"
	ConnectionStale        ConnectionState = "stale"
)

// MatchState describes the lifecycle of one locally observed match session.
type MatchState string

const (
	MatchActive MatchState = "active"
	MatchEnded  MatchState = "ended"
)

// MatchSession is the durable, privacy-minimized summary of one local match.
type MatchSession struct {
	ID                string     `json:"id"`
	Fingerprint       string     `json:"-"`
	State             MatchState `json:"state"`
	StartedAt         time.Time  `json:"started_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	LastSeenAt        time.Time  `json:"last_seen_at"`
	EndedAt           *time.Time `json:"ended_at,omitempty"`
	EndReason         string     `json:"end_reason,omitempty"`
	EndConfidence     float64    `json:"end_confidence,omitempty"`
	StartGameTime     float64    `json:"start_game_time_seconds"`
	LastGameTime      float64    `json:"last_game_time_seconds"`
	PlayerRiotID      string     `json:"player_riot_id,omitempty"`
	ChampionName      string     `json:"champion_name,omitempty"`
	Team              string     `json:"team,omitempty"`
	Position          string     `json:"position,omitempty"`
	MapNumber         int        `json:"map_number,omitempty"`
	QueueID           int        `json:"queue_id,omitempty"`
	MapName           string     `json:"map_name,omitempty"`
	GameMode          string     `json:"game_mode,omitempty"`
	GameVersion       string     `json:"game_version,omitempty"`
	LastEventSequence uint64     `json:"last_event_sequence"`
	// PlayerCheckpoint is stored transactionally with this lifecycle checkpoint,
	// but is omitted from raw session APIs in favor of identity-free history DTOs.
	PlayerCheckpoint *MatchPlayerCheckpoint `json:"-"`

	// Outage fields are durable detector checkpoints. They are intentionally
	// omitted from the public history summary; snapshot.Connection is the API
	// contract while these fields make restart recovery exact.
	ConnectionState  ConnectionState `json:"-"`
	OutageStartedAt  time.Time       `json:"-"`
	LastFailureClass FetchErrorClass `json:"-"`
	LastFailureAt    time.Time       `json:"-"`
	UnavailableSince time.Time       `json:"-"`
}

// EventKind is a stable semantic lifecycle/event name.
type EventKind string

const (
	EventMatchStarted       EventKind = "match_started"
	EventConnectionLost     EventKind = "connection_lost"
	EventConnectionRestored EventKind = "connection_restored"
	EventMatchEnded         EventKind = "match_ended"
	EventPlayerDied         EventKind = "player_died"
	EventInventoryChanged   EventKind = "inventory_changed"
	EventItemPurchased      EventKind = "item_purchased"
	EventReturnedToBase     EventKind = "returned_to_base"
)

// EventSource makes authoritative observation distinct from inference.
type EventSource string

const (
	EventSourceObserved EventSource = "observed"
	EventSourceInferred EventSource = "inferred"
)

// SemanticEvent is the durable event contract used by snapshots and history APIs.
type SemanticEvent struct {
	ID              string          `json:"id"`
	SessionID       string          `json:"session_id"`
	Sequence        uint64          `json:"sequence"`
	DedupeKey       string          `json:"dedupe_key"`
	Kind            EventKind       `json:"kind"`
	OccurredAt      time.Time       `json:"occurred_at"`
	ObservedAt      time.Time       `json:"observed_at"`
	GameTimeSeconds float64         `json:"game_time_seconds"`
	Source          EventSource     `json:"source"`
	Confidence      float64         `json:"confidence"`
	Payload         json.RawMessage `json:"payload,omitempty"`
}

// LifecycleRecovery is the bounded durable state required to resume lifecycle detection.
type LifecycleRecovery struct {
	Session          *MatchSession
	Connection       ConnectionState
	OutageStartedAt  *time.Time
	LastFailureClass FetchErrorClass
	LastFailureAt    *time.Time
	UnavailableSince *time.Time
	RecentEvents     []SemanticEvent
}
