package itemization

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"lolengine/internal/analysis"
	"lolengine/internal/catalog"
	"lolengine/internal/domain"
	"lolengine/internal/itemprofiles"
	"lolengine/internal/itemrules"
	"lolengine/internal/personalization"
)

// Status describes whether the engine can offer an actionable purchase.
type Status string

const (
	StatusWaiting             Status = "waiting"
	StatusStale               Status = "stale"
	StatusUnsupported         Status = "unsupported"
	StatusCatalogUnavailable  Status = "catalog_unavailable"
	StatusReady               Status = "ready"
	StatusSaveForComponent    Status = "save_for_component"
	StatusInventoryFull       Status = "inventory_full"
	StatusComplete            Status = "complete"
	StatusInsufficientContext Status = "insufficient_context"
	StatusError               Status = "error"
)

const (
	itemStridebreaker = 6631
	itemRanduins      = 3143
	itemDeadMans      = 3742
	itemForceNature   = 4401
	itemSpiritVisage  = 3065
	itemSteraks       = 3053
	itemJakSho        = 6665
)

// ItemRef resolves a stable ID against the active patch catalog.
type ItemRef struct {
	ID    int           `json:"id"`
	Name  string        `json:"name"`
	Cost  int           `json:"cost"`
	Image catalog.Image `json:"image"`
}

// RankedItem is one deterministic v2 target with its score and explainability data.
type RankedItem struct {
	Item         ItemRef            `json:"item"`
	Score        int                `json:"score"`
	Category     string             `json:"category"`
	CoveredNeeds []itemrules.Signal `json:"covered_needs,omitempty"`
	ReasonCodes  []string           `json:"reason_codes,omitempty"`
	Critical     bool               `json:"critical,omitempty"`
}

// DecisionPlan retains the current recipe action for a ranked target.
// It is coordinator state and is intentionally omitted from JSON.
type DecisionPlan struct {
	Ranked             RankedItem
	Status             Status
	NextComponent      *ItemRef
	GoldNeeded         int
	InventorySlotsUsed int
	Reason             string
	Warnings           []string
}

// Result is the full recommendation contract consumed by the dashboard.
type Result struct {
	Status             Status                   `json:"status"`
	GeneratedAt        time.Time                `json:"generated_at"`
	SourceGameRevision uint64                   `json:"source_game_revision"`
	Patch              string                   `json:"patch,omitempty"`
	PlayerState        analysis.PlayerState     `json:"player_state,omitempty"`
	DamageMix          analysis.DamageMix       `json:"damage_mix"`
	MainThreat         analysis.Threat          `json:"main_threat"`
	NextComponent      *ItemRef                 `json:"next_component,omitempty"`
	TargetItem         *ItemRef                 `json:"target_item,omitempty"`
	Alternative        *ItemRef                 `json:"alternative,omitempty"`
	Alternatives       []RankedItem             `json:"alternatives,omitempty"`
	RankedOptions      []RankedItem             `json:"ranked_options,omitempty"`
	Phase              domain.Phase             `json:"phase,omitempty"`
	Strategy           domain.Strategy          `json:"strategy,omitempty"`
	LaneOpponent       analysis.Threat          `json:"lane_opponent"`
	TraitConfidence    float64                  `json:"trait_confidence,omitempty"`
	Needs              analysis.NeedScores      `json:"needs,omitempty"`
	ReasonCodes        []string                 `json:"reason_codes,omitempty"`
	CoveredNeeds       []itemrules.Signal       `json:"covered_needs,omitempty"`
	Rules              itemrules.Status         `json:"rules"`
	Profile            *itemprofiles.Provenance `json:"profile,omitempty"`
	ProfileStatus      *itemprofiles.Status     `json:"profile_status,omitempty"`
	Advisories         []itemprofiles.Warning   `json:"advisories,omitempty"`
	RunePlan           *itemprofiles.PlanCopy   `json:"rune_plan,omitempty"`
	SummonerPlan       *itemprofiles.PlanCopy   `json:"summoner_plan,omitempty"`
	InitialPlan        *itemprofiles.PlanCopy   `json:"initial_plan,omitempty"`
	Reason             string                   `json:"reason"`
	Confidence         float64                  `json:"confidence"`
	GoldNeeded         int                      `json:"gold_needed"`
	InventorySlotsUsed int                      `json:"inventory_slots_used,omitempty"`
	Warnings           []string                 `json:"warnings,omitempty"`
	Sources            []string                 `json:"sources,omitempty"`
	Personalization    *personalization.Applied `json:"personalization,omitempty"`

	DecisionPlans          map[int]DecisionPlan      `json:"-"`
	DecisionOrder          []int                     `json:"-"`
	SafetyOrder            []int                     `json:"-"`
	AlternativeLimit       int                       `json:"-"`
	Stability              itemrules.StabilityPolicy `json:"-"`
	BaseWarnings           []string                  `json:"-"`
	PersonalizationVersion uint64                    `json:"-"`
}

// Engine is a pure, deterministic itemization router. Garen remains pinned to
// its v2 provider; enabled non-Garen traffic uses the separate profile registry.
type Engine struct {
	Rules           itemrules.Provider
	Profiles        itemprofiles.Provider
	GenericEnabled  bool
	Strategy        domain.Strategy
	Personalization personalization.Provider
}

// Recommend returns one immutable recommendation for the supplied snapshot and catalog.
func (engine Engine) Recommend(now time.Time, snapshot domain.Snapshot, knowledge catalog.Catalog) Result {
	if engine.GenericEnabled && engine.Profiles != nil && snapshot.Game != nil &&
		!strings.EqualFold(snapshot.Game.ActivePlayer.ChampionName, "Garen") {
		return engine.recommendGeneric(now, snapshot, knowledge)
	}
	if engine.Rules != nil {
		return engine.recommendV2(now, snapshot, knowledge)
	}
	return engine.recommendLegacy(now, snapshot, knowledge)
}

// RulesRevision exposes an optional rule-provider generation to the coordinator.
func (engine Engine) RulesRevision() uint64 {
	var rulesRevision uint64
	if provider, ok := engine.Rules.(interface{ Revision() uint64 }); ok {
		rulesRevision = provider.Revision()
	}
	provider, profilesAvailable := engine.Profiles.(interface{ Revision() uint64 })
	var profilesRevision uint64
	if profilesAvailable {
		profilesRevision = provider.Revision()
	}
	var personalizationRevision uint64
	if engine.Personalization != nil {
		personalizationRevision = engine.Personalization.Revision()
	}
	return rulesRevision*1099511628211 ^ profilesRevision*1469598103934665603 ^ personalizationRevision
}

func (engine Engine) PersonalizationStatus() personalization.Status {
	if engine.Personalization == nil {
		return personalization.Status{Style: personalization.StyleBalanced, State: "unavailable"}
	}
	return engine.Personalization.Status()
}

func (Engine) recommendLegacy(now time.Time, snapshot domain.Snapshot, knowledge catalog.Catalog) Result {
	result := Result{
		GeneratedAt:        now.UTC(),
		SourceGameRevision: snapshot.Revision,
		Patch:              knowledge.Patch,
		Sources:            []string{"Live Client Data", "Heurística determinista local"},
	}
	if knowledge.Patch != "" {
		result.Sources = append(result.Sources, "Data Dragon "+knowledge.Patch)
	}
	if snapshot.Status == domain.StatusWaitingForGame || snapshot.Game == nil {
		result.Status = StatusWaiting
		result.Reason = "Esperando una partida activa."
		return result
	}
	if snapshot.Status == domain.StatusStale {
		result.Status = StatusStale
		result.Reason = "Los datos de la partida están desactualizados."
		return result
	}
	game := *snapshot.Game
	if !strings.EqualFold(game.ActivePlayer.ChampionName, "Garen") {
		result.Status = StatusUnsupported
		result.Reason = "Este MVP solo admite a Garen."
		return result
	}
	if !strings.EqualFold(game.GameMode, "CLASSIC") || game.MapNumber != 11 {
		result.Status = StatusUnsupported
		result.Reason = "Este MVP solo admite la Grieta del Invocador."
		return result
	}
	if err := knowledge.Validate(); err != nil {
		result.Status = StatusCatalogUnavailable
		result.Reason = "El catálogo de objetos no está disponible."
		result.Warnings = []string{err.Error()}
		return result
	}

	analysisResult := analysis.AnalyzeEnemies(game, knowledge)
	result.DamageMix = analysisResult.DamageMix
	result.MainThreat = analysisResult.MainThreat
	result.Confidence = analysisResult.Confidence
	result.Warnings = append(result.Warnings, analysisResult.Warnings...)
	result.PlayerState = analysis.ClassifyPlayerState(game, knowledge)

	candidates, profile := candidateIDs(result.DamageMix, result.PlayerState)
	owned := ownedMultiset(game.ActivePlayer.Items)
	target, alternative, allComplete := chooseTargets(candidates, owned, knowledge)
	if target == nil {
		if allComplete {
			result.Status = StatusComplete
			result.Reason = "Ya completaste las opciones prioritarias para esta situación."
			return result
		}
		result.Status = StatusError
		result.Reason = "El parche actual no contiene candidatos válidos para esta situación."
		result.Warnings = append(result.Warnings, "Los candidatos fueron retirados, no comprables o no disponibles en el mapa 11.")
		return result
	}
	result.TargetItem = itemReference(*target, target.Gold.Total)
	if alternative == nil {
		result.Status = StatusError
		result.Reason = "No hay una alternativa válida para este parche."
		result.Warnings = append(result.Warnings, "La recomendación requiere un objetivo alternativo distinto y disponible en el mapa 11.")
		return result
	}
	result.Alternative = itemReference(*alternative, alternative.Gold.Total)

	options, err := actionableComponents(target.ID, cloneOwned(owned), knowledge)
	if err != nil {
		result.Status = StatusError
		result.Reason = "No se pudo validar la receta del objeto objetivo."
		result.Warnings = append(result.Warnings, err.Error())
		return result
	}
	if len(options) == 0 {
		result.Status = StatusComplete
		result.Reason = "El objeto objetivo ya está completo."
		return result
	}
	gold := max(int(math.Floor(game.ActivePlayer.CurrentGold)), 0)
	result.InventorySlotsUsed = inventorySlotsUsed(game.ActivePlayer.Items, knowledge)
	selected, affordable, slotBlocked := selectComponent(options, gold, result.InventorySlotsUsed)
	result.NextComponent = itemReference(selected.item, selected.cost)
	if slotBlocked {
		result.Status = StatusInventoryFull
		result.GoldNeeded = max(selected.cost-gold, 0)
		result.Reason = "Tu inventario está lleno; libera un espacio antes de comprar " + selected.item.Name + "."
		result.Warnings = append(result.Warnings, "La compra no se combina inmediatamente con objetos existentes y requiere un espacio libre.")
		return result
	}
	result.Reason = spanishReason(profile, result.PlayerState, result.MainThreat.ChampionName)
	if affordable {
		result.Status = StatusReady
		return result
	}
	result.Status = StatusSaveForComponent
	result.GoldNeeded = max(selected.cost-gold, 0)
	return result
}

func candidateIDs(mix analysis.DamageMix, state analysis.PlayerState) ([]int, string) {
	if mix.Magic >= 0.58 {
		return []int{itemForceNature, itemSpiritVisage, itemSteraks, itemJakSho, itemStridebreaker}, "magic"
	}
	if mix.Physical >= 0.58 {
		if state == analysis.PlayerAhead {
			return []int{itemDeadMans, itemRanduins, itemSteraks, itemStridebreaker, itemJakSho}, "physical"
		}
		return []int{itemRanduins, itemDeadMans, itemSteraks, itemJakSho, itemStridebreaker}, "physical"
	}
	if state == analysis.PlayerBehind {
		return []int{itemSteraks, itemJakSho, itemStridebreaker, itemRanduins, itemForceNature}, "mixed"
	}
	return []int{itemStridebreaker, itemSteraks, itemJakSho, itemDeadMans, itemForceNature}, "mixed"
}

func chooseTargets(ids []int, owned map[int]int, knowledge catalog.Catalog) (*catalog.Item, *catalog.Item, bool) {
	valid := make([]catalog.Item, 0, len(ids))
	for _, id := range ids {
		item, exists := knowledge.Items[id]
		if !exists || !validShopItem(item) {
			continue
		}
		valid = append(valid, item)
	}
	remaining := make([]catalog.Item, 0, len(valid))
	for _, item := range valid {
		if owned[item.ID] == 0 {
			remaining = append(remaining, item)
		}
	}
	if len(remaining) == 0 {
		return nil, nil, len(valid) > 0
	}
	target := remaining[0]
	var alternative *catalog.Item
	if len(remaining) > 1 {
		candidate := remaining[1]
		alternative = &candidate
	}
	return &target, alternative, false
}

type purchaseOption struct {
	item          catalog.Item
	cost          int
	combine       bool
	consumedSlots int
}

func actionableComponents(targetID int, owned map[int]int, knowledge catalog.Catalog) ([]purchaseOption, error) {
	outcomes, err := enumerateRecipe(targetID, owned, knowledge, make(map[int]bool))
	if err != nil {
		return nil, err
	}
	if len(outcomes) == 0 {
		return nil, nil
	}
	best := outcomes[0]
	for _, outcome := range outcomes[1:] {
		if betterAllocation(outcome, best) {
			best = outcome
		}
	}
	return best.options, nil
}

type recipeOutcome struct {
	satisfied bool
	remaining map[int]int
	options   []purchaseOption
}

func enumerateRecipe(id int, owned map[int]int, knowledge catalog.Catalog, visiting map[int]bool) ([]recipeOutcome, error) {
	if owned[id] > 0 {
		remaining := cloneOwned(owned)
		remaining[id]--
		return []recipeOutcome{{satisfied: true, remaining: remaining}}, nil
	}
	item, exists := knowledge.Items[id]
	if !exists || !validShopItem(item) {
		return nil, fmt.Errorf("recipe item %d is unavailable", id)
	}
	if visiting[id] {
		return nil, fmt.Errorf("recipe cycle detected at item %d", id)
	}
	visiting[id] = true
	defer delete(visiting, id)
	if len(item.From) == 0 {
		return []recipeOutcome{{
			remaining: cloneOwned(owned),
			options:   []purchaseOption{{item: item, cost: item.Gold.Total}},
		}}, nil
	}

	var outcomes []recipeOutcome
	for _, order := range permutations(len(item.From)) {
		partials := []recipeOutcome{{satisfied: true, remaining: cloneOwned(owned)}}
		for _, childIndex := range order {
			childID := item.From[childIndex]
			next := make([]recipeOutcome, 0)
			for _, partial := range partials {
				childOutcomes, err := enumerateRecipe(childID, partial.remaining, knowledge, visiting)
				if err != nil {
					return nil, err
				}
				for _, child := range childOutcomes {
					combinedOptions := append([]purchaseOption(nil), partial.options...)
					combinedOptions = append(combinedOptions, child.options...)
					next = append(next, recipeOutcome{
						satisfied: partial.satisfied && child.satisfied,
						remaining: child.remaining,
						options:   combinedOptions,
					})
				}
			}
			partials = next
		}
		for _, partial := range partials {
			partial.satisfied = false
			if len(partial.options) == 0 {
				partial.options = append(partial.options, purchaseOption{
					item: item, cost: max(item.Gold.Base, 0), combine: true, consumedSlots: len(item.From),
				})
			}
			outcomes = append(outcomes, partial)
		}
	}
	return outcomes, nil
}

func permutations(size int) [][]int {
	if size <= 1 {
		return [][]int{{0}}
	}
	values := make([]int, size)
	for index := range values {
		values[index] = index
	}
	result := make([][]int, 0)
	var build func(int)
	build = func(position int) {
		if position == len(values) {
			result = append(result, append([]int(nil), values...))
			return
		}
		for index := position; index < len(values); index++ {
			values[position], values[index] = values[index], values[position]
			build(position + 1)
			values[position], values[index] = values[index], values[position]
		}
	}
	build(0)
	return result
}

func betterAllocation(candidate, current recipeOutcome) bool {
	candidateCombines, currentCombines := 0, 0
	candidateCost, currentCost := 0, 0
	for _, option := range candidate.options {
		candidateCost += option.cost
		if option.combine {
			candidateCombines++
		}
	}
	for _, option := range current.options {
		currentCost += option.cost
		if option.combine {
			currentCombines++
		}
	}
	if candidateCombines != currentCombines {
		return candidateCombines > currentCombines
	}
	if candidateCost != currentCost {
		return candidateCost < currentCost
	}
	return allocationKey(candidate.options) < allocationKey(current.options)
}

func allocationKey(options []purchaseOption) string {
	values := append([]purchaseOption(nil), options...)
	sort.SliceStable(values, func(left, right int) bool {
		if values[left].item.ID == values[right].item.ID {
			return values[left].cost < values[right].cost
		}
		return values[left].item.ID < values[right].item.ID
	})
	var builder strings.Builder
	for _, option := range values {
		fmt.Fprintf(&builder, "%d:%d:%t;", option.item.ID, option.cost, option.combine)
	}
	return builder.String()
}

func selectComponent(options []purchaseOption, gold, slotsUsed int) (purchaseOption, bool, bool) {
	eligible := make([]purchaseOption, 0, len(options))
	for _, option := range options {
		if slotsUsed < 6 || option.consumedSlots > 0 {
			eligible = append(eligible, option)
		}
	}
	if len(eligible) == 0 {
		blockedAffordable := make([]purchaseOption, 0, len(options))
		for _, option := range options {
			if option.cost <= gold {
				blockedAffordable = append(blockedAffordable, option)
			}
		}
		if len(blockedAffordable) > 0 {
			return preferredComponent(blockedAffordable, true), false, true
		}
		return preferredComponent(options, false), false, true
	}
	affordable := make([]purchaseOption, 0, len(options))
	for _, option := range eligible {
		if option.cost <= gold {
			affordable = append(affordable, option)
		}
	}
	if len(affordable) > 0 {
		return preferredComponent(affordable, true), true, false
	}
	return preferredComponent(eligible, false), false, false
}

func preferredComponent(options []purchaseOption, affordable bool) purchaseOption {
	values := append([]purchaseOption(nil), options...)
	sort.SliceStable(values, func(left, right int) bool {
		if values[left].combine != values[right].combine {
			return values[left].combine
		}
		if values[left].cost == values[right].cost {
			return values[left].item.ID < values[right].item.ID
		}
		if affordable {
			return values[left].cost > values[right].cost
		}
		return values[left].cost < values[right].cost
	})
	return values[0]
}

func validShopItem(item catalog.Item) bool {
	return item.ID > 0 && item.Purchasable && item.AvailableMap11 && item.Gold.Total > 0 && strings.TrimSpace(item.Name) != ""
}

func ownedMultiset(items []domain.Item) map[int]int {
	owned := make(map[int]int, len(items))
	for _, item := range items {
		owned[item.ID] += max(item.Count, 1)
	}
	return owned
}

func cloneOwned(source map[int]int) map[int]int {
	clone := make(map[int]int, len(source))
	for id, count := range source {
		clone[id] = count
	}
	return clone
}

func inventorySlotsUsed(items []domain.Item, knowledge catalog.Catalog) int {
	used := 0
	for _, owned := range items {
		if owned.ID <= 0 {
			continue
		}
		item, exists := knowledge.Items[owned.ID]
		if exists && hasTag(item.Tags, "Trinket") {
			continue
		}
		used++
	}
	return used
}

func hasTag(tags []string, wanted string) bool {
	for _, tag := range tags {
		if strings.EqualFold(tag, wanted) {
			return true
		}
	}
	return false
}

func itemReference(item catalog.Item, cost int) *ItemRef {
	return &ItemRef{ID: item.ID, Name: item.Name, Cost: cost, Image: item.Image}
}

func spanishReason(profile string, state analysis.PlayerState, threat string) string {
	if threat == "" {
		threat = "la amenaza principal"
	}
	switch profile {
	case "magic":
		return "El daño enemigo es mayormente mágico; prioriza resistencia mágica contra " + threat + "."
	case "physical":
		return "El daño enemigo es mayormente físico; prioriza armadura contra " + threat + "."
	default:
		if state == analysis.PlayerAhead {
			return "Vas por delante; completa tu objeto central sin ignorar el daño mixto."
		}
		if state == analysis.PlayerBehind {
			return "Vas por detrás; prioriza supervivencia contra el daño mixto."
		}
		return "La composición enemiga tiene daño mixto; mantén una compra equilibrada."
	}
}
