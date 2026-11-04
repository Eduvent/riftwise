package pregame

import (
	"context"
	"strings"
	"testing"

	"lolengine/internal/domain"
	"lolengine/internal/itemprofiles"
	"lolengine/internal/itemrules"
)

func TestMalphitePregameAdaptsToTeemo(t *testing.T) {
	service := newSettTestService(t)
	baseline := validSettScenario()
	baseline.Champion = "Malphite"
	baseline.Role = domain.RoleTop
	baseline.LaneOpponent = ""
	plain, err := service.Plan(context.Background(), baseline)
	if err != nil {
		t.Fatal(err)
	}
	if plain.Provenance.Profile.ProfileID != "malphite_top" || plain.Provenance.Profile.Level != itemprofiles.KindChampionRole || plain.Provenance.Profile.Fallback {
		t.Fatalf("profile = %#v", plain.Provenance.Profile)
	}
	if plain.TargetItem == nil || plain.TargetItem.ID != 3068 {
		t.Fatalf("baseline target = %#v", plain.TargetItem)
	}
	baseline.LaneOpponent = "Teemo"
	vs, err := service.Plan(context.Background(), baseline)
	if err != nil {
		t.Fatal(err)
	}
	if vs.Scenario.LaneOpponentID != "Teemo" || vs.Needs[itemrules.SignalMagic] < 500 || vs.Needs[itemrules.SignalSlows] < 650 || vs.Needs[itemrules.SignalSustained] < 650 {
		t.Fatalf("Teemo needs = %#v", vs.Needs)
	}
	if vs.TargetItem == nil || vs.TargetItem.ID != 3111 {
		t.Fatalf("Teemo target = %#v; ranked=%#v", vs.TargetItem, vs.RankedOptions)
	}
	if len(vs.Alternatives) < 2 || len(vs.RankedOptions) < 3 {
		t.Fatalf("Teemo choices = %#v", vs.RankedOptions)
	}
	// Matchup assumptions must not be presented as general Malphite profile copy.
	for _, copy := range []itemprofiles.PlanCopy{vs.RunePlan, vs.SummonerPlan, vs.InitialPlan} {
		if copy.Source != itemprofiles.PlanSourceProfileCurated || len(copy.Options) < 2 {
			t.Fatalf("copy = %#v", copy)
		}
		if strings.Contains(strings.ToLower(copy.Summary), "teemo") {
			t.Fatalf("matchup-only guidance leaked into champion-wide copy: %#v", copy)
		}
	}
	baseline.Pressures.Magic = PressureNone
	explicit, err := service.Plan(context.Background(), baseline)
	if err != nil {
		t.Fatal(err)
	}
	if explicit.Needs[itemrules.SignalMagic] != 0 {
		t.Fatalf("explicit magic=none overridden: %#v", explicit.Needs)
	}
}
