package itemprofiles

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lolengine/internal/catalog"
	"lolengine/internal/domain"
)

func TestBundledProfilesCoverRepresentativeChampionForEveryRole(t *testing.T) {
	knowledge := loadBundledProfileCatalog(t)
	document, registry, status, err := (Loader{Dir: filepath.Join("..", "..", "rules", "generic", "v1")}).Resolve(knowledge.Patch, knowledge)
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if !status.Ready || status.Fallback || status.Source != SourceExternalExact {
		t.Fatalf("status = %#v", status)
	}
	tests := []struct {
		champion string
		role     domain.Role
		profile  string
	}{
		{champion: "Garen", role: domain.RoleTop, profile: "garen_top"},
		{champion: "Amumu", role: domain.RoleJungle, profile: "amumu_jungle"},
		{champion: "Ahri", role: domain.RoleMiddle, profile: "ahri_middle"},
		{champion: "Jinx", role: domain.RoleBottom, profile: "jinx_bottom"},
		{champion: "Leona", role: domain.RoleSupport, profile: "leona_support"},
	}
	for _, test := range tests {
		t.Run(test.profile, func(t *testing.T) {
			resolution := registry.Resolve(Request{Champion: test.champion, Role: test.role})
			if resolution.Profile.ID != test.profile || resolution.Provenance.Level != KindChampionRole {
				t.Fatalf("resolution = profile %q provenance %#v", resolution.Profile.ID, resolution.Provenance)
			}
			if resolution.Provenance.ConfidenceCap != ConfidenceChampionRole || resolution.Provenance.Fallback {
				t.Fatalf("provenance = %#v", resolution.Provenance)
			}
			if len(resolution.Candidates) < document.AlternativeCount+1 {
				t.Fatalf("candidates = %d, want at least %d", len(resolution.Candidates), document.AlternativeCount+1)
			}
		})
	}
}

func TestBundledProfilesRejectAmbiguousClassFallbacks(t *testing.T) {
	knowledge := loadBundledProfileCatalog(t)
	_, registry, _, err := (Loader{Dir: filepath.Join("..", "..", "rules", "generic", "v1")}).Resolve(knowledge.Patch, knowledge)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		champion string
		role     domain.Role
	}{
		{champion: "Zed", role: domain.RoleMiddle},
		{champion: "Mordekaiser", role: domain.RoleTop},
		{champion: "Soraka", role: domain.RoleSupport},
	}
	for _, test := range tests {
		t.Run(test.champion, func(t *testing.T) {
			resolution := registry.Resolve(Request{Champion: test.champion, Role: test.role})
			if resolution.Profile.ID != "neutral" || resolution.Provenance.Level != KindNeutral {
				t.Fatalf("resolution = profile %q provenance %#v, want neutral", resolution.Profile.ID, resolution.Provenance)
			}
			if !hasWarning(resolution.Warnings, WarningNeutralFallback) {
				t.Fatalf("warnings = %#v, want neutral fallback", resolution.Warnings)
			}
		})
	}
}

func TestBundledProfileCandidatesHaveCompleteCatalogRecipes(t *testing.T) {
	knowledge := loadBundledProfileCatalog(t)
	_, registry, _, err := (Loader{Dir: filepath.Join("..", "..", "rules", "generic", "v1")}).Resolve(knowledge.Patch, knowledge)
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range registry.profiles {
		for _, candidate := range profile.candidates {
			assertRecipeClosure(t, candidate.Rule.ItemID, knowledge, map[int]bool{})
		}
	}
}

func loadBundledProfileCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join("testdata", "catalog-16.18.1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var knowledge catalog.Catalog
	if err := json.Unmarshal(payload, &knowledge); err != nil {
		t.Fatal(err)
	}
	return knowledge
}

func assertRecipeClosure(t *testing.T, itemID int, knowledge catalog.Catalog, visiting map[int]bool) {
	t.Helper()
	if visiting[itemID] {
		t.Fatalf("recipe cycle at item %d", itemID)
	}
	item, exists := knowledge.Items[itemID]
	if !exists {
		t.Fatalf("missing recipe item %d", itemID)
	}
	if !item.Purchasable || !item.Gold.Purchasable {
		t.Fatalf("recipe item %d is not purchasable", itemID)
	}
	if !item.AvailableMap11 {
		t.Fatalf("recipe item %d is not available on map 11", itemID)
	}
	if strings.TrimSpace(item.Name) == "" || item.Gold.Total <= 0 {
		t.Fatalf("recipe item %d has invalid catalog fields", itemID)
	}
	visiting[itemID] = true
	defer delete(visiting, itemID)
	for _, componentID := range item.From {
		assertRecipeClosure(t, componentID, knowledge, visiting)
	}
}

func TestBundledProfileCopyRemainsChoiceOriented(t *testing.T) {
	knowledge := loadBundledProfileCatalog(t)
	_, registry, _, err := (Loader{Dir: filepath.Join("..", "..", "rules", "generic", "v1")}).Resolve(knowledge.Patch, knowledge)
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range registry.profiles {
		for subject, copy := range map[string]PlanCopy{
			"runes": profile.profile.Runes, "summoners": profile.profile.Summoners, "initial": profile.profile.InitialPlan,
		} {
			if copy.Source != PlanSourceProfileCurated || len(copy.Options) < 1 || len(copy.Options) > 3 {
				t.Fatalf("profile %q %s copy = %#v", profile.profile.ID, subject, copy)
			}
			value := copy.Summary
			lower := strings.ToLower(strings.TrimSpace(value))
			for _, imperative := range []string{"compra ", "elige ", "mantén ", "reserva ", "combina ", "planifica "} {
				if strings.HasPrefix(lower, imperative) {
					t.Fatalf("profile %q %s copy is imperative: %q", profile.profile.ID, subject, value)
				}
			}
			for _, forbidden := range []string{"win rate", "pick rate", "tier", "best", "mejor"} {
				if strings.Contains(lower, forbidden) {
					t.Fatalf("profile %q %s copy contains meta claim %q", profile.profile.ID, subject, forbidden)
				}
			}
		}
	}
}
