package itemprofiles

import (
	"lolengine/internal/itemrules"
	"path/filepath"
	"testing"

	"lolengine/internal/domain"
)

func TestBundledMalphiteTopProfileIsPatchExact(t *testing.T) {
	knowledge := loadSettProfileCatalog(t)
	document, registry, status, err := (Loader{Dir: filepath.Join("..", "..", "rules", "generic", "v1")}).Resolve(knowledge.Patch, knowledge)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Ready || status.Fallback || status.Source != SourceExternalExact || document.Patch != "16.19.1" || document.RulesetVersion != "generic-v1.16.19.1.2" {
		t.Fatalf("profile status/document = %#v / %#v", status, document.RulesetVersion)
	}
	resolution := registry.Resolve(Request{Champion: "Malphite", Role: domain.RoleTop})
	if resolution.Profile.ID != "malphite_top" || resolution.Provenance.Level != KindChampionRole || resolution.Provenance.Fallback || resolution.Provenance.Patch != knowledge.Patch {
		t.Fatalf("Malphite profile = %#v", resolution.Provenance)
	}
	if len(resolution.Candidates) < document.AlternativeCount+1 {
		t.Fatalf("candidates = %d", len(resolution.Candidates))
	}
	want := map[int]bool{3068: false, 3111: false, 4401: false, 3047: false, 3110: false, 3075: false, 6665: false, 3143: false}
	for _, candidate := range resolution.Candidates {
		if _, ok := want[candidate.Rule.ItemID]; ok {
			want[candidate.Rule.ItemID] = true
		}
		assertRecipeClosure(t, candidate.Rule.ItemID, knowledge, map[int]bool{})
	}
	for item, found := range want {
		if !found {
			t.Errorf("missing candidate %d", item)
		}
	}
	for name, copy := range map[string]PlanCopy{"runes": resolution.Profile.Runes, "summoners": resolution.Profile.Summoners, "initial": resolution.Profile.InitialPlan} {
		if copy.Source != PlanSourceProfileCurated || len(copy.Options) < 2 {
			t.Errorf("%s copy = %#v", name, copy)
		}
	}
	for _, candidate := range resolution.Candidates {
		if candidate.Rule.ItemID != 3143 {
			continue
		}
		for _, signal := range candidate.Rule.Covers {
			if signal == itemrules.SignalBurst {
				t.Fatal("Randuin must not claim generic burst coverage")
			}
		}
		for _, adjustment := range candidate.Rule.Adjustments {
			if adjustment.Signal == itemrules.SignalBurst {
				t.Fatal("Randuin must not receive a generic burst bonus")
			}
		}
	}
	if _, ok := resolution.ChampionTraits["Teemo"]; !ok {
		t.Fatal("Teemo traits missing")
	}
}
