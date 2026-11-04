package itemization

import (
	"math"
	"strings"
	"time"

	"lolengine/internal/analysis"
	"lolengine/internal/catalog"
	"lolengine/internal/domain"
	"lolengine/internal/itemprofiles"
	"lolengine/internal/itemrules"
	"lolengine/internal/personalization"
)

func (engine Engine) recommendGeneric(now time.Time, snapshot domain.Snapshot, knowledge catalog.Catalog) Result {
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
	if (!strings.EqualFold(game.GameMode, "CLASSIC") && !strings.EqualFold(game.GameMode, "PRACTICETOOL")) || game.MapNumber != 11 {
		result.Status = StatusUnsupported
		result.Reason = "Las recomendaciones de perfiles solo admiten la Grieta del Invocador."
		return result
	}
	if err := knowledge.Validate(); err != nil {
		result.Status = StatusCatalogUnavailable
		result.Reason = "El catálogo de objetos no está disponible."
		result.Warnings = []string{err.Error()}
		return result
	}

	role, _ := domain.ParseRole(game.ActivePlayer.Position)
	resolution := engine.Profiles.Resolve(knowledge.Patch, itemprofiles.Request{
		Champion: game.ActivePlayer.ChampionName,
		Role:     role,
	}, knowledge)
	profileStatus := resolution.Status
	result.ProfileStatus = &profileStatus
	result.Advisories = append([]itemprofiles.Warning(nil), resolution.Warnings...)
	result.Rules = itemrules.Status{
		Ready:     resolution.Status.Ready,
		Version:   resolution.Status.Version,
		Patch:     resolution.Status.Patch,
		Source:    "generic_" + resolution.Status.Source,
		Fallback:  resolution.Status.Fallback,
		LastError: resolution.Status.LastError,
	}
	for _, advisory := range result.Advisories {
		result.Warnings = append(result.Warnings, advisory.Message)
	}
	if !resolution.Status.Ready {
		result.Status = StatusError
		result.Reason = "Los perfiles genéricos no están disponibles para este parche."
		if resolution.Status.LastError != "" {
			result.Warnings = append(result.Warnings, resolution.Status.LastError)
		}
		return result
	}

	provenance := resolution.Provenance
	provenance.FamilyIDs = append([]string(nil), resolution.Provenance.FamilyIDs...)
	result.Profile = &provenance
	result.RunePlan = clonePlanCopy(resolution.Profile.Runes)
	result.SummonerPlan = clonePlanCopy(resolution.Profile.Summoners)
	result.InitialPlan = clonePlanCopy(resolution.Profile.InitialPlan)
	strategy := engine.Strategy
	if !strategy.Valid() {
		strategy = domain.StrategyBalanced
	}
	contextRules := itemrules.RuleSet{
		SchemaVersion:    itemrules.CurrentSchemaVersion,
		RulesetVersion:   resolution.Provenance.RulesetVersion,
		Patch:            resolution.Provenance.Patch,
		AlternativeCount: resolution.AlternativeCount,
		Phases:           resolution.Phases,
		Context:          resolution.Context,
		Stability:        resolution.Stability,
		ChampionTraits:   resolution.ChampionTraits,
		ItemTraits:       resolution.ItemTraits,
	}
	context := analysis.BuildMatchContext(game, knowledge, contextRules, strategy)
	result.PlayerState = context.PlayerState
	result.DamageMix = context.DamageMix
	result.MainThreat = context.MainThreat
	result.LaneOpponent = context.LaneOpponent
	result.Phase = context.Phase
	result.Strategy = context.Strategy
	result.TraitConfidence = context.TraitConfidence
	result.Confidence = math.Min(resolution.Provenance.ConfidenceCap, math.Min(context.Confidence, context.TraitConfidence))
	result.Needs = context.Needs
	result.Stability = resolution.Stability
	result.AlternativeLimit = resolution.AlternativeCount
	result.Warnings = append(result.Warnings, context.Warnings...)
	result.Sources = append(result.Sources, "Perfil estructural "+resolution.Provenance.RulesetVersion)

	candidates := resolution.ActiveCandidates(context.Needs)
	if len(candidates) < minimumRankedAlternatives+1 {
		appendAdvisory(&result, itemprofiles.WarningInsufficientContext)
		result.Status = StatusInsufficientContext
		result.Reason = "No hay contexto suficiente para ofrecer un objetivo y dos alternativas distintas."
		result.BaseWarnings = append([]string(nil), result.Warnings...)
		return result
	}
	result.BaseWarnings = append([]string(nil), result.Warnings...)
	insufficientStatus := StatusError
	if resolution.Provenance.Level == itemprofiles.KindNeutral {
		insufficientStatus = StatusInsufficientContext
	}
	personalizationSnapshot := personalization.Snapshot{}
	if engine.Personalization != nil {
		personalizationSnapshot = engine.Personalization.Snapshot(personalization.ContextKey{
			ChampionID: resolution.Provenance.ChampionID, Role: resolution.Provenance.Role, Phase: context.Phase,
			Strategy: context.Strategy, Patch: knowledge.Patch, ProfileID: resolution.Provenance.ProfileID,
			ProfileVersion: resolution.Provenance.RulesetVersion, RulesVersion: resolution.Provenance.RulesetVersion,
		})
	}
	return rankAndPlan(result, liveRankingInput(game, context), knowledge, context, candidates, rankingCopy{
		completeReason:      "Ya completaste o no puedes comprar las opciones válidas de este perfil estructural.",
		insufficientStatus:  insufficientStatus,
		insufficientReason:  "El perfil no produjo un objetivo y dos alternativas distintas para este contexto.",
		insufficientWarning: "Se requieren al menos dos alternativas distintas además del objetivo principal.",
		inventoryFullReason: func(component ItemRef) string {
			return "El inventario está lleno; la opción " + component.Name + " necesita un espacio libre para estar disponible."
		},
		inventoryFullWarning: "Esta opción no se combina de inmediato con el inventario actual y necesita un espacio libre.",
		reason:               genericRankedReason,
	}, personalizationSnapshot)
}

func genericRankedReason(ranked RankedItem, context analysis.MatchContext) string {
	needs := make([]string, 0, len(ranked.CoveredNeeds))
	for _, need := range ranked.CoveredNeeds {
		if context.Needs[need] > 0 {
			needs = append(needs, spanishNeed(need))
		}
	}
	if len(needs) == 0 {
		return "El perfil estructural prioriza esta opción para la fase " + string(context.Phase) + " y la estrategia " + string(context.Strategy) + "."
	}
	return "El perfil estructural prioriza " + strings.Join(needs, ", ") + " en la fase " + string(context.Phase) + "."
}

func clonePlanCopy(source itemprofiles.PlanCopy) *itemprofiles.PlanCopy {
	clone := source
	clone.Options = append([]itemprofiles.PlanOption(nil), source.Options...)
	return &clone
}

func appendAdvisory(result *Result, code string) {
	if result == nil {
		return
	}
	for _, advisory := range result.Advisories {
		if advisory.Code == code {
			return
		}
	}
	message := "No hay contexto suficiente para ofrecer un objetivo y dos alternativas distintas."
	advisory := itemprofiles.Warning{Code: code, Message: message}
	result.Advisories = append(result.Advisories, advisory)
	result.Warnings = append(result.Warnings, message)
}
