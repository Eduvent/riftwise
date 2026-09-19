// Package history builds deterministic, identity-free views over durable local evidence.
package history

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"lolengine/internal/domain"
)

// Build constructs a pure summary. Operational build time is assigned by the
// service after this function returns and is not part of the semantic digest.
func Build(source domain.MatchSummarySource) (domain.MatchSummary, error) {
	session := source.Session
	if strings.TrimSpace(session.ID) == "" {
		return domain.MatchSummary{}, errors.New("match summary source session id is required")
	}
	if session.StartedAt.IsZero() {
		return domain.MatchSummary{}, errors.New("match summary source start time is required")
	}

	summary := domain.MatchSummary{
		SchemaVersion:  domain.MatchSummarySchemaVersion,
		MatchID:        session.ID,
		SourceRevision: sourceRevision(source),
		ChampionName:   strings.TrimSpace(session.ChampionName),
		StartedAt:      session.StartedAt.UTC(),
		EndedAt:        cloneTime(session.EndedAt),
		Outcome: domain.OutcomeSummary{
			Value: domain.MatchOutcomeUnknown, Source: "unavailable_locally",
		},
	}
	addCaveat(&summary, domain.CaveatOutcomeUnavailable)
	if role, ok := domain.ParseRole(session.Position); ok {
		summary.Role, summary.RoleKnown = role, true
	}

	if checkpoint := source.PlayerCheckpoint; checkpoint != nil {
		if checkpoint.MatchID != "" && checkpoint.MatchID != session.ID {
			addIssue(&summary, "player_checkpoint_session_mismatch", "")
		} else {
			if strings.TrimSpace(checkpoint.ChampionName) != "" {
				summary.ChampionName = strings.TrimSpace(checkpoint.ChampionName)
			}
			if checkpoint.RoleKnown && checkpoint.Role.Valid() {
				summary.Role, summary.RoleKnown = checkpoint.Role, true
			}
			summary.Stats = &domain.TerminalStats{
				ObservedAt: checkpoint.ObservedAt.UTC(), GameTimeSeconds: checkpoint.GameTimeSeconds,
				Level: checkpoint.Level, Kills: checkpoint.Kills, Deaths: checkpoint.Deaths,
				Assists: checkpoint.Assists, CreepScore: checkpoint.CreepScore,
				Quality: domain.StatsLastObserved,
			}
			summary.FinalInventory = normalizeInventory(checkpoint.Inventory)
		}
	}
	if summary.Stats == nil {
		addCaveat(&summary, domain.CaveatLegacyMissingStats)
	}

	lifecycle := canonicalLifecycleEvents(session.ID, source.LifecycleEvents, &summary)
	terminal := terminalEvent(lifecycle)
	classifyCompletionAndDuration(&summary, session, terminal, source.PlayerCheckpoint)
	buildLifecycleTimelines(&summary, lifecycle)

	decisions := canonicalDecisionEvents(session.ID, source.DecisionEvents, &summary)
	for _, event := range decisions {
		evidence := validatedEvidence(event.Evidence, event.ID, &summary)
		summary.DecisionChanges = append(summary.DecisionChanges, domain.DecisionChangeSummary{
			EventID: event.ID, Sequence: event.Sequence, Kind: event.Kind, Importance: event.Importance,
			OccurredAt: event.OccurredAt.UTC(), GameTimeSeconds: event.GameTimeSeconds,
			PreviousSummary: event.PreviousSummary, CurrentSummary: event.CurrentSummary,
			ReasonCodes:             append([]string(nil), event.ReasonCodes...),
			RelatedLifecycleEventID: event.RelatedLifecycleEventID,
			Evidence:                evidence,
		})
		if evidence != nil {
			summary.FinalRecommendation = domain.CloneRecommendationEvidence(evidence)
		}
	}
	if source.FinalRecommendationEvidence != nil {
		if evidence := validatedEvidence(source.FinalRecommendationEvidence, "", &summary); evidence != nil {
			summary.FinalRecommendation = evidence
		}
	}
	if summary.FinalRecommendation == nil {
		addCaveat(&summary, domain.CaveatLegacyMissingEvidence)
	}

	sort.Slice(summary.Caveats, func(i, j int) bool { return summary.Caveats[i] < summary.Caveats[j] })
	sort.Slice(summary.IntegrityIssues, func(i, j int) bool {
		if summary.IntegrityIssues[i].Code != summary.IntegrityIssues[j].Code {
			return summary.IntegrityIssues[i].Code < summary.IntegrityIssues[j].Code
		}
		return summary.IntegrityIssues[i].EventID < summary.IntegrityIssues[j].EventID
	})
	digest, err := semanticDigest(summary)
	if err != nil {
		return domain.MatchSummary{}, err
	}
	summary.Digest = digest
	return summary, nil
}

func sourceRevision(source domain.MatchSummarySource) domain.SummarySourceRevision {
	revision := source.Revision
	if revision.SessionUpdatedAtNS == 0 {
		revision.SessionUpdatedAtNS = source.Session.UpdatedAt.UTC().UnixNano()
	}
	if revision.LifecycleSequence == 0 {
		revision.LifecycleSequence = source.Session.LastEventSequence
	}
	if source.PlayerCheckpoint != nil && revision.PlayerObservedAtNS == 0 {
		revision.PlayerObservedAtNS = source.PlayerCheckpoint.ObservedAt.UTC().UnixNano()
	}
	if revision.DecisionCheckpointAtNS == 0 && !source.DecisionCheckpointUpdatedAt.IsZero() {
		revision.DecisionCheckpointAtNS = source.DecisionCheckpointUpdatedAt.UTC().UnixNano()
	}
	for _, event := range source.DecisionEvents {
		if event.Sequence > revision.DecisionSequence {
			revision.DecisionSequence = event.Sequence
		}
	}
	return revision
}

func classifyCompletionAndDuration(summary *domain.MatchSummary, session domain.MatchSession, terminal *domain.SemanticEvent, checkpoint *domain.MatchPlayerCheckpoint) {
	lastGameTime := session.LastGameTime
	if checkpoint != nil && checkpoint.GameTimeSeconds > lastGameTime {
		lastGameTime = checkpoint.GameTimeSeconds
	}
	if terminal != nil {
		summary.Terminal = domain.TerminalSummary{
			Reason: session.EndReason, Source: terminal.Source, Confidence: terminal.Confidence,
		}
		var payload struct {
			Reason string `json:"reason"`
		}
		if json.Unmarshal(terminal.Payload, &payload) == nil && strings.TrimSpace(payload.Reason) != "" {
			summary.Terminal.Reason = strings.TrimSpace(payload.Reason)
		}
		if terminal.GameTimeSeconds > lastGameTime {
			lastGameTime = terminal.GameTimeSeconds
		}
	}

	switch {
	case session.State == domain.MatchActive:
		summary.Completion = domain.SummaryActive
		summary.Duration = durationFromLastObservation(session, lastGameTime)
	case terminal != nil && terminal.Source == domain.EventSourceObserved:
		summary.Completion = domain.SummaryCompleteTerminal
		if lastGameTime > 0 {
			summary.Duration = domain.DurationSummary{Seconds: lastGameTime, Basis: domain.DurationTerminalGameClock}
		} else {
			summary.Duration = durationFromLastObservation(session, lastGameTime)
		}
		if summary.Stats != nil && summary.Stats.GameTimeSeconds+0.001 >= terminal.GameTimeSeconds {
			summary.Stats.Quality = domain.StatsTerminalSnapshot
		} else if summary.Stats != nil {
			addCaveat(summary, domain.CaveatTerminalStatsLastObserved)
		}
	default:
		summary.Completion = domain.SummaryIncompleteInferred
		summary.Duration = durationFromLastObservation(session, lastGameTime)
		summary.Terminal.Reason = session.EndReason
		summary.Terminal.Confidence = session.EndConfidence
		if summary.Terminal.Source == "" {
			summary.Terminal.Source = domain.EventSourceInferred
		}
		addCaveat(summary, domain.CaveatIncompleteSession)
	}
	if summary.Duration.LowerBound {
		addCaveat(summary, domain.CaveatDurationLowerBound)
	}
	if summary.Stats != nil && summary.Stats.Quality == domain.StatsLastObserved && session.State == domain.MatchEnded {
		addCaveat(summary, domain.CaveatTerminalStatsLastObserved)
	}
}

func durationFromLastObservation(session domain.MatchSession, lastGameTime float64) domain.DurationSummary {
	if lastGameTime > 0 {
		return domain.DurationSummary{Seconds: lastGameTime, Basis: domain.DurationLastObservedGameClock, LowerBound: true}
	}
	if !session.StartedAt.IsZero() && session.EndedAt != nil && session.EndedAt.After(session.StartedAt) {
		return domain.DurationSummary{
			Seconds: session.EndedAt.Sub(session.StartedAt).Seconds(), Basis: domain.DurationObservedWindow, LowerBound: true,
		}
	}
	return domain.DurationSummary{Basis: domain.DurationUnknown, LowerBound: true}
}

type payloadItem struct {
	ID    int `json:"id"`
	Count int `json:"count"`
}

type inventoryPayload struct {
	Added                    []payloadItem      `json:"added"`
	Removed                  []payloadItem      `json:"removed"`
	Classification           string             `json:"classification"`
	ClassificationSource     domain.EventSource `json:"classification_source"`
	ClassificationConfidence float64            `json:"classification_confidence"`
}

type basePayload struct {
	Reason           string        `json:"reason"`
	Added            []payloadItem `json:"added"`
	InventoryEventID string        `json:"inventory_event_id"`
	PurchaseEventID  string        `json:"purchase_event_id"`
}

func buildLifecycleTimelines(summary *domain.MatchSummary, events []domain.SemanticEvent) {
	for _, event := range events {
		base := domain.HistoryEventBase{
			EventID: event.ID, Sequence: event.Sequence, OccurredAt: event.OccurredAt.UTC(),
			GameTimeSeconds: event.GameTimeSeconds, Source: event.Source, Confidence: event.Confidence,
		}
		switch event.Kind {
		case domain.EventInventoryChanged, domain.EventItemPurchased:
			var payload inventoryPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil || len(payload.Added)+len(payload.Removed) == 0 ||
				(event.Kind == domain.EventItemPurchased && len(payload.Added) == 0) ||
				!validPayloadItems(payload.Added) || !validPayloadItems(payload.Removed) ||
				(payload.ClassificationSource != "" && !validEventSource(payload.ClassificationSource)) || !validConfidence(payload.ClassificationConfidence) {
				addIssue(summary, "malformed_event_payload", event.ID)
				addCaveat(summary, domain.CaveatMalformedEventPayload)
				continue
			}
			if event.Kind == domain.EventInventoryChanged {
				summary.InventoryChanges = append(summary.InventoryChanges, domain.InventoryChangeSummary{
					HistoryEventBase: base, Added: convertPayloadItems(payload.Added), Removed: convertPayloadItems(payload.Removed),
					Classification: payload.Classification, ClassificationSource: payload.ClassificationSource,
					ClassificationConfidence: payload.ClassificationConfidence,
				})
			} else {
				summary.InferredPurchases = append(summary.InferredPurchases, domain.InferredAcquisitionSummary{
					HistoryEventBase: base, Items: convertPayloadItems(payload.Added),
				})
				addCaveat(summary, domain.CaveatInferredPurchase)
			}
		case domain.EventReturnedToBase:
			var payload basePayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil || len(payload.Added) == 0 || !validPayloadItems(payload.Added) {
				addIssue(summary, "malformed_event_payload", event.ID)
				addCaveat(summary, domain.CaveatMalformedEventPayload)
				continue
			}
			summary.InferredBaseReturns = append(summary.InferredBaseReturns, domain.InferredAcquisitionSummary{
				HistoryEventBase: base, Items: convertPayloadItems(payload.Added), Reason: payload.Reason,
				InventoryEventID: payload.InventoryEventID, PurchaseEventID: payload.PurchaseEventID,
			})
			addCaveat(summary, domain.CaveatInferredBase)
		}
	}
}

func terminalEvent(events []domain.SemanticEvent) *domain.SemanticEvent {
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Kind == domain.EventMatchEnded {
			event := events[index]
			return &event
		}
	}
	return nil
}

func canonicalLifecycleEvents(sessionID string, values []domain.SemanticEvent, summary *domain.MatchSummary) []domain.SemanticEvent {
	bySequence := make(map[uint64]domain.SemanticEvent, len(values))
	for _, event := range values {
		if event.SessionID != sessionID {
			addIssue(summary, "lifecycle_session_mismatch", event.ID)
			continue
		}
		if !validEventSource(event.Source) || !validConfidence(event.Confidence) {
			addIssue(summary, "lifecycle_event_metadata_invalid", event.ID)
			continue
		}
		if previous, exists := bySequence[event.Sequence]; exists {
			if semanticEventEqual(previous, event) {
				continue
			}
			addIssue(summary, "lifecycle_sequence_conflict", event.ID)
			if canonicalLess(event, previous) {
				bySequence[event.Sequence] = cloneSemanticEvent(event)
			}
			continue
		}
		bySequence[event.Sequence] = cloneSemanticEvent(event)
	}
	result := make([]domain.SemanticEvent, 0, len(bySequence))
	for _, event := range bySequence {
		result = append(result, event)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Sequence != result[j].Sequence {
			return result[i].Sequence < result[j].Sequence
		}
		return result[i].ID < result[j].ID
	})
	validateSequence(result, summary.SourceRevision.LifecycleSequence, "lifecycle", summary)
	return result
}

func canonicalDecisionEvents(sessionID string, values []domain.DecisionEvent, summary *domain.MatchSummary) []domain.DecisionEvent {
	bySequence := make(map[uint64]domain.DecisionEvent, len(values))
	for _, event := range values {
		if event.SessionID != sessionID {
			addIssue(summary, "decision_session_mismatch", event.ID)
			continue
		}
		if previous, exists := bySequence[event.Sequence]; exists {
			if decisionEventEqual(previous, event) {
				continue
			}
			addIssue(summary, "decision_sequence_conflict", event.ID)
			if canonicalLess(event, previous) {
				bySequence[event.Sequence] = cloneDecisionEvent(event)
			}
			continue
		}
		bySequence[event.Sequence] = cloneDecisionEvent(event)
	}
	result := make([]domain.DecisionEvent, 0, len(bySequence))
	for _, event := range bySequence {
		result = append(result, event)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Sequence != result[j].Sequence {
			return result[i].Sequence < result[j].Sequence
		}
		return result[i].ID < result[j].ID
	})
	if summary.SourceRevision.DecisionSequence > 0 {
		validateDecisionSequence(result, summary.SourceRevision.DecisionSequence, summary)
	}
	return result
}

func validateSequence(events []domain.SemanticEvent, expected uint64, stream string, summary *domain.MatchSummary) {
	var previous uint64
	for _, event := range events {
		if event.Sequence == 0 || (previous == 0 && event.Sequence != 1) || (previous > 0 && event.Sequence != previous+1) {
			addIssue(summary, stream+"_sequence_gap", event.ID)
		}
		previous = event.Sequence
	}
	if expected > 0 && previous != expected {
		addIssue(summary, stream+"_sequence_max_mismatch", "")
	}
}

func validateDecisionSequence(events []domain.DecisionEvent, expected uint64, summary *domain.MatchSummary) {
	var previous uint64
	for _, event := range events {
		if event.Sequence == 0 || (previous == 0 && event.Sequence != 1) || (previous > 0 && event.Sequence != previous+1) {
			addIssue(summary, "decision_sequence_gap", event.ID)
		}
		previous = event.Sequence
	}
	if previous != expected {
		addIssue(summary, "decision_sequence_max_mismatch", "")
	}
}

func semanticEventEqual(left, right domain.SemanticEvent) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return bytes.Equal(leftJSON, rightJSON)
}

func decisionEventEqual(left, right domain.DecisionEvent) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return bytes.Equal(leftJSON, rightJSON)
}

func canonicalLess(left, right any) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return bytes.Compare(leftJSON, rightJSON) < 0
}

func cloneSemanticEvent(event domain.SemanticEvent) domain.SemanticEvent {
	event.Payload = append(json.RawMessage(nil), event.Payload...)
	return event
}

func cloneDecisionEvent(event domain.DecisionEvent) domain.DecisionEvent {
	event.ReasonCodes = append([]string(nil), event.ReasonCodes...)
	event.Evidence = domain.CloneRecommendationEvidence(event.Evidence)
	return event
}

func validPayloadItems(values []payloadItem) bool {
	for _, value := range values {
		if value.ID <= 0 || value.Count <= 0 {
			return false
		}
	}
	return true
}

func validEventSource(value domain.EventSource) bool {
	return value == domain.EventSourceObserved || value == domain.EventSourceInferred
}

func validConfidence(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

func validatedEvidence(value *domain.RecommendationEvidence, eventID string, summary *domain.MatchSummary) *domain.RecommendationEvidence {
	if value == nil {
		return nil
	}
	invalid := value.SchemaVersion != domain.RecommendationEvidenceSchemaVersion ||
		!validConfidence(value.Confidence) || !validConfidence(value.TraitConfidence) ||
		(value.Target != nil && value.Target.ID <= 0) || (value.Component != nil && value.Component.ID <= 0)
	if value.Profile != nil && !validConfidence(value.Profile.ConfidenceCap) {
		invalid = true
	}
	seenRanks := make(map[int]struct{}, len(value.RankedChoices))
	for _, choice := range value.RankedChoices {
		if choice.Rank <= 0 || choice.Item.ID <= 0 {
			invalid = true
			break
		}
		if _, exists := seenRanks[choice.Rank]; exists {
			invalid = true
			break
		}
		seenRanks[choice.Rank] = struct{}{}
	}
	if invalid {
		addIssue(summary, "recommendation_evidence_invalid", eventID)
		return nil
	}
	return domain.CloneRecommendationEvidence(value)
}

func convertPayloadItems(values []payloadItem) []domain.InventoryCount {
	result := make([]domain.InventoryCount, 0, len(values))
	for _, value := range values {
		result = append(result, domain.InventoryCount{ItemID: value.ID, Count: value.Count})
	}
	return normalizeInventory(result)
}

func normalizeInventory(values []domain.InventoryCount) []domain.InventoryCount {
	counts := make(map[int]int, len(values))
	for _, value := range values {
		if value.ItemID > 0 && value.Count > 0 {
			counts[value.ItemID] += value.Count
		}
	}
	ids := make([]int, 0, len(counts))
	for id := range counts {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	result := make([]domain.InventoryCount, 0, len(ids))
	for _, id := range ids {
		result = append(result, domain.InventoryCount{ItemID: id, Count: counts[id]})
	}
	return result
}

func addCaveat(summary *domain.MatchSummary, caveat domain.SummaryCaveat) {
	for _, existing := range summary.Caveats {
		if existing == caveat {
			return
		}
	}
	summary.Caveats = append(summary.Caveats, caveat)
}

func addIssue(summary *domain.MatchSummary, code, eventID string) {
	summary.IntegrityIssues = append(summary.IntegrityIssues, domain.SummaryIntegrityIssue{Code: code, EventID: eventID})
	addCaveat(summary, domain.CaveatSourceIntegrityDegraded)
}

func semanticDigest(summary domain.MatchSummary) (string, error) {
	summary.Digest = ""
	summary.BuiltAt = time.Time{}
	payload, err := json.Marshal(summary)
	if err != nil {
		return "", fmt.Errorf("encode canonical match summary: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

// VerifyDigest detects valid-JSON materialized corruption that storage-level
// envelope checks cannot distinguish from a legitimate summary.
func VerifyDigest(summary domain.MatchSummary) bool {
	want := summary.Digest
	if want == "" {
		return false
	}
	digest, err := semanticDigest(summary)
	return err == nil && digest == want
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	clone := value.UTC()
	return &clone
}
