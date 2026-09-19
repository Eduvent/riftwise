package recommendation

import (
	"time"

	"lolengine/internal/domain"
	"lolengine/internal/itemization"
)

// Stabilizer owns the temporal state that the pure item scorer intentionally does not.
type Stabilizer struct {
	currentTarget   int
	committedAt     time.Time
	challenger      int
	challengerSince time.Time
	staleSince      time.Time
	rulesKey        string
	lastGameTime    float64
	sessionID       string
}

func NewStabilizer() *Stabilizer {
	return &Stabilizer{}
}

// Reset clears temporal evidence after a rule or personalization revision.
func (stabilizer *Stabilizer) Reset() {
	if stabilizer != nil {
		stabilizer.reset()
	}
}

// Apply keeps the incumbent until margin, confirmation, and minimum-hold gates pass.
func (stabilizer *Stabilizer) Apply(now time.Time, snapshot domain.Snapshot, candidate itemization.Result) itemization.Result {
	if stabilizer == nil {
		return candidate
	}
	sessionID := ""
	if snapshot.Session != nil {
		sessionID = snapshot.Session.ID
	}
	if sessionID != "" && stabilizer.sessionID != "" && sessionID != stabilizer.sessionID {
		stabilizer.reset()
	}
	if sessionID != "" {
		stabilizer.sessionID = sessionID
	}
	if snapshot.Status == domain.StatusStale {
		// A stale snapshot may be a brief Live Client outage in the same match.
		// Pause advice without discarding the incumbent; a later game-time reset
		// still detects a direct transition into a new match.
		stabilizer.pause(now)
		return candidate
	}
	stabilizer.resume(now)
	if snapshot.Status != domain.StatusInGame || snapshot.Game == nil || candidate.TargetItem == nil || len(candidate.DecisionPlans) == 0 || candidate.Rules.Legacy {
		stabilizer.reset()
		return candidate
	}
	gameTime := snapshot.Game.GameTimeSeconds
	rulesKey := candidate.Rules.Version + "@" + candidate.Rules.Patch
	if stabilizer.currentTarget == 0 || stabilizer.rulesKey != rulesKey || gameTime+30 < stabilizer.lastGameTime {
		stabilizer.reset()
		stabilizer.sessionID = sessionID
		stabilizer.rulesKey = rulesKey
		stabilizer.lastGameTime = gameTime
		return stabilizer.commit(now, candidate.TargetItem.ID, candidate)
	}
	stabilizer.lastGameTime = gameTime
	rawTarget := candidate.TargetItem.ID
	if rawTarget == stabilizer.currentTarget {
		stabilizer.challenger = 0
		stabilizer.challengerSince = time.Time{}
		itemization.SelectTarget(&candidate, stabilizer.currentTarget)
		return candidate
	}
	incumbent, incumbentValid := candidate.DecisionPlans[stabilizer.currentTarget]
	challenger, challengerValid := candidate.DecisionPlans[rawTarget]
	if !challengerValid {
		return candidate
	}
	if !incumbentValid || ownsItem(snapshot.Game.ActivePlayer.Items, stabilizer.currentTarget) || challenger.Ranked.Critical {
		return stabilizer.commit(now, rawTarget, candidate)
	}
	policy := candidate.Stability
	if challenger.Ranked.Score-incumbent.Ranked.Score < policy.SwitchMargin {
		stabilizer.challenger = 0
		stabilizer.challengerSince = time.Time{}
		itemization.SelectTarget(&candidate, stabilizer.currentTarget)
		return candidate
	}
	if stabilizer.challenger != rawTarget {
		stabilizer.challenger = rawTarget
		stabilizer.challengerSince = now
	}
	confirmed := now.Sub(stabilizer.challengerSince) >= time.Duration(policy.ConfirmationSeconds)*time.Second
	heldLongEnough := now.Sub(stabilizer.committedAt) >= time.Duration(policy.MinimumHoldSeconds)*time.Second
	if confirmed && heldLongEnough {
		return stabilizer.commit(now, rawTarget, candidate)
	}
	itemization.SelectTarget(&candidate, stabilizer.currentTarget)
	return candidate
}

func (stabilizer *Stabilizer) commit(now time.Time, targetID int, candidate itemization.Result) itemization.Result {
	stabilizer.currentTarget = targetID
	stabilizer.committedAt = now
	stabilizer.challenger = 0
	stabilizer.challengerSince = time.Time{}
	itemization.SelectTarget(&candidate, targetID)
	return candidate
}

func (stabilizer *Stabilizer) reset() {
	stabilizer.currentTarget = 0
	stabilizer.committedAt = time.Time{}
	stabilizer.challenger = 0
	stabilizer.challengerSince = time.Time{}
	stabilizer.staleSince = time.Time{}
	stabilizer.rulesKey = ""
	stabilizer.lastGameTime = 0
	stabilizer.sessionID = ""
}

func (stabilizer *Stabilizer) pause(now time.Time) {
	stabilizer.challenger = 0
	stabilizer.challengerSince = time.Time{}
	if stabilizer.staleSince.IsZero() {
		stabilizer.staleSince = now
	}
}

func (stabilizer *Stabilizer) resume(now time.Time) {
	if stabilizer.staleSince.IsZero() {
		return
	}
	pausedFor := now.Sub(stabilizer.staleSince)
	if pausedFor > 0 && !stabilizer.committedAt.IsZero() {
		stabilizer.committedAt = stabilizer.committedAt.Add(pausedFor)
	}
	stabilizer.staleSince = time.Time{}
}

func ownsItem(items []domain.Item, itemID int) bool {
	for _, item := range items {
		if item.ID == itemID {
			return true
		}
	}
	return false
}
