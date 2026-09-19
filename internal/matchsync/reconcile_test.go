package matchsync

import (
	"testing"
	"time"

	"lolengine/internal/domain"
	"lolengine/internal/riotapi"
	"lolengine/internal/storage"
)

func TestReconcileSessionsExactUniqueAmbiguousAndSegmented(t *testing.T) {
	base := time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)
	official := []storage.RiotStoredMatch{
		{Match: reconciliationMatch("NA1_1", "Garen", base, base.Add(30*time.Minute))},
		{Match: reconciliationMatch("NA1_2", "Garen", base.Add(time.Minute), base.Add(31*time.Minute))},
	}
	ended := base.Add(29 * time.Minute)
	tests := []struct {
		name      string
		candidate LocalCandidate
		wantState storage.SessionLinkState
		wantID    string
	}{
		{
			name: "exact ID wins", candidate: LocalCandidate{Session: domain.MatchSession{ID: "exact", State: domain.MatchEnded, StartedAt: base, EndedAt: &ended}, ExternalMatchID: "NA1_2", Platform: riotapi.PlatformNA1},
			wantState: storage.SessionLinkLinked, wantID: "NA1_2",
		},
		{
			name: "unique interval champion map queue", candidate: LocalCandidate{Session: domain.MatchSession{ID: "unique", State: domain.MatchEnded, StartedAt: base.Add(-time.Hour), EndedAt: timePointer(base.Add(-30 * time.Minute)), ChampionName: "Garen", MapNumber: 11}, Platform: riotapi.PlatformNA1, QueueID: 420},
			wantState: storage.SessionLinkPending,
		},
		{
			name: "ambiguous stays pending", candidate: LocalCandidate{Session: domain.MatchSession{ID: "ambiguous", State: domain.MatchEnded, StartedAt: base, EndedAt: &ended, ChampionName: "Garen", MapNumber: 11}, Platform: riotapi.PlatformNA1, QueueID: 420},
			wantState: storage.SessionLinkAmbiguous,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			links := ReconcileSessions("account", []LocalCandidate{test.candidate}, official, base.Add(time.Hour))
			if len(links) != 1 || links[0].State != test.wantState || links[0].OfficialMatchID != test.wantID {
				t.Fatalf("links = %#v", links)
			}
		})
	}

	// Once only one normalized match remains, multiple local reconnect segments
	// may intentionally point to that same official match.
	segmentEnd := base.Add(10 * time.Minute)
	segments := []LocalCandidate{
		{Session: domain.MatchSession{ID: "segment-a", State: domain.MatchEnded, StartedAt: base, EndedAt: &segmentEnd, ChampionName: "Garen", MapNumber: 11}, Platform: riotapi.PlatformNA1, QueueID: 420},
		{Session: domain.MatchSession{ID: "segment-b", State: domain.MatchEnded, StartedAt: base.Add(11 * time.Minute), EndedAt: &ended, ChampionName: "Garen", MapNumber: 11}, Platform: riotapi.PlatformNA1, QueueID: 420},
	}
	links := ReconcileSessions("account", segments, official[:1], base.Add(time.Hour))
	if len(links) != 2 || links[0].OfficialMatchID != "NA1_1" || links[1].OfficialMatchID != "NA1_1" {
		t.Fatalf("segment links = %#v", links)
	}
}

func TestReconcileSessionsRequiresEveryFallbackEvidenceField(t *testing.T) {
	base := time.Date(2026, 9, 19, 22, 0, 0, 0, time.UTC)
	ended := base.Add(30 * time.Minute)
	official := []storage.RiotStoredMatch{{Match: reconciliationMatch("opaque-key", "Garen", base, ended)}}
	complete := LocalCandidate{
		Session:  domain.MatchSession{ID: "local", State: domain.MatchEnded, StartedAt: base, EndedAt: &ended, ChampionName: "Garen", MapNumber: 11},
		Platform: riotapi.PlatformNA1, QueueID: 420,
	}
	tests := []struct {
		name   string
		mutate func(*LocalCandidate)
	}{
		{name: "missing platform", mutate: func(value *LocalCandidate) { value.Platform = "" }},
		{name: "missing queue", mutate: func(value *LocalCandidate) { value.QueueID = 0 }},
		{name: "missing map", mutate: func(value *LocalCandidate) { value.Session.MapNumber = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := complete
			test.mutate(&candidate)
			links := ReconcileSessions("account", []LocalCandidate{candidate}, official, ended.Add(time.Hour))
			if len(links) != 1 || links[0].State != storage.SessionLinkPending || links[0].OfficialMatchID != "" || links[0].Reason != "insufficient_local_evidence" {
				t.Fatalf("links = %#v", links)
			}
		})
	}

	exact := complete
	exact.Session.MapNumber = 0
	exact.QueueID = 0
	exact.Platform = ""
	exact.ExternalMatchID = "opaque-key"
	links := ReconcileSessions("account", []LocalCandidate{exact}, official, ended.Add(time.Hour))
	if len(links) != 1 || links[0].State != storage.SessionLinkLinked {
		t.Fatalf("exact match did not win: %#v", links)
	}
}

func reconciliationMatch(id, champion string, start, end time.Time) riotapi.Match {
	return riotapi.Match{ID: id, Platform: riotapi.PlatformNA1, QueueID: 420, MapID: 11, StartedAt: start, EndedAt: end, Player: riotapi.LinkedPlayerFacts{ChampionName: champion}}
}

func timePointer(value time.Time) *time.Time { return &value }
