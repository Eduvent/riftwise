package itemrules

import (
	"lolengine/internal/catalog"
	"lolengine/internal/domain"
)

const CurrentSchemaVersion = 2

const (
	SourceExternalExact         = "external_exact"
	SourceExternalFallback      = "external_patch_fallback"
	SourceExternalLastKnownGood = "external_last_known_good"
	SourceLegacyBuiltin         = "legacy_builtin"
)

// Signal is a normalized match need in the inclusive range 0..1000.
type Signal string

const (
	SignalPhysical     Signal = "physical"
	SignalMagic        Signal = "magic"
	SignalHealing      Signal = "healing"
	SignalShielding    Signal = "shielding"
	SignalCrowdControl Signal = "crowd_control"
	SignalSlows        Signal = "slows"
	SignalArmor        Signal = "armor"
	SignalMagicResist  Signal = "magic_resist"
	SignalBurst        Signal = "burst"
	SignalSustained    Signal = "sustained"
	SignalLanePressure Signal = "lane_pressure"
	SignalAhead        Signal = "ahead"
	SignalBehind       Signal = "behind"
)

const (
	CategoryBoots       = "boots"
	CategoryCore        = "core"
	CategorySituational = "situational"
)

// TraitProfile contains explicit, patch-versioned champion or item traits.
// It intentionally does not depend on localized Data Dragon descriptions.
type TraitProfile struct {
	Healing      int `json:"healing,omitempty"`
	Shielding    int `json:"shielding,omitempty"`
	CrowdControl int `json:"crowd_control,omitempty"`
	Slows        int `json:"slows,omitempty"`
	Burst        int `json:"burst,omitempty"`
	Sustained    int `json:"sustained,omitempty"`
}

// PhaseBoundaries defines the first second of mid and late game.
type PhaseBoundaries struct {
	EarlyEndSeconds int `json:"early_end_seconds"`
	MidEndSeconds   int `json:"mid_end_seconds"`
}

// ContextTuning controls lane/team blending and confidence degradation.
type ContextTuning struct {
	LaneWeightByPhase    map[domain.Phase]int `json:"lane_weight_by_phase"`
	MissingTraitPenalty  int                  `json:"missing_trait_penalty"`
	ArmorFullScale       int                  `json:"armor_full_scale"`
	MagicResistFullScale int                  `json:"magic_resist_full_scale,omitempty"`
}

// StabilityPolicy controls coordinator-owned temporal hysteresis.
type StabilityPolicy struct {
	SwitchMargin        int `json:"switch_margin"`
	ConfirmationSeconds int `json:"confirmation_seconds"`
	MinimumHoldSeconds  int `json:"minimum_hold_seconds"`
	CriticalThreshold   int `json:"critical_threshold"`
}

// ScoreAdjustment adds deterministic score when a normalized need crosses a threshold.
type ScoreAdjustment struct {
	Signal     Signal `json:"signal"`
	Minimum    int    `json:"minimum"`
	Add        int    `json:"add"`
	ReasonCode string `json:"reason_code"`
	Critical   bool   `json:"critical,omitempty"`
}

// CandidateRule is one patch-specific item option.
type CandidateRule struct {
	ID                   string                  `json:"id"`
	ItemID               int                     `json:"item_id"`
	Category             string                  `json:"category"`
	MutualExclusionGroup string                  `json:"mutual_exclusion_group,omitempty"`
	BaseScore            int                     `json:"base_score"`
	Covers               []Signal                `json:"covers,omitempty"`
	MinPhase             domain.Phase            `json:"min_phase,omitempty"`
	MaxPhase             domain.Phase            `json:"max_phase,omitempty"`
	PhaseScores          map[domain.Phase]int    `json:"phase_scores,omitempty"`
	StrategyScores       map[domain.Strategy]int `json:"strategy_scores,omitempty"`
	Adjustments          []ScoreAdjustment       `json:"adjustments,omitempty"`
}

// RuleSet is one strict Garen rules document for one exact patch.
type RuleSet struct {
	SchemaVersion    int                     `json:"schema_version"`
	RulesetVersion   string                  `json:"ruleset_version"`
	Champion         string                  `json:"champion"`
	Patch            string                  `json:"patch"`
	AlternativeCount int                     `json:"alternative_count"`
	Phases           PhaseBoundaries         `json:"phases"`
	Context          ContextTuning           `json:"context"`
	Stability        StabilityPolicy         `json:"stability"`
	ChampionTraits   map[string]TraitProfile `json:"champion_traits"`
	ItemTraits       map[int]TraitProfile    `json:"item_traits"`
	Candidates       []CandidateRule         `json:"candidates"`
}

// Status is safe to expose in recommendation, health, and dashboard JSON.
type Status struct {
	Ready     bool   `json:"ready"`
	Version   string `json:"version,omitempty"`
	Patch     string `json:"patch,omitempty"`
	Source    string `json:"source"`
	Fallback  bool   `json:"fallback"`
	Legacy    bool   `json:"legacy"`
	LastError string `json:"last_error,omitempty"`
}

// Resolution is either a validated external ruleset or an explicit legacy fallback.
type Resolution struct {
	Rules  RuleSet
	Status Status
	Legacy bool
}

// Provider resolves rules against the active catalog patch.
type Provider interface {
	Resolve(string, catalog.Catalog) Resolution
}
