package itemrules

import (
	"fmt"
	"strings"

	"lolengine/internal/catalog"
	"lolengine/internal/domain"
)

const maximumRuleScore = 100000
const maximumPhaseBoundarySeconds = 24 * 60 * 60
const minimumMissingTraitPenalty = 100

const (
	maximumCandidates     = 256
	maximumChampionTraits = 512
	maximumItemTraits     = 2048
	maximumAdjustments    = 64
)

// Validate checks both the JSON contract and all catalog-dependent item references.
func (rules RuleSet) Validate(knowledge catalog.Catalog) error {
	if err := rules.validateStructure(); err != nil {
		return err
	}
	if err := knowledge.Validate(); err != nil {
		return fmt.Errorf("invalid catalog: %w", err)
	}
	canonicalTraits := make(map[string]string, len(rules.ChampionTraits))
	for reference := range rules.ChampionTraits {
		champion, exists := catalog.ResolveChampion(knowledge, reference)
		if !exists || strings.TrimSpace(champion.ID) == "" {
			return fmt.Errorf("unknown champion trait reference %q for catalog %s", reference, knowledge.Patch)
		}
		canonical := strings.ToLower(strings.TrimSpace(champion.ID))
		if previous, duplicate := canonicalTraits[canonical]; duplicate {
			return fmt.Errorf("champion trait references %q and %q resolve to the same champion %q", previous, reference, champion.ID)
		}
		canonicalTraits[canonical] = reference
	}
	for itemID := range rules.ItemTraits {
		item, exists := knowledge.Items[itemID]
		if !exists {
			return fmt.Errorf("item trait %d is missing from catalog %s", itemID, knowledge.Patch)
		}
		if !item.Purchasable || !item.Gold.Purchasable {
			return fmt.Errorf("item trait %d is not purchasable", itemID)
		}
		if !item.AvailableMap11 {
			return fmt.Errorf("item trait %d is not available on map 11", itemID)
		}
	}
	for _, candidate := range rules.Candidates {
		item, exists := knowledge.Items[candidate.ItemID]
		if !exists {
			return fmt.Errorf("candidate %q item %d is missing from catalog %s", candidate.ID, candidate.ItemID, knowledge.Patch)
		}
		if !item.Purchasable || !item.Gold.Purchasable {
			return fmt.Errorf("candidate %q item %d is not purchasable", candidate.ID, candidate.ItemID)
		}
		if !item.AvailableMap11 {
			return fmt.Errorf("candidate %q item %d is not available on map 11", candidate.ID, candidate.ItemID)
		}
		if strings.TrimSpace(item.Name) == "" || item.Gold.Total <= 0 {
			return fmt.Errorf("candidate %q item %d has invalid catalog fields", candidate.ID, candidate.ItemID)
		}
	}
	return nil
}

func (rules RuleSet) validateStructure() error {
	if rules.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("unsupported rules schema version %d", rules.SchemaVersion)
	}
	if strings.TrimSpace(rules.RulesetVersion) == "" {
		return fmt.Errorf("ruleset_version is required")
	}
	if !strings.EqualFold(strings.TrimSpace(rules.Champion), "Garen") {
		return fmt.Errorf("champion must be Garen")
	}
	if _, ok := parsePatch(rules.Patch); !ok {
		return fmt.Errorf("invalid rules patch %q", rules.Patch)
	}
	if rules.AlternativeCount < 2 || rules.AlternativeCount > 3 {
		return fmt.Errorf("alternative_count must be between 2 and 3")
	}
	if rules.Phases.EarlyEndSeconds <= 0 || rules.Phases.MidEndSeconds <= rules.Phases.EarlyEndSeconds ||
		rules.Phases.MidEndSeconds > maximumPhaseBoundarySeconds {
		return fmt.Errorf("phase bounds must be positive and ordered")
	}
	if err := validateContext(rules.Context); err != nil {
		return err
	}
	if err := validateStability(rules.Stability); err != nil {
		return err
	}
	if len(rules.ChampionTraits) > maximumChampionTraits {
		return fmt.Errorf("champion_traits exceeds %d entries", maximumChampionTraits)
	}
	championKeys := make(map[string]string, len(rules.ChampionTraits))
	for champion, traits := range rules.ChampionTraits {
		if strings.TrimSpace(champion) == "" {
			return fmt.Errorf("champion trait key must not be empty")
		}
		normalized := strings.ToLower(strings.TrimSpace(champion))
		if previous, exists := championKeys[normalized]; exists {
			return fmt.Errorf("duplicate champion trait keys %q and %q", previous, champion)
		}
		championKeys[normalized] = champion
		if err := validateTraits("champion "+champion, traits); err != nil {
			return err
		}
	}
	if len(rules.ItemTraits) > maximumItemTraits {
		return fmt.Errorf("item_traits exceeds %d entries", maximumItemTraits)
	}
	for itemID, traits := range rules.ItemTraits {
		if itemID <= 0 {
			return fmt.Errorf("item trait ID must be positive")
		}
		if err := validateTraits(fmt.Sprintf("item %d", itemID), traits); err != nil {
			return err
		}
	}
	if len(rules.Candidates) < rules.AlternativeCount+1 {
		return fmt.Errorf("rules require at least %d candidates", rules.AlternativeCount+1)
	}
	if len(rules.Candidates) > maximumCandidates {
		return fmt.Errorf("candidates exceeds %d entries", maximumCandidates)
	}
	ids := make(map[string]struct{}, len(rules.Candidates))
	itemIDs := make(map[int]struct{}, len(rules.Candidates))
	for index, candidate := range rules.Candidates {
		if err := validateCandidate(index, candidate); err != nil {
			return err
		}
		if _, exists := ids[candidate.ID]; exists {
			return fmt.Errorf("duplicate candidate ID %q", candidate.ID)
		}
		ids[candidate.ID] = struct{}{}
		if _, exists := itemIDs[candidate.ItemID]; exists {
			return fmt.Errorf("duplicate candidate item ID %d", candidate.ItemID)
		}
		itemIDs[candidate.ItemID] = struct{}{}
	}
	return nil
}

func validateContext(context ContextTuning) error {
	for _, phase := range []domain.Phase{domain.PhaseEarly, domain.PhaseMid, domain.PhaseLate} {
		value, exists := context.LaneWeightByPhase[phase]
		if !exists || value < 0 || value > 1000 {
			return fmt.Errorf("lane weight for phase %q must be between 0 and 1000", phase)
		}
	}
	for phase := range context.LaneWeightByPhase {
		if !validPhase(phase) {
			return fmt.Errorf("invalid phase %q in lane weights", phase)
		}
	}
	if context.MissingTraitPenalty < minimumMissingTraitPenalty || context.MissingTraitPenalty > 1000 {
		return fmt.Errorf("missing_trait_penalty must be between %d and 1000", minimumMissingTraitPenalty)
	}
	if context.ArmorFullScale <= 0 || context.ArmorFullScale > 1000 {
		return fmt.Errorf("armor_full_scale must be between 1 and 1000")
	}
	if context.MagicResistFullScale < 0 || context.MagicResistFullScale > 1000 {
		return fmt.Errorf("magic_resist_full_scale must be zero or between 1 and 1000")
	}
	return nil
}

// ValidateContextTuning exposes the shared structural contract to other
// patch-versioned profile formats without weakening the Garen v2 schema.
func ValidateContextTuning(context ContextTuning) error {
	return validateContext(context)
}

func validateStability(policy StabilityPolicy) error {
	if policy.SwitchMargin < 0 || policy.SwitchMargin > maximumRuleScore {
		return fmt.Errorf("switch_margin is out of bounds")
	}
	if policy.ConfirmationSeconds < 0 || policy.ConfirmationSeconds > 600 {
		return fmt.Errorf("confirmation_seconds is out of bounds")
	}
	if policy.MinimumHoldSeconds < 0 || policy.MinimumHoldSeconds > 600 {
		return fmt.Errorf("minimum_hold_seconds is out of bounds")
	}
	if policy.CriticalThreshold < 0 || policy.CriticalThreshold > 1000 {
		return fmt.Errorf("critical_threshold must be between 0 and 1000")
	}
	return nil
}

// ValidateStabilityPolicy exposes the shared bounded hysteresis contract.
func ValidateStabilityPolicy(policy StabilityPolicy) error {
	return validateStability(policy)
}

func validateTraits(subject string, traits TraitProfile) error {
	values := []int{traits.Healing, traits.Shielding, traits.CrowdControl, traits.Slows, traits.Burst, traits.Sustained}
	for _, value := range values {
		if value < 0 || value > 1000 {
			return fmt.Errorf("%s trait value must be between 0 and 1000", subject)
		}
	}
	return nil
}

// ValidateTraitProfile checks one externally supplied structured trait set.
func ValidateTraitProfile(subject string, traits TraitProfile) error {
	return validateTraits(subject, traits)
}

func validateCandidate(index int, candidate CandidateRule) error {
	if strings.TrimSpace(candidate.ID) == "" {
		return fmt.Errorf("candidate %d ID is required", index)
	}
	if candidate.ItemID <= 0 {
		return fmt.Errorf("candidate %q item_id must be positive", candidate.ID)
	}
	switch candidate.Category {
	case CategoryBoots, CategoryCore, CategorySituational:
	default:
		return fmt.Errorf("candidate %q has invalid category %q", candidate.ID, candidate.Category)
	}
	if candidate.MutualExclusionGroup != "" && candidate.MutualExclusionGroup != CategoryBoots {
		return fmt.Errorf("candidate %q has invalid mutual exclusion group %q", candidate.ID, candidate.MutualExclusionGroup)
	}
	if candidate.Category == CategoryBoots && candidate.MutualExclusionGroup != CategoryBoots {
		return fmt.Errorf("candidate %q boots candidates must use the boots mutual exclusion group", candidate.ID)
	}
	if candidate.Category != CategoryBoots && candidate.MutualExclusionGroup == CategoryBoots {
		return fmt.Errorf("candidate %q only boots candidates may use the boots mutual exclusion group", candidate.ID)
	}
	if candidate.BaseScore < -maximumRuleScore || candidate.BaseScore > maximumRuleScore {
		return fmt.Errorf("candidate %q base_score is out of bounds", candidate.ID)
	}
	if candidate.MinPhase != "" && !validPhase(candidate.MinPhase) {
		return fmt.Errorf("candidate %q has invalid min_phase %q", candidate.ID, candidate.MinPhase)
	}
	if candidate.MaxPhase != "" && !validPhase(candidate.MaxPhase) {
		return fmt.Errorf("candidate %q has invalid max_phase %q", candidate.ID, candidate.MaxPhase)
	}
	if candidate.MinPhase != "" && candidate.MaxPhase != "" && phaseOrder(candidate.MinPhase) > phaseOrder(candidate.MaxPhase) {
		return fmt.Errorf("candidate %q phase bounds are reversed", candidate.ID)
	}
	seenSignals := make(map[Signal]struct{}, len(candidate.Covers))
	for _, signal := range candidate.Covers {
		if !validSignal(signal) {
			return fmt.Errorf("candidate %q has invalid signal %q", candidate.ID, signal)
		}
		if _, exists := seenSignals[signal]; exists {
			return fmt.Errorf("candidate %q repeats covered signal %q", candidate.ID, signal)
		}
		seenSignals[signal] = struct{}{}
	}
	if len(candidate.Adjustments) > maximumAdjustments {
		return fmt.Errorf("candidate %q exceeds %d adjustments", candidate.ID, maximumAdjustments)
	}
	for phase, score := range candidate.PhaseScores {
		if !validPhase(phase) {
			return fmt.Errorf("candidate %q has invalid phase %q", candidate.ID, phase)
		}
		if score < -maximumRuleScore || score > maximumRuleScore {
			return fmt.Errorf("candidate %q phase score is out of bounds", candidate.ID)
		}
	}
	for strategy, score := range candidate.StrategyScores {
		if !strategy.Valid() {
			return fmt.Errorf("candidate %q has invalid strategy %q", candidate.ID, strategy)
		}
		if score < -maximumRuleScore || score > maximumRuleScore {
			return fmt.Errorf("candidate %q strategy score is out of bounds", candidate.ID)
		}
	}
	for _, adjustment := range candidate.Adjustments {
		if !validSignal(adjustment.Signal) {
			return fmt.Errorf("candidate %q has invalid signal %q", candidate.ID, adjustment.Signal)
		}
		if adjustment.Minimum < 0 || adjustment.Minimum > 1000 {
			return fmt.Errorf("candidate %q adjustment minimum must be between 0 and 1000", candidate.ID)
		}
		if adjustment.Add < -maximumRuleScore || adjustment.Add > maximumRuleScore {
			return fmt.Errorf("candidate %q adjustment score is out of bounds", candidate.ID)
		}
		if strings.TrimSpace(adjustment.ReasonCode) == "" {
			return fmt.Errorf("candidate %q adjustment reason_code is required", candidate.ID)
		}
	}
	return nil
}

// ValidateCandidateRule checks the reusable candidate scoring contract. The
// catalog-dependent item checks remain the caller's responsibility.
func ValidateCandidateRule(index int, candidate CandidateRule) error {
	return validateCandidate(index, candidate)
}

// ValidSignal reports whether a signal is part of the stable scoring contract.
func ValidSignal(signal Signal) bool {
	return validSignal(signal)
}

func validSignal(signal Signal) bool {
	switch signal {
	case SignalPhysical, SignalMagic, SignalHealing, SignalShielding, SignalCrowdControl,
		SignalSlows, SignalArmor, SignalMagicResist, SignalBurst, SignalSustained, SignalLanePressure,
		SignalAhead, SignalBehind:
		return true
	default:
		return false
	}
}

func validPhase(phase domain.Phase) bool {
	return phase == domain.PhaseEarly || phase == domain.PhaseMid || phase == domain.PhaseLate
}

func phaseOrder(phase domain.Phase) int {
	switch phase {
	case domain.PhaseEarly:
		return 0
	case domain.PhaseMid:
		return 1
	case domain.PhaseLate:
		return 2
	default:
		return -1
	}
}
