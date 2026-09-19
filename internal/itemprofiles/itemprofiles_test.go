package itemprofiles

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lolengine/internal/catalog"
	"lolengine/internal/domain"
	"lolengine/internal/itemrules"
)

func TestRegistryResolvesConservativeHierarchy(t *testing.T) {
	knowledge := profileCatalog()
	registry, err := Compile(profileDocument(), knowledge)
	if err != nil {
		t.Fatalf("Compile() error: %v", err)
	}
	tests := []struct {
		name     string
		request  Request
		wantID   string
		wantKind Kind
		wantCap  float64
		warning  string
	}{
		{name: "champion and role", request: Request{Champion: "Ahri", Role: domain.RoleMiddle}, wantID: "ahri_mid", wantKind: KindChampionRole, wantCap: ConfidenceChampionRole},
		{name: "champion default precedes role", request: Request{Champion: "Ahri", Role: domain.RoleSupport}, wantID: "ahri_default", wantKind: KindChampionDefault, wantCap: ConfidenceChampionDefault, warning: WarningChampionDefault},
		{name: "compatible role precedes tag", request: Request{Champion: "Mystery Tank", Role: domain.RoleJungle}, wantID: "jungle_tank", wantKind: KindRoleArchetype, wantCap: ConfidenceRoleArchetype, warning: WarningRoleArchetype},
		{name: "incompatible role falls through to priority tag", request: Request{Champion: "Mystery Mage", Role: domain.RoleJungle}, wantID: "mage_tag", wantKind: KindTagArchetype, wantCap: ConfidenceTagArchetype, warning: WarningTagArchetype},
		{name: "missing role is never inferred", request: Request{Champion: "Mystery Tank"}, wantID: "tank_tag", wantKind: KindTagArchetype, wantCap: ConfidenceTagArchetype, warning: WarningRoleUnavailable},
		{name: "document tag priority is deterministic", request: Request{Champion: "Mystery Hybrid"}, wantID: "mage_tag", wantKind: KindTagArchetype, wantCap: ConfidenceTagArchetype, warning: WarningRoleUnavailable},
		{name: "unknown champion uses compatible explicit role only", request: Request{Champion: "Not In Catalog", Role: domain.RoleJungle}, wantID: "neutral", wantKind: KindNeutral, wantCap: ConfidenceNeutral, warning: WarningChampionUnresolved},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := registry.Resolve(test.request)
			if got.Profile.ID != test.wantID || got.Provenance.Level != test.wantKind || got.Provenance.ConfidenceCap != test.wantCap {
				t.Fatalf("Resolve() = profile %q kind %q cap %.2f", got.Profile.ID, got.Provenance.Level, got.Provenance.ConfidenceCap)
			}
			if got.Status.Source != SourceExternalExact || got.Status.Fallback {
				t.Fatalf("document status must remain independent of hierarchy fallback: %#v", got.Status)
			}
			if test.warning != "" && !hasWarning(got.Warnings, test.warning) {
				t.Fatalf("warnings = %#v, want %q", got.Warnings, test.warning)
			}
		})
	}
}

func TestDocumentValidationRejectsUnsafeOrAmbiguousProfiles(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Document)
		want   string
	}{
		{name: "overlapping exact selector", mutate: func(doc *Document) {
			duplicate := doc.Profiles[0]
			duplicate.ID = "ahri_mid_duplicate"
			doc.Profiles = append(doc.Profiles, duplicate)
		}, want: "overlapping selector"},
		{name: "duplicate family", mutate: func(doc *Document) { doc.Profiles[0].Families = []string{"general", "general"} }, want: "repeats family"},
		{name: "duplicate effective item", mutate: func(doc *Document) {
			duplicate := doc.Families["general"]
			duplicate.Candidates = append([]Candidate(nil), duplicate.Candidates...)
			for index := range duplicate.Candidates {
				duplicate.Candidates[index].Rule.ID += "_duplicate"
			}
			doc.Families["duplicate"] = duplicate
			doc.Profiles[0].Families = []string{"general", "duplicate"}
		}, want: "duplicate effective item"},
		{name: "invalid role", mutate: func(doc *Document) { doc.Profiles[0].Role = "roamer" }, want: "invalid role"},
		{name: "unknown tag", mutate: func(doc *Document) { doc.Profiles[2].Tags = []string{"Astronaut"} }, want: "unknown champion tag"},
		{name: "profile tag must be trimmed", mutate: func(doc *Document) { doc.Profiles[2].Tags = []string{" Tank "} }, want: "trimmed"},
		{name: "unknown excluded tag", mutate: func(doc *Document) { doc.Profiles[2].ExcludedTags = []string{"Astronaut"} }, want: "unknown champion tag"},
		{name: "required and excluded tag overlap", mutate: func(doc *Document) {
			doc.Profiles[2].ExcludedTags = []string{"Tank"}
		}, want: "both required and excluded"},
		{name: "exact selector cannot exclude tags", mutate: func(doc *Document) {
			doc.Profiles[0].ExcludedTags = []string{"Tank"}
		}, want: "cannot include tags"},
		{name: "candidate ID must be canonical", mutate: func(doc *Document) {
			family := doc.Families["general"]
			family.Candidates[0].Rule.ID = " one"
			doc.Families["general"] = family
		}, want: "candidate ID"},
		{name: "unsafe neutral core", mutate: func(doc *Document) {
			family := doc.Families["neutral"]
			family.Candidates[0].Rule.Category = itemrules.CategoryCore
			doc.Families["neutral"] = family
		}, want: "neutral"},
		{name: "unsafe neutral unconditional", mutate: func(doc *Document) {
			family := doc.Families["neutral"]
			family.Candidates[0].Activation = nil
			doc.Families["neutral"] = family
		}, want: "activation"},
		{name: "unsafe neutral zero threshold", mutate: func(doc *Document) {
			family := doc.Families["neutral"]
			family.Candidates[0].Activation[0].Minimum = 0
			doc.Families["neutral"] = family
		}, want: "positive activation"},
		{name: "unsafe neutral non defensive signal", mutate: func(doc *Document) {
			family := doc.Families["neutral"]
			family.Candidates[0].Activation[0].Signal = itemrules.SignalAhead
			doc.Families["neutral"] = family
		}, want: "defensive pressure"},
		{name: "fewer than three candidates", mutate: func(doc *Document) {
			family := doc.Families["general"]
			family.Candidates = family.Candidates[:2]
			doc.Families["general"] = family
		}, want: "at least 3"},
		{name: "catalog invalid item", mutate: func(doc *Document) {
			family := doc.Families["general"]
			family.Candidates[0].Rule.ItemID = 9999
			doc.Families["general"] = family
		}, want: "missing from catalog"},
		{name: "copy markup", mutate: func(doc *Document) { doc.Profiles[0].Runes.Summary = "<b>best</b>" }, want: "plain text"},
		{name: "copy must be trimmed", mutate: func(doc *Document) { doc.Profiles[0].Runes.Summary = " summary" }, want: "trimmed"},
		{name: "copy source is fixed", mutate: func(doc *Document) { doc.Profiles[0].Runes.Source = "community" }, want: "profile_curated"},
		{name: "initial plan is required", mutate: func(doc *Document) { doc.Profiles[0].InitialPlan.Options = nil }, want: "initial plan"},
		{name: "copy rejects control characters", mutate: func(doc *Document) { doc.Profiles[0].Summoners.Summary = "Plan\nseparado" }, want: "plain text"},
		{name: "copy rejects meta claims", mutate: func(doc *Document) { doc.Profiles[0].Runes.Summary = "Best build" }, want: "meta-performance"},
		{name: "copy option ID is stable", mutate: func(doc *Document) { doc.Profiles[0].Runes.Options[0].ID = "Not Stable" }, want: "lowercase ASCII"},
		{name: "fallback copy is explicit", mutate: func(doc *Document) { doc.Profiles[1].Runes.Summary = "Plan determinista." }, want: "generic or assumed"},
		{name: "profile ID is bounded", mutate: func(doc *Document) {
			doc.Profiles[0].ID = strings.Repeat("p", maximumIdentifierLength+1)
		}, want: "profile ID"},
		{name: "family name is bounded", mutate: func(doc *Document) {
			family := doc.Families["general"]
			delete(doc.Families, "general")
			doc.Families[strings.Repeat("f", maximumIdentifierLength+1)] = family
		}, want: "family name"},
		{name: "too many tag priorities", mutate: func(doc *Document) {
			doc.TagPriority = make([]string, maximumTagPriority+1)
		}, want: "tag_priority exceeds"},
		{name: "magic resistance scale is required", mutate: func(doc *Document) {
			doc.Context.MagicResistFullScale = 0
		}, want: "magic_resist_full_scale"},
		{name: "too many champion traits", mutate: func(doc *Document) {
			doc.ChampionTraits = make(map[string]itemrules.TraitProfile, maximumChampionTraits+1)
			for index := 0; index <= maximumChampionTraits; index++ {
				doc.ChampionTraits[fmt.Sprintf("champion-%d", index)] = itemrules.TraitProfile{}
			}
		}, want: "champion_traits exceeds"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			doc := profileDocument()
			test.mutate(&doc)
			_, err := Compile(doc, profileCatalog())
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.want)) {
				t.Fatalf("Compile() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestDocumentValidationRejectsInvalidCandidateRecipeClosure(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*catalog.Catalog)
		want   string
	}{
		{name: "recipe cycle", mutate: func(knowledge *catalog.Catalog) {
			component := knowledge.Items[1001]
			component.From = []int{2001}
			knowledge.Items[1001] = component
		}, want: "recipe cycle"},
		{name: "component not purchasable", mutate: func(knowledge *catalog.Catalog) {
			component := knowledge.Items[1001]
			component.Purchasable = false
			knowledge.Items[1001] = component
		}, want: "recipe item 1001 is not purchasable"},
		{name: "component not on map 11", mutate: func(knowledge *catalog.Catalog) {
			component := knowledge.Items[1001]
			component.AvailableMap11 = false
			knowledge.Items[1001] = component
		}, want: "recipe item 1001 is not available on map 11"},
		{name: "component invalid catalog fields", mutate: func(knowledge *catalog.Catalog) {
			component := knowledge.Items[1001]
			component.Gold.Total = 0
			knowledge.Items[1001] = component
		}, want: "recipe item 1001 has invalid catalog fields"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			knowledge := profileCatalog()
			test.mutate(&knowledge)
			_, err := Compile(profileDocument(), knowledge)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.want)) {
				t.Fatalf("Compile() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestDocumentValidationRejectsUnavailableCatalogCandidates(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*catalog.Item)
		want   string
	}{
		{name: "not purchasable", mutate: func(item *catalog.Item) { item.Purchasable = false }, want: "not purchasable"},
		{name: "gold not purchasable", mutate: func(item *catalog.Item) { item.Gold.Purchasable = false }, want: "not purchasable"},
		{name: "not map 11", mutate: func(item *catalog.Item) { item.AvailableMap11 = false }, want: "not available on map 11"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			knowledge := profileCatalog()
			item := knowledge.Items[2001]
			test.mutate(&item)
			knowledge.Items[2001] = item
			_, err := Compile(profileDocument(), knowledge)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), test.want) {
				t.Fatalf("Compile() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestDecodeDocumentIsStrictAndBounded(t *testing.T) {
	dir := t.TempDir()
	valid, err := json.Marshal(profileDocument())
	if err != nil {
		t.Fatal(err)
	}
	unknown := append([]byte(nil), valid[:len(valid)-1]...)
	unknown = append(unknown, []byte(`,"win_rate":55}`)...)
	trailing := append([]byte(nil), valid...)
	trailing = append(trailing, []byte(` {}`)...)
	tests := []struct {
		name    string
		payload []byte
		want    string
	}{
		{name: "unknown field", payload: unknown, want: "unknown field"},
		{name: "trailing JSON", payload: trailing, want: "trailing"},
		{name: "oversized", payload: make([]byte, maximumProfileFileBytes+1), want: "exceeds"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(dir, test.name+".json")
			if err := os.WriteFile(path, test.payload, 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := decodeDocument(path)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.want)) {
				t.Fatalf("decodeDocument() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestManagerUsesPatchFallbackAndCompatibleLastKnownGood(t *testing.T) {
	dir := t.TempDir()
	doc := profileDocument()
	doc.Patch = "16.18.1"
	writeProfileDocument(t, dir, doc)
	knowledge := profileCatalog()
	knowledge.Patch = "16.18.2"
	manager := NewManager(dir)

	first := manager.Resolve("16.18.2", Request{Champion: "Ahri", Role: domain.RoleMiddle}, knowledge)
	if !first.Status.Ready || !first.Status.Fallback || first.Status.Source != SourceExternalPatchFallback || first.Profile.ID != "ahri_mid" {
		t.Fatalf("fallback resolution = %#v", first)
	}
	if !first.Provenance.Fallback || !hasWarning(first.Warnings, WarningPatchFallback) {
		t.Fatalf("patch fallback provenance/warnings = %#v / %#v", first.Provenance, first.Warnings)
	}

	if err := os.WriteFile(filepath.Join(dir, "16.18.1.json"), []byte(`{"schema_version":1,"unknown":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	retained := manager.Reload("16.18.2", Request{Champion: "Ahri", Role: domain.RoleMiddle}, knowledge)
	if !retained.Status.Ready || retained.Status.Source != SourceExternalLastKnownGood || retained.Status.LastError == "" || retained.Profile.ID != "ahri_mid" {
		t.Fatalf("last-known-good resolution = %#v", retained)
	}
	if !retained.Provenance.Fallback || !hasWarning(retained.Warnings, WarningLastKnownGood) {
		t.Fatalf("last-known-good provenance/warnings = %#v / %#v", retained.Provenance, retained.Warnings)
	}

	knowledge.Patch = "16.19.1"
	missing := manager.Resolve("16.19.1", Request{Champion: "Ahri", Role: domain.RoleMiddle}, knowledge)
	if missing.Status.Ready || missing.Status.LastError == "" {
		t.Fatalf("cross-minor fallback must fail closed: %#v", missing)
	}
}

func TestLoaderPrefersExactThenNewestNonFutureSameMinor(t *testing.T) {
	dir := t.TempDir()
	for _, patch := range []string{"16.18.0", "16.18.1", "16.18.2"} {
		document := profileDocument()
		document.Patch = patch
		document.RulesetVersion = "profiles-" + patch
		writeProfileDocument(t, dir, document)
	}
	tests := []struct {
		active     string
		wantPatch  string
		wantSource string
	}{
		{active: "16.18.1", wantPatch: "16.18.1", wantSource: SourceExternalExact},
		{active: "16.18.3", wantPatch: "16.18.2", wantSource: SourceExternalPatchFallback},
	}
	for _, test := range tests {
		t.Run(test.active, func(t *testing.T) {
			knowledge := profileCatalog()
			knowledge.Patch = test.active
			document, _, status, err := (Loader{Dir: dir}).Resolve(test.active, knowledge)
			if err != nil {
				t.Fatalf("Resolve() error: %v", err)
			}
			if document.Patch != test.wantPatch || status.Patch != test.wantPatch || status.Source != test.wantSource {
				t.Fatalf("selected document/status = %q / %#v", document.Patch, status)
			}
		})
	}
}

func TestGenericPatchParsingRejectsNoncanonicalNumericComponents(t *testing.T) {
	tests := []string{"016.18.1", "16.018.1", "16.18.01"}
	for _, value := range tests {
		t.Run(value, func(t *testing.T) {
			if _, ok := parsePatch(value); ok {
				t.Fatalf("parsePatch(%q) accepted a noncanonical alias", value)
			}
		})
	}
}

func TestLoaderRejectsEquivalentPatchAliasInEitherWriteOrder(t *testing.T) {
	orders := [][]string{{"16.18.1", "16.18.01"}, {"16.18.01", "16.18.1"}}
	for index, order := range orders {
		t.Run(fmt.Sprintf("order_%d", index+1), func(t *testing.T) {
			dir := t.TempDir()
			for _, patch := range order {
				document := profileDocument()
				document.Patch = patch
				writeProfileDocument(t, dir, document)
			}
			knowledge := profileCatalog()
			if _, _, _, err := (Loader{Dir: dir}).Resolve(knowledge.Patch, knowledge); err == nil || !strings.Contains(err.Error(), "invalid profile patch") {
				t.Fatalf("Resolve() error = %v, want noncanonical alias rejection", err)
			}
		})
	}
}

func TestLoaderRejectsSymbolicLinksAndFutureOnlyProfiles(t *testing.T) {
	dir := t.TempDir()
	doc := profileDocument()
	writeProfileDocument(t, dir, doc)
	if err := os.Symlink(filepath.Join(dir, doc.Patch+".json"), filepath.Join(dir, "16.18.2.json")); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	knowledge := profileCatalog()
	knowledge.Patch = "16.18.2"
	if _, _, _, err := (Loader{Dir: dir}).Resolve(knowledge.Patch, knowledge); err == nil || !strings.Contains(strings.ToLower(err.Error()), "symbolic link") {
		t.Fatalf("Resolve() error = %v, want symbolic link rejection", err)
	}

	futureDir := t.TempDir()
	future := profileDocument()
	future.Patch = "16.18.3"
	writeProfileDocument(t, futureDir, future)
	if _, _, _, err := (Loader{Dir: futureDir}).Resolve(knowledge.Patch, knowledge); err == nil || !strings.Contains(strings.ToLower(err.Error()), "non-future") {
		t.Fatalf("Resolve() error = %v, want non-future rejection", err)
	}
}

func TestLoaderRejectsSymbolicLinkDirectory(t *testing.T) {
	realDir := t.TempDir()
	doc := profileDocument()
	writeProfileDocument(t, realDir, doc)
	linkDir := filepath.Join(t.TempDir(), "profiles")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}

	knowledge := profileCatalog()
	if _, _, _, err := (Loader{Dir: linkDir}).Resolve(knowledge.Patch, knowledge); err == nil || !strings.Contains(strings.ToLower(err.Error()), "symbolic link") {
		t.Fatalf("Resolve() error = %v, want symbolic-link directory rejection", err)
	}
}

func TestEffectiveCandidatesActivateDeterministically(t *testing.T) {
	registry, err := Compile(profileDocument(), profileCatalog())
	if err != nil {
		t.Fatal(err)
	}
	resolution := registry.Resolve(Request{})
	if got := resolution.ActiveCandidates(nil); len(got) != 0 {
		t.Fatalf("neutral candidates without pressure = %d, want 0", len(got))
	}
	needs := map[itemrules.Signal]int{
		itemrules.SignalPhysical:     900,
		itemrules.SignalMagic:        900,
		itemrules.SignalCrowdControl: 900,
	}
	first := resolution.ActiveCandidates(needs)
	second := resolution.ActiveCandidates(needs)
	if len(first) != 3 || len(second) != 3 {
		t.Fatalf("activated candidates = %d/%d, want 3", len(first), len(second))
	}
	for index := range first {
		if first[index].ItemID != second[index].ItemID {
			t.Fatalf("candidate order changed: %#v vs %#v", first, second)
		}
	}
}

func TestValidatePlainTextRejectsSeparatedMetaClaimsWithoutFalsePositives(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "win hyphen rate", value: "Use this for its win-rate.", wantErr: true},
		{name: "win underscore rate", value: "Use this for its win_rate.", wantErr: true},
		{name: "win repeated whitespace", value: "Use this for its win   rate.", wantErr: true},
		{name: "win rates plural", value: "Compare the win rates.", wantErr: true},
		{name: "win rates underscore", value: "Compare the win_rates.", wantErr: true},
		{name: "winrates plural compact", value: "Compare the winrates.", wantErr: true},
		{name: "pick hyphen rate", value: "Use this for its pick-rate.", wantErr: true},
		{name: "pick punctuation rate", value: "Use this for its pick.rate.", wantErr: true},
		{name: "pick rates plural", value: "Compare pick rates.", wantErr: true},
		{name: "pickrates plural compact", value: "Compare pickrates.", wantErr: true},
		{name: "selection rates plural", value: "Compare selection_rates.", wantErr: true},
		{name: "win percentages plural", value: "Compare win percentages.", wantErr: true},
		{name: "winning percentages plural", value: "Compare winning-percentages.", wantErr: true},
		{name: "pick percentages plural", value: "Compare pick_percentages.", wantErr: true},
		{name: "tier list", value: "A tier-list option.", wantErr: true},
		{name: "tier underscore list", value: "A tier_list option.", wantErr: true},
		{name: "tiers plural", value: "Compare los tiers disponibles.", wantErr: true},
		{name: "tierlists plural compact", value: "Compare tierlists.", wantErr: true},
		{name: "best build", value: "The best-build path.", wantErr: true},
		{name: "best underscore build", value: "The best_build path.", wantErr: true},
		{name: "best builds plural", value: "The best builds available.", wantErr: true},
		{name: "mejores plural", value: "Una de las mejores rutas.", wantErr: true},
		{name: "tasas de victoria plural", value: "Compara las tasas de victoria.", wantErr: true},
		{name: "tasa de victorias plural", value: "Compara la tasa_de_victorias.", wantErr: true},
		{name: "tasas y victorias plural", value: "Compara las tasas-de-victorias.", wantErr: true},
		{name: "tasas de seleccion plural", value: "Compara las tasas de selección.", wantErr: true},
		{name: "tasas de seleccion without accent", value: "Compara las tasas_de_seleccion.", wantErr: true},
		{name: "tasa de selecciones plural", value: "Compara la tasa_de_selecciones.", wantErr: true},
		{name: "tasas y selecciones plural", value: "Compara las tasas-de-selecciones.", wantErr: true},
		{name: "porcentajes de victoria plural", value: "Compara los porcentajes_de_victoria.", wantErr: true},
		{name: "porcentaje de victorias plural", value: "Compara el porcentaje-de-victorias.", wantErr: true},
		{name: "porcentajes de seleccion plural", value: "Compara los porcentajes de selección.", wantErr: true},
		{name: "porcentajes de seleccion without accent", value: "Compara los porcentajes-de-seleccion.", wantErr: true},
		{name: "porcentaje de selecciones plural", value: "Compara el porcentaje_de_selecciones.", wantErr: true},
		{name: "neutral bestia word", value: "La bestia cercana cambia el intercambio."},
		{name: "neutral mejoras word", value: "Las mejoras cambian el intercambio."},
		{name: "neutral tierna word", value: "La presión tierna no es una afirmación de nivel."},
		{name: "neutral victoria word", value: "La condición de victoria depende del escenario."},
		{name: "neutral seleccion word", value: "La selección manual conserva la decisión."},
		{name: "neutral winner word", value: "The winner chooses between options."},
		{name: "neutral picked word", value: "The manually picked option preserves agency."},
		{name: "neutral criterion", value: "El criterio es conservar recursos."},
		{name: "neutral choices", value: "Compara las opciones según el escenario manual."},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validatePlainText("copy", test.value, true)
			if test.wantErr && (err == nil || !strings.Contains(err.Error(), "meta-performance")) {
				t.Fatalf("validatePlainText(%q) error = %v, want meta-performance rejection", test.value, err)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("validatePlainText(%q) error = %v, want accepted neutral language", test.value, err)
			}
		})
	}
}

func TestValidatePlainTextTreatsNFCAndNFDSpanishTermsEqually(t *testing.T) {
	tests := []struct {
		name    string
		nfc     string
		nfd     string
		wantErr bool
	}{
		{name: "selection rate", nfc: "Compara la tasa de selección.", nfd: "Compara la tasa de seleccio\u0301n.", wantErr: true},
		{name: "selection rates", nfc: "Compara las tasas de selección.", nfd: "Compara las tasas de seleccio\u0301n.", wantErr: true},
		{name: "selection percentage", nfc: "Compara el porcentaje de selección.", nfd: "Compara el porcentaje de seleccio\u0301n.", wantErr: true},
		{name: "selection percentages", nfc: "Compara los porcentajes de selección.", nfd: "Compara los porcentajes de seleccio\u0301n.", wantErr: true},
		{name: "benign option", nfc: "La opción manual conserva la decisión.", nfd: "La opcio\u0301n manual conserva la decisio\u0301n."},
		{name: "benign pressure", nfc: "La presión cambia el intercambio.", nfd: "La presio\u0301n cambia el intercambio."},
		{name: "benign bestia", nfc: "La bestía imaginaria no afirma rendimiento.", nfd: "La besti\u0301a imaginaria no afirma rendimiento."},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			forms := []struct {
				name  string
				value string
			}{{name: "NFC", value: test.nfc}, {name: "NFD", value: test.nfd}}
			for _, form := range forms {
				t.Run(form.name, func(t *testing.T) {
					err := validatePlainText("copy", form.value, true)
					if test.wantErr && (err == nil || !strings.Contains(err.Error(), "meta-performance")) {
						t.Fatalf("validatePlainText(%q) error = %v, want meta-performance rejection", form.value, err)
					}
					if !test.wantErr && err != nil {
						t.Fatalf("validatePlainText(%q) error = %v, want benign text accepted", form.value, err)
					}
				})
			}
		})
	}
}

func TestManagerCachesUnavailableResolutionUntilExplicitReload(t *testing.T) {
	manager := NewManager(t.TempDir())
	knowledge := profileCatalog()
	first := manager.Resolve(knowledge.Patch, Request{}, knowledge)
	firstRevision := manager.Revision()
	second := manager.Resolve(knowledge.Patch, Request{}, knowledge)
	if first.Status.Ready || second.Status.Ready || second.Status.LastError != first.Status.LastError {
		t.Fatalf("unavailable resolutions = %#v / %#v", first, second)
	}
	if manager.Revision() != firstRevision {
		t.Fatalf("cached unavailable resolve advanced revision from %d to %d", firstRevision, manager.Revision())
	}
	manager.Reload(knowledge.Patch, Request{}, knowledge)
	if manager.Revision() != firstRevision+1 {
		t.Fatalf("explicit reload revision = %d, want %d", manager.Revision(), firstRevision+1)
	}
}

func profileDocument() Document {
	general := CandidateFamily{Candidates: []Candidate{
		profileCandidate("one", 2001, itemrules.CategorySituational, nil),
		profileCandidate("two", 2002, itemrules.CategorySituational, nil),
		profileCandidate("three", 2003, itemrules.CategorySituational, nil),
		profileCandidate("four", 2004, itemrules.CategorySituational, nil),
	}}
	neutral := CandidateFamily{Candidates: []Candidate{
		profileCandidate("physical", 2001, itemrules.CategorySituational, []Activation{{Signal: itemrules.SignalPhysical, Minimum: 700}}),
		profileCandidate("magic", 2002, itemrules.CategorySituational, []Activation{{Signal: itemrules.SignalMagic, Minimum: 700}}),
		profileCandidate("cc", 2003, itemrules.CategorySituational, []Activation{{Signal: itemrules.SignalCrowdControl, Minimum: 700}}),
	}}
	return Document{
		SchemaVersion:    CurrentSchemaVersion,
		RulesetVersion:   "generic-test",
		Patch:            "16.18.1",
		TagPriority:      []string{"Mage", "Tank"},
		AlternativeCount: 2,
		Phases:           itemrules.PhaseBoundaries{EarlyEndSeconds: 840, MidEndSeconds: 1500},
		Context: itemrules.ContextTuning{LaneWeightByPhase: map[domain.Phase]int{
			domain.PhaseEarly: 700, domain.PhaseMid: 400, domain.PhaseLate: 150,
		}, MissingTraitPenalty: 300, ArmorFullScale: 160, MagicResistFullScale: 160},
		Stability: itemrules.StabilityPolicy{SwitchMargin: 100, ConfirmationSeconds: 8, MinimumHoldSeconds: 15, CriticalThreshold: 800},
		Families:  map[string]CandidateFamily{"general": general, "neutral": neutral},
		Profiles: []Profile{
			{ID: "ahri_mid", Kind: KindChampionRole, Champion: "Ahri", Role: domain.RoleMiddle, Families: []string{"general"}, Runes: validCopy(), Summoners: validCopy(), InitialPlan: validCopy()},
			{ID: "ahri_default", Kind: KindChampionDefault, Champion: "Ahri", Families: []string{"general"}, Runes: validCopy(), Summoners: validCopy(), InitialPlan: validCopy()},
			{ID: "jungle_tank", Kind: KindRoleArchetype, Role: domain.RoleJungle, Tags: []string{"Tank"}, Families: []string{"general"}, Runes: validCopy(), Summoners: validCopy(), InitialPlan: validCopy()},
			{ID: "mage_tag", Kind: KindTagArchetype, Tags: []string{"Mage"}, Families: []string{"general"}, Runes: validCopy(), Summoners: validCopy(), InitialPlan: validCopy()},
			{ID: "tank_tag", Kind: KindTagArchetype, Tags: []string{"Tank"}, Families: []string{"general"}, Runes: validCopy(), Summoners: validCopy(), InitialPlan: validCopy()},
			{ID: "neutral", Kind: KindNeutral, Families: []string{"neutral"}, Runes: validCopy(), Summoners: validCopy(), InitialPlan: validCopy()},
		},
		NeutralProfile: "neutral",
	}
}

func profileCatalog() catalog.Catalog {
	items := make(map[int]catalog.Item)
	items[1001] = catalog.Item{ID: 1001, Name: "Component", Purchasable: true, AvailableMap11: true, Gold: catalog.Gold{Total: 300, Purchasable: true}, Into: []int{2001, 2002, 2003, 2004}}
	for id := 2001; id <= 2004; id++ {
		items[id] = catalog.Item{ID: id, Name: "Candidate", Purchasable: true, AvailableMap11: true, Gold: catalog.Gold{Total: 1000, Purchasable: true}, From: []int{1001}}
	}
	return catalog.Catalog{
		Patch: "16.18.1", Locale: "es_MX", SyncedAt: time.Unix(1, 0), Items: items,
		Champions: map[string]catalog.Champion{
			"Ahri":          {ID: "Ahri", Key: "103", Name: "Ahri", Tags: []string{"Mage", "Assassin"}},
			"MysteryTank":   {ID: "MysteryTank", Key: "9001", Name: "Mystery Tank", Tags: []string{"Tank", "Fighter"}},
			"MysteryMage":   {ID: "MysteryMage", Key: "9002", Name: "Mystery Mage", Tags: []string{"Mage"}},
			"MysteryHybrid": {ID: "MysteryHybrid", Key: "9003", Name: "Mystery Hybrid", Tags: []string{"Tank", "Mage"}},
		},
	}
}

func profileCandidate(id string, itemID int, category string, activation []Activation) Candidate {
	return Candidate{Rule: itemrules.CandidateRule{ID: id, ItemID: itemID, Category: category, BaseScore: 0}, Activation: activation}
}

func validCopy() PlanCopy {
	return PlanCopy{Summary: "Plan genérico asumido y determinista.", Source: PlanSourceProfileCurated, Options: []PlanOption{{ID: "option", Label: "Opción", When: "Si aplica.", Tradeoff: "Expone el intercambio."}}}
}

func hasWarning(values []Warning, code string) bool {
	for _, value := range values {
		if value.Code == code {
			return true
		}
	}
	return false
}

func writeProfileDocument(t *testing.T, dir string, doc Document) {
	t.Helper()
	payload, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, doc.Patch+".json"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
}
