package analysis

import (
	"math"
	"sort"
	"strings"

	"lolengine/internal/catalog"
	"lolengine/internal/domain"
)

// PlayerState is Garen's deterministic economic/combat position.
type PlayerState string

const (
	PlayerAhead  PlayerState = "ahead"
	PlayerEven   PlayerState = "even"
	PlayerBehind PlayerState = "behind"
)

// DamageMix is normalized and always sums to one when analysis has opponents.
type DamageMix struct {
	Physical float64 `json:"physical"`
	Magic    float64 `json:"magic"`
	True     float64 `json:"true"`
}

// Threat describes the enemy currently contributing most to the weighted mix.
type Threat struct {
	RiotID         string  `json:"riot_id,omitempty"`
	ChampionName   string  `json:"champion_name"`
	Score          float64 `json:"score"`
	Level          int     `json:"level"`
	Kills          int     `json:"kills"`
	Deaths         int     `json:"deaths"`
	Assists        int     `json:"assists"`
	CreepScore     int     `json:"creep_score"`
	InventoryValue int     `json:"inventory_value"`
}

// Result is deterministic analysis suitable for direct itemization input.
type Result struct {
	DamageMix  DamageMix `json:"damage_mix"`
	MainThreat Threat    `json:"main_threat"`
	Confidence float64   `json:"confidence"`
	Warnings   []string  `json:"warnings,omitempty"`
}

// AnalyzeEnemies combines champion text/tags, offensive item stats, and live threat weighting.
func AnalyzeEnemies(game domain.GameState, knowledge catalog.Catalog) Result {
	enemies := enemyPlayers(game)
	if len(enemies) == 0 {
		return Result{
			DamageMix:  DamageMix{Physical: 0.45, Magic: 0.45, True: 0.10},
			Confidence: 0.20,
			Warnings:   []string{"No se detectaron enemigos; se usa una mezcla neutral."},
		}
	}

	var physical, magicDamage, trueDamage, totalWeight, confidenceWeight float64
	var main Threat
	unknown := 0
	for _, enemy := range enemies {
		profile, profileConfidence, known := championDamageProfile(enemy, knowledge)
		if !known {
			unknown++
		}
		weight, inventory := threatScore(enemy, game.ActivePlayer.Level, knowledge)
		physical += profile.Physical * weight
		magicDamage += profile.Magic * weight
		trueDamage += profile.True * weight
		confidenceWeight += profileConfidence * weight
		totalWeight += weight
		if weight > main.Score {
			main = Threat{
				RiotID: enemy.Identity.RiotID, ChampionName: enemy.ChampionName, Score: round(weight, 3),
				Level: enemy.Level, Kills: enemy.Score.Kills, Deaths: enemy.Score.Deaths,
				Assists: enemy.Score.Assists, CreepScore: enemy.Score.CreepScore, InventoryValue: inventory,
			}
		}
	}
	mix := normalizeMix(physical, magicDamage, trueDamage)
	result := Result{
		DamageMix:  mix,
		MainThreat: main,
		Confidence: round(clamp(confidenceWeight/totalWeight, 0.20, 0.95), 3),
	}
	if unknown > 0 {
		result.Warnings = append(result.Warnings, "Uno o más campeones no tienen un perfil inequívoco; se aplicó una mezcla neutral.")
	}
	return result
}

// ClassifyPlayerState compares the active player with the most relevant explicit
// lane opponent, or the deterministic main threat when lane roles are absent.
// Score = 1.5*level delta + 0.35*KDA contribution delta + CS delta/30 + spendable-value delta/1000.
// Scores >= 2 are ahead, <= -2 are behind, and the middle interval is even.
func ClassifyPlayerState(game domain.GameState, knowledge catalog.Catalog) PlayerState {
	enemies := enemyPlayers(game)
	if len(enemies) == 0 {
		return PlayerEven
	}
	opponent := chooseOpponent(game.ActivePlayer, enemies, knowledge)
	playerValue := inventoryValue(game.ActivePlayer.Items, knowledge) + int(math.Floor(game.ActivePlayer.CurrentGold))
	opponentValue := inventoryValue(opponent.Items, knowledge)
	delta := 1.5*float64(game.ActivePlayer.Level-opponent.Level) +
		0.35*(kdaContribution(game.ActivePlayer.Score)-kdaContribution(opponent.Score)) +
		float64(game.ActivePlayer.Score.CreepScore-opponent.Score.CreepScore)/30.0 +
		float64(playerValue-opponentValue)/1000.0
	if delta >= 2.0 {
		return PlayerAhead
	}
	if delta <= -2.0 {
		return PlayerBehind
	}
	return PlayerEven
}

func championDamageProfile(player domain.Player, knowledge catalog.Catalog) (DamageMix, float64, bool) {
	champion, known := findChampion(knowledge, player.ChampionName)
	if !known {
		return applyItemBias(DamageMix{Physical: 0.45, Magic: 0.45, True: 0.10}, player.Items, knowledge), 0.30, false
	}
	prior, confidence := ChampionDamageEstimate(champion)
	return applyItemBias(prior, player.Items, knowledge), confidence, true
}

// ChampionDamageEstimate returns the catalog-only damage prior used when a
// caller has a known champion but no live enemy inventory or combat state.
// It does not invent KDA, gold, team composition, or matchup outcomes.
func ChampionDamageEstimate(champion catalog.Champion) (DamageMix, float64) {

	profiles := make([]DamageMix, 0, len(champion.Tags))
	for _, tag := range champion.Tags {
		switch strings.ToLower(tag) {
		case "mage":
			profiles = append(profiles, DamageMix{Physical: 0.15, Magic: 0.78, True: 0.07})
		case "marksman":
			profiles = append(profiles, DamageMix{Physical: 0.82, Magic: 0.14, True: 0.04})
		case "assassin":
			profiles = append(profiles, DamageMix{Physical: 0.63, Magic: 0.32, True: 0.05})
		case "fighter":
			profiles = append(profiles, DamageMix{Physical: 0.67, Magic: 0.24, True: 0.09})
		case "tank":
			profiles = append(profiles, DamageMix{Physical: 0.38, Magic: 0.47, True: 0.15})
		case "support":
			profiles = append(profiles, DamageMix{Physical: 0.27, Magic: 0.60, True: 0.13})
		}
	}
	prior := averageProfiles(profiles)
	if len(profiles) == 0 {
		prior = DamageMix{Physical: 0.45, Magic: 0.45, True: 0.10}
	}

	physicalEvidence, magicEvidence, trueEvidence := damageTextEvidence(champion)
	totalEvidence := physicalEvidence + magicEvidence + trueEvidence
	confidence := 0.55
	if totalEvidence > 0 {
		evidence := normalizeMix(physicalEvidence, magicEvidence, trueEvidence)
		prior = normalizeMix(
			prior.Physical*0.55+evidence.Physical*0.45,
			prior.Magic*0.55+evidence.Magic*0.45,
			prior.True*0.55+evidence.True*0.45,
		)
		confidence = clamp(0.65+0.04*totalEvidence, 0.65, 0.90)
	}
	return prior, confidence
}

func damageTextEvidence(champion catalog.Champion) (float64, float64, float64) {
	texts := []string{champion.Passive.Description, champion.Passive.Tooltip}
	for _, spell := range champion.Spells {
		texts = append(texts, spell.Description, spell.Tooltip)
	}
	joined := strings.ToLower(strings.Join(texts, " "))
	physical := countAny(joined, "<physicaldamage>", "physical damage", "physicaldamage")
	magicDamage := countAny(joined, "<magicdamage>", "magic damage", "magicdamage")
	trueDamage := countAny(joined, "<truedamage>", "true damage", "truedamage")
	return float64(physical), float64(magicDamage), float64(trueDamage)
}

func countAny(text string, needles ...string) int {
	count := 0
	for _, needle := range needles {
		count += strings.Count(text, needle)
	}
	return count
}

func applyItemBias(profile DamageMix, items []domain.Item, knowledge catalog.Catalog) DamageMix {
	physical, magicDamage := 0.0, 0.0
	for _, owned := range items {
		item, exists := knowledge.Items[owned.ID]
		if !exists {
			continue
		}
		for key, value := range item.Stats {
			if value <= 0 {
				continue
			}
			lower := strings.ToLower(key)
			switch {
			case strings.Contains(lower, "physicaldamagemod"), strings.Contains(lower, "attackdamage"):
				physical += value / 100.0
			case strings.Contains(lower, "magicdamagemod"), strings.Contains(lower, "abilitypower"):
				magicDamage += value / 100.0
			case strings.Contains(lower, "crit"), strings.Contains(lower, "attackspeed"):
				physical += value / 250.0
			}
		}
	}
	return normalizeMix(profile.Physical+physical, profile.Magic+magicDamage, profile.True)
}

func threatScore(player domain.Player, activeLevel int, knowledge catalog.Catalog) (float64, int) {
	value := inventoryValue(player.Items, knowledge)
	completed := 0
	for _, owned := range player.Items {
		item, exists := knowledge.Items[owned.ID]
		if exists && len(item.Into) == 0 && item.Gold.Total >= 1000 {
			completed += max(owned.Count, 1)
		}
	}
	score := 1.0 + 0.12*float64(player.Level) +
		0.75*float64(max(player.Score.Kills, 0)) +
		0.25*float64(max(player.Score.Assists, 0)) -
		0.15*float64(max(player.Score.Deaths, 0)) +
		0.003*float64(max(player.Score.CreepScore, 0)) +
		0.45*float64(completed) +
		float64(value)/5000.0 +
		0.35*float64(max(player.Level-activeLevel, 0))
	return math.Max(score, 0.25), value
}

func chooseOpponent(active domain.ActivePlayer, enemies []domain.Player, knowledge catalog.Catalog) domain.Player {
	candidates := enemies
	if activeRole, ok := domain.ParseRole(active.Position); ok {
		matching := make([]domain.Player, 0, len(enemies))
		for _, enemy := range enemies {
			enemyRole, valid := domain.ParseRole(enemy.Position)
			if valid && enemyRole == activeRole {
				matching = append(matching, enemy)
			}
		}
		if len(matching) > 0 {
			candidates = matching
		}
	}
	sort.SliceStable(candidates, func(left, right int) bool {
		leftScore, _ := threatScore(candidates[left], active.Level, knowledge)
		rightScore, _ := threatScore(candidates[right], active.Level, knowledge)
		return leftScore > rightScore
	})
	return candidates[0]
}

func enemyPlayers(game domain.GameState) []domain.Player {
	enemies := make([]domain.Player, 0, len(game.Players))
	for _, player := range game.Players {
		if game.ActivePlayer.Team != "" && strings.EqualFold(player.Team, game.ActivePlayer.Team) {
			continue
		}
		if identitiesEqual(player.Identity, game.ActivePlayer.Identity) {
			continue
		}
		enemies = append(enemies, player)
	}
	return enemies
}

func identitiesEqual(left, right domain.PlayerIdentity) bool {
	if left.RiotID != "" && right.RiotID != "" {
		return strings.EqualFold(left.RiotID, right.RiotID)
	}
	return left.SummonerName != "" && right.SummonerName != "" && strings.EqualFold(left.SummonerName, right.SummonerName)
}

func findChampion(knowledge catalog.Catalog, name string) (catalog.Champion, bool) {
	return catalog.ResolveChampion(knowledge, name)
}

func inventoryValue(items []domain.Item, knowledge catalog.Catalog) int {
	total := 0
	for _, owned := range items {
		if item, exists := knowledge.Items[owned.ID]; exists {
			total += item.Gold.Total * max(owned.Count, 1)
		}
	}
	return total
}

func kdaContribution(score domain.Score) float64 {
	return float64(score.Kills) + 0.5*float64(score.Assists) - 0.75*float64(score.Deaths)
}

func averageProfiles(profiles []DamageMix) DamageMix {
	if len(profiles) == 0 {
		return DamageMix{}
	}
	var physical, magicDamage, trueDamage float64
	for _, profile := range profiles {
		physical += profile.Physical
		magicDamage += profile.Magic
		trueDamage += profile.True
	}
	return normalizeMix(physical, magicDamage, trueDamage)
}

func normalizeMix(physical, magicDamage, trueDamage float64) DamageMix {
	total := physical + magicDamage + trueDamage
	if total <= 0 {
		return DamageMix{Physical: 0.45, Magic: 0.45, True: 0.10}
	}
	physical = physical / total
	magicDamage = magicDamage / total
	trueDamage = 1 - physical - magicDamage
	return DamageMix{Physical: round(physical, 4), Magic: round(magicDamage, 4), True: round(1-round(physical, 4)-round(magicDamage, 4), 4)}
}

func clamp(value, minimum, maximum float64) float64 {
	return math.Min(math.Max(value, minimum), maximum)
}

func round(value float64, places int) float64 {
	factor := math.Pow10(places)
	return math.Round(value*factor) / factor
}
