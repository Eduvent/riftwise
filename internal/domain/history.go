package domain

import (
	"encoding/json"
	"time"
)

const (
	MatchSummarySchemaVersion           = 1
	RecommendationEvidenceSchemaVersion = 1
)

// InventoryCount is a slot-independent item stack used by durable history.
type InventoryCount struct {
	ItemID int `json:"item_id"`
	Count  int `json:"count"`
}

// MatchPlayerCheckpoint is the last successfully observed local-player state.
// It intentionally contains no player or opponent identity.
type MatchPlayerCheckpoint struct {
	MatchID         string           `json:"match_id"`
	ObservedAt      time.Time        `json:"observed_at"`
	GameTimeSeconds float64          `json:"game_time_seconds"`
	ChampionName    string           `json:"champion_name"`
	Role            Role             `json:"role,omitempty"`
	RoleKnown       bool             `json:"role_known"`
	Level           int              `json:"level"`
	Kills           int              `json:"kills"`
	Deaths          int              `json:"deaths"`
	Assists         int              `json:"assists"`
	CreepScore      int              `json:"creep_score"`
	Inventory       []InventoryCount `json:"inventory"`
}

// EvidenceItem is an identity-free item reference retained with a decision.
type EvidenceItem struct {
	ID   int    `json:"id"`
	Name string `json:"name,omitempty"`
}

// EvidenceChoice is one stable ranked recommendation option.
type EvidenceChoice struct {
	Rank        int          `json:"rank"`
	Item        EvidenceItem `json:"item"`
	Score       int          `json:"score,omitempty"`
	Category    string       `json:"category,omitempty"`
	ReasonCodes []string     `json:"reason_codes,omitempty"`
	Critical    bool         `json:"critical,omitempty"`
}

// RuleProvenance records the validated rule source without volatile errors.
type RuleProvenance struct {
	Version  string `json:"version,omitempty"`
	Patch    string `json:"patch,omitempty"`
	Source   string `json:"source,omitempty"`
	Fallback bool   `json:"fallback"`
	Legacy   bool   `json:"legacy"`
}

// ProfileProvenance records generic champion/role profile resolution.
type ProfileProvenance struct {
	Level          string   `json:"level,omitempty"`
	ProfileID      string   `json:"profile_id,omitempty"`
	FamilyIDs      []string `json:"family_ids,omitempty"`
	RulesetVersion string   `json:"ruleset_version,omitempty"`
	Patch          string   `json:"patch,omitempty"`
	ChampionID     string   `json:"champion_id,omitempty"`
	Role           Role     `json:"role,omitempty"`
	ChampionTag    string   `json:"champion_tag,omitempty"`
	Fallback       bool     `json:"fallback"`
	ConfidenceCap  float64  `json:"confidence_cap,omitempty"`
}

// PersonalizationProvenance records only deterministic adjustment codes. It
// contains no raw request, participant, or opponent identifiers.
type PersonalizationProvenance struct {
	Version      uint64   `json:"version"`
	Style        string   `json:"style"`
	ReasonCodes  []string `json:"reason_codes,omitempty"`
	WarningCodes []string `json:"warning_codes,omitempty"`
}

// RecommendationEvidence is the stable, versioned subset of an itemization
// result needed to explain history. Generated timestamps, revisions, warnings,
// and participant context are deliberately excluded.
type RecommendationEvidence struct {
	SchemaVersion   int                        `json:"schema_version"`
	Status          string                     `json:"status"`
	Patch           string                     `json:"patch,omitempty"`
	Phase           Phase                      `json:"phase,omitempty"`
	Strategy        Strategy                   `json:"strategy,omitempty"`
	Target          *EvidenceItem              `json:"target,omitempty"`
	Component       *EvidenceItem              `json:"component,omitempty"`
	RankedChoices   []EvidenceChoice           `json:"ranked_choices,omitempty"`
	Confidence      float64                    `json:"confidence,omitempty"`
	TraitConfidence float64                    `json:"trait_confidence,omitempty"`
	Rules           RuleProvenance             `json:"rules"`
	Profile         *ProfileProvenance         `json:"profile,omitempty"`
	ReasonCodes     []string                   `json:"reason_codes,omitempty"`
	Personalization *PersonalizationProvenance `json:"personalization,omitempty"`
}

type SummaryCompletion string

const (
	SummaryActive             SummaryCompletion = "active"
	SummaryCompleteTerminal   SummaryCompletion = "complete_terminal"
	SummaryIncompleteInferred SummaryCompletion = "incomplete_inferred"
)

type DurationBasis string

const (
	DurationUnknown               DurationBasis = "unknown"
	DurationTerminalGameClock     DurationBasis = "terminal_game_clock"
	DurationLastObservedGameClock DurationBasis = "last_observed_game_clock"
	DurationObservedWindow        DurationBasis = "observed_window"
)

type MatchOutcome string

const MatchOutcomeUnknown MatchOutcome = "unknown"

type OutcomeSummary struct {
	Value  MatchOutcome `json:"value"`
	Source string       `json:"source"`
}

type DurationSummary struct {
	Seconds    float64       `json:"seconds"`
	Basis      DurationBasis `json:"basis"`
	LowerBound bool          `json:"lower_bound"`
}

type TerminalStatsQuality string

const (
	StatsTerminalSnapshot TerminalStatsQuality = "terminal_snapshot"
	StatsLastObserved     TerminalStatsQuality = "last_observed"
	StatsUnavailable      TerminalStatsQuality = "unavailable"
)

type TerminalStats struct {
	ObservedAt      time.Time            `json:"observed_at"`
	GameTimeSeconds float64              `json:"game_time_seconds"`
	Level           int                  `json:"level"`
	Kills           int                  `json:"kills"`
	Deaths          int                  `json:"deaths"`
	Assists         int                  `json:"assists"`
	CreepScore      int                  `json:"creep_score"`
	Quality         TerminalStatsQuality `json:"quality"`
}

type HistoryEventBase struct {
	EventID         string      `json:"event_id"`
	Sequence        uint64      `json:"sequence"`
	OccurredAt      time.Time   `json:"occurred_at"`
	GameTimeSeconds float64     `json:"game_time_seconds"`
	Source          EventSource `json:"source"`
	Confidence      float64     `json:"confidence"`
}

type InventoryChangeSummary struct {
	HistoryEventBase
	Added                    []InventoryCount `json:"added,omitempty"`
	Removed                  []InventoryCount `json:"removed,omitempty"`
	Classification           string           `json:"classification,omitempty"`
	ClassificationSource     EventSource      `json:"classification_source,omitempty"`
	ClassificationConfidence float64          `json:"classification_confidence,omitempty"`
}

type InferredAcquisitionSummary struct {
	HistoryEventBase
	Items            []InventoryCount `json:"items,omitempty"`
	Reason           string           `json:"reason,omitempty"`
	InventoryEventID string           `json:"inventory_event_id,omitempty"`
	PurchaseEventID  string           `json:"purchase_event_id,omitempty"`
}

type DecisionChangeSummary struct {
	EventID                 string                  `json:"event_id"`
	Sequence                uint64                  `json:"sequence"`
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

type TerminalSummary struct {
	Reason     string      `json:"reason,omitempty"`
	Source     EventSource `json:"source,omitempty"`
	Confidence float64     `json:"confidence,omitempty"`
}

type SummaryCaveat string

const (
	CaveatInferredPurchase          SummaryCaveat = "inferred_purchase"
	CaveatInferredBase              SummaryCaveat = "inferred_return_to_base"
	CaveatIncompleteSession         SummaryCaveat = "incomplete_session"
	CaveatDurationLowerBound        SummaryCaveat = "duration_lower_bound"
	CaveatTerminalStatsLastObserved SummaryCaveat = "terminal_stats_last_observed"
	CaveatLegacyMissingStats        SummaryCaveat = "legacy_missing_stats"
	CaveatLegacyMissingEvidence     SummaryCaveat = "legacy_missing_recommendation_evidence"
	CaveatMalformedEventPayload     SummaryCaveat = "malformed_event_payload"
	CaveatSourceIntegrityDegraded   SummaryCaveat = "source_integrity_degraded"
	CaveatOutcomeUnavailable        SummaryCaveat = "outcome_unavailable"
)

type SummaryIntegrityIssue struct {
	Code    string `json:"code"`
	EventID string `json:"event_id,omitempty"`
}

// SummarySourceRevision identifies the exact authoritative inputs used by a
// materialized summary.
type SummarySourceRevision struct {
	SessionUpdatedAtNS     int64  `json:"session_updated_at_ns"`
	PlayerObservedAtNS     int64  `json:"player_observed_at_ns"`
	LifecycleSequence      uint64 `json:"lifecycle_sequence"`
	DecisionSequence       uint64 `json:"decision_sequence"`
	DecisionCheckpointAtNS int64  `json:"decision_checkpoint_at_ns"`
}

// MatchSummarySource is loaded in one consistent storage transaction.
type MatchSummarySource struct {
	Session                     MatchSession
	PlayerCheckpoint            *MatchPlayerCheckpoint
	LifecycleEvents             []SemanticEvent
	DecisionEvents              []DecisionEvent
	FinalRecommendationEvidence *RecommendationEvidence
	DecisionCheckpointUpdatedAt time.Time
	Revision                    SummarySourceRevision
}

// MatchSummary is an identity-free, rebuildable view of one locally observed match.
type MatchSummary struct {
	SchemaVersion       int                          `json:"schema_version"`
	MatchID             string                       `json:"match_id"`
	SourceRevision      SummarySourceRevision        `json:"source_revision"`
	Digest              string                       `json:"digest"`
	BuiltAt             time.Time                    `json:"built_at"`
	ChampionName        string                       `json:"champion_name,omitempty"`
	Role                Role                         `json:"role,omitempty"`
	RoleKnown           bool                         `json:"role_known"`
	Completion          SummaryCompletion            `json:"completion"`
	StartedAt           time.Time                    `json:"started_at"`
	EndedAt             *time.Time                   `json:"ended_at,omitempty"`
	Duration            DurationSummary              `json:"duration"`
	Stats               *TerminalStats               `json:"stats,omitempty"`
	FinalInventory      []InventoryCount             `json:"final_inventory,omitempty"`
	InventoryChanges    []InventoryChangeSummary     `json:"inventory_changes,omitempty"`
	InferredPurchases   []InferredAcquisitionSummary `json:"inferred_purchases,omitempty"`
	InferredBaseReturns []InferredAcquisitionSummary `json:"inferred_base_returns,omitempty"`
	DecisionChanges     []DecisionChangeSummary      `json:"decision_changes,omitempty"`
	FinalRecommendation *RecommendationEvidence      `json:"final_recommendation,omitempty"`
	Terminal            TerminalSummary              `json:"terminal"`
	Outcome             OutcomeSummary               `json:"outcome"`
	Caveats             []SummaryCaveat              `json:"caveats,omitempty"`
	IntegrityIssues     []SummaryIntegrityIssue      `json:"integrity_issues,omitempty"`
}

// HistoryCursor is the stable newest-first keyset boundary.
type HistoryCursor struct {
	StartedAtNS int64  `json:"started_at_ns"`
	MatchID     string `json:"match_id"`
}

type HistoryPage struct {
	Matches    []MatchSummary `json:"matches"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

type HistoryPreferences struct {
	RetentionDays       *int      `json:"retention_days,omitempty"`
	MaxCompletedMatches *int      `json:"max_completed_matches,omitempty"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type HistoryExport struct {
	SchemaVersion int            `json:"schema_version"`
	ExportedAt    time.Time      `json:"exported_at"`
	Scope         string         `json:"scope"`
	Matches       []MatchSummary `json:"matches"`
}

// CloneRecommendationEvidence isolates mutable slices and pointers.
func CloneRecommendationEvidence(value *RecommendationEvidence) *RecommendationEvidence {
	if value == nil {
		return nil
	}
	payload, _ := json.Marshal(value)
	var clone RecommendationEvidence
	_ = json.Unmarshal(payload, &clone)
	return &clone
}
