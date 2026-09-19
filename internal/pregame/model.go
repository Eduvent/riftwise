// Package pregame provides pure manual planning models and scoring orchestration.
// It intentionally has no dependency on tracker snapshots, lifecycle state,
// persistence, decision events, or browser alert delivery.
package pregame

import (
	"fmt"
	"sort"
	"strings"

	"lolengine/internal/analysis"
	"lolengine/internal/catalog"
	"lolengine/internal/domain"
	"lolengine/internal/itemization"
	"lolengine/internal/itemprofiles"
	"lolengine/internal/itemrules"
	"lolengine/internal/personalization"
)

const (
	ModeManualPregame   = "manual_pregame"
	ManualConfidenceCap = 0.80
	MaximumInventory    = 7
	MaximumStackCount   = 99
)

type Stage string

const (
	StageLaneStart   Stage = "lane_start"
	StageFirstRecall Stage = "first_recall"
	StageMidGame     Stage = "mid_game"
	StageLateGame    Stage = "late_game"
)

func (stage Stage) Valid() bool {
	switch stage {
	case StageLaneStart, StageFirstRecall, StageMidGame, StageLateGame:
		return true
	default:
		return false
	}
}

func (stage Stage) Phase() domain.Phase {
	switch stage {
	case StageMidGame:
		return domain.PhaseMid
	case StageLateGame:
		return domain.PhaseLate
	default:
		return domain.PhaseEarly
	}
}

type Pressure string

const (
	PressureUnset  Pressure = "unset"
	PressureNone   Pressure = "none"
	PressureLow    Pressure = "low"
	PressureMedium Pressure = "medium"
	PressureHigh   Pressure = "high"
)

func (pressure Pressure) Valid() bool {
	switch pressure {
	case PressureUnset, PressureNone, PressureLow, PressureMedium, PressureHigh:
		return true
	default:
		return false
	}
}

func (pressure Pressure) Score() int {
	switch pressure {
	case PressureLow:
		return 300
	case PressureMedium:
		return 650
	case PressureHigh:
		return 1000
	default:
		return 0
	}
}

type Pressures struct {
	Physical     Pressure `json:"physical,omitempty"`
	Magic        Pressure `json:"magic,omitempty"`
	CrowdControl Pressure `json:"crowd_control,omitempty"`
	Healing      Pressure `json:"healing,omitempty"`
	Shielding    Pressure `json:"shielding,omitempty"`
	Armor        Pressure `json:"armor,omitempty"`
	MagicResist  Pressure `json:"magic_resist,omitempty"`
	Burst        Pressure `json:"burst,omitempty"`
	Sustained    Pressure `json:"sustained,omitempty"`
	Slows        Pressure `json:"slows,omitempty"`
	LanePressure Pressure `json:"lane_pressure,omitempty"`
}

func (pressures Pressures) values() []Pressure {
	return []Pressure{
		pressures.Physical, pressures.Magic, pressures.CrowdControl, pressures.Healing,
		pressures.Shielding, pressures.Armor, pressures.MagicResist, pressures.Burst,
		pressures.Sustained, pressures.Slows, pressures.LanePressure,
	}
}

func (pressures Pressures) entries() []pressureEntry {
	return []pressureEntry{
		{Path: "physical", Signal: itemrules.SignalPhysical, Value: pressures.Physical},
		{Path: "magic", Signal: itemrules.SignalMagic, Value: pressures.Magic},
		{Path: "crowd_control", Signal: itemrules.SignalCrowdControl, Value: pressures.CrowdControl},
		{Path: "healing", Signal: itemrules.SignalHealing, Value: pressures.Healing},
		{Path: "shielding", Signal: itemrules.SignalShielding, Value: pressures.Shielding},
		{Path: "armor", Signal: itemrules.SignalArmor, Value: pressures.Armor},
		{Path: "magic_resist", Signal: itemrules.SignalMagicResist, Value: pressures.MagicResist},
		{Path: "burst", Signal: itemrules.SignalBurst, Value: pressures.Burst},
		{Path: "sustained", Signal: itemrules.SignalSustained, Value: pressures.Sustained},
		{Path: "slows", Signal: itemrules.SignalSlows, Value: pressures.Slows},
		{Path: "lane_pressure", Signal: itemrules.SignalLanePressure, Value: pressures.LanePressure},
	}
}

type pressureEntry struct {
	Path   string
	Signal itemrules.Signal
	Value  Pressure
}

type InventoryStack struct {
	ItemID int `json:"item_id"`
	Count  int `json:"count"`
}

type Scenario struct {
	Champion     string           `json:"champion"`
	Role         domain.Role      `json:"role"`
	Stage        Stage            `json:"stage"`
	Strategy     domain.Strategy  `json:"strategy"`
	LaneOpponent string           `json:"lane_opponent,omitempty"`
	Pressures    Pressures        `json:"pressures,omitempty"`
	Gold         int              `json:"gold"`
	Inventory    []InventoryStack `json:"inventory,omitempty"`
}

type NormalizedInventoryStack struct {
	ItemID int    `json:"item_id"`
	Name   string `json:"name"`
	Count  int    `json:"count"`
}

type NormalizedScenario struct {
	ChampionID       string                     `json:"champion_id"`
	ChampionName     string                     `json:"champion_name"`
	Role             domain.Role                `json:"role"`
	Stage            Stage                      `json:"stage"`
	Strategy         domain.Strategy            `json:"strategy"`
	LaneOpponentID   string                     `json:"lane_opponent_id,omitempty"`
	LaneOpponentName string                     `json:"lane_opponent_name,omitempty"`
	Pressures        Pressures                  `json:"pressures"`
	Gold             int                        `json:"gold"`
	Inventory        []NormalizedInventoryStack `json:"inventory"`
}

type ValidationError struct {
	Fields map[string]string `json:"fields"`
}

func (validation *ValidationError) Error() string {
	if validation == nil || len(validation.Fields) == 0 {
		return "invalid manual pregame scenario"
	}
	keys := make([]string, 0, len(validation.Fields))
	for key := range validation.Fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return fmt.Sprintf("invalid manual pregame scenario field %s", keys[0])
}

type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

const (
	WarningOpponentAssumption    = "opponent_profile_assumption"
	WarningOpponentTraitsMissing = "opponent_traits_missing"
	WarningProfileFallback       = "manual_profile_fallback"
)

type Provenance struct {
	Profile             itemprofiles.Provenance `json:"profile"`
	ProfileStatus       itemprofiles.Status     `json:"profile_status"`
	Rules               *itemrules.Status       `json:"rules,omitempty"`
	ManualConfidenceCap float64                 `json:"manual_confidence_cap"`
	ContextConfidence   float64                 `json:"context_confidence"`
	TraitConfidence     float64                 `json:"trait_confidence"`
}

type Plan struct {
	Mode               string                   `json:"mode"`
	Status             itemization.Status       `json:"status"`
	GeneratedAt        string                   `json:"generated_at"`
	Patch              string                   `json:"patch"`
	Scenario           NormalizedScenario       `json:"scenario"`
	Phase              domain.Phase             `json:"phase"`
	Provenance         Provenance               `json:"provenance"`
	Confidence         float64                  `json:"confidence"`
	Needs              analysis.NeedScores      `json:"needs"`
	TargetItem         *itemization.ItemRef     `json:"target_item,omitempty"`
	NextComponent      *itemization.ItemRef     `json:"next_component,omitempty"`
	Alternative        *itemization.ItemRef     `json:"alternative,omitempty"`
	Alternatives       []itemization.RankedItem `json:"alternatives,omitempty"`
	RankedOptions      []itemization.RankedItem `json:"ranked_options,omitempty"`
	Reason             string                   `json:"reason"`
	GoldNeeded         int                      `json:"gold_needed"`
	InventorySlotsUsed int                      `json:"inventory_slots_used"`
	RunePlan           itemprofiles.PlanCopy    `json:"rune_plan"`
	SummonerPlan       itemprofiles.PlanCopy    `json:"summoner_plan"`
	InitialPlan        itemprofiles.PlanCopy    `json:"initial_plan"`
	Warnings           []Warning                `json:"warnings,omitempty"`
	Sources            []string                 `json:"sources"`
	Personalization    *personalization.Applied `json:"personalization,omitempty"`
}

type SelectOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type ChampionOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ItemOption struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type Options struct {
	Mode            string                 `json:"mode"`
	Patch           string                 `json:"patch"`
	Catalog         catalog.Status         `json:"catalog_status"`
	Profiles        itemprofiles.Status    `json:"profile_status"`
	Rules           itemrules.Status       `json:"rules_status"`
	Personalization personalization.Status `json:"personalization_status"`
	Champions       []ChampionOption       `json:"champions"`
	Items           []ItemOption           `json:"items"`
	Roles           []SelectOption         `json:"roles"`
	Stages          []SelectOption         `json:"stages"`
	Strategies      []SelectOption         `json:"strategies"`
	PressureLevels  []SelectOption         `json:"pressure_levels"`
}

func Normalize(source Scenario, knowledge catalog.Catalog) (NormalizedScenario, *ValidationError) {
	fields := make(map[string]string)
	championText := strings.TrimSpace(source.Champion)
	if championText == "" || len(championText) > 128 {
		fields["champion"] = "Selecciona un campeón válido del catálogo."
	}
	champion, championKnown := catalog.ResolveChampion(knowledge, championText)
	if championText != "" && !championKnown {
		fields["champion"] = "El campeón no existe en el catálogo activo."
	}
	if !source.Role.Valid() {
		fields["role"] = "Selecciona un rol canónico."
	}
	if !source.Stage.Valid() {
		fields["stage"] = "Selecciona una etapa válida."
	}
	if !source.Strategy.Valid() {
		fields["strategy"] = "Selecciona una estrategia válida."
	}

	laneText := strings.TrimSpace(source.LaneOpponent)
	var laneOpponent catalog.Champion
	if len(laneText) > 128 {
		fields["lane_opponent"] = "El oponente de línea no es válido."
	} else if laneText != "" {
		var laneKnown bool
		laneOpponent, laneKnown = catalog.ResolveChampion(knowledge, laneText)
		if !laneKnown {
			fields["lane_opponent"] = "El oponente de línea no existe en el catálogo activo."
		}
	}

	pressures := source.Pressures
	for _, entry := range pressures.entries() {
		value := entry.Value
		if value == "" {
			value = PressureUnset
			setPressure(&pressures, entry.Signal, value)
		}
		if !value.Valid() {
			fields["pressures."+entry.Path] = "Usa unset, none, low, medium o high."
		}
	}
	if source.Gold < 0 || source.Gold > 100000 {
		fields["gold"] = "El oro debe estar entre 0 y 100000."
	}
	if len(source.Inventory) > MaximumInventory {
		fields["inventory"] = "El inventario admite como máximo siete pilas."
	}
	seenItems := make(map[int]struct{}, len(source.Inventory))
	inventory := make([]NormalizedInventoryStack, 0, min(len(source.Inventory), MaximumInventory))
	for index, stack := range source.Inventory {
		path := fmt.Sprintf("inventory[%d]", index)
		item, exists := knowledge.Items[stack.ItemID]
		if !exists || !item.AvailableMap11 || strings.TrimSpace(item.Name) == "" {
			fields[path+".item_id"] = "Selecciona un objeto válido del catálogo para el mapa 11."
		} else if _, duplicate := seenItems[stack.ItemID]; duplicate {
			fields[path+".item_id"] = "Cada objeto solo puede aparecer en una pila."
		} else {
			seenItems[stack.ItemID] = struct{}{}
		}
		if stack.Count < 1 || stack.Count > MaximumStackCount {
			fields[path+".count"] = "La cantidad debe estar entre 1 y 99."
		}
		if exists && item.AvailableMap11 && strings.TrimSpace(item.Name) != "" && stack.Count >= 1 && stack.Count <= MaximumStackCount {
			inventory = append(inventory, NormalizedInventoryStack{ItemID: item.ID, Name: item.Name, Count: stack.Count})
		}
	}
	if len(fields) > 0 {
		return NormalizedScenario{}, &ValidationError{Fields: fields}
	}
	sort.Slice(inventory, func(left, right int) bool { return inventory[left].ItemID < inventory[right].ItemID })
	result := NormalizedScenario{
		ChampionID: champion.ID, ChampionName: champion.Name, Role: source.Role,
		Stage: source.Stage, Strategy: source.Strategy, Pressures: pressures,
		Gold: source.Gold, Inventory: inventory,
	}
	if laneText != "" {
		result.LaneOpponentID = laneOpponent.ID
		result.LaneOpponentName = laneOpponent.Name
	}
	return result, nil
}

func setPressure(pressures *Pressures, signal itemrules.Signal, value Pressure) {
	switch signal {
	case itemrules.SignalPhysical:
		pressures.Physical = value
	case itemrules.SignalMagic:
		pressures.Magic = value
	case itemrules.SignalCrowdControl:
		pressures.CrowdControl = value
	case itemrules.SignalHealing:
		pressures.Healing = value
	case itemrules.SignalShielding:
		pressures.Shielding = value
	case itemrules.SignalArmor:
		pressures.Armor = value
	case itemrules.SignalMagicResist:
		pressures.MagicResist = value
	case itemrules.SignalBurst:
		pressures.Burst = value
	case itemrules.SignalSustained:
		pressures.Sustained = value
	case itemrules.SignalSlows:
		pressures.Slows = value
	case itemrules.SignalLanePressure:
		pressures.LanePressure = value
	}
}
