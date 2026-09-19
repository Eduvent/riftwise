package analysis

import (
	"math"
	"strings"

	"lolengine/internal/catalog"
	"lolengine/internal/domain"
	"lolengine/internal/itemrules"
)

// NeedScores are deterministic basis-point-like values in the inclusive range 0..1000.
type NeedScores map[itemrules.Signal]int

// MatchContext is the complete, explicit input consumed by deterministic item scoring.
type MatchContext struct {
	Phase           domain.Phase    `json:"phase"`
	Strategy        domain.Strategy `json:"strategy"`
	PlayerState     PlayerState     `json:"player_state"`
	LaneOpponent    Threat          `json:"lane_opponent"`
	MainThreat      Threat          `json:"main_threat"`
	DamageMix       DamageMix       `json:"damage_mix"`
	Needs           NeedScores      `json:"needs"`
	Confidence      float64         `json:"confidence"`
	TraitConfidence float64         `json:"trait_confidence"`
	Warnings        []string        `json:"warnings,omitempty"`
}

// PhaseForTime classifies game time using rule-controlled boundaries.
func PhaseForTime(seconds float64, boundaries itemrules.PhaseBoundaries) domain.Phase {
	seconds = math.Max(seconds, 0)
	if seconds < float64(boundaries.EarlyEndSeconds) {
		return domain.PhaseEarly
	}
	if seconds < float64(boundaries.MidEndSeconds) {
		return domain.PhaseMid
	}
	return domain.PhaseLate
}

// BuildMatchContext combines live state, Data Dragon metadata, and structured rule traits.
func BuildMatchContext(game domain.GameState, knowledge catalog.Catalog, rules itemrules.RuleSet, strategy domain.Strategy) MatchContext {
	if !strategy.Valid() {
		strategy = domain.StrategyBalanced
	}
	phase := PhaseForTime(game.GameTimeSeconds, rules.Phases)
	analysisResult := AnalyzeEnemies(game, knowledge)
	result := MatchContext{
		Phase: phase, Strategy: strategy, PlayerState: ClassifyPlayerState(game, knowledge),
		MainThreat: analysisResult.MainThreat, DamageMix: analysisResult.DamageMix,
		Confidence: analysisResult.Confidence, Needs: make(NeedScores),
		Warnings: append([]string(nil), analysisResult.Warnings...),
	}
	enemies := enemyPlayers(game)
	if len(enemies) == 0 {
		result.TraitConfidence = 0.2
		setDamageNeeds(result.Needs, result.DamageMix)
		return result
	}
	lane := chooseOpponent(game.ActivePlayer, append([]domain.Player(nil), enemies...), knowledge)
	result.LaneOpponent = threatFor(lane, game.ActivePlayer.Level, knowledge)

	teamNeeds := make(NeedScores)
	teamWeight := 0.0
	missingTraits := 0
	trackMagicResist := rules.Context.MagicResistFullScale > 0
	for _, enemy := range enemies {
		weight, _ := threatScore(enemy, game.ActivePlayer.Level, knowledge)
		traits, known := structuredTraits(enemy, knowledge, rules)
		if !known {
			missingTraits++
		}
		addWeightedTraits(teamNeeds, traits, weight)
		teamNeeds[itemrules.SignalArmor] += int(math.Round(float64(armorNeed(enemy, knowledge, rules.Context.ArmorFullScale)) * weight))
		if trackMagicResist {
			teamNeeds[itemrules.SignalMagicResist] += int(math.Round(float64(magicResistNeed(enemy, knowledge, rules.Context.MagicResistFullScale)) * weight))
		}
		teamWeight += weight
	}
	if teamWeight > 0 {
		for _, signal := range contextSignals(trackMagicResist) {
			teamNeeds[signal] = clampNeed(int(math.Round(float64(teamNeeds[signal]) / teamWeight)))
		}
	}
	laneTraits, laneKnown := structuredTraits(lane, knowledge, rules)
	laneNeeds := make(NeedScores)
	setTraits(laneNeeds, laneTraits)
	laneNeeds[itemrules.SignalArmor] = armorNeed(lane, knowledge, rules.Context.ArmorFullScale)
	if trackMagicResist {
		laneNeeds[itemrules.SignalMagicResist] = magicResistNeed(lane, knowledge, rules.Context.MagicResistFullScale)
	}
	if !laneKnown && missingTraits == 0 {
		missingTraits++
	}

	laneWeight := rules.Context.LaneWeightByPhase[phase]
	for _, signal := range contextSignals(trackMagicResist) {
		result.Needs[signal] = blendNeed(teamNeeds[signal], laneNeeds[signal], laneWeight)
	}
	laneDamage, _, _ := championDamageProfile(lane, knowledge)
	teamPhysical := clampNeed(int(math.Round(result.DamageMix.Physical * 1000)))
	teamMagic := clampNeed(int(math.Round(result.DamageMix.Magic * 1000)))
	result.Needs[itemrules.SignalPhysical] = blendNeed(teamPhysical, int(math.Round(laneDamage.Physical*1000)), laneWeight)
	result.Needs[itemrules.SignalMagic] = blendNeed(teamMagic, int(math.Round(laneDamage.Magic*1000)), laneWeight)
	if result.MainThreat.Score > 0 {
		result.Needs[itemrules.SignalLanePressure] = clampNeed(int(math.Round(result.LaneOpponent.Score / result.MainThreat.Score * 1000)))
	}
	if result.PlayerState == PlayerAhead {
		result.Needs[itemrules.SignalAhead] = 1000
	}
	if result.PlayerState == PlayerBehind {
		result.Needs[itemrules.SignalBehind] = 1000
	}

	missingFraction := float64(missingTraits) / float64(len(enemies))
	penalty := missingFraction * float64(rules.Context.MissingTraitPenalty) / 1000
	result.TraitConfidence = round(clamp(1-penalty, 0.2, 1), 3)
	if missingTraits > 0 {
		result.TraitConfidence = math.Min(result.TraitConfidence, 0.99)
	}
	result.Confidence = round(math.Min(result.Confidence, result.TraitConfidence), 3)
	if missingTraits > 0 {
		result.Warnings = append(result.Warnings, "Uno o más enemigos no tienen un perfil estructurado para este parche; las necesidades situacionales tienen menor confianza.")
	}
	return result
}

func setDamageNeeds(needs NeedScores, mix DamageMix) {
	needs[itemrules.SignalPhysical] = clampNeed(int(math.Round(mix.Physical * 1000)))
	needs[itemrules.SignalMagic] = clampNeed(int(math.Round(mix.Magic * 1000)))
}

func structuredTraits(player domain.Player, knowledge catalog.Catalog, rules itemrules.RuleSet) (itemrules.TraitProfile, bool) {
	traits, known := championTraits(rules.ChampionTraits, player.ChampionName, knowledge)
	for _, owned := range player.Items {
		if itemTraits, exists := rules.ItemTraits[owned.ID]; exists {
			count := max(owned.Count, 1)
			for range count {
				traits = addTraits(traits, itemTraits)
			}
		}
	}
	return traits, known
}

func championTraits(profiles map[string]itemrules.TraitProfile, champion string, knowledge catalog.Catalog) (itemrules.TraitProfile, bool) {
	canonical := canonicalChampionID(knowledge, champion)
	if value, exists := profiles[canonical]; exists {
		return value, true
	}
	for name, value := range profiles {
		if strings.EqualFold(canonicalChampionID(knowledge, name), canonical) {
			return value, true
		}
	}
	return itemrules.TraitProfile{}, false
}

func canonicalChampionID(knowledge catalog.Catalog, identity string) string {
	champion, exists := catalog.ResolveChampion(knowledge, identity)
	if exists && strings.TrimSpace(champion.ID) != "" {
		return champion.ID
	}
	return strings.TrimSpace(identity)
}

func addTraits(left, right itemrules.TraitProfile) itemrules.TraitProfile {
	return itemrules.TraitProfile{
		Healing: clampNeed(left.Healing + right.Healing), Shielding: clampNeed(left.Shielding + right.Shielding),
		CrowdControl: clampNeed(left.CrowdControl + right.CrowdControl), Slows: clampNeed(left.Slows + right.Slows),
		Burst: clampNeed(left.Burst + right.Burst), Sustained: clampNeed(left.Sustained + right.Sustained),
	}
}

func addWeightedTraits(needs NeedScores, traits itemrules.TraitProfile, weight float64) {
	for signal, value := range traitsAsNeeds(traits) {
		needs[signal] += int(math.Round(float64(value) * weight))
	}
}

func setTraits(needs NeedScores, traits itemrules.TraitProfile) {
	for signal, value := range traitsAsNeeds(traits) {
		needs[signal] = value
	}
}

func traitsAsNeeds(traits itemrules.TraitProfile) NeedScores {
	return NeedScores{
		itemrules.SignalHealing: traits.Healing, itemrules.SignalShielding: traits.Shielding,
		itemrules.SignalCrowdControl: traits.CrowdControl, itemrules.SignalSlows: traits.Slows,
		itemrules.SignalBurst: traits.Burst, itemrules.SignalSustained: traits.Sustained,
	}
}

func traitSignals() []itemrules.Signal {
	return []itemrules.Signal{
		itemrules.SignalHealing, itemrules.SignalShielding, itemrules.SignalCrowdControl,
		itemrules.SignalSlows, itemrules.SignalBurst, itemrules.SignalSustained,
	}
}

func contextSignals(includeMagicResist bool) []itemrules.Signal {
	result := append(traitSignals(), itemrules.SignalArmor)
	if includeMagicResist {
		result = append(result, itemrules.SignalMagicResist)
	}
	return result
}

func armorNeed(player domain.Player, knowledge catalog.Catalog, fullScale int) int {
	return defensiveStatNeed(player, knowledge, fullScale, func(stat string) bool {
		return strings.Contains(stat, "armor") && !strings.Contains(stat, "penetr")
	})
}

func magicResistNeed(player domain.Player, knowledge catalog.Catalog, fullScale int) int {
	return defensiveStatNeed(player, knowledge, fullScale, func(stat string) bool {
		return (strings.Contains(stat, "spellblock") || strings.Contains(stat, "magicresist")) && !strings.Contains(stat, "penetr")
	})
}

func defensiveStatNeed(player domain.Player, knowledge catalog.Catalog, fullScale int, matches func(string) bool) int {
	if fullScale <= 0 {
		return 0
	}
	total := 0.0
	for _, owned := range player.Items {
		item, exists := knowledge.Items[owned.ID]
		if !exists {
			continue
		}
		for key, value := range item.Stats {
			lower := strings.ToLower(key)
			if value > 0 && matches(lower) {
				total += value * float64(max(owned.Count, 1))
			}
		}
	}
	return clampNeed(int(math.Round(total / float64(fullScale) * 1000)))
}

func blendNeed(team, lane, laneWeight int) int {
	laneWeight = clampNeed(laneWeight)
	return clampNeed((team*(1000-laneWeight) + lane*laneWeight + 500) / 1000)
}

func clampNeed(value int) int {
	return min(max(value, 0), 1000)
}

func threatFor(player domain.Player, activeLevel int, knowledge catalog.Catalog) Threat {
	score, inventory := threatScore(player, activeLevel, knowledge)
	return Threat{
		RiotID: player.Identity.RiotID, ChampionName: player.ChampionName, Score: round(score, 3),
		Level: player.Level, Kills: player.Score.Kills, Deaths: player.Score.Deaths,
		Assists: player.Score.Assists, CreepScore: player.Score.CreepScore, InventoryValue: inventory,
	}
}
