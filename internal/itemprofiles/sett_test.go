package itemprofiles

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"lolengine/internal/catalog"
	"lolengine/internal/domain"
)

func TestBundledSettTopProfileIsExactAndPatchScoped(t *testing.T) {
	loader := Loader{Dir: filepath.Join("..", "..", "rules", "generic", "v1")}
	knowledge := loadSettProfileCatalog(t)
	document, registry, status, err := loader.Resolve(knowledge.Patch, knowledge)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Ready || status.Source != SourceExternalExact || status.Fallback || document.Patch != "16.19.1" {
		t.Fatalf("16.19.1 profile status = %#v", status)
	}
	resolution := registry.Resolve(Request{Champion: "Sett", Role: domain.RoleTop})
	if resolution.Profile.ID != "sett_top" || resolution.Provenance.Level != KindChampionRole || resolution.Provenance.Fallback {
		t.Fatalf("Sett resolution = %#v", resolution.Provenance)
	}
	if resolution.Provenance.ConfidenceCap != ConfidenceChampionRole || resolution.Provenance.Patch != "16.19.1" || len(resolution.Provenance.FamilyIDs) != 1 || resolution.Provenance.FamilyIDs[0] != "sett_top" {
		t.Fatalf("Sett provenance = %#v", resolution.Provenance)
	}
	if len(resolution.Candidates) < document.AlternativeCount+1 {
		t.Fatalf("Sett candidates = %d", len(resolution.Candidates))
	}
	want := map[int]bool{6631: false, 2501: false, 3053: false, 3047: false, 3111: false, 3181: false, 3161: false, 3071: false}
	for _, candidate := range resolution.Candidates {
		if _, ok := want[candidate.Rule.ItemID]; ok {
			want[candidate.Rule.ItemID] = true
		}
		assertRecipeClosure(t, candidate.Rule.ItemID, knowledge, map[int]bool{})
	}
	for id, found := range want {
		if !found {
			t.Errorf("Sett candidate item %d is absent", id)
		}
	}
	for subject, copy := range map[string]PlanCopy{"runes": resolution.Profile.Runes, "summoners": resolution.Profile.Summoners, "initial": resolution.Profile.InitialPlan} {
		if copy.Source != PlanSourceProfileCurated || len(copy.Options) < 2 {
			t.Errorf("Sett %s plan = %#v", subject, copy)
		}
	}

	old := loadBundledProfileCatalog(t)
	oldDocument, oldRegistry, oldStatus, err := loader.Resolve(old.Patch, old)
	if err != nil {
		t.Fatal(err)
	}
	if oldStatus.Patch != "16.18.1" || oldStatus.Fallback || oldDocument.RulesetVersion != "generic-v1.16.18.1.3" {
		t.Fatalf("old patch changed: document=%#v status=%#v", oldDocument, oldStatus)
	}
	if got := oldRegistry.Resolve(Request{Champion: "Garen", Role: domain.RoleTop}); got.Profile.ID != "garen_top" {
		t.Fatalf("old patch Garen profile = %q", got.Profile.ID)
	}
}

func loadSettProfileCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join("testdata", "catalog-16.19.1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var knowledge catalog.Catalog
	if err := json.Unmarshal(payload, &knowledge); err != nil {
		t.Fatal(err)
	}
	return knowledge
}
