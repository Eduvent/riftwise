// Package statistics builds deterministic, observational aggregates from
// normalized Match-V5 facts. It does not infer purchase order, historical
// rank, or causal effects and does not mutate itemization rules.
package statistics

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"

	"lolengine/internal/domain"
	"lolengine/internal/riotapi"
	"lolengine/internal/storage"
)

const (
	RankUnknown     = "unknown"
	RankUnavailable = "unavailable"
)

const observationalNotice = "Observed historical results only; this is not a causal claim or a recommendation."
const maximumSourceMatches = 2000

var ErrInvalidQuery = errors.New("invalid historical statistics query")

type GroupKey struct {
	Patch              string                     `json:"patch"`
	ChampionName       string                     `json:"champion_name"`
	Role               domain.Role                `json:"role,omitempty"`
	RoleProvenance     riotapi.RoleProvenance     `json:"role_provenance"`
	RankBucket         string                     `json:"rank_bucket"`
	RankProvenance     string                     `json:"rank_provenance"`
	OpponentChampion   string                     `json:"opponent_champion,omitempty"`
	OpponentProvenance riotapi.OpponentProvenance `json:"opponent_provenance"`
	FinalItems         []int                      `json:"final_items"`
}

type Aggregate struct {
	Key                  GroupKey `json:"key"`
	Games                int      `json:"games"`
	Wins                 int      `json:"wins"`
	WinRate              float64  `json:"win_rate"`
	MinimumSampleWarning bool     `json:"minimum_sample_warning"`
}

type Report struct {
	TotalGames          int         `json:"total_games"`
	MinimumSample       int         `json:"minimum_sample"`
	ObservationalNotice string      `json:"observational_notice"`
	Groups              []Aggregate `json:"groups"`
	SourceTruncated     bool        `json:"source_truncated"`
}

type groupAccumulator struct {
	key   GroupKey
	games int
	wins  int
}

func Build(matches []storage.RiotStoredMatch, minimumSample int) Report {
	if minimumSample <= 0 {
		minimumSample = 10
	}
	groups := make(map[string]*groupAccumulator)
	seenMatches := make(map[string]struct{}, len(matches))
	for _, stored := range matches {
		match := stored.Match
		if strings.TrimSpace(match.ID) == "" {
			continue
		}
		if _, ok := seenMatches[match.ID]; ok {
			continue
		}
		seenMatches[match.ID] = struct{}{}
		key := normalizedKey(stored)
		serialized := sortableKey(key)
		group := groups[serialized]
		if group == nil {
			group = &groupAccumulator{key: key}
			groups[serialized] = group
		}
		group.games++
		if match.Player.Win {
			group.wins++
		}
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	report := Report{TotalGames: len(seenMatches), MinimumSample: minimumSample, ObservationalNotice: observationalNotice}
	for _, key := range keys {
		group := groups[key]
		aggregate := Aggregate{Key: group.key, Games: group.games, Wins: group.wins, MinimumSampleWarning: group.games < minimumSample}
		if group.games > 0 {
			aggregate.WinRate = float64(group.wins) / float64(group.games)
		}
		report.Groups = append(report.Groups, aggregate)
	}
	return report
}

func normalizedKey(stored storage.RiotStoredMatch) GroupKey {
	match := stored.Match
	rankBucket := strings.TrimSpace(stored.RankBucket)
	if rankBucket == "" {
		rankBucket = RankUnknown
	}
	rankProvenance := strings.TrimSpace(stored.RankProvenance)
	if rankProvenance == "" {
		rankProvenance = RankUnavailable
	}
	opponent := ""
	opponentSource := riotapi.OpponentUnknown
	if match.Player.OpponentProvenance == riotapi.OpponentUniqueSameRole {
		opponent = strings.TrimSpace(match.Player.OpponentChampion)
		if opponent != "" {
			opponentSource = riotapi.OpponentUniqueSameRole
		}
	}
	items := make([]int, 0, len(match.Player.FinalItems))
	for _, item := range match.Player.FinalItems {
		if item.ItemID > 0 && item.Slot != 6 {
			items = append(items, item.ItemID)
		}
	}
	sort.Ints(items)
	return GroupKey{
		Patch: match.Patch, ChampionName: match.Player.ChampionName,
		Role: match.Player.Role, RoleProvenance: match.Player.RoleProvenance,
		RankBucket: rankBucket, RankProvenance: rankProvenance,
		OpponentChampion: opponent, OpponentProvenance: opponentSource, FinalItems: items,
	}
}

func sortableKey(key GroupKey) string {
	parts := []string{key.Patch, key.ChampionName, string(key.Role), string(key.RoleProvenance), key.RankBucket, key.RankProvenance, key.OpponentChampion, string(key.OpponentProvenance)}
	for _, itemID := range key.FinalItems {
		parts = append(parts, strconv.Itoa(itemID))
	}
	return strings.Join(parts, "\x00")
}

type Repository interface {
	ListRiotMatches(context.Context, string, storage.RiotMatchFilter) ([]storage.RiotStoredMatch, error)
}

type Query struct {
	AccountID     string      `json:"account_id"`
	Patch         string      `json:"patch,omitempty"`
	ChampionName  string      `json:"champion_name,omitempty"`
	Role          domain.Role `json:"role,omitempty"`
	QueueID       int         `json:"queue_id,omitempty"`
	MapID         int         `json:"map_id,omitempty"`
	MinimumSample int         `json:"minimum_sample,omitempty"`
}

type Service struct {
	repository    Repository
	minimumSample int
}

func NewService(repository Repository, minimumSample int) *Service {
	if minimumSample <= 0 {
		minimumSample = 10
	}
	return &Service{repository: repository, minimumSample: minimumSample}
}

func (service *Service) Query(ctx context.Context, query Query) (Report, error) {
	if strings.TrimSpace(query.AccountID) == "" || len(query.AccountID) > 128 || len([]rune(query.ChampionName)) > 100 ||
		(query.Patch != "" && !validPatch(query.Patch)) || (query.Role != "" && !query.Role.Valid()) ||
		query.QueueID < 0 || query.MapID < 0 || query.MinimumSample < 0 || query.MinimumSample > 100 {
		return Report{}, ErrInvalidQuery
	}
	minimum := query.MinimumSample
	if minimum <= 0 {
		minimum = service.minimumSample
	}
	matches, err := service.repository.ListRiotMatches(ctx, query.AccountID, storage.RiotMatchFilter{
		Patch: query.Patch, Champion: query.ChampionName, Role: query.Role,
		QueueID: query.QueueID, MapID: query.MapID, Limit: maximumSourceMatches + 1,
	})
	if err != nil {
		return Report{}, err
	}
	truncated := len(matches) > maximumSourceMatches
	if truncated {
		matches = matches[:maximumSourceMatches]
	}
	report := Build(matches, minimum)
	report.SourceTruncated = truncated
	return report, nil
}

type CandidateObservation struct {
	AccountID       string      `json:"account_id"`
	Patch           string      `json:"patch"`
	ChampionName    string      `json:"champion_name"`
	Role            domain.Role `json:"role"`
	CandidateItemID int         `json:"candidate_item_id"`
}

type HistoricalBaseline struct {
	CandidateItemID      int     `json:"candidate_item_id"`
	Games                int     `json:"games"`
	Wins                 int     `json:"wins"`
	WinRate              float64 `json:"win_rate"`
	MinimumSampleWarning bool    `json:"minimum_sample_warning"`
	ObservationalNotice  string  `json:"observational_notice"`
	SourceTruncated      bool    `json:"source_truncated"`
}

type HistoricalBaselineProvider interface {
	Observe(context.Context, CandidateObservation) (HistoricalBaseline, error)
}

type Provider struct {
	repository    Repository
	minimumSample int
}

func NewProvider(repository Repository, minimumSample int) *Provider {
	if minimumSample <= 0 {
		minimumSample = 10
	}
	return &Provider{repository: repository, minimumSample: minimumSample}
}

func (provider *Provider) Observe(ctx context.Context, observation CandidateObservation) (HistoricalBaseline, error) {
	if strings.TrimSpace(observation.AccountID) == "" || !validPatch(observation.Patch) || strings.TrimSpace(observation.ChampionName) == "" ||
		!observation.Role.Valid() || observation.CandidateItemID <= 0 {
		return HistoricalBaseline{}, ErrInvalidQuery
	}
	matches, err := provider.repository.ListRiotMatches(ctx, observation.AccountID, storage.RiotMatchFilter{
		Patch: observation.Patch, Champion: observation.ChampionName, Role: observation.Role, Limit: maximumSourceMatches + 1,
	})
	if err != nil {
		return HistoricalBaseline{}, err
	}
	seen := make(map[string]struct{}, len(matches))
	baseline := HistoricalBaseline{CandidateItemID: observation.CandidateItemID, ObservationalNotice: observationalNotice}
	if len(matches) > maximumSourceMatches {
		matches = matches[:maximumSourceMatches]
		baseline.SourceTruncated = true
	}
	for _, stored := range matches {
		if _, ok := seen[stored.Match.ID]; ok {
			continue
		}
		contains := false
		for _, item := range stored.Match.Player.FinalItems {
			if item.Slot != 6 && item.ItemID == observation.CandidateItemID {
				contains = true
				break
			}
		}
		if !contains {
			continue
		}
		seen[stored.Match.ID] = struct{}{}
		baseline.Games++
		if stored.Match.Player.Win {
			baseline.Wins++
		}
	}
	if baseline.Games > 0 {
		baseline.WinRate = float64(baseline.Wins) / float64(baseline.Games)
	}
	baseline.MinimumSampleWarning = baseline.Games < provider.minimumSample
	return baseline, nil
}

var _ HistoricalBaselineProvider = (*Provider)(nil)

func validPatch(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 2 {
		return false
	}
	for index, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 || strconv.Itoa(number) != part || (index == 0 && number == 0) {
			return false
		}
	}
	return true
}
