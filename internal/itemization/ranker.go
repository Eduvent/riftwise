package itemization

import (
	"fmt"
	"math"
	"sort"

	"lolengine/internal/analysis"
	"lolengine/internal/catalog"
	"lolengine/internal/domain"
	"lolengine/internal/itemrules"
	"lolengine/internal/personalization"
)

// RankingInventoryItem is the inventory shape consumed by the deterministic
// scorer. It keeps manual planning independent from live GameState/Snapshot
// construction while allowing the live route to adapt its real inventory.
type RankingInventoryItem struct {
	ID    int
	Count int
	Slot  int
}

// RankingInput contains only the state that candidate scoring and recipe
// planning actually need. It is shared by live and manual callers.
type RankingInput struct {
	Phase     domain.Phase
	Strategy  domain.Strategy
	Needs     analysis.NeedScores
	Gold      int
	Inventory []RankingInventoryItem
}

type PlanningRoute string

const (
	PlanningRouteGarenV2 PlanningRoute = "garen_v2"
	PlanningRouteGeneric PlanningRoute = "generic"
)

// PlanningRankingRequest is the pure manual-planning entry point to the same
// deterministic scorer and recipe solver used by live recommendations.
type PlanningRankingRequest struct {
	Base            Result
	Input           RankingInput
	Candidates      []itemrules.CandidateRule
	Route           PlanningRoute
	Neutral         bool
	Personalization personalization.Snapshot
}

// rankingCopy keeps route-specific product language outside the shared,
// deterministic candidate scorer and recipe planner.
type rankingCopy struct {
	completeReason       string
	insufficientStatus   Status
	insufficientReason   string
	insufficientWarning  string
	inventoryFullReason  func(ItemRef) string
	inventoryFullWarning string
	reason               func(RankedItem, analysis.MatchContext) string
}

func rankAndPlan(
	result Result,
	input RankingInput,
	knowledge catalog.Catalog,
	context analysis.MatchContext,
	candidateRules []itemrules.CandidateRule,
	copy rankingCopy,
	personalizationSnapshot personalization.Snapshot,
) Result {
	items := domainInventory(input.Inventory)
	owned := ownedMultiset(items)
	hasUpgradedBoots := ownsUpgradedBoots(items, candidateRules, knowledge)
	type scoredCandidate struct {
		rule            itemrules.CandidateRule
		item            catalog.Item
		score           int
		baselineScore   int
		order           int
		reasons         []string
		activeSignals   []itemrules.Signal
		criticalSignals []itemrules.Signal
		critical        bool
	}
	candidates := make([]scoredCandidate, 0, len(candidateRules))
	personalizationActive := personalizationSnapshot.Preferences.Style.Valid() && !personalizationSnapshot.Preferences.Default()
	preferred := intSet(personalizationSnapshot.Preferences.PreferredItemIDs)
	avoided := intSet(personalizationSnapshot.Preferences.AvoidedItemIDs)
	appliedReasons := make([]string, 0, 4)
	warningCodes := make([]string, 0, 2)
	excluded := 0
	for index, rule := range candidateRules {
		if owned[rule.ItemID] > 0 || !phaseEligible(context.Phase, rule.MinPhase, rule.MaxPhase) {
			continue
		}
		if rule.MutualExclusionGroup == "boots" && hasUpgradedBoots {
			continue
		}
		item, exists := knowledge.Items[rule.ItemID]
		if !exists || !validShopItem(item) {
			continue
		}
		score := rule.BaseScore + rule.PhaseScores[context.Phase] + rule.StrategyScores[context.Strategy]
		reasons := []string{rule.ID}
		critical := false
		activeSignals := make([]itemrules.Signal, 0, len(rule.Adjustments))
		criticalSignals := make([]itemrules.Signal, 0, len(rule.Adjustments))
		for _, adjustment := range rule.Adjustments {
			need := context.Needs[adjustment.Signal]
			if need < adjustment.Minimum {
				continue
			}
			score += adjustment.Add
			activeSignals = appendUniqueSignal(activeSignals, adjustment.Signal)
			reasons = appendUniqueString(reasons, adjustment.ReasonCode)
			if adjustment.Critical && need >= result.Stability.CriticalThreshold {
				critical = true
				criticalSignals = appendUniqueSignal(criticalSignals, adjustment.Signal)
			}
		}
		baselineScore := score
		if personalizationActive {
			if _, skip := avoided[rule.ItemID]; skip && !critical {
				excluded++
				appliedReasons = appendUniqueString(appliedReasons, personalization.ReasonAvoidedItem)
				continue
			} else if skip {
				warningCodes = appendUniqueString(warningCodes, personalization.ReasonAvoidanceCriticalOverride)
				reasons = appendUniqueString(reasons, personalization.ReasonAvoidanceCriticalOverride)
			}
			if delta, code := styleAdjustment(personalizationSnapshot.Preferences.Style, rule, item); delta != 0 {
				score += delta
				reasons = appendUniqueString(reasons, code)
				appliedReasons = appendUniqueString(appliedReasons, code)
			}
			if _, selected := preferred[rule.ItemID]; selected {
				score += personalization.PreferredItemBonus
				reasons = appendUniqueString(reasons, personalization.ReasonPreferredItem)
				appliedReasons = appendUniqueString(appliedReasons, personalization.ReasonPreferredItem)
			}
			if effect, exists := personalizationSnapshot.Feedback[rule.ItemID]; exists && effect.Adjustment != 0 {
				score += effect.Adjustment
				reasons = appendUniqueString(reasons, effect.ReasonCode)
				appliedReasons = appendUniqueString(appliedReasons, effect.ReasonCode)
			}
		}
		candidates = append(candidates, scoredCandidate{
			rule: rule, item: item, score: score, baselineScore: baselineScore, order: index,
			reasons: reasons, activeSignals: activeSignals, criticalSignals: criticalSignals, critical: critical,
		})
	}
	sort.SliceStable(candidates, func(left, right int) bool {
		if candidates[left].score != candidates[right].score {
			return candidates[left].score > candidates[right].score
		}
		if candidates[left].order != candidates[right].order {
			return candidates[left].order < candidates[right].order
		}
		return candidates[left].item.ID < candidates[right].item.ID
	})

	result.DecisionPlans = make(map[int]DecisionPlan, len(candidates))
	for _, candidate := range candidates {
		ranked := RankedItem{
			Item: *itemReference(candidate.item, candidate.item.Gold.Total), Score: candidate.score,
			Category: candidate.rule.Category, CoveredNeeds: append([]itemrules.Signal(nil), candidate.rule.Covers...),
			ReasonCodes: append([]string(nil), candidate.reasons...), Critical: candidate.critical,
		}
		reason := ""
		if copy.reason != nil {
			reason = copy.reason(ranked, context)
		}
		plan, err := buildDecisionPlan(ranked, input, owned, knowledge, reason)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("La opción %s se omitió porque su receta no pudo validarse: %v", candidate.item.Name, err))
			continue
		}
		if plan.Status == StatusInventoryFull {
			if copy.inventoryFullReason != nil && plan.NextComponent != nil {
				plan.Reason = copy.inventoryFullReason(*plan.NextComponent)
			}
			if copy.inventoryFullWarning != "" {
				plan.Warnings = []string{copy.inventoryFullWarning}
			}
		}
		result.DecisionPlans[candidate.item.ID] = plan
		result.DecisionOrder = append(result.DecisionOrder, candidate.item.ID)
	}
	visibleCapacity := min(max(result.AlternativeLimit, 0), 3) + 1
	mandatory := make([]int, 0, visibleCapacity)
	criticalSignalsReserved := make(map[itemrules.Signal]struct{})
	for _, candidate := range candidates {
		if _, valid := result.DecisionPlans[candidate.item.ID]; !valid {
			continue
		}
		for _, signal := range candidate.criticalSignals {
			if _, covered := criticalSignalsReserved[signal]; covered {
				continue
			}
			criticalSignalsReserved[signal] = struct{}{}
			mandatory = appendUniqueInt(mandatory, candidate.item.ID)
		}
	}
	baseline := append([]scoredCandidate(nil), candidates...)
	sort.SliceStable(baseline, func(left, right int) bool {
		if baseline[left].baselineScore != baseline[right].baselineScore {
			return baseline[left].baselineScore > baseline[right].baselineScore
		}
		if baseline[left].order != baseline[right].order {
			return baseline[left].order < baseline[right].order
		}
		return baseline[left].item.ID < baseline[right].item.ID
	})
	baselineVisible := 0
	for _, candidate := range baseline {
		if _, valid := result.DecisionPlans[candidate.item.ID]; !valid {
			continue
		}
		if baselineVisible >= visibleCapacity {
			break
		}
		baselineVisible++
		if len(candidate.activeSignals) > 0 {
			mandatory = appendUniqueInt(mandatory, candidate.item.ID)
		}
	}
	if len(mandatory) > visibleCapacity {
		mandatory = mandatory[:visibleCapacity]
	}
	result.SafetyOrder = append([]int(nil), mandatory...)
	result.DecisionOrder = reserveVisibleChoices(result.DecisionOrder, visibleCapacity, mandatory)
	result.BaseWarnings = append([]string(nil), result.Warnings...)
	if personalizationActive {
		result.PersonalizationVersion = personalizationSnapshot.Preferences.Version
		result.Personalization = &personalization.Applied{Enabled: true, Version: personalizationSnapshot.Preferences.Version, Style: personalizationSnapshot.Preferences.Style, ReasonCodes: appliedReasons, WarningCodes: warningCodes}
	}
	if len(result.DecisionOrder) < minimumRankedAlternatives+1 && personalizationActive && excluded > 0 {
		result.Status = StatusInsufficientContext
		result.Reason = "Las preferencias activas dejan menos de tres opciones distintas y válidas para comparar."
		result.Warnings = append(result.Warnings, "Cambiar los objetos evitados puede recuperar una opción principal y dos alternativas.")
		result.Personalization.WarningCodes = appendUniqueString(result.Personalization.WarningCodes, personalization.WarningTooFewChoices)
		return result
	}
	if len(result.DecisionOrder) == 0 {
		result.Status = StatusComplete
		result.Reason = copy.completeReason
		return result
	}
	if len(result.DecisionOrder) < minimumRankedAlternatives+1 {
		result.Status = copy.insufficientStatus
		if result.Status == "" {
			result.Status = StatusError
		}
		result.Reason = copy.insufficientReason
		result.Warnings = append(result.Warnings, copy.insufficientWarning)
		return result
	}
	SelectTarget(&result, result.DecisionOrder[0])
	return result
}

func reserveVisibleChoices(order []int, capacity int, mandatory []int) []int {
	if capacity <= 0 || len(order) <= capacity || len(mandatory) == 0 {
		return order
	}
	capacity = min(capacity, len(order))
	visible := append([]int(nil), order[:capacity]...)
	mandatorySet := intSet(mandatory)
	contains := func(values []int, id int) bool {
		for _, value := range values {
			if value == id {
				return true
			}
		}
		return false
	}
	for _, id := range mandatory {
		if contains(visible, id) || !contains(order, id) {
			continue
		}
		replace := -1
		for index := len(visible) - 1; index >= 0; index-- {
			if _, protected := mandatorySet[visible[index]]; !protected {
				replace = index
				break
			}
		}
		if replace < 0 {
			break
		}
		visible[replace] = id
	}
	result := append([]int(nil), visible...)
	for _, id := range order {
		if !contains(result, id) {
			result = append(result, id)
		}
	}
	return result
}

func appendUniqueSignal(values []itemrules.Signal, value itemrules.Signal) []itemrules.Signal {
	for _, current := range values {
		if current == value {
			return values
		}
	}
	return append(values, value)
}

func appendUniqueInt(values []int, value int) []int {
	for _, current := range values {
		if current == value {
			return values
		}
	}
	return append(values, value)
}

func liveRankingInput(game domain.GameState, context analysis.MatchContext) RankingInput {
	inventory := make([]RankingInventoryItem, len(game.ActivePlayer.Items))
	for index, item := range game.ActivePlayer.Items {
		inventory[index] = RankingInventoryItem{ID: item.ID, Count: item.Count, Slot: item.Slot}
	}
	return RankingInput{
		Phase: context.Phase, Strategy: context.Strategy, Needs: cloneNeeds(context.Needs),
		Gold: max(int(math.Floor(game.ActivePlayer.CurrentGold)), 0), Inventory: inventory,
	}
}

// RankPlanning applies manual input directly without constructing live domain
// state. Valid but non-actionable manual inputs return insufficient_context.
func RankPlanning(request PlanningRankingRequest, knowledge catalog.Catalog) Result {
	result := request.Base
	result.Phase = request.Input.Phase
	result.Strategy = request.Input.Strategy
	result.Needs = cloneNeeds(request.Input.Needs)
	context := analysis.MatchContext{Phase: request.Input.Phase, Strategy: request.Input.Strategy, Needs: cloneNeeds(request.Input.Needs)}
	copy := rankingCopy{
		insufficientStatus:  StatusInsufficientContext,
		insufficientReason:  "El escenario manual no produjo una opción principal y dos alternativas distintas.",
		insufficientWarning: "Se necesitan al menos tres opciones distintas y válidas para presentar una comparación manual.",
		inventoryFullReason: func(component ItemRef) string {
			return "El inventario manual está lleno; la opción " + component.Name + " necesita un espacio libre para estar disponible."
		},
		inventoryFullWarning: "La opción necesita un espacio libre según el inventario indicado en el escenario manual.",
	}
	switch request.Route {
	case PlanningRouteGarenV2:
		copy.completeReason = "Las opciones válidas de Garen v2 ya aparecen completas en el inventario manual."
		copy.reason = rankedReason
	case PlanningRouteGeneric:
		copy.completeReason = "Las opciones válidas del perfil estructural ya aparecen completas en el inventario manual."
		copy.reason = genericRankedReason
	default:
		result.Status = StatusError
		result.Reason = "La ruta de puntuación manual no es válida."
		return result
	}
	if request.Neutral {
		copy.completeReason = "El escenario manual no activa suficientes respuestas defensivas del perfil neutral."
	}
	if len(request.Candidates) < minimumRankedAlternatives+1 {
		result.Status = StatusInsufficientContext
		result.Reason = copy.insufficientReason
		result.Warnings = append(result.Warnings, copy.insufficientWarning)
		result.BaseWarnings = append([]string(nil), result.Warnings...)
		return result
	}
	return rankAndPlan(result, request.Input, knowledge, context, request.Candidates, copy, request.Personalization)
}

func intSet(values []int) map[int]struct{} {
	result := make(map[int]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func styleAdjustment(style personalization.Style, rule itemrules.CandidateRule, item catalog.Item) (int, string) {
	safeTraits := 0
	aggressiveTraits := 0
	for _, signal := range rule.Covers {
		switch signal {
		case itemrules.SignalPhysical, itemrules.SignalMagic, itemrules.SignalCrowdControl, itemrules.SignalSlows, itemrules.SignalBurst:
			safeTraits++
		case itemrules.SignalArmor, itemrules.SignalMagicResist:
			aggressiveTraits++
		}
	}
	for _, key := range []string{"FlatHPPoolMod", "FlatArmorMod", "FlatSpellBlockMod"} {
		if item.Stats[key] > 0 {
			safeTraits++
		}
	}
	for _, key := range []string{"FlatPhysicalDamageMod", "FlatMagicDamageMod", "PercentAttackSpeedMod", "FlatMovementSpeedMod", "PercentMovementSpeedMod"} {
		if item.Stats[key] > 0 {
			aggressiveTraits++
		}
	}
	switch style {
	case personalization.StyleSafe:
		if safeTraits > 0 {
			return min(12, safeTraits*6), personalization.ReasonStyleSafe
		}
		if aggressiveTraits > 0 {
			return -3, personalization.ReasonStyleSafe
		}
	case personalization.StyleAggressive:
		if aggressiveTraits > 0 {
			return min(12, aggressiveTraits*10), personalization.ReasonStyleAggressive
		}
		if rule.Category == itemrules.CategoryBoots {
			return 6, personalization.ReasonStyleAggressive
		}
		if safeTraits > 0 {
			return -3, personalization.ReasonStyleAggressive
		}
	}
	return 0, ""
}

func domainInventory(source []RankingInventoryItem) []domain.Item {
	result := make([]domain.Item, len(source))
	for index, item := range source {
		result[index] = domain.Item{ID: item.ID, Count: item.Count, Slot: item.Slot}
	}
	return result
}

func cloneNeeds(source analysis.NeedScores) analysis.NeedScores {
	result := make(analysis.NeedScores, len(source))
	for signal, score := range source {
		result[signal] = score
	}
	return result
}
