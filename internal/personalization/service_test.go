package personalization

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"lolengine/internal/catalog"
	"lolengine/internal/domain"
)

func TestUpdatePreferencesValidatesCatalogAndOverlap(t *testing.T) {
	repository := newMemoryRepository()
	service, err := NewService(repository, fixedCatalogProvider(testCatalog()), Config{Now: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		update  PreferenceUpdate
		wantErr string
	}{
		{name: "valid sorted snapshot", update: PreferenceUpdate{Style: StyleSafe, PreferredItemIDs: []int{3002, 3001}, AvoidedItemIDs: []int{3003}, FeedbackEnabled: true}},
		{name: "overlap", update: PreferenceUpdate{Style: StyleBalanced, PreferredItemIDs: []int{3001}, AvoidedItemIDs: []int{3001}}, wantErr: "overlap"},
		{name: "unknown item", update: PreferenceUpdate{Style: StyleBalanced, PreferredItemIDs: []int{9999}}, wantErr: "catalog"},
		{name: "not purchasable", update: PreferenceUpdate{Style: StyleBalanced, AvoidedItemIDs: []int{3004}}, wantErr: "purchasable"},
		{name: "wrong map", update: PreferenceUpdate{Style: StyleBalanced, PreferredItemIDs: []int{3005}}, wantErr: "map 11"},
		{name: "invalid style", update: PreferenceUpdate{Style: Style("fast")}, wantErr: "style"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := service.Update(context.Background(), test.update)
			if test.wantErr != "" {
				if err == nil || !containsError(err, test.wantErr) {
					t.Fatalf("Update() error = %v, want containing %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Update() error = %v", err)
			}
			if got.Version != 2 || got.Style != StyleSafe || len(got.PreferredItemIDs) != 2 || got.PreferredItemIDs[0] != 3001 {
				t.Fatalf("Update() = %+v", got)
			}
			got.PreferredItemIDs[0] = 99
			if service.Current().Preferences.PreferredItemIDs[0] != 3001 {
				t.Fatal("returned preferences alias immutable service snapshot")
			}
		})
	}
}

func TestFeedbackIsOptInIdempotentBoundedAndContextual(t *testing.T) {
	repository := newMemoryRepository()
	service, err := NewService(repository, fixedCatalogProvider(testCatalog()), Config{Now: fixedNow, MinimumFeedbackSamples: 3, MaximumFeedbackAdjustment: 9})
	if err != nil {
		t.Fatal(err)
	}
	decision := FeedbackDecision{
		EventID: "match-1:decision:000001", SessionID: "match-1", Sequence: 1,
		Evidence: domain.RecommendationEvidence{
			SchemaVersion: 1, Patch: "16.18.1", Phase: domain.PhaseMid, Strategy: domain.StrategyTeamfight,
			Rules: domain.RuleProvenance{Version: "garen-v2.1", Source: "external_exact"},
			RankedChoices: []domain.EvidenceChoice{
				{Rank: 1, Item: domain.EvidenceItem{ID: 3001}},
				{Rank: 2, Item: domain.EvidenceItem{ID: 3002}},
			},
		},
		ChampionID: "Garen", Role: domain.RoleTop,
	}
	repository.decisions[decision.EventID] = decision
	service.SetFeedbackEligibilityProvider(fixedEligibilityProvider{eligibility: eligibilityForDecision(decision)})
	if _, err := service.SubmitFeedback(context.Background(), FeedbackSubmission{ClientFeedbackID: "client-1", DecisionEventID: decision.EventID, ItemID: 3001, Value: FeedbackUseful}); !errors.Is(err, ErrFeedbackDisabled) {
		t.Fatalf("SubmitFeedback() disabled error = %v", err)
	}
	if _, err := service.Update(context.Background(), PreferenceUpdate{Style: StyleBalanced, FeedbackEnabled: true}); err != nil {
		t.Fatal(err)
	}
	key := ContextKey{ChampionID: "Garen", Role: domain.RoleTop, Phase: domain.PhaseMid, Strategy: domain.StrategyTeamfight, Patch: "16.18.1", RulesVersion: "garen-v2.1"}
	for index := 1; index <= 8; index++ {
		currentDecision := decision
		currentDecision.EventID = fmt.Sprintf("match-1:decision:%06d", index)
		currentDecision.Sequence = uint64(index)
		repository.decisions[currentDecision.EventID] = currentDecision
		service.SetFeedbackEligibilityProvider(fixedEligibilityProvider{eligibility: eligibilityForDecision(currentDecision)})
		result, err := service.SubmitFeedback(context.Background(), FeedbackSubmission{
			ClientFeedbackID: "client-" + string(rune('0'+index)), DecisionEventID: currentDecision.EventID, ItemID: 3001, Value: FeedbackUseful,
		})
		if err != nil {
			t.Fatal(err)
		}
		if index == 1 {
			revision := service.Revision()
			retry, retryErr := service.SubmitFeedback(context.Background(), FeedbackSubmission{
				ClientFeedbackID: "client-1", DecisionEventID: currentDecision.EventID, ItemID: 3001, Value: FeedbackUseful,
			})
			if retryErr != nil || retry.Inserted || retry.Record.ClientFeedbackID != result.Record.ClientFeedbackID || service.Revision() != revision {
				t.Fatalf("idempotent retry = %+v, %v", retry, retryErr)
			}
		}
		if index == 2 && len(service.Snapshot(key).Feedback) != 0 {
			t.Fatal("feedback activated below the minimum sample threshold")
		}
	}
	snapshot := service.Snapshot(key)
	effect := snapshot.Feedback[3001]
	if effect.Samples != 8 || effect.Adjustment != 9 || effect.ReasonCode != ReasonFeedbackUseful {
		t.Fatalf("feedback effect = %+v", effect)
	}
	if other := service.Snapshot(ContextKey{ChampionID: "Ahri", Role: domain.RoleMiddle, Phase: domain.PhaseMid, Strategy: domain.StrategyTeamfight, Patch: "16.18.1"}); len(other.Feedback) != 0 {
		t.Fatalf("unrelated context feedback = %+v", other.Feedback)
	}
	for index := 1; index <= 5; index++ {
		currentDecision := decision
		currentDecision.EventID = fmt.Sprintf("match-1:decision:%06d", index+8)
		currentDecision.Sequence = uint64(index + 8)
		repository.decisions[currentDecision.EventID] = currentDecision
		service.SetFeedbackEligibilityProvider(fixedEligibilityProvider{eligibility: eligibilityForDecision(currentDecision)})
		if _, err := service.SubmitFeedback(context.Background(), FeedbackSubmission{
			ClientFeedbackID: fmt.Sprintf("negative-%d", index), DecisionEventID: currentDecision.EventID, ItemID: 3002, Value: FeedbackNotUseful,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if effect := service.Snapshot(key).Feedback[3002]; effect.Samples != 5 || effect.Adjustment != -9 || effect.ReasonCode != ReasonFeedbackNotUseful {
		t.Fatalf("negative bounded effect = %+v", effect)
	}
	if summary := service.Summary(); summary.MinimumSamples != 3 || summary.MaximumAdjustment != 9 || summary.Total != 13 {
		t.Fatalf("explainable feedback summary = %+v", summary)
	}
	if _, err := service.SubmitFeedback(context.Background(), FeedbackSubmission{ClientFeedbackID: "wrong-choice", DecisionEventID: "match-1:decision:000013", ItemID: 3003, Value: FeedbackUseful}); !errors.Is(err, ErrChoiceNotVisible) {
		t.Fatalf("invisible choice error = %v", err)
	}
	if _, err := service.Update(context.Background(), PreferenceUpdate{Style: StyleBalanced, FeedbackEnabled: false}); err != nil {
		t.Fatal(err)
	}
	if disabled := service.Snapshot(key); len(disabled.Feedback) != 0 || !disabled.Default() {
		t.Fatalf("disabled feedback still affects snapshot: %+v", disabled)
	}
}

func TestResetAndDisableRestoreDefaultSnapshot(t *testing.T) {
	repository := newMemoryRepository()
	service, err := NewService(repository, fixedCatalogProvider(testCatalog()), Config{Now: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Update(context.Background(), PreferenceUpdate{Style: StyleAggressive, PreferredItemIDs: []int{3001}, FeedbackEnabled: true}); err != nil {
		t.Fatal(err)
	}
	before := service.Revision()
	reset, err := service.Reset(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if reset.Style != StyleBalanced || reset.FeedbackEnabled || len(reset.PreferredItemIDs) != 0 || !reset.ResetAt.Equal(fixedNow()) {
		t.Fatalf("Reset() = %+v", reset)
	}
	if !service.Snapshot(ContextKey{}).Default() || service.Revision() <= before {
		t.Fatalf("default=%v revision=%d before=%d", service.Snapshot(ContextKey{}).Default(), service.Revision(), before)
	}
}

func TestActiveCatalogChangeFailsClosedUntilPreferencesValidateAgain(t *testing.T) {
	repository := newMemoryRepository()
	provider := &mutableCatalogProvider{knowledge: testCatalog(), ready: true}
	service, err := NewService(repository, provider, Config{Now: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Update(context.Background(), PreferenceUpdate{
		Style: StyleSafe, PreferredItemIDs: []int{3001}, FeedbackEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	before := service.Revision()
	if snapshot := service.Snapshot(ContextKey{Patch: "16.18.1"}); snapshot.Preferences.Style != StyleSafe {
		t.Fatalf("active patch snapshot = %+v", snapshot)
	}

	next := testCatalog()
	next.Patch = "16.19.1"
	delete(next.Items, 3001)
	provider.knowledge = next
	if snapshot := service.Snapshot(ContextKey{Patch: next.Patch}); !snapshot.Default() {
		t.Fatalf("catalog-invalid preferences remained active: %+v", snapshot)
	}
	status := service.Status()
	if status.Available || status.Enabled || status.State != "catalog_mismatch" || status.Revision == before {
		t.Fatalf("catalog mismatch status = %+v, prior revision=%d", status, before)
	}
	if current := service.Current().Preferences; current.Style != StyleSafe || len(current.PreferredItemIDs) != 1 {
		t.Fatalf("stored preferences were silently discarded: %+v", current)
	}

	updated, err := service.Update(context.Background(), PreferenceUpdate{
		Style: StyleSafe, PreferredItemIDs: []int{3002}, FeedbackEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.PreferredItemIDs[0] != 3002 || !service.Status().Available || service.Snapshot(ContextKey{Patch: next.Patch}).Preferences.Style != StyleSafe {
		t.Fatalf("preferences did not recover on the active patch: updated=%+v status=%+v", updated, service.Status())
	}
}

func TestFeedbackIdentifiersAreCanonicalAndWhitespaceStrict(t *testing.T) {
	repository := newMemoryRepository()
	knowledge := testCatalog()
	knowledge.Champions["DrMundo"] = catalog.Champion{ID: "DrMundo", Name: "Dr. Mundo"}
	service, err := NewService(repository, fixedCatalogProvider(knowledge), Config{Now: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Update(context.Background(), PreferenceUpdate{Style: StyleBalanced, FeedbackEnabled: true}); err != nil {
		t.Fatal(err)
	}
	decision := FeedbackDecision{
		EventID: "match-2:decision:000001", SessionID: "match-2", Sequence: 1, ChampionID: "Dr. Mundo", Role: domain.RoleTop,
		Evidence: domain.RecommendationEvidence{
			Patch: "16.18.1", Phase: domain.PhaseMid, Strategy: domain.StrategyBalanced,
			Rules:         domain.RuleProvenance{Version: "garen-v2.1", Source: "external_exact"},
			RankedChoices: []domain.EvidenceChoice{{Rank: 1, Item: domain.EvidenceItem{ID: 3001}}},
		},
	}
	repository.decisions[decision.EventID] = decision
	service.SetFeedbackEligibilityProvider(fixedEligibilityProvider{eligibility: eligibilityForDecision(decision)})
	for _, submission := range []FeedbackSubmission{
		{ClientFeedbackID: " client-space", DecisionEventID: decision.EventID, ItemID: 3001, Value: FeedbackUseful},
		{ClientFeedbackID: "client-space", DecisionEventID: decision.EventID + " ", ItemID: 3001, Value: FeedbackUseful},
	} {
		if _, err := service.SubmitFeedback(context.Background(), submission); !errors.Is(err, ErrInvalidPreference) {
			t.Fatalf("whitespace identifier error = %v", err)
		}
	}
	result, err := service.SubmitFeedback(context.Background(), FeedbackSubmission{
		ClientFeedbackID: "client-canonical", DecisionEventID: decision.EventID, ItemID: 3001, Value: FeedbackUseful,
	})
	if err != nil || result.Record.ChampionID != "DrMundo" {
		t.Fatalf("canonical feedback = %+v, %v", result, err)
	}
}

func TestFeedbackRequiresExactCurrentPersistedRecommendationContext(t *testing.T) {
	repository := newMemoryRepository()
	catalogProvider := &mutableCatalogProvider{knowledge: testCatalog(), ready: true}
	service, err := NewService(repository, catalogProvider, Config{Now: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Update(context.Background(), PreferenceUpdate{Style: StyleBalanced, FeedbackEnabled: true}); err != nil {
		t.Fatal(err)
	}
	decision := FeedbackDecision{
		EventID: "match-current:decision:000001", SessionID: "match-current", Sequence: 1,
		ChampionID: "Garen", Role: domain.RoleTop,
		Evidence: domain.RecommendationEvidence{
			SchemaVersion: 1, Status: "ready", Patch: "16.18.1", Phase: domain.PhaseMid, Strategy: domain.StrategyTeamfight,
			Rules:         domain.RuleProvenance{Version: "garen-v2.1", Patch: "16.18.1", Source: "external_exact"},
			RankedChoices: []domain.EvidenceChoice{{Rank: 1, Item: domain.EvidenceItem{ID: 3001}}},
		},
	}
	repository.decisions[decision.EventID] = decision

	tests := []struct {
		name        string
		eligibility FeedbackEligibility
	}{
		{name: "no current evidence"},
		{name: "different session", eligibility: func() FeedbackEligibility {
			value := eligibilityForDecision(decision)
			value.SessionID = "match-other"
			return value
		}()},
		{name: "different phase", eligibility: func() FeedbackEligibility {
			value := eligibilityForDecision(decision)
			value.Evidence.Phase = domain.PhaseLate
			return value
		}()},
		{name: "different strategy", eligibility: func() FeedbackEligibility {
			value := eligibilityForDecision(decision)
			value.Evidence.Strategy = domain.StrategySplitPush
			return value
		}()},
		{name: "different patch", eligibility: func() FeedbackEligibility {
			value := eligibilityForDecision(decision)
			value.Evidence.Patch = "16.19.1"
			return value
		}()},
		{name: "different rule provenance", eligibility: func() FeedbackEligibility {
			value := eligibilityForDecision(decision)
			value.Evidence.Rules.Version = "garen-v2.2"
			return value
		}()},
		{name: "choice no longer visible", eligibility: func() FeedbackEligibility {
			value := eligibilityForDecision(decision)
			value.Evidence.RankedChoices = []domain.EvidenceChoice{{Rank: 1, Item: domain.EvidenceItem{ID: 3002}}}
			value.VisibleItemIDs = []int{3002}
			return value
		}()},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service.SetFeedbackEligibilityProvider(fixedEligibilityProvider{eligibility: test.eligibility, ready: test.name != "no current evidence"})
			_, err := service.SubmitFeedback(context.Background(), FeedbackSubmission{
				ClientFeedbackID: fmt.Sprintf("context-%d", index), DecisionEventID: decision.EventID, ItemID: 3001, Value: FeedbackUseful,
			})
			if !errors.Is(err, ErrChoiceNotVisible) {
				t.Fatalf("SubmitFeedback() error = %v", err)
			}
		})
	}

	service.SetFeedbackEligibilityProvider(fixedEligibilityProvider{eligibility: eligibilityForDecision(decision), ready: true})
	if result, err := service.SubmitFeedback(context.Background(), FeedbackSubmission{
		ClientFeedbackID: "context-valid", DecisionEventID: decision.EventID, ItemID: 3001, Value: FeedbackUseful,
	}); err != nil || !result.Inserted {
		t.Fatalf("matching current evidence = %+v, %v", result, err)
	}
	retry, err := service.SubmitFeedback(context.Background(), FeedbackSubmission{
		ClientFeedbackID: "context-lost-response-retry", DecisionEventID: decision.EventID, ItemID: 3001, Value: FeedbackUseful,
	})
	if err != nil || retry.Inserted || retry.Record.ClientFeedbackID != "context-valid" || service.Summary().Total != 1 {
		t.Fatalf("logical retry with a different client id = %+v, %v summary=%+v", retry, err, service.Summary())
	}
	patchChanged := decision
	patchChanged.EventID = "match-current:decision:000002"
	patchChanged.Sequence = 2
	repository.decisions[patchChanged.EventID] = patchChanged
	service.SetFeedbackEligibilityProvider(fixedEligibilityProvider{eligibility: eligibilityForDecision(patchChanged), ready: true})
	nextCatalog := testCatalog()
	nextCatalog.Patch = "16.19.1"
	catalogProvider.knowledge = nextCatalog
	if _, err := service.SubmitFeedback(context.Background(), FeedbackSubmission{
		ClientFeedbackID: "context-old-catalog", DecisionEventID: patchChanged.EventID, ItemID: 3001, Value: FeedbackUseful,
	}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("old catalog evidence remained eligible: %v", err)
	}
}

type memoryRepository struct {
	preferences Preferences
	feedback    map[string]FeedbackRecord
	decisions   map[string]FeedbackDecision
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{feedback: make(map[string]FeedbackRecord), decisions: make(map[string]FeedbackDecision)}
}

func (repository *memoryRepository) LoadPersonalizationPreferences(context.Context) (Preferences, error) {
	return clonePreferences(repository.preferences), nil
}
func (repository *memoryRepository) SavePersonalizationPreferences(_ context.Context, value Preferences) error {
	repository.preferences = clonePreferences(value)
	return nil
}
func (repository *memoryRepository) LoadRecommendationFeedback(context.Context) ([]FeedbackRecord, error) {
	result := make([]FeedbackRecord, 0, len(repository.feedback))
	for _, record := range repository.feedback {
		result = append(result, record)
	}
	return result, nil
}
func (repository *memoryRepository) SaveRecommendationFeedback(_ context.Context, record FeedbackRecord) (bool, error) {
	if current, exists := repository.feedback[record.ClientFeedbackID]; exists {
		if !feedbackRecordsEqual(current, record) {
			return false, ErrFeedbackConflict
		}
		return false, nil
	}
	for _, current := range repository.feedback {
		if current.DecisionEventID == record.DecisionEventID && current.ItemID == record.ItemID {
			if current.Value != record.Value {
				return false, ErrFeedbackConflict
			}
			return false, nil
		}
	}
	repository.feedback[record.ClientFeedbackID] = record
	return true, nil
}
func (repository *memoryRepository) LoadFeedbackDecision(_ context.Context, id string) (FeedbackDecision, error) {
	value, exists := repository.decisions[id]
	if !exists {
		return FeedbackDecision{}, ErrDecisionNotFound
	}
	return value, nil
}
func (repository *memoryRepository) DeleteRecommendationFeedback(context.Context) (int64, error) {
	count := int64(len(repository.feedback))
	repository.feedback = make(map[string]FeedbackRecord)
	return count, nil
}

type fixedCatalogProvider catalog.Catalog

func (provider fixedCatalogProvider) Current() (catalog.Catalog, catalog.Status, bool) {
	value := catalog.Catalog(provider)
	return value, catalog.Status{Ready: true, Patch: value.Patch}, true
}

type mutableCatalogProvider struct {
	knowledge catalog.Catalog
	ready     bool
}

type fixedEligibilityProvider struct {
	eligibility FeedbackEligibility
	ready       bool
}

func (provider fixedEligibilityProvider) CurrentFeedbackEligibility() (FeedbackEligibility, bool) {
	ready := provider.ready || provider.eligibility.EventID != ""
	return provider.eligibility, ready
}

func eligibilityForDecision(decision FeedbackDecision) FeedbackEligibility {
	visible := make([]int, 0, len(decision.Evidence.RankedChoices))
	for _, choice := range decision.Evidence.RankedChoices {
		visible = append(visible, choice.Item.ID)
	}
	return FeedbackEligibility{
		EventID: decision.EventID, SessionID: decision.SessionID, ChampionID: decision.ChampionID,
		Role: decision.Role, Evidence: decision.Evidence, VisibleItemIDs: visible,
	}
}

func (provider *mutableCatalogProvider) Current() (catalog.Catalog, catalog.Status, bool) {
	return provider.knowledge, catalog.Status{Ready: provider.ready, Patch: provider.knowledge.Patch}, provider.ready
}

func testCatalog() catalog.Catalog {
	return catalog.Catalog{
		Patch: "16.18.1", Locale: "es_MX",
		Champions: map[string]catalog.Champion{"Garen": {ID: "Garen", Name: "Garen"}},
		Items: map[int]catalog.Item{
			3001: {ID: 3001, Name: "Uno", Purchasable: true, AvailableMap11: true, Gold: catalog.Gold{Purchasable: true}},
			3002: {ID: 3002, Name: "Dos", Purchasable: true, AvailableMap11: true, Gold: catalog.Gold{Purchasable: true}},
			3003: {ID: 3003, Name: "Tres", Purchasable: true, AvailableMap11: true, Gold: catalog.Gold{Purchasable: true}},
			3004: {ID: 3004, Name: "Cuatro", Purchasable: false, AvailableMap11: true, Gold: catalog.Gold{Purchasable: false}},
			3005: {ID: 3005, Name: "Cinco", Purchasable: true, AvailableMap11: false, Gold: catalog.Gold{Purchasable: true}},
		},
	}
}

func fixedNow() time.Time { return time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC) }

func containsError(err error, value string) bool {
	return err != nil && strings.Contains(err.Error(), value)
}
