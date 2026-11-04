package itemrules

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lolengine/internal/catalog"
)

func TestBundledGarenV2RulesCoverPatch16191WithCompleteRecipes(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("..", "itemprofiles", "testdata", "catalog-16.19.1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var knowledge catalog.Catalog
	if err := json.Unmarshal(payload, &knowledge); err != nil {
		t.Fatal(err)
	}
	rules, status, err := (Loader{Dir: filepath.Join("..", "..", "rules", "garen", "v2")}).Resolve(knowledge.Patch, knowledge)
	if err != nil {
		t.Fatal(err)
	}
	if rules.Patch != "16.19.1" || status.Source != SourceExternalExact || status.Fallback || !status.Ready || status.Version != "garen-v2.16.19.1.1" {
		t.Fatalf("Garen rules/status = %#v / %#v", rules, status)
	}
	for _, candidate := range rules.Candidates {
		assertGarenRecipeClosure(t, candidate.ItemID, knowledge, map[int]bool{})
	}
}

func assertGarenRecipeClosure(t *testing.T, itemID int, knowledge catalog.Catalog, visiting map[int]bool) {
	t.Helper()
	if visiting[itemID] {
		t.Fatalf("recipe cycle at item %d", itemID)
	}
	item, exists := knowledge.Items[itemID]
	if !exists || !item.Purchasable || !item.Gold.Purchasable || !item.AvailableMap11 || item.Gold.Total <= 0 || strings.TrimSpace(item.Name) == "" {
		t.Fatalf("item %d is absent or not a valid purchasable Map 11 recipe component", itemID)
	}
	visiting[itemID] = true
	defer delete(visiting, itemID)
	for _, componentID := range item.From {
		assertGarenRecipeClosure(t, componentID, knowledge, visiting)
	}
}
