package domain

import "time"

// DecisionKind names a stable, user-relevant recommendation transition.
type DecisionKind string

const (
	DecisionTargetChanged            DecisionKind = "target_changed"
	DecisionComponentChanged         DecisionKind = "component_changed"
	DecisionComponentAffordable      DecisionKind = "component_affordable"
	DecisionCriticalCounterActivated DecisionKind = "critical_counter_activated"
	DecisionInventoryFull            DecisionKind = "inventory_full"
	DecisionDataStale                DecisionKind = "data_stale"
	DecisionRulesUnavailable         DecisionKind = "rules_unavailable"
	DecisionConnectionRestored       DecisionKind = "connection_restored"
	DecisionReturnToBaseOptions      DecisionKind = "return_to_base_options"
)

// DecisionImportance lets presentation layers reserve audible alerts for
// transitions that merit interruption while keeping every event available as text.
type DecisionImportance string

const (
	DecisionInformational DecisionImportance = "informational"
	DecisionImportant     DecisionImportance = "important"
	DecisionCritical      DecisionImportance = "critical"
)

const (
	DecisionEventStatusReady       = "ready"
	DecisionEventStatusUnavailable = "unavailable"
	DecisionEventStatusError       = "error"
)

// DecisionEvent is the durable, neutral notification contract. It describes
// what changed without instructing the player to take an action.
type DecisionEvent struct {
	ID                      string                  `json:"id"`
	SessionID               string                  `json:"session_id"`
	Sequence                uint64                  `json:"sequence"`
	DedupeKey               string                  `json:"dedupe_key"`
	Kind                    DecisionKind            `json:"kind"`
	Importance              DecisionImportance      `json:"importance"`
	OccurredAt              time.Time               `json:"occurred_at"`
	GameTimeSeconds         float64                 `json:"game_time_seconds"`
	PreviousSummary         string                  `json:"previous_summary,omitempty"`
	CurrentSummary          string                  `json:"current_summary"`
	ReasonCodes             []string                `json:"reason_codes,omitempty"`
	RelatedLifecycleEventID string                  `json:"related_lifecycle_event_id,omitempty"`
	Evidence                *RecommendationEvidence `json:"evidence,omitempty"`
}

// DecisionEventAvailability exposes the durable alert subsystem without
// conflating it with whether a recommendation itself is actionable.
type DecisionEventAvailability struct {
	Available bool           `json:"available"`
	Status    string         `json:"status"`
	Latest    *DecisionEvent `json:"latest,omitempty"`
}
