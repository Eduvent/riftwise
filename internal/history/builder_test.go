package history

import (
	"encoding/json"
	"testing"
	"time"

	"lolengine/internal/domain"
)

func TestBuildMatchSummaryCompletionAndDuration(t *testing.T) {
	t0 := time.Date(2026, 9, 19, 2, 0, 0, 0, time.UTC)
	ended := t0.Add(35 * time.Minute)
	tests := []struct {
		name       string
		session    domain.MatchSession
		events     []domain.SemanticEvent
		completion domain.SummaryCompletion
		duration   float64
		basis      domain.DurationBasis
		lowerBound bool
	}{
		{
			name: "observed terminal event is complete and uses absolute game clock",
			session: domain.MatchSession{
				ID: "m1", State: domain.MatchEnded, StartedAt: t0, UpdatedAt: ended,
				EndedAt: &ended, StartGameTime: 600, LastGameTime: 2098, ChampionName: "Garen", Position: "TOP", LastEventSequence: 2,
			},
			events: []domain.SemanticEvent{
				{ID: "m1:1", SessionID: "m1", Sequence: 1, Kind: domain.EventMatchStarted, Source: domain.EventSourceObserved, OccurredAt: t0, GameTimeSeconds: 600},
				{ID: "m1:2", SessionID: "m1", Sequence: 2, Kind: domain.EventMatchEnded, Source: domain.EventSourceObserved, Confidence: 1, OccurredAt: ended, GameTimeSeconds: 2100, Payload: json.RawMessage(`{"reason":"terminal_event"}`)},
			},
			completion: domain.SummaryCompleteTerminal,
			duration:   2100,
			basis:      domain.DurationTerminalGameClock,
		},
		{
			name: "inferred end is incomplete and last observed time is a lower bound",
			session: domain.MatchSession{
				ID: "m2", State: domain.MatchEnded, StartedAt: t0, UpdatedAt: ended,
				EndedAt: &ended, StartGameTime: 420, LastGameTime: 1800, ChampionName: "Ahri", Position: "MIDDLE", LastEventSequence: 1,
			},
			events: []domain.SemanticEvent{
				{ID: "m2:1", SessionID: "m2", Sequence: 1, Kind: domain.EventMatchEnded, Source: domain.EventSourceInferred, Confidence: .75, OccurredAt: ended, GameTimeSeconds: 1800},
			},
			completion: domain.SummaryIncompleteInferred,
			duration:   1800,
			basis:      domain.DurationLastObservedGameClock,
			lowerBound: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summary, err := Build(domain.MatchSummarySource{Session: tt.session, LifecycleEvents: tt.events})
			if err != nil {
				t.Fatalf("Build() error = %v", err)
			}
			if summary.Completion != tt.completion {
				t.Fatalf("completion = %q, want %q", summary.Completion, tt.completion)
			}
			if summary.Duration.Seconds != tt.duration || summary.Duration.Basis != tt.basis || summary.Duration.LowerBound != tt.lowerBound {
				t.Fatalf("duration = %+v", summary.Duration)
			}
			if summary.Duration.Seconds == tt.session.LastGameTime-tt.session.StartGameTime {
				t.Fatal("duration incorrectly subtracts late-attachment game time")
			}
			if summary.Outcome.Value != domain.MatchOutcomeUnknown {
				t.Fatalf("outcome = %+v", summary.Outcome)
			}
		})
	}
}

func TestBuildMatchSummaryKeepsObservedAndInferredInventorySemantics(t *testing.T) {
	now := time.Date(2026, 9, 19, 3, 0, 0, 0, time.UTC)
	payload := json.RawMessage(`{"added":[{"id":2003,"count":1}],"removed":[],"classification":"acquisition","classification_source":"inferred","classification_confidence":0.8,"occurrence_sequence":2}`)
	source := domain.MatchSummarySource{
		Session: domain.MatchSession{ID: "m", State: domain.MatchActive, StartedAt: now, UpdatedAt: now, ChampionName: "Garen", Position: "top", LastEventSequence: 4},
		LifecycleEvents: []domain.SemanticEvent{
			{ID: "m:1", SessionID: "m", Sequence: 1, Kind: domain.EventMatchStarted, Source: domain.EventSourceObserved, Confidence: 1, OccurredAt: now},
			{ID: "m:2", SessionID: "m", Sequence: 2, Kind: domain.EventInventoryChanged, Source: domain.EventSourceObserved, Confidence: 1, OccurredAt: now, GameTimeSeconds: 100, Payload: payload},
			{ID: "m:3", SessionID: "m", Sequence: 3, Kind: domain.EventItemPurchased, Source: domain.EventSourceInferred, Confidence: .55, OccurredAt: now, GameTimeSeconds: 100, Payload: payload},
			{ID: "m:4", SessionID: "m", Sequence: 4, Kind: domain.EventReturnedToBase, Source: domain.EventSourceInferred, Confidence: .55, OccurredAt: now, GameTimeSeconds: 100, Payload: json.RawMessage(`{"reason":"shop_inventory_acquisition","added":[{"id":2003,"count":1}],"inventory_event_id":"m:2","purchase_event_id":"m:3"}`)},
		},
	}

	summary, err := Build(source)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(summary.InventoryChanges) != 1 || summary.InventoryChanges[0].Source != domain.EventSourceObserved {
		t.Fatalf("inventory changes = %+v", summary.InventoryChanges)
	}
	if len(summary.InferredPurchases) != 1 || summary.InferredPurchases[0].Source != domain.EventSourceInferred || summary.InferredPurchases[0].Confidence != .55 {
		t.Fatalf("purchases = %+v", summary.InferredPurchases)
	}
	if len(summary.InferredBaseReturns) != 1 || summary.InferredBaseReturns[0].Source != domain.EventSourceInferred {
		t.Fatalf("base returns = %+v", summary.InferredBaseReturns)
	}
	if !containsCaveat(summary.Caveats, domain.CaveatInferredPurchase) || !containsCaveat(summary.Caveats, domain.CaveatInferredBase) {
		t.Fatalf("caveats = %v", summary.Caveats)
	}
}

func TestBuildMatchSummaryFlagsMissingInitialSequence(t *testing.T) {
	now := time.Date(2026, 9, 19, 5, 30, 0, 0, time.UTC)
	summary, err := Build(domain.MatchSummarySource{
		Session: domain.MatchSession{ID: "m", State: domain.MatchActive, StartedAt: now, UpdatedAt: now, LastEventSequence: 2},
		LifecycleEvents: []domain.SemanticEvent{{
			ID: "m:2", SessionID: "m", Sequence: 2, Kind: domain.EventPlayerDied,
			Source: domain.EventSourceObserved, Confidence: 1, OccurredAt: now,
		}},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(summary.IntegrityIssues) == 0 || summary.IntegrityIssues[0].Code != "lifecycle_sequence_gap" {
		t.Fatalf("integrity issues = %+v", summary.IntegrityIssues)
	}
}

func TestBuildMatchSummaryOmitsInvalidVersionedEvidenceWithIntegrityCaveat(t *testing.T) {
	now := time.Date(2026, 9, 19, 5, 45, 0, 0, time.UTC)
	summary, err := Build(domain.MatchSummarySource{
		Session: domain.MatchSession{ID: "m", State: domain.MatchActive, StartedAt: now, UpdatedAt: now},
		FinalRecommendationEvidence: &domain.RecommendationEvidence{
			SchemaVersion: 99, Status: "ready", Target: &domain.EvidenceItem{ID: 4401}, Confidence: .8,
		},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if summary.FinalRecommendation != nil || len(summary.IntegrityIssues) == 0 ||
		summary.IntegrityIssues[0].Code != "recommendation_evidence_invalid" {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestBuildLegacySourceUsesExplicitMissingCheckpointAndEvidenceCaveats(t *testing.T) {
	now := time.Date(2026, 9, 19, 6, 0, 0, 0, time.UTC)
	endedAt := now.Add(20 * time.Minute)
	summary, err := Build(domain.MatchSummarySource{
		Session: domain.MatchSession{
			ID: "legacy", State: domain.MatchEnded, StartedAt: now, UpdatedAt: endedAt,
			EndedAt: &endedAt, LastGameTime: 1200, EndReason: "legacy_recovery", EndConfidence: .6,
		},
		LifecycleEvents: []domain.SemanticEvent{{
			ID: "legacy:1", SessionID: "legacy", Sequence: 1, Kind: domain.EventMatchEnded,
			Source: domain.EventSourceInferred, Confidence: .6, OccurredAt: endedAt, GameTimeSeconds: 1200,
		}},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	for _, caveat := range []domain.SummaryCaveat{
		domain.CaveatLegacyMissingStats, domain.CaveatLegacyMissingEvidence, domain.CaveatIncompleteSession,
	} {
		if !containsCaveat(summary.Caveats, caveat) {
			t.Fatalf("legacy caveats = %v; missing %q", summary.Caveats, caveat)
		}
	}
}

func TestBuildMatchSummaryIsDeterministicAcrossDuplicateRetries(t *testing.T) {
	now := time.Date(2026, 9, 19, 4, 0, 0, 0, time.UTC)
	event := domain.SemanticEvent{ID: "m:1", SessionID: "m", Sequence: 1, Kind: domain.EventMatchStarted, Source: domain.EventSourceObserved, Confidence: 1, OccurredAt: now}
	source := domain.MatchSummarySource{
		Session:         domain.MatchSession{ID: "m", State: domain.MatchActive, StartedAt: now, UpdatedAt: now, LastEventSequence: 1},
		LifecycleEvents: []domain.SemanticEvent{event, event},
	}
	first, err := Build(source)
	if err != nil {
		t.Fatalf("Build(first) error = %v", err)
	}
	second, err := Build(source)
	if err != nil {
		t.Fatalf("Build(second) error = %v", err)
	}
	if first.Digest == "" || first.Digest != second.Digest {
		t.Fatalf("digests = %q, %q", first.Digest, second.Digest)
	}
	if len(first.IntegrityIssues) != 0 {
		t.Fatalf("exact duplicate retry reported as corruption: %+v", first.IntegrityIssues)
	}
}

func TestBuildMatchSummaryDegradesMalformedAllowlistedPayload(t *testing.T) {
	now := time.Date(2026, 9, 19, 5, 0, 0, 0, time.UTC)
	summary, err := Build(domain.MatchSummarySource{
		Session: domain.MatchSession{ID: "m", State: domain.MatchActive, StartedAt: now, UpdatedAt: now, LastEventSequence: 1},
		LifecycleEvents: []domain.SemanticEvent{{
			ID: "m:1", SessionID: "m", Sequence: 1, Kind: domain.EventInventoryChanged,
			Source: domain.EventSourceObserved, Confidence: 1, OccurredAt: now, Payload: json.RawMessage(`{"added":"not-an-array"}`),
		}},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(summary.InventoryChanges) != 0 || len(summary.IntegrityIssues) != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	if !containsCaveat(summary.Caveats, domain.CaveatMalformedEventPayload) {
		t.Fatalf("caveats = %v", summary.Caveats)
	}
}

func containsCaveat(values []domain.SummaryCaveat, wanted domain.SummaryCaveat) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
