package personalization

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"lolengine/internal/catalog"
)

var clientFeedbackIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,95}$`)

type Repository interface {
	LoadPersonalizationPreferences(context.Context) (Preferences, error)
	SavePersonalizationPreferences(context.Context, Preferences) error
	LoadRecommendationFeedback(context.Context) ([]FeedbackRecord, error)
	SaveRecommendationFeedback(context.Context, FeedbackRecord) (bool, error)
	LoadFeedbackDecision(context.Context, string) (FeedbackDecision, error)
	DeleteRecommendationFeedback(context.Context) (int64, error)
}

type CatalogProvider interface {
	Current() (catalog.Catalog, catalog.Status, bool)
}

type Provider interface {
	Snapshot(ContextKey) Snapshot
	Revision() uint64
	Status() Status
}

type Config struct {
	Now                       func() time.Time
	MinimumFeedbackSamples    int
	MaximumFeedbackAdjustment int
}

type Service struct {
	repository Repository
	catalogs   CatalogProvider
	now        func() time.Time
	minimum    int
	cap        int

	mu          sync.RWMutex
	writeMu     sync.Mutex
	preferences Preferences
	feedback    map[string]FeedbackRecord
	revision    uint64
	eligibility FeedbackEligibilityProvider
}

func NewService(repository Repository, catalogs CatalogProvider, config Config) (*Service, error) {
	if repository == nil || catalogs == nil {
		return nil, errors.New("personalization repository and catalog provider are required")
	}
	if config.Now == nil {
		config.Now = func() time.Time { return time.Now().UTC() }
	}
	if config.MinimumFeedbackSamples <= 0 {
		config.MinimumFeedbackSamples = DefaultMinimumSamples
	}
	if config.MaximumFeedbackAdjustment <= 0 {
		config.MaximumFeedbackAdjustment = DefaultFeedbackCap
	}
	preferences, err := repository.LoadPersonalizationPreferences(context.Background())
	if err != nil {
		return nil, fmt.Errorf("load personalization preferences: %w", err)
	}
	preferences = normalizedDefaults(preferences, config.Now())
	records, err := repository.LoadRecommendationFeedback(context.Background())
	if err != nil {
		return nil, fmt.Errorf("load recommendation feedback: %w", err)
	}
	service := &Service{
		repository: repository, catalogs: catalogs, now: config.Now,
		minimum: config.MinimumFeedbackSamples, cap: config.MaximumFeedbackAdjustment,
		preferences: clonePreferences(preferences), feedback: make(map[string]FeedbackRecord, len(records)), revision: preferences.Version,
	}
	for _, record := range records {
		service.feedback[record.ClientFeedbackID] = record
	}
	return service, nil
}

// SetFeedbackEligibilityProvider binds feedback writes to the current durable
// live recommendation after the coordinator has been constructed.
func (service *Service) SetFeedbackEligibilityProvider(provider FeedbackEligibilityProvider) {
	service.mu.Lock()
	service.eligibility = provider
	service.mu.Unlock()
}

func (service *Service) Current() Snapshot {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return Snapshot{Preferences: clonePreferences(service.preferences)}
}

func (service *Service) Options() (Options, error) {
	knowledge, status, ready := service.catalogs.Current()
	if !ready || !status.Ready || knowledge.Validate() != nil {
		return Options{}, ErrUnavailable
	}
	ids := make([]int, 0, len(knowledge.Items))
	for itemID, item := range knowledge.Items {
		if eligibleItem(item) {
			ids = append(ids, itemID)
		}
	}
	sort.Ints(ids)
	result := Options{Patch: knowledge.Patch, Items: make([]ItemOption, 0, len(ids))}
	for _, itemID := range ids {
		result.Items = append(result.Items, ItemOption{ID: itemID, Name: knowledge.Items[itemID].Name})
	}
	return result, nil
}

func (service *Service) Status() Status {
	service.mu.RLock()
	preferences := clonePreferences(service.preferences)
	baseRevision := service.revision
	service.mu.RUnlock()
	knowledge, catalogStatus, ready := service.catalogs.Current()
	catalogReady := ready && catalogStatus.Ready && knowledge.Validate() == nil
	preferencesValid := catalogReady && validateStoredPreferences(preferences, knowledge) == nil
	revision := revisionForCatalog(baseRevision, preferences, catalogStatus.Patch, catalogReady, preferencesValid)
	result := Status{
		Available: true, Enabled: !preferences.Default(), FeedbackEnabled: preferences.FeedbackEnabled,
		Style: preferences.Style, Version: preferences.Version, Revision: revision, State: "ready",
	}
	if !catalogReady {
		result.Available = false
		result.Enabled = false
		result.State = "catalog_unavailable"
		result.LastError = "active catalog is unavailable"
		return result
	}
	if !preferencesValid {
		result.Available = false
		result.Enabled = false
		result.State = "catalog_mismatch"
		result.LastError = "stored item preferences are not valid for the active catalog"
	}
	return result
}

func (service *Service) Revision() uint64 {
	service.mu.RLock()
	baseRevision := service.revision
	preferences := clonePreferences(service.preferences)
	service.mu.RUnlock()
	knowledge, status, ready := service.catalogs.Current()
	catalogReady := ready && status.Ready && knowledge.Validate() == nil
	preferencesValid := catalogReady && validateStoredPreferences(preferences, knowledge) == nil
	return revisionForCatalog(baseRevision, preferences, status.Patch, catalogReady, preferencesValid)
}

func (service *Service) Snapshot(key ContextKey) Snapshot {
	service.mu.RLock()
	preferences := clonePreferences(service.preferences)
	feedback := make([]FeedbackRecord, 0, len(service.feedback))
	for _, record := range service.feedback {
		feedback = append(feedback, record)
	}
	service.mu.RUnlock()
	knowledge, catalogStatus, ready := service.catalogs.Current()
	if !ready || !catalogStatus.Ready || knowledge.Validate() != nil ||
		(key.Patch != "" && key.Patch != knowledge.Patch) || validateStoredPreferences(preferences, knowledge) != nil {
		return Snapshot{Preferences: inactivePreferences(preferences)}
	}
	result := Snapshot{Preferences: preferences}
	if !preferences.FeedbackEnabled {
		return result
	}
	counts := make(map[int]FeedbackEffect)
	for _, record := range feedback {
		if !feedbackMatches(record, key) {
			continue
		}
		effect := counts[record.ItemID]
		effect.Samples++
		if record.Value == FeedbackUseful {
			effect.Useful++
		} else {
			effect.NotUseful++
		}
		counts[record.ItemID] = effect
	}
	result.Feedback = make(map[int]FeedbackEffect)
	for itemID, effect := range counts {
		if effect.Samples < service.minimum {
			continue
		}
		delta := (effect.Useful - effect.NotUseful) * 2
		if delta > service.cap {
			delta = service.cap
		}
		if delta < -service.cap {
			delta = -service.cap
		}
		if delta == 0 {
			continue
		}
		effect.Adjustment = delta
		if delta > 0 {
			effect.ReasonCode = ReasonFeedbackUseful
		} else {
			effect.ReasonCode = ReasonFeedbackNotUseful
		}
		result.Feedback[itemID] = effect
	}
	return result
}

func (service *Service) Update(ctx context.Context, update PreferenceUpdate) (Preferences, error) {
	service.writeMu.Lock()
	defer service.writeMu.Unlock()
	knowledge, status, ready := service.catalogs.Current()
	if !ready || !status.Ready || knowledge.Validate() != nil {
		return Preferences{}, ErrUnavailable
	}
	next, err := validatePreferenceUpdate(update, knowledge)
	if err != nil {
		return Preferences{}, err
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	next.Version = max(service.preferences.Version, 1) + 1
	next.UpdatedAt = service.now().UTC()
	next.ResetAt = service.preferences.ResetAt
	if err := service.repository.SavePersonalizationPreferences(ctx, next); err != nil {
		return Preferences{}, err
	}
	service.preferences = clonePreferences(next)
	service.revision++
	return clonePreferences(next), nil
}

func (service *Service) Reset(ctx context.Context) (Preferences, error) {
	service.writeMu.Lock()
	defer service.writeMu.Unlock()
	service.mu.Lock()
	defer service.mu.Unlock()
	now := service.now().UTC()
	next := Preferences{
		Style: StyleBalanced, PreferredItemIDs: []int{}, AvoidedItemIDs: []int{},
		Version: max(service.preferences.Version, 1) + 1, UpdatedAt: now, ResetAt: now,
	}
	if err := service.repository.SavePersonalizationPreferences(ctx, next); err != nil {
		return Preferences{}, err
	}
	service.preferences = next
	service.revision++
	return clonePreferences(next), nil
}

func (service *Service) SubmitFeedback(ctx context.Context, submission FeedbackSubmission) (FeedbackResult, error) {
	service.writeMu.Lock()
	defer service.writeMu.Unlock()
	clientFeedbackID := strings.TrimSpace(submission.ClientFeedbackID)
	decisionEventID := strings.TrimSpace(submission.DecisionEventID)
	if clientFeedbackID != submission.ClientFeedbackID || !clientFeedbackIDPattern.MatchString(clientFeedbackID) {
		return FeedbackResult{}, fmt.Errorf("%w: client_feedback_id is invalid", ErrInvalidPreference)
	}
	if decisionEventID != submission.DecisionEventID || decisionEventID == "" || len(decisionEventID) > 192 {
		return FeedbackResult{}, fmt.Errorf("%w: decision_event_id is invalid", ErrInvalidPreference)
	}
	if submission.ItemID <= 0 || !submission.Value.Valid() {
		return FeedbackResult{}, fmt.Errorf("%w: item_id and value are invalid", ErrInvalidPreference)
	}
	service.mu.RLock()
	enabled := service.preferences.FeedbackEnabled
	if existing, exists := service.feedback[clientFeedbackID]; exists {
		service.mu.RUnlock()
		if feedbackSubmissionMatches(existing, submission) {
			return FeedbackResult{Record: existing}, nil
		}
		return FeedbackResult{}, ErrFeedbackConflict
	}
	eligibilityProvider := service.eligibility
	service.mu.RUnlock()
	if !enabled {
		return FeedbackResult{}, ErrFeedbackDisabled
	}
	if eligibilityProvider == nil {
		return FeedbackResult{}, ErrChoiceNotVisible
	}
	eligibility, eligible := eligibilityProvider.CurrentFeedbackEligibility()
	if !eligible || eligibility.EventID != decisionEventID || eligibility.SessionID == "" ||
		!containsInt(eligibility.VisibleItemIDs, submission.ItemID) {
		return FeedbackResult{}, ErrChoiceNotVisible
	}
	decision, err := service.repository.LoadFeedbackDecision(ctx, decisionEventID)
	if err != nil {
		return FeedbackResult{}, err
	}
	visible := false
	for _, choice := range decision.Evidence.RankedChoices {
		if choice.Item.ID == submission.ItemID {
			visible = true
			break
		}
	}
	if !visible {
		return FeedbackResult{}, ErrChoiceNotVisible
	}
	if !feedbackDecisionMatchesEligibility(decision, eligibility) {
		return FeedbackResult{}, ErrChoiceNotVisible
	}
	knowledge, status, ready := service.catalogs.Current()
	item, exists := knowledge.Items[submission.ItemID]
	if !ready || !status.Ready || knowledge.Validate() != nil || decision.Evidence.Patch != knowledge.Patch ||
		eligibility.Evidence.Patch != knowledge.Patch || !exists || !eligibleItem(item) {
		return FeedbackResult{}, ErrUnavailable
	}
	champion, championKnown := catalog.ResolveChampion(knowledge, decision.ChampionID)
	currentChampion, currentChampionKnown := catalog.ResolveChampion(knowledge, eligibility.ChampionID)
	if !championKnown || !currentChampionKnown || champion.ID != currentChampion.ID || decision.Role != eligibility.Role ||
		!decision.Role.Valid() || strings.TrimSpace(decision.Evidence.Rules.Version) == "" || strings.TrimSpace(decision.Evidence.Rules.Source) == "" {
		return FeedbackResult{}, ErrChoiceNotVisible
	}
	record := FeedbackRecord{
		ClientFeedbackID: clientFeedbackID, DecisionEventID: decision.EventID,
		SessionID: decision.SessionID, DecisionSequence: decision.Sequence,
		RulesVersion: decision.Evidence.Rules.Version, RulesSource: decision.Evidence.Rules.Source,
		Patch: decision.Evidence.Patch, ChampionID: champion.ID, Role: decision.Role,
		Stage: decision.Evidence.Phase, Strategy: decision.Evidence.Strategy,
		ItemID: submission.ItemID, Value: submission.Value, CreatedAt: service.now().UTC(),
	}
	if decision.Evidence.Profile != nil {
		record.ProfileID = decision.Evidence.Profile.ProfileID
		record.ProfileVersion = decision.Evidence.Profile.RulesetVersion
		if record.Role == "" {
			record.Role = decision.Evidence.Profile.Role
		}
	}
	inserted, err := service.repository.SaveRecommendationFeedback(ctx, record)
	if err != nil {
		return FeedbackResult{}, err
	}
	if !inserted {
		records, loadErr := service.repository.LoadRecommendationFeedback(ctx)
		if loadErr != nil {
			return FeedbackResult{}, loadErr
		}
		found := false
		for _, stored := range records {
			if stored.ClientFeedbackID == record.ClientFeedbackID ||
				(stored.DecisionEventID == record.DecisionEventID && stored.ItemID == record.ItemID) {
				if !feedbackRecordsLogicallyEqual(stored, record) {
					return FeedbackResult{}, ErrFeedbackConflict
				}
				record = stored
				found = true
				break
			}
		}
		if !found {
			return FeedbackResult{}, errors.New("feedback retry was not found after an idempotent save")
		}
	}
	service.mu.Lock()
	if stored, exists := service.feedback[record.ClientFeedbackID]; exists {
		service.mu.Unlock()
		if feedbackRecordsEqual(stored, record) {
			return FeedbackResult{Record: stored}, nil
		}
		return FeedbackResult{}, ErrFeedbackConflict
	}
	service.feedback[record.ClientFeedbackID] = record
	service.revision++
	service.mu.Unlock()
	return FeedbackResult{Inserted: inserted, Record: record}, nil
}

func feedbackDecisionMatchesEligibility(decision FeedbackDecision, eligibility FeedbackEligibility) bool {
	if decision.EventID != eligibility.EventID || decision.SessionID != eligibility.SessionID || decision.Role != eligibility.Role {
		return false
	}
	stored, storedErr := json.Marshal(decision.Evidence)
	current, currentErr := json.Marshal(eligibility.Evidence)
	return storedErr == nil && currentErr == nil && string(stored) == string(current)
}

func containsInt(values []int, value int) bool {
	for _, current := range values {
		if current == value {
			return true
		}
	}
	return false
}

func (service *Service) Summary() FeedbackSummary {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return feedbackSummary(service.preferences.FeedbackEnabled, service.minimum, service.cap, service.feedback)
}

func (service *Service) Export() Export {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return Export{SchemaVersion: 1, ExportedAt: service.now().UTC(), Preferences: clonePreferences(service.preferences), Feedback: feedbackSummary(service.preferences.FeedbackEnabled, service.minimum, service.cap, service.feedback)}
}

func (service *Service) DeleteFeedback(ctx context.Context, confirmation string) (int64, error) {
	service.writeMu.Lock()
	defer service.writeMu.Unlock()
	if confirmation != DeleteFeedbackConfirmation {
		return 0, ErrDeleteConfirmation
	}
	count, err := service.repository.DeleteRecommendationFeedback(ctx)
	if err != nil {
		return 0, err
	}
	service.mu.Lock()
	service.feedback = make(map[string]FeedbackRecord)
	if count > 0 {
		service.revision++
	}
	service.mu.Unlock()
	return count, nil
}

func normalizedDefaults(value Preferences, now time.Time) Preferences {
	if !value.Style.Valid() {
		value.Style = StyleBalanced
	}
	if value.Version == 0 {
		value.Version = 1
	}
	if value.UpdatedAt.IsZero() {
		value.UpdatedAt = now.UTC()
	}
	value.PreferredItemIDs = sortedUniqueInts(value.PreferredItemIDs)
	value.AvoidedItemIDs = sortedUniqueInts(value.AvoidedItemIDs)
	return value
}

func validatePreferenceUpdate(update PreferenceUpdate, knowledge catalog.Catalog) (Preferences, error) {
	if !update.Style.Valid() {
		return Preferences{}, fmt.Errorf("%w: style must be safe, balanced, or aggressive", ErrInvalidPreference)
	}
	if len(update.PreferredItemIDs) > MaximumPreferredItems || len(update.AvoidedItemIDs) > MaximumAvoidedItems {
		return Preferences{}, fmt.Errorf("%w: item preference count exceeds bounds", ErrInvalidPreference)
	}
	preferred := sortedUniqueInts(update.PreferredItemIDs)
	avoided := sortedUniqueInts(update.AvoidedItemIDs)
	if len(preferred) != len(update.PreferredItemIDs) || len(avoided) != len(update.AvoidedItemIDs) {
		return Preferences{}, fmt.Errorf("%w: duplicate item id", ErrInvalidPreference)
	}
	for _, itemID := range append(append([]int(nil), preferred...), avoided...) {
		item, exists := knowledge.Items[itemID]
		if !exists {
			return Preferences{}, fmt.Errorf("%w: item %d is not in the active catalog", ErrInvalidPreference, itemID)
		}
		if !item.Purchasable || !item.Gold.Purchasable {
			return Preferences{}, fmt.Errorf("%w: item %d is not purchasable", ErrInvalidPreference, itemID)
		}
		if !item.AvailableMap11 {
			return Preferences{}, fmt.Errorf("%w: item %d is not available on map 11", ErrInvalidPreference, itemID)
		}
	}
	preferredSet := make(map[int]struct{}, len(preferred))
	for _, itemID := range preferred {
		preferredSet[itemID] = struct{}{}
	}
	for _, itemID := range avoided {
		if _, overlap := preferredSet[itemID]; overlap {
			return Preferences{}, fmt.Errorf("%w: preferred and avoided item sets overlap", ErrInvalidPreference)
		}
	}
	return Preferences{Style: update.Style, PreferredItemIDs: preferred, AvoidedItemIDs: avoided, FeedbackEnabled: update.FeedbackEnabled}, nil
}

func validateStoredPreferences(preferences Preferences, knowledge catalog.Catalog) error {
	_, err := validatePreferenceUpdate(PreferenceUpdate{
		Style: preferences.Style, PreferredItemIDs: preferences.PreferredItemIDs,
		AvoidedItemIDs: preferences.AvoidedItemIDs, FeedbackEnabled: preferences.FeedbackEnabled,
	}, knowledge)
	return err
}

func inactivePreferences(preferences Preferences) Preferences {
	return Preferences{
		Style: StyleBalanced, PreferredItemIDs: []int{}, AvoidedItemIDs: []int{}, Version: preferences.Version,
		UpdatedAt: preferences.UpdatedAt, ResetAt: preferences.ResetAt,
	}
}

func revisionForCatalog(base uint64, preferences Preferences, patch string, catalogReady, preferencesValid bool) uint64 {
	// Preference/feedback mutations advance the high bits monotonically. The
	// low catalog fingerprint makes an active preference snapshot change when
	// the catalog patch or its validation state changes without persisting a
	// synthetic preference version.
	result := base << 32
	if preferences.Default() {
		return result
	}
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(strings.TrimSpace(patch)))
	if catalogReady {
		_, _ = hash.Write([]byte{1})
	}
	if preferencesValid {
		_, _ = hash.Write([]byte{2})
	}
	return result | (hash.Sum64() & ((1 << 32) - 1))
}

func eligibleItem(item catalog.Item) bool {
	return item.ID > 0 && strings.TrimSpace(item.Name) != "" && item.Purchasable && item.Gold.Purchasable && item.AvailableMap11
}

func feedbackMatches(record FeedbackRecord, key ContextKey) bool {
	if !strings.EqualFold(record.ChampionID, key.ChampionID) || record.Role != key.Role || record.Stage != key.Phase || record.Strategy != key.Strategy || record.Patch != key.Patch {
		return false
	}
	if record.ProfileID != "" && record.ProfileID != key.ProfileID {
		return false
	}
	if record.ProfileVersion != "" && record.ProfileVersion != key.ProfileVersion {
		return false
	}
	return record.RulesVersion == key.RulesVersion
}

func feedbackSubmissionMatches(record FeedbackRecord, submission FeedbackSubmission) bool {
	return record.DecisionEventID == strings.TrimSpace(submission.DecisionEventID) && record.ItemID == submission.ItemID && record.Value == submission.Value
}

func feedbackRecordsEqual(left, right FeedbackRecord) bool {
	return left.ClientFeedbackID == right.ClientFeedbackID && left.DecisionEventID == right.DecisionEventID && left.SessionID == right.SessionID &&
		left.DecisionSequence == right.DecisionSequence && left.ProfileID == right.ProfileID && left.ProfileVersion == right.ProfileVersion &&
		left.RulesVersion == right.RulesVersion && left.RulesSource == right.RulesSource && left.Patch == right.Patch &&
		left.ChampionID == right.ChampionID && left.Role == right.Role && left.Stage == right.Stage && left.Strategy == right.Strategy &&
		left.ItemID == right.ItemID && left.Value == right.Value && left.CreatedAt.Equal(right.CreatedAt)
}

func feedbackRecordsLogicallyEqual(left, right FeedbackRecord) bool {
	return left.DecisionEventID == right.DecisionEventID && left.SessionID == right.SessionID &&
		left.DecisionSequence == right.DecisionSequence && left.ProfileID == right.ProfileID && left.ProfileVersion == right.ProfileVersion &&
		left.RulesVersion == right.RulesVersion && left.RulesSource == right.RulesSource && left.Patch == right.Patch &&
		left.ChampionID == right.ChampionID && left.Role == right.Role && left.Stage == right.Stage && left.Strategy == right.Strategy &&
		left.ItemID == right.ItemID && left.Value == right.Value
}

func feedbackSummary(enabled bool, minimumSamples, maximumAdjustment int, records map[string]FeedbackRecord) FeedbackSummary {
	result := FeedbackSummary{
		Enabled: enabled, MinimumSamples: minimumSamples, MaximumAdjustment: maximumAdjustment,
		Items: make([]ItemFeedbackSummary, 0),
	}
	byItem := make(map[int]ItemFeedbackSummary)
	for _, record := range records {
		result.Total++
		item := byItem[record.ItemID]
		item.ItemID = record.ItemID
		if record.Value == FeedbackUseful {
			result.Useful++
			item.Useful++
		} else {
			result.NotUseful++
			item.NotUseful++
		}
		byItem[record.ItemID] = item
	}
	ids := make([]int, 0, len(byItem))
	for itemID := range byItem {
		ids = append(ids, itemID)
	}
	sort.Ints(ids)
	for _, itemID := range ids {
		result.Items = append(result.Items, byItem[itemID])
	}
	return result
}

func sortedUniqueInts(values []int) []int {
	result := append([]int{}, values...)
	sort.Ints(result)
	if len(result) < 2 {
		return result
	}
	write := 1
	for read := 1; read < len(result); read++ {
		if result[read] == result[write-1] {
			continue
		}
		result[write] = result[read]
		write++
	}
	return result[:write]
}

func clonePreferences(value Preferences) Preferences {
	value.PreferredItemIDs = append([]int{}, value.PreferredItemIDs...)
	value.AvoidedItemIDs = append([]int{}, value.AvoidedItemIDs...)
	return value
}
