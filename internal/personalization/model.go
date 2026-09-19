// Package personalization implements local, deterministic and identity-free
// preference and recommendation-feedback adjustments.
package personalization

import (
	"errors"
	"time"

	"lolengine/internal/domain"
)

const (
	MaximumPreferredItems = 12
	MaximumAvoidedItems   = 12
	MaximumClientIDLength = 96
	DefaultMinimumSamples = 3
	DefaultFeedbackCap    = 9
	PreferredItemBonus    = 8
)

var (
	ErrUnavailable        = errors.New("personalization is unavailable")
	ErrInvalidPreference  = errors.New("invalid personalization preference")
	ErrFeedbackDisabled   = errors.New("recommendation feedback is disabled")
	ErrFeedbackConflict   = errors.New("client feedback id conflicts with an existing record")
	ErrDecisionNotFound   = errors.New("decision event was not found")
	ErrChoiceNotVisible   = errors.New("feedback item was not a visible ranked choice")
	ErrDeleteConfirmation = errors.New("personalization feedback deletion requires confirmation")
)

const DeleteFeedbackConfirmation = "DELETE_PERSONALIZATION_FEEDBACK"

type Style string

const (
	StyleSafe       Style = "safe"
	StyleBalanced   Style = "balanced"
	StyleAggressive Style = "aggressive"
)

func (style Style) Valid() bool {
	return style == StyleSafe || style == StyleBalanced || style == StyleAggressive
}

type FeedbackValue string

const (
	FeedbackUseful    FeedbackValue = "useful"
	FeedbackNotUseful FeedbackValue = "not_useful"
)

func (value FeedbackValue) Valid() bool {
	return value == FeedbackUseful || value == FeedbackNotUseful
}

const (
	ReasonStyleSafe                 = "personalization_style_safe"
	ReasonStyleAggressive           = "personalization_style_aggressive"
	ReasonPreferredItem             = "personalization_preferred_item"
	ReasonAvoidedItem               = "personalization_avoided_item"
	ReasonAvoidanceCriticalOverride = "personalization_avoidance_critical_override"
	ReasonFeedbackUseful            = "personalization_feedback_useful"
	ReasonFeedbackNotUseful         = "personalization_feedback_not_useful"
	WarningTooFewChoices            = "personalization_insufficient_choices"
)

type Preferences struct {
	Style            Style     `json:"style"`
	PreferredItemIDs []int     `json:"preferred_item_ids"`
	AvoidedItemIDs   []int     `json:"avoided_item_ids"`
	FeedbackEnabled  bool      `json:"feedback_enabled"`
	Version          uint64    `json:"version"`
	UpdatedAt        time.Time `json:"updated_at"`
	ResetAt          time.Time `json:"reset_at,omitempty"`
}

func (preferences Preferences) Default() bool {
	return preferences.Style == StyleBalanced && len(preferences.PreferredItemIDs) == 0 &&
		len(preferences.AvoidedItemIDs) == 0 && !preferences.FeedbackEnabled
}

type PreferenceUpdate struct {
	Style            Style `json:"style"`
	PreferredItemIDs []int `json:"preferred_item_ids"`
	AvoidedItemIDs   []int `json:"avoided_item_ids"`
	FeedbackEnabled  bool  `json:"feedback_enabled"`
}

type ContextKey struct {
	ChampionID     string
	Role           domain.Role
	Phase          domain.Phase
	Strategy       domain.Strategy
	Patch          string
	ProfileID      string
	ProfileVersion string
	RulesVersion   string
}

type FeedbackEffect struct {
	Samples    int    `json:"samples"`
	Useful     int    `json:"useful"`
	NotUseful  int    `json:"not_useful"`
	Adjustment int    `json:"adjustment"`
	ReasonCode string `json:"reason_code"`
}

type Snapshot struct {
	Preferences Preferences            `json:"preferences"`
	Feedback    map[int]FeedbackEffect `json:"feedback,omitempty"`
}

func (snapshot Snapshot) Default() bool { return snapshot.Preferences.Default() }

type Applied struct {
	Enabled      bool     `json:"enabled"`
	Version      uint64   `json:"version"`
	Style        Style    `json:"style"`
	ReasonCodes  []string `json:"reason_codes,omitempty"`
	WarningCodes []string `json:"warning_codes,omitempty"`
}

type Status struct {
	Available       bool   `json:"available"`
	Enabled         bool   `json:"enabled"`
	FeedbackEnabled bool   `json:"feedback_enabled"`
	Style           Style  `json:"style"`
	Version         uint64 `json:"version"`
	Revision        uint64 `json:"revision"`
	State           string `json:"state"`
	LastError       string `json:"last_error,omitempty"`
}

type FeedbackSubmission struct {
	ClientFeedbackID string        `json:"client_feedback_id"`
	DecisionEventID  string        `json:"decision_event_id"`
	ItemID           int           `json:"item_id"`
	Value            FeedbackValue `json:"value"`
}

type FeedbackRecord struct {
	ClientFeedbackID string          `json:"client_feedback_id"`
	DecisionEventID  string          `json:"decision_event_id"`
	SessionID        string          `json:"-"`
	DecisionSequence uint64          `json:"decision_sequence"`
	ProfileID        string          `json:"profile_id,omitempty"`
	ProfileVersion   string          `json:"profile_version,omitempty"`
	RulesVersion     string          `json:"rules_version,omitempty"`
	RulesSource      string          `json:"rules_source,omitempty"`
	Patch            string          `json:"patch"`
	ChampionID       string          `json:"champion_id"`
	Role             domain.Role     `json:"role,omitempty"`
	Stage            domain.Phase    `json:"stage"`
	Strategy         domain.Strategy `json:"strategy"`
	ItemID           int             `json:"item_id"`
	Value            FeedbackValue   `json:"value"`
	CreatedAt        time.Time       `json:"created_at"`
}

type FeedbackDecision struct {
	EventID    string
	SessionID  string
	Sequence   uint64
	Evidence   domain.RecommendationEvidence
	ChampionID string
	Role       domain.Role
}

// FeedbackEligibility is the exact, current durable recommendation context
// against which a new feedback sample may be accepted.
type FeedbackEligibility struct {
	EventID        string
	SessionID      string
	ChampionID     string
	Role           domain.Role
	Evidence       domain.RecommendationEvidence
	VisibleItemIDs []int
}

type FeedbackEligibilityProvider interface {
	CurrentFeedbackEligibility() (FeedbackEligibility, bool)
}

type FeedbackResult struct {
	Inserted bool           `json:"inserted"`
	Record   FeedbackRecord `json:"record"`
}

type ItemFeedbackSummary struct {
	ItemID    int `json:"item_id"`
	Useful    int `json:"useful"`
	NotUseful int `json:"not_useful"`
}

type FeedbackSummary struct {
	Enabled           bool                  `json:"enabled"`
	MinimumSamples    int                   `json:"minimum_samples"`
	MaximumAdjustment int                   `json:"maximum_adjustment"`
	Total             int                   `json:"total"`
	Useful            int                   `json:"useful"`
	NotUseful         int                   `json:"not_useful"`
	Items             []ItemFeedbackSummary `json:"items"`
}

type Export struct {
	SchemaVersion int             `json:"schema_version"`
	ExportedAt    time.Time       `json:"exported_at"`
	Preferences   Preferences     `json:"preferences"`
	Feedback      FeedbackSummary `json:"feedback"`
}

type ItemOption struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type Options struct {
	Patch string       `json:"patch"`
	Items []ItemOption `json:"items"`
}
