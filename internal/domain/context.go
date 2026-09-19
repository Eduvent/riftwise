package domain

// Phase is the deterministic time segment used by patch item rules.
type Phase string

const (
	PhaseEarly Phase = "early"
	PhaseMid   Phase = "mid"
	PhaseLate  Phase = "late"
)

// Strategy is an explicit user preference. It is never inferred from live telemetry.
type Strategy string

const (
	StrategyBalanced  Strategy = "balanced"
	StrategySplitPush Strategy = "split_push"
	StrategyTeamfight Strategy = "teamfight"
)

// Valid reports whether the strategy is part of the stable configuration contract.
func (strategy Strategy) Valid() bool {
	switch strategy {
	case StrategyBalanced, StrategySplitPush, StrategyTeamfight:
		return true
	default:
		return false
	}
}
