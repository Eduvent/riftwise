package itemprofiles

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"lolengine/internal/catalog"
	"lolengine/internal/itemrules"
)

const (
	maximumProfiles         = 512
	maximumFamilies         = 256
	maximumFamilyCandidates = 256
	maximumTagPriority      = 32
	maximumProfileTags      = 16
	maximumProfileFamilies  = 32
	maximumChampionTraits   = 512
	maximumItemTraits       = 2048
	maximumCopyOptions      = 3
	maximumCopyLength       = 500
	maximumIdentifierLength = 128
)

// Compile validates and freezes one generic profile document against the
// active catalog. The registry is immutable after this call.
func Compile(document Document, knowledge catalog.Catalog) (*Registry, error) {
	if err := validateDocumentShape(document); err != nil {
		return nil, err
	}
	if err := knowledge.Validate(); err != nil {
		return nil, fmt.Errorf("invalid catalog: %w", err)
	}
	knownTags := catalogTags(knowledge)
	if err := validateTagPriority(document.TagPriority, knownTags); err != nil {
		return nil, err
	}

	families := make(map[string][]Candidate, len(document.Families))
	familyNames := make([]string, 0, len(document.Families))
	for name := range document.Families {
		familyNames = append(familyNames, name)
	}
	sort.Strings(familyNames)
	for _, name := range familyNames {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" || trimmed != name || len(name) > maximumIdentifierLength {
			return nil, fmt.Errorf("family name %q must be non-empty, trimmed, and at most %d bytes", name, maximumIdentifierLength)
		}
		family := document.Families[name]
		if len(family.Candidates) == 0 || len(family.Candidates) > maximumFamilyCandidates {
			return nil, fmt.Errorf("family %q candidates must be between 1 and %d", name, maximumFamilyCandidates)
		}
		seenIDs := make(map[string]struct{}, len(family.Candidates))
		seenItems := make(map[int]struct{}, len(family.Candidates))
		for index, candidate := range family.Candidates {
			if candidate.Rule.ID != strings.TrimSpace(candidate.Rule.ID) || len(candidate.Rule.ID) > maximumIdentifierLength {
				return nil, fmt.Errorf("family %q candidate ID %q must be trimmed and at most %d bytes", name, candidate.Rule.ID, maximumIdentifierLength)
			}
			if err := itemrules.ValidateCandidateRule(index, candidate.Rule); err != nil {
				return nil, fmt.Errorf("family %q: %w", name, err)
			}
			if _, exists := seenIDs[candidate.Rule.ID]; exists {
				return nil, fmt.Errorf("family %q repeats candidate ID %q", name, candidate.Rule.ID)
			}
			seenIDs[candidate.Rule.ID] = struct{}{}
			if _, exists := seenItems[candidate.Rule.ItemID]; exists {
				return nil, fmt.Errorf("family %q repeats item %d", name, candidate.Rule.ItemID)
			}
			seenItems[candidate.Rule.ItemID] = struct{}{}
			if err := validateActivation(name, candidate); err != nil {
				return nil, err
			}
			if err := validateCatalogCandidate(name, candidate.Rule, knowledge); err != nil {
				return nil, err
			}
			if err := validateCandidateRecipe(name, candidate.Rule, knowledge); err != nil {
				return nil, err
			}
		}
		families[name] = cloneCandidates(family.Candidates)
	}

	if err := validateTraits(document, knowledge); err != nil {
		return nil, err
	}
	registry := &Registry{document: cloneDocument(document), knowledge: catalog.Clone(knowledge)}
	seenProfileIDs := make(map[string]struct{}, len(document.Profiles))
	seenSelectors := make(map[string]string, len(document.Profiles))
	neutralCount := 0
	for _, source := range document.Profiles {
		profile := cloneProfile(source)
		if strings.TrimSpace(profile.ID) == "" || strings.TrimSpace(profile.ID) != profile.ID || len(profile.ID) > maximumIdentifierLength {
			return nil, fmt.Errorf("profile ID %q must be non-empty, trimmed, and at most %d bytes", profile.ID, maximumIdentifierLength)
		}
		if _, exists := seenProfileIDs[profile.ID]; exists {
			return nil, fmt.Errorf("duplicate profile ID %q", profile.ID)
		}
		seenProfileIDs[profile.ID] = struct{}{}
		if err := validatePlanCopy(profile.ID+" runes", profile.Runes); err != nil {
			return nil, err
		}
		if err := validatePlanCopy(profile.ID+" summoners", profile.Summoners); err != nil {
			return nil, err
		}
		if err := validatePlanCopy(profile.ID+" initial plan", profile.InitialPlan); err != nil {
			return nil, err
		}
		if profile.Kind != KindChampionRole {
			if err := validateFallbackPlanCopy(profile); err != nil {
				return nil, err
			}
		}

		compiled, selector, err := compileSelector(profile, knowledge, knownTags)
		if err != nil {
			return nil, err
		}
		if previous, exists := seenSelectors[selector]; exists {
			return nil, fmt.Errorf("profiles %q and %q have overlapping selector %q", previous, profile.ID, selector)
		}
		seenSelectors[selector] = profile.ID
		compiled.candidates, err = effectiveCandidates(profile, families)
		if err != nil {
			return nil, err
		}
		if len(compiled.candidates) < document.AlternativeCount+1 {
			return nil, fmt.Errorf("profile %q requires at least %d effective candidates", profile.ID, document.AlternativeCount+1)
		}
		if profile.Kind == KindNeutral {
			neutralCount++
			if profile.ID != document.NeutralProfile {
				return nil, fmt.Errorf("neutral profile %q does not match neutral_profile %q", profile.ID, document.NeutralProfile)
			}
			if err := validateNeutral(compiled); err != nil {
				return nil, err
			}
			registry.neutral = compiled
		}
		registry.profiles = append(registry.profiles, compiled)
	}
	if neutralCount != 1 {
		return nil, fmt.Errorf("document must contain exactly one neutral profile")
	}
	return registry, nil
}

func validateDocumentShape(document Document) error {
	if document.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("unsupported generic profile schema version %d", document.SchemaVersion)
	}
	if strings.TrimSpace(document.RulesetVersion) == "" {
		return fmt.Errorf("ruleset_version is required")
	}
	if document.RulesetVersion != strings.TrimSpace(document.RulesetVersion) || len(document.RulesetVersion) > maximumIdentifierLength {
		return fmt.Errorf("ruleset_version must be trimmed and at most %d bytes", maximumIdentifierLength)
	}
	if _, ok := parsePatch(document.Patch); !ok {
		return fmt.Errorf("invalid profile patch %q", document.Patch)
	}
	if document.AlternativeCount < 2 || document.AlternativeCount > 3 {
		return fmt.Errorf("alternative_count must be between 2 and 3")
	}
	if document.Phases.EarlyEndSeconds <= 0 || document.Phases.MidEndSeconds <= document.Phases.EarlyEndSeconds || document.Phases.MidEndSeconds > 24*60*60 {
		return fmt.Errorf("phase bounds must be positive and ordered")
	}
	if err := itemrules.ValidateContextTuning(document.Context); err != nil {
		return err
	}
	if document.Context.MagicResistFullScale <= 0 {
		return fmt.Errorf("magic_resist_full_scale must be between 1 and 1000")
	}
	if err := itemrules.ValidateStabilityPolicy(document.Stability); err != nil {
		return err
	}
	if len(document.Families) == 0 || len(document.Families) > maximumFamilies {
		return fmt.Errorf("families must contain between 1 and %d entries", maximumFamilies)
	}
	if len(document.Profiles) == 0 || len(document.Profiles) > maximumProfiles {
		return fmt.Errorf("profiles must contain between 1 and %d entries", maximumProfiles)
	}
	if len(document.TagPriority) > maximumTagPriority {
		return fmt.Errorf("tag_priority exceeds %d entries", maximumTagPriority)
	}
	if len(document.ChampionTraits) > maximumChampionTraits {
		return fmt.Errorf("champion_traits exceeds %d entries", maximumChampionTraits)
	}
	if len(document.ItemTraits) > maximumItemTraits {
		return fmt.Errorf("item_traits exceeds %d entries", maximumItemTraits)
	}
	if strings.TrimSpace(document.NeutralProfile) == "" || document.NeutralProfile != strings.TrimSpace(document.NeutralProfile) || len(document.NeutralProfile) > maximumIdentifierLength {
		return fmt.Errorf("neutral_profile must be non-empty, trimmed, and at most %d bytes", maximumIdentifierLength)
	}
	return nil
}

func validateTagPriority(priority []string, known map[string]string) error {
	if len(priority) == 0 {
		return fmt.Errorf("tag_priority must not be empty")
	}
	seen := make(map[string]struct{}, len(priority))
	for _, tag := range priority {
		key := strings.ToLower(strings.TrimSpace(tag))
		if key == "" || tag != strings.TrimSpace(tag) {
			return fmt.Errorf("tag_priority entries must be non-empty and trimmed")
		}
		if _, exists := known[key]; !exists {
			return fmt.Errorf("unknown champion tag %q in tag_priority", tag)
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate champion tag %q in tag_priority", tag)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func compileSelector(profile Profile, knowledge catalog.Catalog, knownTags map[string]string) (compiledProfile, string, error) {
	compiled := compiledProfile{profile: profile}
	if len(profile.Tags) > maximumProfileTags {
		return compiled, "", fmt.Errorf("profile %q exceeds %d tags", profile.ID, maximumProfileTags)
	}
	if len(profile.ExcludedTags) > maximumProfileTags {
		return compiled, "", fmt.Errorf("profile %q exceeds %d excluded tags", profile.ID, maximumProfileTags)
	}
	if len(profile.Families) > maximumProfileFamilies {
		return compiled, "", fmt.Errorf("profile %q exceeds %d families", profile.ID, maximumProfileFamilies)
	}
	if profile.Champion != strings.TrimSpace(profile.Champion) {
		return compiled, "", fmt.Errorf("profile %q champion selector must be trimmed", profile.ID)
	}
	switch profile.Kind {
	case KindChampionRole:
		champion, exists := catalog.ResolveChampion(knowledge, profile.Champion)
		if !exists {
			return compiled, "", fmt.Errorf("profile %q references unknown champion %q", profile.ID, profile.Champion)
		}
		if !profile.Role.Valid() {
			return compiled, "", fmt.Errorf("profile %q has invalid role %q", profile.ID, profile.Role)
		}
		if len(profile.Tags) != 0 || len(profile.ExcludedTags) != 0 {
			return compiled, "", fmt.Errorf("profile %q champion_role selector cannot include tags", profile.ID)
		}
		compiled.championID = champion.ID
		return compiled, "champion_role:" + strings.ToLower(champion.ID) + ":" + string(profile.Role), nil
	case KindChampionDefault:
		champion, exists := catalog.ResolveChampion(knowledge, profile.Champion)
		if !exists {
			return compiled, "", fmt.Errorf("profile %q references unknown champion %q", profile.ID, profile.Champion)
		}
		if profile.Role != "" || len(profile.Tags) != 0 || len(profile.ExcludedTags) != 0 {
			return compiled, "", fmt.Errorf("profile %q champion_default selector cannot include role or tags", profile.ID)
		}
		compiled.championID = champion.ID
		return compiled, "champion_default:" + strings.ToLower(champion.ID), nil
	case KindRoleArchetype:
		if !profile.Role.Valid() {
			return compiled, "", fmt.Errorf("profile %q has invalid role %q", profile.ID, profile.Role)
		}
		if profile.Champion != "" || len(profile.Tags) == 0 {
			return compiled, "", fmt.Errorf("profile %q role_archetype requires tags and no champion", profile.ID)
		}
		compiled.tags = append([]string(nil), profile.Tags...)
		compiled.excludedTags = append([]string(nil), profile.ExcludedTags...)
		if err := validateProfileCompatibilityTags(profile.ID, compiled.tags, compiled.excludedTags, knownTags); err != nil {
			return compiled, "", err
		}
		// One role archetype per role keeps matching deterministic; tags are its
		// compatibility predicate rather than a second ordering mechanism.
		return compiled, "role_archetype:" + string(profile.Role), nil
	case KindTagArchetype:
		if profile.Champion != "" || profile.Role != "" || len(profile.Tags) != 1 {
			return compiled, "", fmt.Errorf("profile %q tag archetype requires exactly one tag", profile.ID)
		}
		compiled.tags = append([]string(nil), profile.Tags...)
		compiled.excludedTags = append([]string(nil), profile.ExcludedTags...)
		if err := validateProfileCompatibilityTags(profile.ID, compiled.tags, compiled.excludedTags, knownTags); err != nil {
			return compiled, "", err
		}
		return compiled, "tag_archetype:" + strings.ToLower(profile.Tags[0]), nil
	case KindNeutral:
		if profile.Champion != "" || profile.Role != "" || len(profile.Tags) != 0 || len(profile.ExcludedTags) != 0 {
			return compiled, "", fmt.Errorf("profile %q neutral selector cannot include champion, role, or tags", profile.ID)
		}
		return compiled, "neutral", nil
	default:
		return compiled, "", fmt.Errorf("profile %q has invalid kind %q", profile.ID, profile.Kind)
	}
}

func effectiveCandidates(profile Profile, families map[string][]Candidate) ([]Candidate, error) {
	seenFamilies := make(map[string]struct{}, len(profile.Families))
	seenIDs := make(map[string]string)
	seenItems := make(map[int]string)
	result := make([]Candidate, 0)
	for _, familyName := range profile.Families {
		if _, exists := seenFamilies[familyName]; exists {
			return nil, fmt.Errorf("profile %q repeats family %q", profile.ID, familyName)
		}
		seenFamilies[familyName] = struct{}{}
		family, exists := families[familyName]
		if !exists {
			return nil, fmt.Errorf("profile %q references missing family %q", profile.ID, familyName)
		}
		for _, candidate := range family {
			if previous, exists := seenIDs[candidate.Rule.ID]; exists {
				return nil, fmt.Errorf("profile %q has duplicate effective candidate ID %q from families %q and %q", profile.ID, candidate.Rule.ID, previous, familyName)
			}
			seenIDs[candidate.Rule.ID] = familyName
			if previous, exists := seenItems[candidate.Rule.ItemID]; exists {
				return nil, fmt.Errorf("profile %q has duplicate effective item %d from families %q and %q", profile.ID, candidate.Rule.ItemID, previous, familyName)
			}
			seenItems[candidate.Rule.ItemID] = familyName
			result = append(result, candidate)
		}
	}
	return result, nil
}

func validateNeutral(profile compiledProfile) error {
	for _, candidate := range profile.candidates {
		if candidate.Rule.Category == itemrules.CategoryCore {
			return fmt.Errorf("neutral profile %q cannot include core candidate %q", profile.profile.ID, candidate.Rule.ID)
		}
		if len(candidate.Activation) == 0 {
			return fmt.Errorf("neutral candidate %q requires explicit activation", candidate.Rule.ID)
		}
		for _, activation := range candidate.Activation {
			if activation.Minimum <= 0 {
				return fmt.Errorf("neutral candidate %q requires a positive activation threshold", candidate.Rule.ID)
			}
			if !neutralDefensiveSignal(activation.Signal) {
				return fmt.Errorf("neutral candidate %q activation %q is not explicit defensive pressure", candidate.Rule.ID, activation.Signal)
			}
		}
		if candidate.Rule.BaseScore > 0 {
			return fmt.Errorf("neutral candidate %q cannot have a positive unconditional score", candidate.Rule.ID)
		}
		for _, score := range candidate.Rule.PhaseScores {
			if score > 0 {
				return fmt.Errorf("neutral candidate %q cannot have a positive phase score", candidate.Rule.ID)
			}
		}
		for _, score := range candidate.Rule.StrategyScores {
			if score > 0 {
				return fmt.Errorf("neutral candidate %q cannot have a positive strategy score", candidate.Rule.ID)
			}
		}
	}
	return nil
}

func neutralDefensiveSignal(signal itemrules.Signal) bool {
	switch signal {
	case itemrules.SignalPhysical, itemrules.SignalMagic, itemrules.SignalHealing, itemrules.SignalShielding,
		itemrules.SignalCrowdControl, itemrules.SignalSlows, itemrules.SignalArmor,
		itemrules.SignalMagicResist, itemrules.SignalBurst, itemrules.SignalSustained:
		return true
	default:
		return false
	}
}

func validateActivation(family string, candidate Candidate) error {
	seen := make(map[itemrules.Signal]struct{}, len(candidate.Activation))
	for _, activation := range candidate.Activation {
		if !itemrules.ValidSignal(activation.Signal) {
			return fmt.Errorf("family %q candidate %q has invalid activation signal %q", family, candidate.Rule.ID, activation.Signal)
		}
		if activation.Minimum < 0 || activation.Minimum > 1000 {
			return fmt.Errorf("family %q candidate %q activation minimum must be between 0 and 1000", family, candidate.Rule.ID)
		}
		if _, exists := seen[activation.Signal]; exists {
			return fmt.Errorf("family %q candidate %q repeats activation signal %q", family, candidate.Rule.ID, activation.Signal)
		}
		seen[activation.Signal] = struct{}{}
	}
	return nil
}

func validateCatalogCandidate(family string, candidate itemrules.CandidateRule, knowledge catalog.Catalog) error {
	item, exists := knowledge.Items[candidate.ItemID]
	if !exists {
		return fmt.Errorf("family %q candidate %q item %d is missing from catalog %s", family, candidate.ID, candidate.ItemID, knowledge.Patch)
	}
	if !item.Purchasable || !item.Gold.Purchasable {
		return fmt.Errorf("family %q candidate %q item %d is not purchasable", family, candidate.ID, candidate.ItemID)
	}
	if !item.AvailableMap11 {
		return fmt.Errorf("family %q candidate %q item %d is not available on map 11", family, candidate.ID, candidate.ItemID)
	}
	if strings.TrimSpace(item.Name) == "" || item.Gold.Total <= 0 {
		return fmt.Errorf("family %q candidate %q item %d has invalid catalog fields", family, candidate.ID, candidate.ItemID)
	}
	return nil
}

func validateCandidateRecipe(family string, candidate itemrules.CandidateRule, knowledge catalog.Catalog) error {
	validated := make(map[int]bool)
	var visit func(int, map[int]bool) error
	visit = func(itemID int, visiting map[int]bool) error {
		if visiting[itemID] {
			return fmt.Errorf("family %q candidate %q recipe cycle at item %d", family, candidate.ID, itemID)
		}
		if validated[itemID] {
			return nil
		}
		item, exists := knowledge.Items[itemID]
		if !exists {
			return fmt.Errorf("family %q candidate %q recipe item %d is missing from catalog %s", family, candidate.ID, itemID, knowledge.Patch)
		}
		if !item.Purchasable || !item.Gold.Purchasable {
			return fmt.Errorf("family %q candidate %q recipe item %d is not purchasable", family, candidate.ID, itemID)
		}
		if !item.AvailableMap11 {
			return fmt.Errorf("family %q candidate %q recipe item %d is not available on map 11", family, candidate.ID, itemID)
		}
		if strings.TrimSpace(item.Name) == "" || item.Gold.Total <= 0 {
			return fmt.Errorf("family %q candidate %q recipe item %d has invalid catalog fields", family, candidate.ID, itemID)
		}
		visiting[itemID] = true
		defer delete(visiting, itemID)
		for _, componentID := range item.From {
			if err := visit(componentID, visiting); err != nil {
				return err
			}
		}
		validated[itemID] = true
		return nil
	}
	return visit(candidate.ItemID, make(map[int]bool))
}

func validateTraits(document Document, knowledge catalog.Catalog) error {
	canonical := make(map[string]string, len(document.ChampionTraits))
	for reference, traits := range document.ChampionTraits {
		champion, exists := catalog.ResolveChampion(knowledge, reference)
		if !exists {
			return fmt.Errorf("unknown champion trait reference %q", reference)
		}
		key := strings.ToLower(champion.ID)
		if previous, exists := canonical[key]; exists {
			return fmt.Errorf("champion trait references %q and %q resolve to the same champion", previous, reference)
		}
		canonical[key] = reference
		if err := itemrules.ValidateTraitProfile("champion "+reference, traits); err != nil {
			return err
		}
	}
	for itemID, traits := range document.ItemTraits {
		item, exists := knowledge.Items[itemID]
		if !exists || !item.Purchasable || !item.Gold.Purchasable || !item.AvailableMap11 {
			return fmt.Errorf("item trait %d is not a valid map-11 catalog item", itemID)
		}
		if err := itemrules.ValidateTraitProfile(fmt.Sprintf("item %d", itemID), traits); err != nil {
			return err
		}
	}
	return nil
}

func validatePlanCopy(subject string, copy PlanCopy) error {
	if copy.Source != PlanSourceProfileCurated {
		return fmt.Errorf("%s source must be %q", subject, PlanSourceProfileCurated)
	}
	if err := validatePlainText(subject+" summary", copy.Summary, true); err != nil {
		return err
	}
	if len(copy.Options) == 0 || len(copy.Options) > maximumCopyOptions {
		return fmt.Errorf("%s must contain between 1 and %d options", subject, maximumCopyOptions)
	}
	seen := make(map[string]struct{}, len(copy.Options))
	for _, option := range copy.Options {
		if err := validateStableID(subject+" option ID", option.ID); err != nil {
			return err
		}
		if _, exists := seen[option.ID]; exists {
			return fmt.Errorf("%s repeats option ID %q", subject, option.ID)
		}
		seen[option.ID] = struct{}{}
		for label, value := range map[string]string{"label": option.Label, "when": option.When, "tradeoff": option.Tradeoff} {
			if err := validatePlainText(subject+" "+label, value, true); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateFallbackPlanCopy(profile Profile) error {
	for label, copy := range map[string]PlanCopy{
		"runes": profile.Runes, "summoners": profile.Summoners, "initial plan": profile.InitialPlan,
	} {
		lower := strings.ToLower(copy.Summary)
		if !strings.Contains(lower, "genéric") && !strings.Contains(lower, "asum") {
			return fmt.Errorf("profile %q %s summary must identify generic or assumed fallback copy", profile.ID, label)
		}
	}
	return nil
}

func validateStableID(subject, value string) error {
	if value == "" || value != strings.TrimSpace(value) || len(value) > maximumIdentifierLength {
		return fmt.Errorf("%s must be non-empty, trimmed, and at most %d bytes", subject, maximumIdentifierLength)
	}
	for index, character := range value {
		valid := character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '_' || character == '-'
		if !valid || index == 0 && (character == '_' || character == '-') {
			return fmt.Errorf("%s must use lowercase ASCII letters, digits, underscores, or hyphens", subject)
		}
	}
	return nil
}

func validatePlainText(subject, value string, required bool) error {
	if required && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", subject)
	}
	if value != strings.TrimSpace(value) {
		return fmt.Errorf("%s must be trimmed", subject)
	}
	if len(value) > maximumCopyLength {
		return fmt.Errorf("%s exceeds %d bytes", subject, maximumCopyLength)
	}
	if strings.ContainsAny(value, "<>") {
		return fmt.Errorf("%s must be plain text", subject)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("%s must be plain text", subject)
		}
	}
	normalized := normalizeClaimText(value)
	for _, forbidden := range []string{
		"win rate", "win rates", "winrate", "winrates",
		"pick rate", "pick rates", "pickrate", "pickrates",
		"selection rate", "selection rates",
		"win percentage", "win percentages", "winning percentage", "winning percentages",
		"pick percentage", "pick percentages",
		"tier", "tiers", "tier list", "tier lists", "tierlist", "tierlists",
		"best", "best build", "best builds", "bestbuild", "bestbuilds",
		"mejor", "mejores",
		"tasa de victoria", "tasas de victoria", "tasa de victorias", "tasas de victorias",
		"tasa de selección", "tasas de selección", "tasa de selecciones", "tasas de selecciones",
		"tasa de seleccion", "tasas de seleccion", "tasa de selecciones", "tasas de selecciones",
		"porcentaje de victoria", "porcentajes de victoria", "porcentaje de victorias", "porcentajes de victorias",
		"porcentaje de selección", "porcentajes de selección", "porcentaje de selecciones", "porcentajes de selecciones",
		"porcentaje de seleccion", "porcentajes de seleccion", "porcentaje de selecciones", "porcentajes de selecciones",
	} {
		if containsNormalizedPhrase(normalized, forbidden) {
			return fmt.Errorf("%s must not contain meta-performance claims", subject)
		}
	}
	return nil
}

func normalizeClaimText(value string) string {
	var normalized strings.Builder
	separated := true
	for _, character := range strings.ToLower(value) {
		if unicode.Is(unicode.M, character) {
			continue
		}
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			normalized.WriteRune(character)
			separated = false
			continue
		}
		if !separated {
			normalized.WriteByte(' ')
			separated = true
		}
	}
	return strings.TrimSpace(normalized.String())
}

func containsNormalizedPhrase(value, phrase string) bool {
	normalizedPhrase := normalizeClaimText(phrase)
	if value == "" || normalizedPhrase == "" {
		return false
	}
	return strings.Contains(" "+value+" ", " "+normalizedPhrase+" ")
}

func validateProfileTags(profile string, tags []string, known map[string]string) error {
	seen := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		if tag != strings.TrimSpace(tag) {
			return fmt.Errorf("profile %q champion tag %q must be trimmed", profile, tag)
		}
		key := strings.ToLower(strings.TrimSpace(tag))
		if _, exists := known[key]; !exists {
			return fmt.Errorf("profile %q references unknown champion tag %q", profile, tag)
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("profile %q repeats champion tag %q", profile, tag)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateProfileCompatibilityTags(profile string, required, excluded []string, known map[string]string) error {
	if err := validateProfileTags(profile, required, known); err != nil {
		return err
	}
	if err := validateProfileTags(profile, excluded, known); err != nil {
		return err
	}
	for _, tag := range required {
		if containsFold(excluded, tag) {
			return fmt.Errorf("profile %q champion tag %q cannot be both required and excluded", profile, tag)
		}
	}
	return nil
}

func catalogTags(knowledge catalog.Catalog) map[string]string {
	result := make(map[string]string)
	for _, champion := range knowledge.Champions {
		for _, tag := range champion.Tags {
			result[strings.ToLower(strings.TrimSpace(tag))] = tag
		}
	}
	return result
}

func cloneDocument(source Document) Document {
	clone := source
	clone.TagPriority = append([]string(nil), source.TagPriority...)
	clone.Context = cloneContext(source.Context)
	clone.ChampionTraits = cloneTraits(source.ChampionTraits)
	clone.ItemTraits = cloneItemTraits(source.ItemTraits)
	clone.Families = make(map[string]CandidateFamily, len(source.Families))
	for key, family := range source.Families {
		clone.Families[key] = CandidateFamily{Candidates: cloneCandidates(family.Candidates)}
	}
	clone.Profiles = make([]Profile, len(source.Profiles))
	for index, profile := range source.Profiles {
		clone.Profiles[index] = cloneProfile(profile)
	}
	return clone
}
