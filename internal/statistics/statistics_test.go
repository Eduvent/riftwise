package statistics

import (
	"context"
	"reflect"
	"testing"
	"time"

	"lolengine/internal/domain"
	"lolengine/internal/riotapi"
	"lolengine/internal/storage"
)

func TestBuildGroupsExactPatchRoleProvenanceUnknownRankAndItemMultiset(t *testing.T) {
	base := time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)
	first := statisticalMatch("NA1_1", base, true, []riotapi.FinalItem{{Slot: 0, ItemID: 3078}, {Slot: 1, ItemID: 3006}, {Slot: 6, ItemID: 3340}})
	second := statisticalMatch("NA1_2", base.Add(time.Hour), false, []riotapi.FinalItem{{Slot: 4, ItemID: 3006}, {Slot: 2, ItemID: 3078}, {Slot: 6, ItemID: 3363}})
	third := statisticalMatch("NA1_3", base.Add(2*time.Hour), true, []riotapi.FinalItem{{Slot: 0, ItemID: 3078}})
	third.Match.Patch = "16.19"
	// Duplicate input cannot double-count one official match.
	report := Build([]storage.RiotStoredMatch{second, first, first, third}, 3)
	if report.TotalGames != 3 || len(report.Groups) != 2 {
		t.Fatalf("report = %#v", report)
	}
	group := report.Groups[0]
	if group.Key.Patch != "16.18" || group.Key.Role != domain.RoleTop || group.Key.RoleProvenance != riotapi.RoleFromTeamPosition || group.Key.RankBucket != RankUnknown || group.Key.RankProvenance != RankUnavailable {
		t.Fatalf("key = %#v", group.Key)
	}
	if !reflect.DeepEqual(group.Key.FinalItems, []int{3006, 3078}) || group.Games != 2 || group.Wins != 1 || group.WinRate != .5 || !group.MinimumSampleWarning {
		t.Fatalf("group = %#v", group)
	}
	if group.Key.OpponentChampion != "Darius" || group.Key.OpponentProvenance != riotapi.OpponentUniqueSameRole {
		t.Fatalf("matchup = %#v", group.Key)
	}
	if report.ObservationalNotice == "" {
		t.Fatal("missing observational notice")
	}
}

func TestBuildNeverLabelsAmbiguousOpponentMatchup(t *testing.T) {
	value := statisticalMatch("NA1_1", time.Now(), true, nil)
	value.Match.Player.OpponentChampion = "Darius"
	value.Match.Player.OpponentProvenance = riotapi.OpponentUnknown
	report := Build([]storage.RiotStoredMatch{value}, 1)
	if report.Groups[0].Key.OpponentChampion != "" || report.Groups[0].Key.OpponentProvenance != riotapi.OpponentUnknown {
		t.Fatalf("ambiguous matchup leaked into grouping: %#v", report.Groups[0].Key)
	}
}

func TestProviderReturnsObservationWithoutMutatingRules(t *testing.T) {
	repository := &fakeRepository{matches: []storage.RiotStoredMatch{
		statisticalMatch("NA1_1", time.Now(), true, []riotapi.FinalItem{{Slot: 0, ItemID: 3078}}),
	}}
	provider := NewProvider(repository, 5)
	baseline, err := provider.Observe(context.Background(), CandidateObservation{AccountID: "account", Patch: "16.18", ChampionName: "Garen", Role: domain.RoleTop, CandidateItemID: 3078})
	if err != nil {
		t.Fatal(err)
	}
	if baseline.Games != 1 || baseline.Wins != 1 || !baseline.MinimumSampleWarning || baseline.CandidateItemID != 3078 {
		t.Fatalf("baseline = %#v", baseline)
	}
	if repository.filter.Patch != "16.18" || repository.filter.Champion != "Garen" || repository.filter.Role != domain.RoleTop {
		t.Fatalf("filter = %#v", repository.filter)
	}
}

type fakeRepository struct {
	matches []storage.RiotStoredMatch
	filter  storage.RiotMatchFilter
}

func (repository *fakeRepository) ListRiotMatches(_ context.Context, _ string, filter storage.RiotMatchFilter) ([]storage.RiotStoredMatch, error) {
	repository.filter = filter
	return repository.matches, nil
}

func statisticalMatch(id string, end time.Time, win bool, items []riotapi.FinalItem) storage.RiotStoredMatch {
	return storage.RiotStoredMatch{
		AccountID: "account", RankBucket: "unknown", RankProvenance: "unavailable",
		Match: riotapi.Match{
			ID: id, Patch: "16.18", EndedAt: end,
			Player: riotapi.LinkedPlayerFacts{
				ChampionName: "Garen", Role: domain.RoleTop, RoleProvenance: riotapi.RoleFromTeamPosition,
				Win: win, FinalItems: items, OpponentChampion: "Darius", OpponentProvenance: riotapi.OpponentUniqueSameRole,
			},
		},
	}
}
