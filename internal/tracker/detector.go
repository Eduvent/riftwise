package tracker

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"lolengine/internal/domain"
)

const defaultRollbackToleranceSeconds = 30
const defaultRecentEventLimit = 50

// DetectorConfig makes lifecycle time, identity, and thresholds deterministic in tests.
type DetectorConfig struct {
	Now                 func() time.Time
	NewSessionID        func(fingerprint string, startedAt time.Time) string
	StaleAfter          time.Duration
	EndAfter            time.Duration
	RollbackTolerance   float64
	RecentLimit         int
	InitialSession      *domain.MatchSession
	InitialConnection   domain.ConnectionState
	InitialOutage       time.Time
	InitialFailureClass domain.FetchErrorClass
	InitialFailureAt    time.Time
	InitialUnavailable  time.Time
	InitialEvents       []domain.SemanticEvent
}

// Detection is one pure state-machine transition. Sessions and events are persisted together.
type Detection struct {
	Connection             domain.ConnectionState
	Session                *domain.MatchSession
	Sessions               []domain.MatchSession
	Events                 []domain.SemanticEvent
	RecommendationRelevant bool
}

// Detector turns cumulative observations into reconnect-safe semantic transitions.
type Detector struct {
	now          func() time.Time
	newSessionID func(string, time.Time) string
	staleAfter   time.Duration
	endAfter     time.Duration
	rollback     float64
	recentLimit  int

	connection       domain.ConnectionState
	session          *domain.MatchSession
	lastGame         *domain.GameState
	lastSuccess      time.Time
	outageStarted    time.Time
	lastFailureClass domain.FetchErrorClass
	lastFailureAt    time.Time
	unavailableSince time.Time
	processedSource  map[string]struct{}
	recent           []domain.SemanticEvent
}

// NewDetector constructs an in-memory lifecycle state machine.
func NewDetector(cfg DetectorConfig) *Detector {
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.NewSessionID == nil {
		cfg.NewSessionID = defaultSessionID
	}
	if cfg.RollbackTolerance <= 0 {
		cfg.RollbackTolerance = defaultRollbackToleranceSeconds
	}
	if cfg.RecentLimit <= 0 {
		cfg.RecentLimit = defaultRecentEventLimit
	}
	detector := &Detector{
		now: cfg.Now, newSessionID: cfg.NewSessionID, staleAfter: cfg.StaleAfter,
		endAfter: cfg.EndAfter, rollback: cfg.RollbackTolerance, recentLimit: cfg.RecentLimit,
		connection: domain.ConnectionWaiting, processedSource: make(map[string]struct{}),
	}
	if cfg.InitialSession != nil {
		detector.session = cloneSession(cfg.InitialSession)
		detector.lastSuccess = detector.session.LastSeenAt
		if detector.session.State == domain.MatchEnded {
			detector.connection = domain.ConnectionWaiting
		} else {
			detector.connection = cfg.InitialConnection
			if detector.connection == "" {
				detector.connection = detector.session.ConnectionState
			}
			if detector.connection == "" || detector.connection == domain.ConnectionWaiting {
				detector.connection = domain.ConnectionConnected
			}
			detector.outageStarted = cfg.InitialOutage.UTC()
			if detector.outageStarted.IsZero() {
				detector.outageStarted = detector.session.OutageStartedAt.UTC()
			}
			detector.lastFailureClass = cfg.InitialFailureClass
			if detector.lastFailureClass == "" {
				detector.lastFailureClass = detector.session.LastFailureClass
			}
			detector.lastFailureAt = cfg.InitialFailureAt.UTC()
			if detector.lastFailureAt.IsZero() {
				detector.lastFailureAt = detector.session.LastFailureAt.UTC()
			}
			detector.unavailableSince = cfg.InitialUnavailable.UTC()
			if detector.unavailableSince.IsZero() {
				detector.unavailableSince = detector.session.UnavailableSince.UTC()
			}
			if !detector.outageStarted.IsZero() && detector.staleAfter > 0 && detector.now().UTC().Sub(detector.lastSuccess) >= detector.staleAfter {
				detector.connection = domain.ConnectionStale
			}
		}
		detector.recent = cloneEvents(cfg.InitialEvents)
		if len(detector.recent) > detector.recentLimit {
			detector.recent = append([]domain.SemanticEvent(nil), detector.recent[len(detector.recent)-detector.recentLimit:]...)
		}
	}
	return detector
}

// Observe accepts one successful cumulative Live Client observation.
func (detector *Detector) Observe(game domain.GameState) Detection {
	now := detector.now().UTC()
	game.SourceEvents = normalizedSourceEvents(game.SourceEvents)
	fingerprint := matchFingerprint(game)
	wasOutage := !detector.outageStarted.IsZero()
	previousConnection := detector.connection
	result := Detection{Connection: domain.ConnectionConnected}

	boundary := detector.session == nil
	if detector.session != nil {
		fingerprintCompatible := compatibleMatchFingerprints(detector.session.Fingerprint, fingerprint)
		legacyFingerprintUpgrade := false
		if !fingerprintCompatible {
			_, _, _, currentFormat := splitMatchFingerprint(detector.session.Fingerprint)
			legacyFingerprintUpgrade = !currentFormat && matchSessionSummaryCompatible(*detector.session, game)
			fingerprintCompatible = legacyFingerprintUpgrade
		}
		boundary = !fingerprintCompatible
		previousGameTime := detector.session.LastGameTime
		if detector.lastGame != nil {
			previousGameTime = detector.lastGame.GameTimeSeconds
		}
		if game.GameTimeSeconds+detector.rollback < previousGameTime {
			boundary = true
		}
		if !boundary {
			if legacyFingerprintUpgrade {
				detector.session.Fingerprint = fingerprint
			} else {
				detector.session.Fingerprint = mergeMatchFingerprintEvidence(detector.session.Fingerprint, fingerprint)
			}
			if detector.session.GameVersion == "" && strings.TrimSpace(game.GameVersion) != "" {
				detector.session.GameVersion = game.GameVersion
			}
		}
	}

	if boundary {
		if detector.session != nil && detector.session.State == domain.MatchActive {
			ended := detector.endCurrent(now, detector.lastGameTime(), "new_match_observed", domain.EventSourceInferred, 0.75, "lifecycle:end:new-match")
			result.Events = append(result.Events, ended)
			result.Sessions = append(result.Sessions, *cloneSession(detector.session))
		}
		detector.startSession(now, fingerprint, game)
		started := detector.newEvent(domain.EventMatchStarted, startOccurredAt(now, game), game.GameTimeSeconds,
			startSource(game), startConfidence(game), "lifecycle:start", map[string]any{"reason": "first_observation"})
		result.Events = append(result.Events, started)
		result.RecommendationRelevant = true
		detector.processedSource = make(map[string]struct{}, len(game.SourceEvents))
		for _, source := range game.SourceEvents {
			detector.processedSource[source.DedupeKey] = struct{}{}
		}
	} else if detector.lastGame == nil {
		// Process restart with an incomplete persisted session: establish a new
		// score/inventory baseline without replaying history. Terminal events are
		// excluded because they remain authoritative even on the recovery poll.
		for _, source := range game.SourceEvents {
			if !terminalSourceEvent(source.Name) {
				detector.processedSource[source.DedupeKey] = struct{}{}
			}
		}
	}

	if wasOutage && !boundary && detector.session != nil && detector.session.State == domain.MatchActive {
		restored := detector.newEvent(domain.EventConnectionRestored, now, game.GameTimeSeconds,
			domain.EventSourceObserved, 1, fmt.Sprintf("connection:restored:%d", detector.session.LastEventSequence+1),
			map[string]any{"outage_seconds": now.Sub(detector.outageStarted).Seconds()})
		result.Events = append(result.Events, restored)
		result.RecommendationRelevant = true
	}

	if detector.lastGame != nil && !boundary && detector.session != nil && detector.session.State == domain.MatchActive {
		inventoryEvents := detector.inventoryEvents(now, *detector.lastGame, game)
		if len(inventoryEvents) > 0 {
			result.Events = append(result.Events, inventoryEvents...)
			result.RecommendationRelevant = true
		}
		deathEvents := detector.deathEvents(now, *detector.lastGame, game)
		if len(deathEvents) > 0 {
			result.Events = append(result.Events, deathEvents...)
			result.RecommendationRelevant = true
		}
		if recommendationRelevantDiff(*detector.lastGame, game) {
			result.RecommendationRelevant = true
		}
	}

	for _, source := range game.SourceEvents {
		if _, seen := detector.processedSource[source.DedupeKey]; seen {
			continue
		}
		detector.processedSource[source.DedupeKey] = struct{}{}
		if detector.session != nil && detector.session.State == domain.MatchActive && terminalSourceEvent(source.Name) {
			ended := detector.endCurrent(now, source.GameTimeSeconds, "terminal_event", domain.EventSourceObserved, 1,
				"lifecycle:end:"+source.DedupeKey)
			result.Events = append(result.Events, ended)
			result.RecommendationRelevant = true
		}
	}
	// A terminal source event may already be present on the first successful
	// attachment. It is not historical noise: it is authoritative match state.
	if boundary && detector.session != nil && detector.session.State == domain.MatchActive {
		for _, source := range game.SourceEvents {
			if terminalSourceEvent(source.Name) {
				ended := detector.endCurrent(now, source.GameTimeSeconds, "terminal_event", domain.EventSourceObserved, 1,
					"lifecycle:end:"+source.DedupeKey)
				result.Events = append(result.Events, ended)
				result.RecommendationRelevant = true
				break
			}
		}
	}

	detector.connection = domain.ConnectionConnected
	detector.lastSuccess = now
	detector.outageStarted = time.Time{}
	detector.lastFailureClass = ""
	detector.lastFailureAt = time.Time{}
	detector.unavailableSince = time.Time{}
	detector.lastGame = cloneGame(&game)
	if detector.session != nil {
		detector.session.UpdatedAt = now
		detector.session.LastSeenAt = now
		detector.session.LastGameTime = game.GameTimeSeconds
		detector.session.ConnectionState = detector.connection
		detector.session.OutageStartedAt = time.Time{}
		detector.session.LastFailureClass = ""
		detector.session.LastFailureAt = time.Time{}
		detector.session.UnavailableSince = time.Time{}
		detector.session.PlayerCheckpoint = playerCheckpoint(detector.session.ID, now, game)
		result.Sessions = upsertSession(result.Sessions, *cloneSession(detector.session))
	}
	if previousConnection == domain.ConnectionStale && detector.connection == domain.ConnectionConnected {
		result.RecommendationRelevant = true
	}
	detector.record(result.Events)
	result.Connection = detector.connection
	result.Session = cloneSessionForSnapshot(detector.session)
	return result
}

// ObserveFailure advances connection state without discarding the last valid baseline.
func (detector *Detector) ObserveFailure(class domain.FetchErrorClass, message string) Detection {
	now := detector.now().UTC()
	result := Detection{Connection: detector.connection, Session: cloneSessionForSnapshot(detector.session)}
	if detector.session == nil || detector.session.State == domain.MatchEnded {
		detector.connection = domain.ConnectionWaiting
		result.Connection = detector.connection
		return result
	}
	if detector.outageStarted.IsZero() {
		detector.outageStarted = now
		detector.connection = domain.ConnectionReconnecting
		lost := detector.newEvent(domain.EventConnectionLost, now, detector.lastGameTime(), domain.EventSourceObserved, 1,
			fmt.Sprintf("connection:lost:%d", detector.session.LastEventSequence+1),
			map[string]any{"class": class, "message": message})
		result.Events = append(result.Events, lost)
		result.RecommendationRelevant = true
	}
	if detector.staleAfter > 0 && now.Sub(detector.lastSuccess) >= detector.staleAfter {
		if detector.connection != domain.ConnectionStale {
			result.RecommendationRelevant = true
		}
		detector.connection = domain.ConnectionStale
	}
	if class == domain.FetchGameUnavailable {
		if detector.lastFailureClass != domain.FetchGameUnavailable || detector.unavailableSince.IsZero() {
			detector.unavailableSince = now
		}
	} else {
		detector.unavailableSince = time.Time{}
	}
	detector.lastFailureClass = class
	detector.lastFailureAt = now
	if detector.endAfter > 0 && !detector.unavailableSince.IsZero() && now.Sub(detector.unavailableSince) >= detector.endAfter && detector.session.State == domain.MatchActive {
		ended := detector.endCurrent(now, detector.lastGameTime(), "unavailable_grace_elapsed", domain.EventSourceInferred, 0.6,
			"lifecycle:end:unavailable-grace")
		result.Events = append(result.Events, ended)
		result.RecommendationRelevant = true
	}
	if detector.session != nil {
		detector.session.UpdatedAt = now
		detector.session.ConnectionState = detector.connection
		detector.session.OutageStartedAt = detector.outageStarted
		detector.session.LastFailureClass = detector.lastFailureClass
		detector.session.LastFailureAt = detector.lastFailureAt
		detector.session.UnavailableSince = detector.unavailableSince
		result.Sessions = upsertSession(result.Sessions, *cloneSession(detector.session))
	}
	detector.record(result.Events)
	result.Connection = detector.connection
	result.Session = cloneSessionForSnapshot(detector.session)
	return result
}

// RecentEvents returns a bounded isolated copy suitable for snapshots.
func (detector *Detector) RecentEvents() []domain.SemanticEvent {
	return cloneEvents(detector.recent)
}

func (detector *Detector) startSession(now time.Time, fingerprint string, game domain.GameState) {
	startedAt := startOccurredAt(now, game)
	detector.session = &domain.MatchSession{
		ID: detector.newSessionID(fingerprint, startedAt), Fingerprint: fingerprint, State: domain.MatchActive,
		StartedAt: startedAt, UpdatedAt: now, LastSeenAt: now, StartGameTime: game.GameTimeSeconds,
		LastGameTime: game.GameTimeSeconds, PlayerRiotID: canonicalIdentity(game.ActivePlayer.Identity),
		ChampionName: game.ActivePlayer.ChampionName, Team: game.ActivePlayer.Team, Position: game.ActivePlayer.Position,
		MapNumber: game.MapNumber, MapName: game.MapName, GameMode: game.GameMode, GameVersion: game.GameVersion,
		ConnectionState: domain.ConnectionConnected,
	}
	detector.processedSource = make(map[string]struct{})
	detector.lastGame = nil
}

func (detector *Detector) endCurrent(now time.Time, gameTime float64, reason string, source domain.EventSource, confidence float64, dedupe string) domain.SemanticEvent {
	endedAt := now
	detector.session.State = domain.MatchEnded
	detector.session.EndedAt = &endedAt
	detector.session.EndReason = reason
	detector.session.EndConfidence = confidence
	detector.session.UpdatedAt = now
	detector.session.LastGameTime = gameTime
	return detector.newEvent(domain.EventMatchEnded, now, gameTime, source, confidence, dedupe, map[string]any{"reason": reason})
}

func (detector *Detector) inventoryEvents(now time.Time, previous, next domain.GameState) []domain.SemanticEvent {
	before := itemCounts(previous.ActivePlayer.Items)
	after := itemCounts(next.ActivePlayer.Items)
	added, removed := itemCountDiff(before, after)
	if len(added) == 0 && len(removed) == 0 {
		return nil
	}
	classification := "inventory_change"
	classificationConfidence := 0.5
	if len(added) > 0 && len(removed) > 0 {
		classification = "component_combine_or_replacement"
		classificationConfidence = 0.65
	} else if len(added) > 0 {
		classification = "acquisition"
		classificationConfidence = 0.8
	} else if next.ActivePlayer.CurrentGold > previous.ActivePlayer.CurrentGold {
		classification = "sale_or_undo"
		classificationConfidence = 0.55
	} else {
		classification = "removal_or_consumption"
		classificationConfidence = 0.45
	}
	payload := map[string]any{
		"added": added, "removed": removed, "classification": classification,
		"classification_source": string(domain.EventSourceInferred), "classification_confidence": classificationConfidence,
		"gold_delta": next.ActivePlayer.CurrentGold - previous.ActivePlayer.CurrentGold,
	}
	digest := shortDigest(mustJSON(payload))
	occurrence := detector.session.LastEventSequence + 1
	payload["occurrence_sequence"] = occurrence
	inventory := detector.newEvent(domain.EventInventoryChanged, now, next.GameTimeSeconds,
		domain.EventSourceObserved, 1, fmt.Sprintf("inventory:%06d:%s", occurrence, digest), payload)
	events := []domain.SemanticEvent{inventory}
	if len(added) == 0 {
		return events
	}
	confidence := 0.8
	if len(removed) > 0 {
		confidence = 0.65
	} else if next.ActivePlayer.CurrentGold >= previous.ActivePlayer.CurrentGold {
		confidence = 0.55
	}
	purchase := detector.newEvent(domain.EventItemPurchased, now, next.GameTimeSeconds, domain.EventSourceInferred, confidence,
		fmt.Sprintf("purchase:%06d:%s", occurrence, digest), payload)
	returned := detector.newEvent(domain.EventReturnedToBase, now, next.GameTimeSeconds, domain.EventSourceInferred, minFloat(confidence, 0.55),
		fmt.Sprintf("base:%06d:%s", occurrence, digest), map[string]any{
			"reason": "shop_inventory_acquisition", "added": added,
			"inventory_event_id": inventory.ID, "purchase_event_id": purchase.ID,
		})
	events = append(events, purchase, returned)
	return events
}

func (detector *Detector) deathEvents(now time.Time, previous, next domain.GameState) []domain.SemanticEvent {
	if next.ActivePlayer.Score.Deaths <= previous.ActivePlayer.Score.Deaths {
		return nil
	}
	events := make([]domain.SemanticEvent, 0, next.ActivePlayer.Score.Deaths-previous.ActivePlayer.Score.Deaths)
	for death := previous.ActivePlayer.Score.Deaths + 1; death <= next.ActivePlayer.Score.Deaths; death++ {
		events = append(events, detector.newEvent(domain.EventPlayerDied, now, next.GameTimeSeconds,
			domain.EventSourceObserved, 0.95, fmt.Sprintf("death:%d", death),
			map[string]any{"death_number": death, "is_dead": next.ActivePlayer.IsDead, "respawn_timer": next.ActivePlayer.RespawnTimer}))
	}
	return events
}

func (detector *Detector) newEvent(kind domain.EventKind, occurredAt time.Time, gameTime float64, source domain.EventSource, confidence float64, dedupe string, payload any) domain.SemanticEvent {
	detector.session.LastEventSequence++
	sequence := detector.session.LastEventSequence
	return domain.SemanticEvent{
		ID: fmt.Sprintf("%s:%06d", detector.session.ID, sequence), SessionID: detector.session.ID,
		Sequence: sequence, DedupeKey: dedupe, Kind: kind, OccurredAt: occurredAt.UTC(), ObservedAt: detector.now().UTC(),
		GameTimeSeconds: gameTime, Source: source, Confidence: confidence, Payload: mustJSON(payload),
	}
}

func (detector *Detector) record(events []domain.SemanticEvent) {
	if len(events) == 0 {
		return
	}
	detector.recent = append(detector.recent, cloneEvents(events)...)
	if len(detector.recent) > detector.recentLimit {
		detector.recent = append([]domain.SemanticEvent(nil), detector.recent[len(detector.recent)-detector.recentLimit:]...)
	}
}

func matchFingerprint(game domain.GameState) string {
	roster := make([]string, 0, len(game.Players))
	for _, player := range game.Players {
		roster = append(roster, strings.ToLower(strings.Join([]string{
			canonicalIdentity(player.Identity), player.Team, player.ChampionName,
		}, "|")))
	}
	sort.Strings(roster)
	gameStart := ""
	for _, event := range game.SourceEvents {
		if strings.EqualFold(strings.TrimSpace(event.Name), "GameStart") {
			gameStart = fmt.Sprintf("%s|%.3f", event.DedupeKey, event.GameTimeSeconds)
			break
		}
	}
	corePayload := strings.Join([]string{
		strings.ToLower(canonicalIdentity(game.ActivePlayer.Identity)), strings.ToLower(game.GameMode),
		fmt.Sprintf("%d", game.MapNumber), strings.Join(roster, ","),
	}, "\n")
	coreDigest := sha256.Sum256([]byte(corePayload))
	versionEvidence := "none"
	if version := strings.ToLower(strings.TrimSpace(game.GameVersion)); version != "" {
		versionDigest := sha256.Sum256([]byte(version))
		versionEvidence = hex.EncodeToString(versionDigest[:])
	}
	startEvidence := "none"
	if gameStart != "" {
		startDigest := sha256.Sum256([]byte(gameStart))
		startEvidence = hex.EncodeToString(startDigest[:])
	}
	return strings.Join([]string{hex.EncodeToString(coreDigest[:]), versionEvidence, startEvidence}, ":")
}

func compatibleMatchFingerprints(previous, next string) bool {
	if previous == next {
		return true
	}
	previousCore, previousVersion, previousStart, previousOK := splitMatchFingerprint(previous)
	nextCore, nextVersion, nextStart, nextOK := splitMatchFingerprint(next)
	return previousOK && nextOK && previousCore == nextCore &&
		compatibleOptionalEvidence(previousVersion, nextVersion) && compatibleOptionalEvidence(previousStart, nextStart)
}

func mergeMatchFingerprintEvidence(previous, next string) string {
	previousCore, previousVersion, previousStart, previousOK := splitMatchFingerprint(previous)
	nextCore, nextVersion, nextStart, nextOK := splitMatchFingerprint(next)
	if !previousOK || !nextOK || previousCore != nextCore ||
		!compatibleOptionalEvidence(previousVersion, nextVersion) || !compatibleOptionalEvidence(previousStart, nextStart) {
		return previous
	}
	return strings.Join([]string{
		previousCore,
		strongerOptionalEvidence(previousVersion, nextVersion),
		strongerOptionalEvidence(previousStart, nextStart),
	}, ":")
}

func splitMatchFingerprint(value string) (core, version, start string, ok bool) {
	parts := strings.SplitN(value, ":", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

func compatibleOptionalEvidence(previous, next string) bool {
	return previous == next || previous == "none" || next == "none"
}

func strongerOptionalEvidence(previous, next string) string {
	if previous == "none" && next != "none" {
		return next
	}
	return previous
}

func matchSessionSummaryCompatible(session domain.MatchSession, game domain.GameState) bool {
	if session.PlayerRiotID != "" && !strings.EqualFold(strings.TrimSpace(session.PlayerRiotID), canonicalIdentity(game.ActivePlayer.Identity)) {
		return false
	}
	if session.ChampionName != "" && !strings.EqualFold(strings.TrimSpace(session.ChampionName), strings.TrimSpace(game.ActivePlayer.ChampionName)) {
		return false
	}
	if session.Team != "" && !strings.EqualFold(strings.TrimSpace(session.Team), strings.TrimSpace(game.ActivePlayer.Team)) {
		return false
	}
	if session.MapNumber != 0 && session.MapNumber != game.MapNumber {
		return false
	}
	if session.GameMode != "" && !strings.EqualFold(strings.TrimSpace(session.GameMode), strings.TrimSpace(game.GameMode)) {
		return false
	}
	return session.GameVersion == "" || game.GameVersion == "" || strings.EqualFold(strings.TrimSpace(session.GameVersion), strings.TrimSpace(game.GameVersion))
}

func recommendationRelevantDiff(previous, next domain.GameState) bool {
	if previous.GameMode != next.GameMode || previous.GameVersion != next.GameVersion || previous.MapNumber != next.MapNumber ||
		previous.ActivePlayer.Level != next.ActivePlayer.Level || previous.ActivePlayer.Score.Kills != next.ActivePlayer.Score.Kills ||
		previous.ActivePlayer.Score.Deaths != next.ActivePlayer.Score.Deaths || previous.ActivePlayer.Score.Assists != next.ActivePlayer.Score.Assists ||
		!sameItemCounts(previous.ActivePlayer.Items, next.ActivePlayer.Items) {
		return true
	}
	previousPlayers := participantSignals(previous.Players)
	nextPlayers := participantSignals(next.Players)
	if len(previousPlayers) != len(nextPlayers) {
		return true
	}
	for key, before := range previousPlayers {
		after, exists := nextPlayers[key]
		if !exists || before != after {
			return true
		}
	}
	return false
}

type participantSignal struct {
	Level   int
	Kills   int
	Deaths  int
	Assists int
	Items   string
}

func participantSignals(players []domain.Player) map[string]participantSignal {
	result := make(map[string]participantSignal, len(players))
	for _, player := range players {
		result[canonicalIdentity(player.Identity)+"|"+player.Team+"|"+player.ChampionName] = participantSignal{
			Level: player.Level, Kills: player.Score.Kills, Deaths: player.Score.Deaths, Assists: player.Score.Assists,
			Items: itemCountSignature(player.Items),
		}
	}
	return result
}

type itemDelta struct {
	ID    int `json:"id"`
	Count int `json:"count"`
}

func itemCountDiff(before, after map[int]int) (added, removed []itemDelta) {
	ids := make(map[int]struct{}, len(before)+len(after))
	for id := range before {
		ids[id] = struct{}{}
	}
	for id := range after {
		ids[id] = struct{}{}
	}
	ordered := make([]int, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Ints(ordered)
	for _, id := range ordered {
		delta := after[id] - before[id]
		if delta > 0 {
			added = append(added, itemDelta{ID: id, Count: delta})
		}
		if delta < 0 {
			removed = append(removed, itemDelta{ID: id, Count: -delta})
		}
	}
	return added, removed
}

func itemCounts(items []domain.Item) map[int]int {
	counts := make(map[int]int)
	for _, item := range items {
		if item.ID <= 0 {
			continue
		}
		count := item.Count
		if count <= 0 {
			count = 1
		}
		counts[item.ID] += count
	}
	return counts
}

func sameItemCounts(left, right []domain.Item) bool {
	return itemCountSignature(left) == itemCountSignature(right)
}

func itemCountSignature(items []domain.Item) string {
	counts := itemCounts(items)
	ids := make([]int, 0, len(counts))
	for id := range counts {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, fmt.Sprintf("%d:%d", id, counts[id]))
	}
	return strings.Join(parts, ",")
}

func normalizedSourceEvents(events []domain.SourceEvent) []domain.SourceEvent {
	byKey := make(map[string]domain.SourceEvent, len(events))
	for _, event := range events {
		key := strings.TrimSpace(event.DedupeKey)
		if key == "" {
			if event.ID != 0 {
				key = fmt.Sprintf("source:%d", event.ID)
			} else {
				key = "source-hash:" + shortDigest(mustJSON(event))
			}
			event.DedupeKey = key
		}
		previous, exists := byKey[key]
		candidateCanonical, _ := json.Marshal(event)
		previousCanonical, _ := json.Marshal(previous)
		if !exists || bytes.Compare(candidateCanonical, previousCanonical) < 0 {
			byKey[key] = event
		}
	}
	result := make([]domain.SourceEvent, 0, len(byKey))
	for _, event := range byKey {
		result = append(result, event)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ID != result[j].ID {
			return result[i].ID < result[j].ID
		}
		if result[i].GameTimeSeconds != result[j].GameTimeSeconds {
			return result[i].GameTimeSeconds < result[j].GameTimeSeconds
		}
		return result[i].DedupeKey < result[j].DedupeKey
	})
	return result
}

func terminalSourceEvent(name string) bool {
	normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "", " ", "").Replace(strings.TrimSpace(name)))
	switch normalized {
	case "gameend", "gameended", "endofgame":
		return true
	default:
		return false
	}
}

func startOccurredAt(now time.Time, game domain.GameState) time.Time {
	for _, event := range game.SourceEvents {
		if strings.EqualFold(strings.TrimSpace(event.Name), "GameStart") {
			return now.Add(time.Duration(event.GameTimeSeconds-game.GameTimeSeconds) * time.Second).UTC()
		}
	}
	return now.Add(-time.Duration(game.GameTimeSeconds * float64(time.Second))).UTC()
}

func startSource(game domain.GameState) domain.EventSource {
	for _, event := range game.SourceEvents {
		if strings.EqualFold(strings.TrimSpace(event.Name), "GameStart") {
			return domain.EventSourceObserved
		}
	}
	return domain.EventSourceInferred
}

func startConfidence(game domain.GameState) float64 {
	if startSource(game) == domain.EventSourceObserved {
		return 1
	}
	return 0.8
}

func defaultSessionID(fingerprint string, startedAt time.Time) string {
	digest := sha256.Sum256([]byte(fingerprint + "|" + startedAt.UTC().Format(time.RFC3339Nano)))
	return "match-" + hex.EncodeToString(digest[:12])
}

func canonicalIdentity(identity domain.PlayerIdentity) string {
	if value := strings.TrimSpace(identity.RiotID); value != "" {
		return value
	}
	if identity.GameName != "" && identity.TagLine != "" {
		return strings.TrimSpace(identity.GameName) + "#" + strings.TrimSpace(identity.TagLine)
	}
	return strings.TrimSpace(identity.SummonerName)
}

func (detector *Detector) lastGameTime() float64 {
	if detector.lastGame != nil {
		return detector.lastGame.GameTimeSeconds
	}
	if detector.session != nil {
		return detector.session.LastGameTime
	}
	return 0
}

func cloneGame(game *domain.GameState) *domain.GameState {
	if game == nil {
		return nil
	}
	payload, _ := json.Marshal(game)
	var clone domain.GameState
	_ = json.Unmarshal(payload, &clone)
	return &clone
}

func cloneSession(session *domain.MatchSession) *domain.MatchSession {
	if session == nil {
		return nil
	}
	clone := *session
	if session.EndedAt != nil {
		ended := *session.EndedAt
		clone.EndedAt = &ended
	}
	if session.PlayerCheckpoint != nil {
		checkpoint := *session.PlayerCheckpoint
		checkpoint.Inventory = append([]domain.InventoryCount(nil), session.PlayerCheckpoint.Inventory...)
		clone.PlayerCheckpoint = &checkpoint
	}
	return &clone
}

func cloneSessionForSnapshot(session *domain.MatchSession) *domain.MatchSession {
	clone := cloneSession(session)
	if clone != nil {
		clone.PlayerCheckpoint = nil
	}
	return clone
}

func playerCheckpoint(matchID string, observedAt time.Time, game domain.GameState) *domain.MatchPlayerCheckpoint {
	role, roleKnown := domain.ParseRole(game.ActivePlayer.Position)
	counts := itemCounts(game.ActivePlayer.Items)
	ids := make([]int, 0, len(counts))
	for id := range counts {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	inventory := make([]domain.InventoryCount, 0, len(ids))
	for _, id := range ids {
		inventory = append(inventory, domain.InventoryCount{ItemID: id, Count: counts[id]})
	}
	return &domain.MatchPlayerCheckpoint{
		MatchID: matchID, ObservedAt: observedAt.UTC(), GameTimeSeconds: game.GameTimeSeconds,
		ChampionName: strings.TrimSpace(game.ActivePlayer.ChampionName), Role: role, RoleKnown: roleKnown,
		Level: game.ActivePlayer.Level, Kills: game.ActivePlayer.Score.Kills, Deaths: game.ActivePlayer.Score.Deaths,
		Assists: game.ActivePlayer.Score.Assists, CreepScore: game.ActivePlayer.Score.CreepScore, Inventory: inventory,
	}
}

func cloneEvents(events []domain.SemanticEvent) []domain.SemanticEvent {
	result := make([]domain.SemanticEvent, len(events))
	for index, event := range events {
		result[index] = event
		result[index].Payload = append(json.RawMessage(nil), event.Payload...)
	}
	return result
}

func upsertSession(sessions []domain.MatchSession, session domain.MatchSession) []domain.MatchSession {
	for index := range sessions {
		if sessions[index].ID == session.ID {
			sessions[index] = session
			return sessions
		}
	}
	return append(sessions, session)
}

func mustJSON(value any) json.RawMessage {
	payload, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return payload
}

func shortDigest(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:8])
}

func minFloat(left, right float64) float64 {
	if left < right {
		return left
	}
	return right
}
