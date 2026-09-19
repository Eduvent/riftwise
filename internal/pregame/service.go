package pregame

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"lolengine/internal/analysis"
	"lolengine/internal/catalog"
	"lolengine/internal/domain"
	"lolengine/internal/itemization"
	"lolengine/internal/itemprofiles"
	"lolengine/internal/itemrules"
	"lolengine/internal/personalization"
)

var ErrUnavailable = errors.New("manual pregame planning is unavailable")

type CatalogProvider interface {
	Current() (catalog.Catalog, catalog.Status, bool)
}

type Planner interface {
	Options(context.Context) (Options, error)
	Plan(context.Context, Scenario) (Plan, error)
}

type Service struct {
	catalogs        CatalogProvider
	rules           itemrules.Provider
	profiles        itemprofiles.Provider
	personalization personalization.Provider
	now             func() time.Time
}

func NewService(catalogs CatalogProvider, rules itemrules.Provider, profiles itemprofiles.Provider, providers ...personalization.Provider) (*Service, error) {
	if catalogs == nil || rules == nil || profiles == nil {
		return nil, errors.New("pregame catalog, rules, and profiles are required")
	}
	service := &Service{catalogs: catalogs, rules: rules, profiles: profiles, now: func() time.Time { return time.Now().UTC() }}
	if len(providers) > 0 {
		service.personalization = providers[0]
	}
	return service, nil
}

func (service *Service) Options(ctx context.Context) (Options, error) {
	if err := ctx.Err(); err != nil {
		return Options{}, err
	}
	knowledge, catalogStatus, ready := service.catalogs.Current()
	if !ready || !catalogStatus.Ready || knowledge.Validate() != nil {
		return Options{}, ErrUnavailable
	}
	profileResolution := service.profiles.Resolve(knowledge.Patch, itemprofiles.Request{Champion: "Garen", Role: domain.RoleTop}, knowledge)
	if !profileResolution.Status.Ready {
		return Options{}, ErrUnavailable
	}
	rulesResolution := service.rules.Resolve(knowledge.Patch, knowledge)
	result := Options{
		Mode: ModeManualPregame, Patch: knowledge.Patch, Catalog: catalogStatus,
		Profiles: profileResolution.Status, Rules: rulesResolution.Status,
		Roles: []SelectOption{
			{Value: string(domain.RoleTop), Label: "Superior"},
			{Value: string(domain.RoleJungle), Label: "Jungla"},
			{Value: string(domain.RoleMiddle), Label: "Central"},
			{Value: string(domain.RoleBottom), Label: "Inferior"},
			{Value: string(domain.RoleSupport), Label: "Soporte"},
		},
		Stages: []SelectOption{
			{Value: string(StageLaneStart), Label: "Inicio de línea"},
			{Value: string(StageFirstRecall), Label: "Primer regreso"},
			{Value: string(StageMidGame), Label: "Medio juego"},
			{Value: string(StageLateGame), Label: "Juego tardío"},
		},
		Strategies: []SelectOption{
			{Value: string(domain.StrategyBalanced), Label: "Equilibrada"},
			{Value: string(domain.StrategySplitPush), Label: "Presión lateral"},
			{Value: string(domain.StrategyTeamfight), Label: "Pelea de equipo"},
		},
		PressureLevels: []SelectOption{
			{Value: string(PressureUnset), Label: "Sin indicar"},
			{Value: string(PressureNone), Label: "Ninguna"},
			{Value: string(PressureLow), Label: "Baja"},
			{Value: string(PressureMedium), Label: "Media"},
			{Value: string(PressureHigh), Label: "Alta"},
		},
	}
	if service.personalization != nil {
		result.Personalization = service.personalization.Status()
	} else {
		result.Personalization = personalization.Status{Style: personalization.StyleBalanced, State: "unavailable"}
	}
	championKeys := make([]string, 0, len(knowledge.Champions))
	for key := range knowledge.Champions {
		championKeys = append(championKeys, key)
	}
	sort.Slice(championKeys, func(left, right int) bool {
		leftChampion := knowledge.Champions[championKeys[left]]
		rightChampion := knowledge.Champions[championKeys[right]]
		if leftChampion.Name != rightChampion.Name {
			return leftChampion.Name < rightChampion.Name
		}
		return leftChampion.ID < rightChampion.ID
	})
	for _, key := range championKeys {
		champion := knowledge.Champions[key]
		if strings.TrimSpace(champion.ID) == "" || strings.TrimSpace(champion.Name) == "" {
			continue
		}
		result.Champions = append(result.Champions, ChampionOption{ID: champion.ID, Name: champion.Name})
	}
	itemIDs := make([]int, 0, len(knowledge.Items))
	for id := range knowledge.Items {
		itemIDs = append(itemIDs, id)
	}
	sort.Ints(itemIDs)
	for _, id := range itemIDs {
		item := knowledge.Items[id]
		if item.ID <= 0 || !item.AvailableMap11 || strings.TrimSpace(item.Name) == "" {
			continue
		}
		result.Items = append(result.Items, ItemOption{ID: item.ID, Name: item.Name})
	}
	return result, nil
}

func (service *Service) Plan(ctx context.Context, source Scenario) (Plan, error) {
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	knowledge, catalogStatus, ready := service.catalogs.Current()
	if !ready || !catalogStatus.Ready || knowledge.Validate() != nil {
		return Plan{}, ErrUnavailable
	}
	scenario, validation := Normalize(source, knowledge)
	if validation != nil {
		return Plan{}, validation
	}
	profileResolution := service.profiles.Resolve(knowledge.Patch, itemprofiles.Request{
		Champion: scenario.ChampionID, Role: scenario.Role,
	}, knowledge)
	if !profileResolution.Status.Ready {
		return Plan{}, ErrUnavailable
	}

	traits := profileResolution.ChampionTraits
	candidates := []itemrules.CandidateRule(nil)
	route := itemization.PlanningRouteGeneric
	stability := profileResolution.Stability
	alternativeCount := profileResolution.AlternativeCount
	var ruleStatus *itemrules.Status
	if strings.EqualFold(scenario.ChampionID, "Garen") {
		resolution := service.rules.Resolve(knowledge.Patch, knowledge)
		if resolution.Legacy || !resolution.Status.Ready || len(resolution.Rules.Candidates) == 0 {
			return Plan{}, ErrUnavailable
		}
		status := resolution.Status
		ruleStatus = &status
		traits = resolution.Rules.ChampionTraits
		candidates = append([]itemrules.CandidateRule(nil), resolution.Rules.Candidates...)
		stability = resolution.Rules.Stability
		alternativeCount = resolution.Rules.AlternativeCount
		route = itemization.PlanningRouteGarenV2
	}

	needs, explicitNeeds, contextConfidence, traitConfidence, warnings := buildNeeds(scenario, knowledge, traits)
	if route == itemization.PlanningRouteGeneric {
		activationNeeds := needs
		if profileResolution.Provenance.Level == itemprofiles.KindNeutral {
			activationNeeds = explicitNeeds
		}
		candidates = profileResolution.ActiveCandidates(activationNeeds)
	}
	for _, advisory := range profileResolution.Warnings {
		warnings = appendUniqueWarning(warnings, Warning{Code: advisory.Code, Message: advisory.Message})
	}
	if profileResolution.Provenance.Level != itemprofiles.KindChampionRole {
		warnings = appendUniqueWarning(warnings, Warning{
			Code:    WarningProfileFallback,
			Message: "El texto y las opciones de preparación usan un perfil genérico o asumido de menor confianza.",
		})
	}

	confidence := minFloat(
		ManualConfidenceCap,
		profileResolution.Provenance.ConfidenceCap,
		contextConfidence,
		traitConfidence,
	)
	input := itemization.RankingInput{
		Phase: scenario.Stage.Phase(), Strategy: scenario.Strategy, Needs: needs,
		Gold: scenario.Gold, Inventory: rankingInventory(scenario.Inventory),
	}
	base := itemization.Result{
		GeneratedAt: service.now().UTC(), Patch: knowledge.Patch,
		Confidence: confidence, TraitConfidence: traitConfidence, Needs: cloneNeedScores(needs),
		AlternativeLimit: alternativeCount, Stability: stability,
	}
	personalizationSnapshot := personalization.Snapshot{}
	if service.personalization != nil {
		key := personalization.ContextKey{ChampionID: scenario.ChampionID, Role: scenario.Role, Phase: input.Phase, Strategy: scenario.Strategy, Patch: knowledge.Patch, ProfileID: profileResolution.Provenance.ProfileID, ProfileVersion: profileResolution.Provenance.RulesetVersion, RulesVersion: profileResolution.Provenance.RulesetVersion}
		if ruleStatus != nil {
			key.RulesVersion = ruleStatus.Version
		}
		personalizationSnapshot = service.personalization.Snapshot(key)
	}
	ranked := itemization.RankPlanning(itemization.PlanningRankingRequest{
		Base: base, Input: input, Candidates: candidates, Route: route,
		Neutral: profileResolution.Provenance.Level == itemprofiles.KindNeutral, Personalization: personalizationSnapshot,
	}, knowledge)
	for _, message := range ranked.Warnings {
		warnings = appendUniqueWarning(warnings, Warning{Code: "item_plan", Message: message})
	}

	profile := profileResolution.Provenance
	profile.FamilyIDs = append([]string(nil), profileResolution.Provenance.FamilyIDs...)
	sources := []string{"User-supplied scenario", "Data Dragon " + knowledge.Patch, "Profile " + profileResolution.Provenance.RulesetVersion}
	if ruleStatus != nil {
		sources = append(sources, "Garen v2 rules "+ruleStatus.Version+" ("+ruleStatus.Source+")")
	}
	if ranked.Personalization != nil {
		sources = append(sources, "Preferencias deterministas locales (versión "+fmt.Sprint(ranked.Personalization.Version)+")")
	}
	return Plan{
		Mode: ModeManualPregame, Status: ranked.Status, GeneratedAt: service.now().UTC().Format(time.RFC3339Nano),
		Patch: knowledge.Patch, Scenario: scenario, Phase: input.Phase,
		Provenance: Provenance{
			Profile: profile, ProfileStatus: profileResolution.Status, Rules: ruleStatus,
			ManualConfidenceCap: ManualConfidenceCap, ContextConfidence: contextConfidence, TraitConfidence: traitConfidence,
		},
		Confidence: confidence, Needs: cloneNeedScores(needs),
		TargetItem: cloneItemRef(ranked.TargetItem), NextComponent: cloneItemRef(ranked.NextComponent),
		Alternative: cloneItemRef(ranked.Alternative), Alternatives: cloneRanked(ranked.Alternatives), RankedOptions: cloneRanked(ranked.RankedOptions),
		Reason: ranked.Reason, GoldNeeded: ranked.GoldNeeded, InventorySlotsUsed: ranked.InventorySlotsUsed,
		RunePlan: clonePlanCopy(profileResolution.Profile.Runes), SummonerPlan: clonePlanCopy(profileResolution.Profile.Summoners),
		InitialPlan: clonePlanCopy(profileResolution.Profile.InitialPlan), Warnings: warnings,
		Sources:         sources,
		Personalization: ranked.Personalization,
	}, nil
}

func buildNeeds(scenario NormalizedScenario, knowledge catalog.Catalog, traits map[string]itemrules.TraitProfile) (analysis.NeedScores, analysis.NeedScores, float64, float64, []Warning) {
	needs := make(analysis.NeedScores)
	explicitNeeds := make(analysis.NeedScores)
	explicit := make(map[itemrules.Signal]bool)
	explicitCount := 0
	for _, entry := range scenario.Pressures.entries() {
		needs[entry.Signal] = entry.Value.Score()
		if entry.Value != PressureUnset {
			explicit[entry.Signal] = true
			explicitNeeds[entry.Signal] = entry.Value.Score()
			explicitCount++
		}
	}
	contextConfidence := math.Round((0.45+0.35*float64(explicitCount)/float64(len(scenario.Pressures.entries())))*1000) / 1000
	traitConfidence := 1.0
	warnings := []Warning(nil)
	if scenario.LaneOpponentID == "" {
		return needs, explicitNeeds, contextConfidence, traitConfidence, warnings
	}
	opponent, known := catalog.ResolveChampion(knowledge, scenario.LaneOpponentID)
	if known {
		mix, _ := analysis.ChampionDamageEstimate(opponent)
		if !explicit[itemrules.SignalPhysical] {
			needs[itemrules.SignalPhysical] = clampNeed(int(math.Round(mix.Physical * 1000)))
		}
		if !explicit[itemrules.SignalMagic] {
			needs[itemrules.SignalMagic] = clampNeed(int(math.Round(mix.Magic * 1000)))
		}
	}
	profile, profileKnown := opponentTraits(scenario.LaneOpponentID, traits, knowledge)
	if profileKnown {
		fillTraitNeed(needs, explicit, itemrules.SignalHealing, profile.Healing)
		fillTraitNeed(needs, explicit, itemrules.SignalShielding, profile.Shielding)
		fillTraitNeed(needs, explicit, itemrules.SignalCrowdControl, profile.CrowdControl)
		fillTraitNeed(needs, explicit, itemrules.SignalSlows, profile.Slows)
		fillTraitNeed(needs, explicit, itemrules.SignalBurst, profile.Burst)
		fillTraitNeed(needs, explicit, itemrules.SignalSustained, profile.Sustained)
		traitConfidence = 0.80
	} else {
		traitConfidence = 0.55
		warnings = append(warnings, Warning{
			Code:    WarningOpponentTraitsMissing,
			Message: "El oponente no tiene rasgos estructurados completos; las presiones sin indicar permanecen conservadoras.",
		})
	}
	warnings = append(warnings, Warning{
		Code:    WarningOpponentAssumption,
		Message: "Las presiones sin indicar se completan solo con metadatos estructurados del oponente conocido; no representan al equipo enemigo ni el estado de una partida.",
	})
	return needs, explicitNeeds, contextConfidence, traitConfidence, warnings
}

func opponentTraits(championID string, traits map[string]itemrules.TraitProfile, knowledge catalog.Catalog) (itemrules.TraitProfile, bool) {
	keys := make([]string, 0, len(traits))
	for key := range traits {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		champion, exists := catalog.ResolveChampion(knowledge, key)
		if exists && strings.EqualFold(champion.ID, championID) {
			return traits[key], true
		}
	}
	return itemrules.TraitProfile{}, false
}

func fillTraitNeed(needs analysis.NeedScores, explicit map[itemrules.Signal]bool, signal itemrules.Signal, value int) {
	if !explicit[signal] {
		needs[signal] = clampNeed(value)
	}
}

func rankingInventory(source []NormalizedInventoryStack) []itemization.RankingInventoryItem {
	result := make([]itemization.RankingInventoryItem, len(source))
	for index, stack := range source {
		result[index] = itemization.RankingInventoryItem{ID: stack.ItemID, Count: stack.Count, Slot: index}
	}
	return result
}

func clonePlanCopy(source itemprofiles.PlanCopy) itemprofiles.PlanCopy {
	return itemprofiles.PlanCopy{Summary: source.Summary, Source: source.Source, Options: append([]itemprofiles.PlanOption(nil), source.Options...)}
}

func cloneItemRef(source *itemization.ItemRef) *itemization.ItemRef {
	if source == nil {
		return nil
	}
	clone := *source
	return &clone
}

func cloneRanked(source []itemization.RankedItem) []itemization.RankedItem {
	result := make([]itemization.RankedItem, len(source))
	for index, ranked := range source {
		result[index] = ranked
		result[index].CoveredNeeds = append([]itemrules.Signal(nil), ranked.CoveredNeeds...)
		result[index].ReasonCodes = append([]string(nil), ranked.ReasonCodes...)
	}
	return result
}

func cloneNeedScores(source analysis.NeedScores) analysis.NeedScores {
	result := make(analysis.NeedScores, len(source))
	for signal, score := range source {
		result[signal] = score
	}
	return result
}

func appendUniqueWarning(values []Warning, candidate Warning) []Warning {
	for _, current := range values {
		if current.Code == candidate.Code && current.Message == candidate.Message {
			return values
		}
	}
	return append(values, candidate)
}

func clampNeed(value int) int {
	return min(max(value, 0), 1000)
}

func minFloat(values ...float64) float64 {
	result := 1.0
	for _, value := range values {
		if value < result {
			result = value
		}
	}
	return math.Max(result, 0)
}
