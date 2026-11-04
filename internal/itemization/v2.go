package itemization

import (
	"strings"
	"time"

	"lolengine/internal/analysis"
	"lolengine/internal/catalog"
	"lolengine/internal/domain"
	"lolengine/internal/itemrules"
	"lolengine/internal/personalization"
)

const minimumRankedAlternatives = 2

func (engine Engine) recommendV2(now time.Time, snapshot domain.Snapshot, knowledge catalog.Catalog) Result {
	legacy := (Engine{}).recommendLegacy(now, snapshot, knowledge)
	if err := knowledge.Validate(); err != nil {
		legacy.Rules = itemrules.Status{Source: "unavailable", LastError: err.Error()}
		return legacy
	}
	resolution := engine.Rules.Resolve(knowledge.Patch, knowledge)
	legacy.Rules = resolution.Status
	if resolution.Legacy {
		legacy.Warnings = append(legacy.Warnings, "No hay reglas Garen v2 válidas para este catálogo; se usan reglas heredadas de forma explícita.")
		return legacy
	}
	switch legacy.Status {
	case StatusWaiting, StatusStale, StatusUnsupported, StatusCatalogUnavailable:
		return legacy
	}
	if snapshot.Game == nil {
		return legacy
	}
	strategy := engine.Strategy
	if !strategy.Valid() {
		strategy = domain.StrategyBalanced
	}
	context := analysis.BuildMatchContext(*snapshot.Game, knowledge, resolution.Rules, strategy)
	result := Result{
		GeneratedAt: now.UTC(), SourceGameRevision: snapshot.Revision, Patch: knowledge.Patch,
		PlayerState: context.PlayerState, DamageMix: context.DamageMix, MainThreat: context.MainThreat,
		LaneOpponent: context.LaneOpponent, Phase: context.Phase, Strategy: context.Strategy,
		Confidence: context.Confidence, TraitConfidence: context.TraitConfidence, Needs: context.Needs,
		Rules: resolution.Status, Stability: resolution.Rules.Stability,
		AlternativeLimit: resolution.Rules.AlternativeCount,
		Warnings:         append([]string(nil), context.Warnings...),
		Sources:          []string{"Live Client Data", "Heurística determinista local", "Data Dragon " + knowledge.Patch, "Reglas " + resolution.Rules.RulesetVersion},
	}
	result.BaseWarnings = append([]string(nil), result.Warnings...)
	personalizationSnapshot := personalization.Snapshot{}
	if engine.Personalization != nil {
		role, _ := domain.ParseRole(snapshot.Game.ActivePlayer.Position)
		championID := "Garen"
		if champion, ok := catalog.ResolveChampion(knowledge, snapshot.Game.ActivePlayer.ChampionName); ok {
			championID = champion.ID
		}
		personalizationSnapshot = engine.Personalization.Snapshot(personalization.ContextKey{ChampionID: championID, Role: role, Phase: context.Phase, Strategy: context.Strategy, Patch: knowledge.Patch, RulesVersion: resolution.Rules.RulesetVersion})
	}
	return rankAndPlan(result, liveRankingInput(*snapshot.Game, context), knowledge, context, resolution.Rules.Candidates, rankingCopy{
		completeReason:      "Ya completaste o no puedes comprar las opciones válidas de las reglas Garen v2 para esta situación.",
		insufficientReason:  "Las reglas Garen v2 no produjeron suficientes alternativas válidas para recomendar una compra.",
		insufficientWarning: "Se requieren al menos dos alternativas distintas además del objetivo principal.",
		reason:              rankedReason,
	}, personalizationSnapshot)
}

func buildDecisionPlan(ranked RankedItem, input RankingInput, owned map[int]int, knowledge catalog.Catalog, reason string) (DecisionPlan, error) {
	options, err := actionableComponents(ranked.Item.ID, cloneOwned(owned), knowledge)
	if err != nil {
		return DecisionPlan{}, err
	}
	plan := DecisionPlan{Ranked: ranked, Status: StatusComplete, Reason: reason}
	if len(options) == 0 {
		plan.Reason = "El objeto objetivo ya está completo."
		return plan, nil
	}
	gold := max(input.Gold, 0)
	plan.InventorySlotsUsed = inventorySlotsUsed(domainInventory(input.Inventory), knowledge)
	// Jak'Sho is a mixed-defense target: build the resistance matching the
	// larger estimated damage pressure before its neutral health component.
	if ranked.Item.ID == itemJakSho {
		physical := input.Needs[itemrules.SignalPhysical]
		magic := input.Needs[itemrules.SignalMagic]
		defense := make([]purchaseOption, 0, len(options))
		for _, option := range options {
			if physical >= magic+50 && option.item.Stats["FlatArmorMod"] > 0 && option.item.Stats["FlatSpellBlockMod"] == 0 ||
				magic >= physical+50 && option.item.Stats["FlatSpellBlockMod"] > 0 && option.item.Stats["FlatArmorMod"] == 0 {
				defense = append(defense, option)
			}
		}
		// A full inventory can still buy a combining component from owned
		// parts; never hide that path behind an unbuyable leaf.
		if len(defense) > 0 &&
			(plan.InventorySlotsUsed < 6 || hasCombiningOption(defense)) &&
			(hasAffordableActionable(defense, gold, plan.InventorySlotsUsed) || !hasAffordableActionable(options, gold, plan.InventorySlotsUsed)) {
			options = defense
		}
	}
	selected, affordable, slotBlocked := selectComponent(options, gold, plan.InventorySlotsUsed)
	plan.NextComponent = itemReference(selected.item, selected.cost)
	if slotBlocked {
		plan.Status = StatusInventoryFull
		plan.GoldNeeded = max(selected.cost-gold, 0)
		plan.Reason = "Tu inventario está lleno; libera un espacio antes de comprar " + selected.item.Name + "."
		plan.Warnings = []string{"La compra no se combina inmediatamente con objetos existentes y requiere un espacio libre."}
		return plan, nil
	}
	if affordable {
		plan.Status = StatusReady
		return plan, nil
	}
	plan.Status = StatusSaveForComponent
	plan.GoldNeeded = max(selected.cost-gold, 0)
	return plan, nil
}

func hasCombiningOption(options []purchaseOption) bool {
	for _, option := range options {
		if option.consumedSlots > 0 {
			return true
		}
	}
	return false
}

func hasAffordableActionable(options []purchaseOption, gold, slotsUsed int) bool {
	for _, option := range options {
		if option.cost <= gold && (slotsUsed < 6 || option.consumedSlots > 0) {
			return true
		}
	}
	return false
}

// SelectTarget applies a precomputed target plan without recalculating its recipe.
// The stabilizer uses it to keep an incumbent while gold and component state continue updating.
func SelectTarget(result *Result, targetID int) bool {
	if result == nil {
		return false
	}
	plan, exists := result.DecisionPlans[targetID]
	if !exists {
		return false
	}
	result.Status = plan.Status
	target := plan.Ranked.Item
	result.TargetItem = &target
	result.NextComponent = cloneItemRef(plan.NextComponent)
	result.GoldNeeded = plan.GoldNeeded
	result.InventorySlotsUsed = plan.InventorySlotsUsed
	result.Reason = plan.Reason
	result.ReasonCodes = append([]string(nil), plan.Ranked.ReasonCodes...)
	result.CoveredNeeds = append([]itemrules.Signal(nil), plan.Ranked.CoveredNeeds...)
	result.Warnings = append(append([]string(nil), result.BaseWarnings...), plan.Warnings...)

	ordered := []RankedItem{cloneRankedItem(plan.Ranked)}
	for _, id := range result.SafetyOrder {
		if id == targetID {
			continue
		}
		if safety, ok := result.DecisionPlans[id]; ok {
			ordered = append(ordered, cloneRankedItem(safety.Ranked))
		}
	}
	for _, id := range result.DecisionOrder {
		if id == targetID {
			continue
		}
		alreadyIncluded := false
		for _, option := range ordered {
			if option.Item.ID == id {
				alreadyIncluded = true
				break
			}
		}
		if alreadyIncluded {
			continue
		}
		if alternative, ok := result.DecisionPlans[id]; ok {
			ordered = append(ordered, cloneRankedItem(alternative.Ranked))
		}
	}
	limit := min(max(result.AlternativeLimit, 0), 3)
	if len(ordered) > limit+1 {
		ordered = ordered[:limit+1]
	}
	result.RankedOptions = ordered
	result.Alternatives = nil
	result.Alternative = nil
	if len(ordered) > 1 {
		result.Alternatives = append([]RankedItem(nil), ordered[1:]...)
		alternative := ordered[1].Item
		result.Alternative = &alternative
	}
	return true
}

func cloneRankedItem(source RankedItem) RankedItem {
	clone := source
	clone.CoveredNeeds = append([]itemrules.Signal(nil), source.CoveredNeeds...)
	clone.ReasonCodes = append([]string(nil), source.ReasonCodes...)
	return clone
}

func cloneItemRef(source *ItemRef) *ItemRef {
	if source == nil {
		return nil
	}
	clone := *source
	return &clone
}

func phaseEligible(current, minimum, maximum domain.Phase) bool {
	currentOrder := phaseNumber(current)
	if minimum != "" && currentOrder < phaseNumber(minimum) {
		return false
	}
	return maximum == "" || currentOrder <= phaseNumber(maximum)
}

func phaseNumber(phase domain.Phase) int {
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

func ownsUpgradedBoots(items []domain.Item, candidates []itemrules.CandidateRule, knowledge catalog.Catalog) bool {
	targets := make(map[int]struct{})
	components := make(map[int]struct{})
	for _, candidate := range candidates {
		if candidate.MutualExclusionGroup != "boots" {
			continue
		}
		targets[candidate.ItemID] = struct{}{}
		collectRecipeComponents(candidate.ItemID, knowledge, components, make(map[int]bool))
	}
	for _, owned := range items {
		if _, isTarget := targets[owned.ID]; isTarget {
			return true
		}
		item, exists := knowledge.Items[owned.ID]
		if !exists || !hasTag(item.Tags, "Boots") {
			continue
		}
		if _, isComponent := components[owned.ID]; isComponent || reachesAnyTarget(owned.ID, targets, knowledge, make(map[int]bool)) {
			continue
		}
		return true
	}
	return false
}

func collectRecipeComponents(itemID int, knowledge catalog.Catalog, components map[int]struct{}, visiting map[int]bool) {
	if visiting[itemID] {
		return
	}
	visiting[itemID] = true
	defer delete(visiting, itemID)
	item, exists := knowledge.Items[itemID]
	if !exists {
		return
	}
	for _, componentID := range item.From {
		components[componentID] = struct{}{}
		collectRecipeComponents(componentID, knowledge, components, visiting)
	}
}

func reachesAnyTarget(itemID int, targets map[int]struct{}, knowledge catalog.Catalog, visiting map[int]bool) bool {
	if visiting[itemID] {
		return false
	}
	visiting[itemID] = true
	defer delete(visiting, itemID)
	item, exists := knowledge.Items[itemID]
	if !exists {
		return false
	}
	for _, nextID := range item.Into {
		if _, target := targets[nextID]; target || reachesAnyTarget(nextID, targets, knowledge, visiting) {
			return true
		}
	}
	return false
}

func rankedReason(ranked RankedItem, context analysis.MatchContext) string {
	needs := make([]string, 0, len(ranked.CoveredNeeds))
	for _, need := range ranked.CoveredNeeds {
		if context.Needs[need] > 0 {
			needs = append(needs, spanishNeed(need))
		}
	}
	if len(needs) == 0 {
		return "Las reglas Garen v2 priorizan esta opción para la fase " + string(context.Phase) + " y la estrategia " + string(context.Strategy) + "."
	}
	return "Las reglas Garen v2 priorizan " + strings.Join(needs, ", ") + " en la fase " + string(context.Phase) + "."
}

func spanishNeed(signal itemrules.Signal) string {
	switch signal {
	case itemrules.SignalPhysical:
		return "resistencia física"
	case itemrules.SignalMagic:
		return "resistencia mágica"
	case itemrules.SignalHealing:
		return "anti-curación"
	case itemrules.SignalShielding:
		return "anti-escudos"
	case itemrules.SignalCrowdControl:
		return "tenacidad contra control"
	case itemrules.SignalSlows:
		return "respuesta a ralentizaciones"
	case itemrules.SignalArmor:
		return "penetración contra armadura"
	case itemrules.SignalMagicResist:
		return "penetración contra resistencia mágica"
	case itemrules.SignalBurst:
		return "supervivencia ante ráfaga"
	case itemrules.SignalSustained:
		return "supervivencia sostenida"
	case itemrules.SignalLanePressure:
		return "el enfrentamiento de línea"
	default:
		return string(signal)
	}
}

func appendUniqueString(values []string, value string) []string {
	for _, current := range values {
		if current == value {
			return values
		}
	}
	return append(values, value)
}
