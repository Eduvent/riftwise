package matchsync

import (
	"strings"
	"time"

	"lolengine/internal/domain"
	"lolengine/internal/riotapi"
	"lolengine/internal/storage"
)

const reconciliationSlack = 5 * time.Minute

// LocalCandidate carries matching evidence not represented by the original
// Live Client session schema. Missing optional evidence never gets invented.
type LocalCandidate struct {
	Session         domain.MatchSession
	ExternalMatchID string
	Platform        riotapi.Platform
	QueueID         int
}

// ReconcileSessions is deterministic and intentionally permits several local
// reconnect segments to link to one official match. Statistics later count the
// account-match fact, not these links.
func ReconcileSessions(accountID string, locals []LocalCandidate, officials []storage.RiotStoredMatch, observedAt time.Time) []storage.SessionMatchLink {
	byID := make(map[string]storage.RiotStoredMatch, len(officials))
	for _, official := range officials {
		byID[official.Match.ID] = official
	}
	result := make([]storage.SessionMatchLink, 0, len(locals))
	for _, local := range locals {
		link := storage.SessionMatchLink{LocalSessionID: local.Session.ID, AccountID: accountID, UpdatedAt: observedAt.UTC()}
		if externalID := strings.TrimSpace(local.ExternalMatchID); externalID != "" {
			if _, ok := byID[externalID]; ok {
				link.OfficialMatchID, link.State, link.Reason, link.Confidence = externalID, storage.SessionLinkLinked, "exact_external_match_id", 1
			} else {
				link.State, link.Reason = storage.SessionLinkPending, "exact_external_match_not_fetched"
			}
			result = append(result, link)
			continue
		}
		if local.Session.EndedAt == nil || local.Session.StartedAt.IsZero() || strings.TrimSpace(local.Session.ChampionName) == "" ||
			local.QueueID <= 0 || local.Session.MapNumber <= 0 {
			link.State, link.Reason = storage.SessionLinkPending, "insufficient_local_evidence"
			result = append(result, link)
			continue
		}
		if _, ok := local.Platform.Region(); !ok {
			link.State, link.Reason = storage.SessionLinkPending, "insufficient_local_evidence"
			result = append(result, link)
			continue
		}
		var candidates []string
		for _, official := range officials {
			match := official.Match
			if match.Platform != local.Platform {
				continue
			}
			if match.QueueID != local.QueueID {
				continue
			}
			if match.MapID != local.Session.MapNumber {
				continue
			}
			if !strings.EqualFold(strings.TrimSpace(match.Player.ChampionName), strings.TrimSpace(local.Session.ChampionName)) {
				continue
			}
			if match.StartedAt.After(local.Session.EndedAt.Add(reconciliationSlack)) || match.EndedAt.Before(local.Session.StartedAt.Add(-reconciliationSlack)) {
				continue
			}
			candidates = append(candidates, match.ID)
		}
		switch len(candidates) {
		case 0:
			link.State, link.Reason = storage.SessionLinkPending, "no_unique_interval_match"
		case 1:
			link.OfficialMatchID, link.State, link.Reason, link.Confidence = candidates[0], storage.SessionLinkLinked, "unique_interval_champion_queue_map_platform", .9
		default:
			link.State, link.Reason = storage.SessionLinkAmbiguous, "multiple_interval_matches"
		}
		result = append(result, link)
	}
	return result
}
