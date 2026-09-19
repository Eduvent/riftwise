// Package itemprofiles loads and resolves strict patch-versioned generic item
// profiles without changing the pinned Garen v2 rule contract.
package itemprofiles

import (
	"strings"

	"lolengine/internal/catalog"
	"lolengine/internal/domain"
	"lolengine/internal/itemrules"
)

const CurrentSchemaVersion = 2

const PlanSourceProfileCurated = "profile_curated"

const (
	ConfidenceChampionRole    = 0.95
	ConfidenceChampionDefault = 0.80
	ConfidenceRoleArchetype   = 0.65
	ConfidenceTagArchetype    = 0.50
	ConfidenceNeutral         = 0.25
)

const (
	SourceExternalExact         = "external_exact"
	SourceExternalPatchFallback = "external_patch_fallback"
	SourceExternalLastKnownGood = "external_last_known_good"
	SourceUnavailable           = "unavailable"
)

const (
	WarningChampionDefault     = "profile_champion_default"
	WarningRoleArchetype       = "profile_role_archetype"
	WarningTagArchetype        = "profile_tag_archetype"
	WarningNeutralFallback     = "profile_neutral_fallback"
	WarningRoleUnavailable     = "role_unavailable"
	WarningChampionUnresolved  = "champion_unresolved"
	WarningInsufficientContext = "insufficient_context"
	WarningPatchFallback       = "profile_patch_fallback"
	WarningLastKnownGood       = "profile_last_known_good"
)

type Kind string

const (
	KindChampionRole    Kind = "champion_role"
	KindChampionDefault Kind = "champion_default"
	KindRoleArchetype   Kind = "role_archetype"
	KindTagArchetype    Kind = "champion_tag_archetype"
	KindNeutral         Kind = "neutral"
)

type Activation struct {
	Signal  itemrules.Signal `json:"signal"`
	Minimum int              `json:"minimum"`
}

type Candidate struct {
	Rule       itemrules.CandidateRule `json:"rule"`
	Activation []Activation            `json:"activation,omitempty"`
}

type CandidateFamily struct {
	Candidates []Candidate `json:"candidates"`
}

type PlanOption struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	When     string `json:"when"`
	Tradeoff string `json:"tradeoff"`
}

type PlanCopy struct {
	Summary string       `json:"summary"`
	Source  string       `json:"source"`
	Options []PlanOption `json:"options"`
}

type Profile struct {
	ID           string      `json:"id"`
	Kind         Kind        `json:"kind"`
	Champion     string      `json:"champion,omitempty"`
	Role         domain.Role `json:"role,omitempty"`
	Tags         []string    `json:"tags,omitempty"`
	ExcludedTags []string    `json:"excluded_tags,omitempty"`
	Families     []string    `json:"families"`
	Runes        PlanCopy    `json:"runes"`
	Summoners    PlanCopy    `json:"summoners"`
	InitialPlan  PlanCopy    `json:"initial_plan"`
}

type Document struct {
	SchemaVersion    int                               `json:"schema_version"`
	RulesetVersion   string                            `json:"ruleset_version"`
	Patch            string                            `json:"patch"`
	TagPriority      []string                          `json:"tag_priority"`
	AlternativeCount int                               `json:"alternative_count"`
	Phases           itemrules.PhaseBoundaries         `json:"phases"`
	Context          itemrules.ContextTuning           `json:"context"`
	Stability        itemrules.StabilityPolicy         `json:"stability"`
	ChampionTraits   map[string]itemrules.TraitProfile `json:"champion_traits,omitempty"`
	ItemTraits       map[int]itemrules.TraitProfile    `json:"item_traits,omitempty"`
	Families         map[string]CandidateFamily        `json:"families"`
	Profiles         []Profile                         `json:"profiles"`
	NeutralProfile   string                            `json:"neutral_profile"`
}

type Request struct {
	Champion string
	Role     domain.Role
}

type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Provenance struct {
	Level          Kind        `json:"level"`
	ProfileID      string      `json:"profile_id"`
	FamilyIDs      []string    `json:"family_ids"`
	RulesetVersion string      `json:"ruleset_version"`
	Patch          string      `json:"patch"`
	ChampionID     string      `json:"champion_id,omitempty"`
	Role           domain.Role `json:"role,omitempty"`
	ChampionTag    string      `json:"champion_tag,omitempty"`
	Fallback       bool        `json:"fallback"`
	ConfidenceCap  float64     `json:"confidence_cap"`
}

type Status struct {
	Ready     bool   `json:"ready"`
	Version   string `json:"version,omitempty"`
	Patch     string `json:"patch,omitempty"`
	Source    string `json:"source"`
	Fallback  bool   `json:"fallback"`
	LastError string `json:"last_error,omitempty"`
}

type Resolution struct {
	Profile          Profile
	Provenance       Provenance
	Warnings         []Warning
	Status           Status
	Candidates       []Candidate
	AlternativeCount int
	Phases           itemrules.PhaseBoundaries
	Context          itemrules.ContextTuning
	Stability        itemrules.StabilityPolicy
	ChampionTraits   map[string]itemrules.TraitProfile
	ItemTraits       map[int]itemrules.TraitProfile
}

func (resolution Resolution) ActiveCandidates(needs map[itemrules.Signal]int) []itemrules.CandidateRule {
	result := make([]itemrules.CandidateRule, 0, len(resolution.Candidates))
	for _, candidate := range resolution.Candidates {
		active := len(candidate.Activation) == 0
		for _, threshold := range candidate.Activation {
			if needs[threshold.Signal] >= threshold.Minimum {
				active = true
				break
			}
		}
		if active {
			result = append(result, cloneCandidateRule(candidate.Rule))
		}
	}
	return result
}

type compiledProfile struct {
	profile      Profile
	championID   string
	tags         []string
	excludedTags []string
	candidates   []Candidate
}

type Registry struct {
	document  Document
	knowledge catalog.Catalog
	profiles  []compiledProfile
	neutral   compiledProfile
}

func (registry *Registry) Resolve(request Request) Resolution {
	warnings := make([]Warning, 0, 3)
	champion, championKnown := catalog.ResolveChampion(registry.knowledge, request.Champion)
	if !championKnown {
		warnings = append(warnings, warning(WarningChampionUnresolved))
	}
	if !request.Role.Valid() {
		warnings = append(warnings, warning(WarningRoleUnavailable))
	}

	if championKnown && request.Role.Valid() {
		if profile, ok := registry.find(func(profile compiledProfile) bool {
			return profile.profile.Kind == KindChampionRole && profile.championID == champion.ID && profile.profile.Role == request.Role
		}); ok {
			return registry.resolution(profile, champion.ID, request.Role, "", warnings)
		}
	}
	if championKnown {
		if profile, ok := registry.find(func(profile compiledProfile) bool {
			return profile.profile.Kind == KindChampionDefault && profile.championID == champion.ID
		}); ok {
			warnings = append(warnings, warning(WarningChampionDefault))
			return registry.resolution(profile, champion.ID, request.Role, "", warnings)
		}
	}
	if championKnown && request.Role.Valid() {
		if profile, ok := registry.find(func(profile compiledProfile) bool {
			return profile.profile.Kind == KindRoleArchetype && profile.profile.Role == request.Role && compatibleTags(champion.Tags, profile.tags, profile.excludedTags)
		}); ok {
			warnings = append(warnings, warning(WarningRoleArchetype))
			return registry.resolution(profile, champion.ID, request.Role, matchedTag(champion.Tags, profile.tags), warnings)
		}
	}
	if championKnown {
		for _, priority := range registry.document.TagPriority {
			if !containsFold(champion.Tags, priority) {
				continue
			}
			if profile, ok := registry.find(func(profile compiledProfile) bool {
				return profile.profile.Kind == KindTagArchetype && containsFold(profile.tags, priority) &&
					compatibleTags(champion.Tags, profile.tags, profile.excludedTags)
			}); ok {
				warnings = append(warnings, warning(WarningTagArchetype))
				return registry.resolution(profile, champion.ID, request.Role, priority, warnings)
			}
		}
	}
	warnings = append(warnings, warning(WarningNeutralFallback))
	championID := ""
	if championKnown {
		championID = champion.ID
	}
	return registry.resolution(registry.neutral, championID, request.Role, "", warnings)
}

func (registry *Registry) find(match func(compiledProfile) bool) (compiledProfile, bool) {
	for _, profile := range registry.profiles {
		if match(profile) {
			return profile, true
		}
	}
	return compiledProfile{}, false
}

func (registry *Registry) resolution(profile compiledProfile, championID string, role domain.Role, tag string, warnings []Warning) Resolution {
	return Resolution{
		Profile: cloneProfile(profile.profile), Candidates: cloneCandidates(profile.candidates),
		Provenance: Provenance{
			Level: profile.profile.Kind, ProfileID: profile.profile.ID, FamilyIDs: append([]string(nil), profile.profile.Families...),
			RulesetVersion: registry.document.RulesetVersion, Patch: registry.document.Patch,
			ChampionID: championID, Role: role, ChampionTag: tag,
			Fallback: profile.profile.Kind != KindChampionRole, ConfidenceCap: confidenceCap(profile.profile.Kind),
		},
		Warnings:         append([]Warning(nil), warnings...),
		Status:           Status{Ready: true, Version: registry.document.RulesetVersion, Patch: registry.document.Patch, Source: SourceExternalExact},
		AlternativeCount: registry.document.AlternativeCount,
		Phases:           registry.document.Phases, Context: cloneContext(registry.document.Context), Stability: registry.document.Stability,
		ChampionTraits: cloneTraits(registry.document.ChampionTraits), ItemTraits: cloneItemTraits(registry.document.ItemTraits),
	}
}

func confidenceCap(kind Kind) float64 {
	switch kind {
	case KindChampionRole:
		return ConfidenceChampionRole
	case KindChampionDefault:
		return ConfidenceChampionDefault
	case KindRoleArchetype:
		return ConfidenceRoleArchetype
	case KindTagArchetype:
		return ConfidenceTagArchetype
	default:
		return ConfidenceNeutral
	}
}

func warning(code string) Warning {
	messages := map[string]string{
		WarningChampionDefault:     "No hay un perfil para esta combinación de campeón y rol; se usa el perfil general del campeón.",
		WarningRoleArchetype:       "Se usa un arquetipo conservador para el rol y las etiquetas del campeón.",
		WarningTagArchetype:        "Se usa un arquetipo conservador basado en etiquetas de Data Dragon; no representa autoridad del meta.",
		WarningNeutralFallback:     "No hay un perfil compatible; solo se consideran respuestas defensivas activadas por presión explícita.",
		WarningRoleUnavailable:     "El rol no está disponible y no se inferirá a partir del campeón.",
		WarningChampionUnresolved:  "El campeón no pudo resolverse contra el catálogo activo.",
		WarningInsufficientContext: "No hay contexto suficiente para ofrecer un objetivo y dos alternativas distintas.",
		WarningPatchFallback:       "No hay un perfil exacto para el parche activo; se usa el perfil compatible más reciente de la misma versión mayor y menor.",
		WarningLastKnownGood:       "La recarga del perfil falló; se conserva el último perfil compatible validado.",
	}
	return Warning{Code: code, Message: messages[code]}
}

func applyStatus(resolution Resolution, status Status) Resolution {
	resolution.Status = status
	if !status.Fallback {
		return resolution
	}
	resolution.Provenance.Fallback = true
	switch status.Source {
	case SourceExternalPatchFallback:
		resolution.Warnings = appendWarning(resolution.Warnings, WarningPatchFallback)
	case SourceExternalLastKnownGood:
		resolution.Warnings = appendWarning(resolution.Warnings, WarningLastKnownGood)
	}
	return resolution
}

func appendWarning(values []Warning, code string) []Warning {
	for _, value := range values {
		if value.Code == code {
			return values
		}
	}
	return append(values, warning(code))
}

func cloneCandidateRule(source itemrules.CandidateRule) itemrules.CandidateRule {
	clone := source
	clone.Covers = append([]itemrules.Signal(nil), source.Covers...)
	clone.Adjustments = append([]itemrules.ScoreAdjustment(nil), source.Adjustments...)
	clone.PhaseScores = make(map[domain.Phase]int, len(source.PhaseScores))
	for key, value := range source.PhaseScores {
		clone.PhaseScores[key] = value
	}
	clone.StrategyScores = make(map[domain.Strategy]int, len(source.StrategyScores))
	for key, value := range source.StrategyScores {
		clone.StrategyScores[key] = value
	}
	return clone
}

func cloneCandidates(source []Candidate) []Candidate {
	result := make([]Candidate, len(source))
	for index, candidate := range source {
		result[index] = Candidate{Rule: cloneCandidateRule(candidate.Rule), Activation: append([]Activation(nil), candidate.Activation...)}
	}
	return result
}

func cloneProfile(source Profile) Profile {
	clone := source
	clone.Tags = append([]string(nil), source.Tags...)
	clone.ExcludedTags = append([]string(nil), source.ExcludedTags...)
	clone.Families = append([]string(nil), source.Families...)
	clone.Runes = clonePlanCopy(source.Runes)
	clone.Summoners = clonePlanCopy(source.Summoners)
	clone.InitialPlan = clonePlanCopy(source.InitialPlan)
	return clone
}

func clonePlanCopy(source PlanCopy) PlanCopy {
	return PlanCopy{Summary: source.Summary, Source: source.Source, Options: append([]PlanOption(nil), source.Options...)}
}

func cloneContext(source itemrules.ContextTuning) itemrules.ContextTuning {
	clone := source
	clone.LaneWeightByPhase = make(map[domain.Phase]int, len(source.LaneWeightByPhase))
	for key, value := range source.LaneWeightByPhase {
		clone.LaneWeightByPhase[key] = value
	}
	return clone
}

func cloneTraits(source map[string]itemrules.TraitProfile) map[string]itemrules.TraitProfile {
	clone := make(map[string]itemrules.TraitProfile, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func cloneItemTraits(source map[int]itemrules.TraitProfile) map[int]itemrules.TraitProfile {
	clone := make(map[int]itemrules.TraitProfile, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func containsFold(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(wanted)) {
			return true
		}
	}
	return false
}

func anyTag(left, right []string) bool {
	for _, value := range left {
		if containsFold(right, value) {
			return true
		}
	}
	return false
}

func compatibleTags(championTags, required, excluded []string) bool {
	return anyTag(championTags, required) && !anyTag(championTags, excluded)
}

func matchedTag(left, right []string) string {
	for _, value := range left {
		if containsFold(right, value) {
			return value
		}
	}
	return ""
}
