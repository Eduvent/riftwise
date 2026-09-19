package riotapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"lolengine/internal/domain"
)

type rawMatch struct {
	Metadata struct {
		MatchID      string   `json:"matchId"`
		Participants []string `json:"participants"`
	} `json:"metadata"`
	Info struct {
		GameCreation       int64            `json:"gameCreation"`
		GameStartTimestamp int64            `json:"gameStartTimestamp"`
		GameEndTimestamp   int64            `json:"gameEndTimestamp"`
		GameDuration       int64            `json:"gameDuration"`
		GameVersion        string           `json:"gameVersion"`
		QueueID            int              `json:"queueId"`
		MapID              int              `json:"mapId"`
		PlatformID         string           `json:"platformId"`
		Participants       []rawParticipant `json:"participants"`
	} `json:"info"`
}

type rawParticipant struct {
	PUUID              string `json:"puuid"`
	ChampionName       string `json:"championName"`
	TeamID             int    `json:"teamId"`
	Win                bool   `json:"win"`
	Kills              int    `json:"kills"`
	Deaths             int    `json:"deaths"`
	Assists            int    `json:"assists"`
	TotalMinionsKilled int    `json:"totalMinionsKilled"`
	NeutralMinions     int    `json:"neutralMinionsKilled"`
	TeamPosition       string `json:"teamPosition"`
	IndividualPosition string `json:"individualPosition"`
	Lane               string `json:"lane"`
	Role               string `json:"role"`
	Item0              int    `json:"item0"`
	Item1              int    `json:"item1"`
	Item2              int    `json:"item2"`
	Item3              int    `json:"item3"`
	Item4              int    `json:"item4"`
	Item5              int    `json:"item5"`
	Item6              int    `json:"item6"`
}

// NormalizeMatch validates and reduces a Match-V5 body to the linked player.
func NormalizeMatch(payload []byte, requestedMatchID, linkedPUUID string) (Match, error) {
	if !json.Valid(payload) {
		return Match{}, errors.New("invalid JSON")
	}
	var raw rawMatch
	if err := json.Unmarshal(payload, &raw); err != nil {
		return Match{}, err
	}
	if raw.Metadata.MatchID != requestedMatchID || !validMatchID(raw.Metadata.MatchID) {
		return Match{}, errors.New("match ID mismatch")
	}
	prefixPlatform, prefixOK := ParsePlatform(strings.SplitN(raw.Metadata.MatchID, "_", 2)[0])
	if !prefixOK {
		return Match{}, errors.New("unknown match ID platform")
	}
	platform, ok := ParsePlatform(raw.Info.PlatformID)
	if !ok {
		platform = prefixPlatform
	} else if platform != prefixPlatform {
		return Match{}, errors.New("platform does not match match ID")
	}
	participantIndex := -1
	metadataLinked := false
	for _, participantPUUID := range raw.Metadata.Participants {
		if participantPUUID == linkedPUUID {
			metadataLinked = true
			break
		}
	}
	if !metadataLinked {
		return Match{}, errors.New("linked PUUID not present in metadata")
	}
	for index := range raw.Info.Participants {
		if raw.Info.Participants[index].PUUID == linkedPUUID {
			if participantIndex >= 0 {
				return Match{}, errors.New("linked PUUID appears more than once")
			}
			participantIndex = index
		}
	}
	if participantIndex < 0 {
		return Match{}, errors.New("linked PUUID not present")
	}
	participant := raw.Info.Participants[participantIndex]
	role, roleSource := participantRole(participant)
	duration := normalizedDuration(raw.Info.GameVersion, raw.Info.GameDuration)
	if duration <= 0 {
		return Match{}, errors.New("invalid duration")
	}
	startMillis := raw.Info.GameStartTimestamp
	if startMillis <= 0 {
		startMillis = raw.Info.GameCreation
	}
	if startMillis <= 0 {
		return Match{}, errors.New("missing start time")
	}
	endMillis := raw.Info.GameEndTimestamp
	if endMillis <= 0 {
		endMillis = startMillis + duration*1000
	}
	if endMillis < startMillis {
		return Match{}, errors.New("end precedes start")
	}
	items := []int{participant.Item0, participant.Item1, participant.Item2, participant.Item3, participant.Item4, participant.Item5, participant.Item6}
	finalItems := make([]FinalItem, 0, 7)
	for slot, itemID := range items {
		if itemID > 0 {
			finalItems = append(finalItems, FinalItem{Slot: slot, ItemID: itemID})
		}
	}
	opponent, opponentSource := uniqueSameRoleOpponent(raw.Info.Participants, participantIndex, role)
	patch := normalizePatch(raw.Info.GameVersion)
	if patch == "" || strings.TrimSpace(participant.ChampionName) == "" {
		return Match{}, errors.New("missing normalized facts")
	}
	match := Match{
		ID: requestedMatchID, Platform: platform, QueueID: raw.Info.QueueID, MapID: raw.Info.MapID,
		Patch: patch, StartedAt: time.UnixMilli(startMillis).UTC(), EndedAt: time.UnixMilli(endMillis).UTC(), DurationSeconds: duration,
		Player: LinkedPlayerFacts{
			ChampionName: participant.ChampionName, Role: role, RoleProvenance: roleSource,
			TeamID: participant.TeamID, Win: participant.Win, Kills: participant.Kills,
			Deaths: participant.Deaths, Assists: participant.Assists,
			CreepScore: participant.TotalMinionsKilled + participant.NeutralMinions,
			FinalItems: finalItems, OpponentChampion: opponent, OpponentProvenance: opponentSource,
		},
	}
	canonical, err := json.Marshal(match)
	if err != nil {
		return Match{}, err
	}
	digest := sha256.Sum256(canonical)
	match.ContentHash = hex.EncodeToString(digest[:])
	return match, nil
}

func participantRole(participant rawParticipant) (domain.Role, RoleProvenance) {
	if role, ok := domain.ParseRole(participant.TeamPosition); ok {
		return role, RoleFromTeamPosition
	}
	if role, ok := domain.ParseRole(participant.IndividualPosition); ok {
		return role, RoleFromIndividualPosition
	}
	lane := strings.ToUpper(strings.TrimSpace(participant.Lane))
	playerRole := strings.ToUpper(strings.TrimSpace(participant.Role))
	if lane == "BOTTOM" && playerRole == "DUO_SUPPORT" {
		return domain.RoleSupport, RoleFromLaneRole
	}
	if lane == "BOTTOM" && (playerRole == "DUO_CARRY" || playerRole == "CARRY") {
		return domain.RoleBottom, RoleFromLaneRole
	}
	if role, ok := domain.ParseRole(lane); ok && (playerRole == "SOLO" || playerRole == "NONE" || playerRole == "") {
		return role, RoleFromLaneRole
	}
	return "", RoleUnknown
}

func uniqueSameRoleOpponent(participants []rawParticipant, linkedIndex int, linkedRole domain.Role) (string, OpponentProvenance) {
	if !linkedRole.Valid() || linkedIndex < 0 || linkedIndex >= len(participants) {
		return "", OpponentUnknown
	}
	teamID := participants[linkedIndex].TeamID
	var candidates []string
	for index, participant := range participants {
		if index == linkedIndex || participant.TeamID == teamID || strings.TrimSpace(participant.ChampionName) == "" {
			continue
		}
		role, _ := participantRole(participant)
		if role == linkedRole {
			candidates = append(candidates, participant.ChampionName)
		}
	}
	if len(candidates) != 1 {
		return "", OpponentUnknown
	}
	return candidates[0], OpponentUniqueSameRole
}

func normalizePatch(version string) string {
	parts := strings.Split(strings.TrimSpace(version), ".")
	if len(parts) < 2 {
		return ""
	}
	major, errMajor := strconv.Atoi(parts[0])
	minor, errMinor := strconv.Atoi(parts[1])
	if errMajor != nil || errMinor != nil || major < 1 || minor < 0 {
		return ""
	}
	return strconv.Itoa(major) + "." + strconv.Itoa(minor)
}

func normalizedDuration(version string, duration int64) int64 {
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return 0
	}
	major, errMajor := strconv.Atoi(parts[0])
	minor, errMinor := strconv.Atoi(parts[1])
	if errMajor != nil || errMinor != nil {
		return 0
	}
	if major < 11 || (major == 11 && minor < 20) {
		return duration / 1000
	}
	return duration
}

func sortedItemMultiset(items []FinalItem) []int {
	result := make([]int, 0, len(items))
	for _, item := range items {
		if item.ItemID > 0 && item.Slot != 6 {
			result = append(result, item.ItemID)
		}
	}
	sort.Ints(result)
	return result
}
