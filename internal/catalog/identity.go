package catalog

import (
	"sort"
	"strings"
	"unicode"
)

// ResolveChampion maps a Live Client display name or Data Dragon key/name to
// the canonical catalog champion record. Punctuation and spacing differences
// are ignored only after exact and case-insensitive matches are attempted.
func ResolveChampion(knowledge Catalog, identity string) (Champion, bool) {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return Champion{}, false
	}
	if champion, exists := knowledge.Champions[identity]; exists {
		return champion, true
	}
	keys := make([]string, 0, len(knowledge.Champions))
	for key := range knowledge.Champions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		champion := knowledge.Champions[key]
		if strings.EqualFold(key, identity) || strings.EqualFold(champion.ID, identity) || strings.EqualFold(champion.Name, identity) {
			return champion, true
		}
	}
	normalized := normalizeChampionIdentity(identity)
	if normalized == "" {
		return Champion{}, false
	}
	for _, key := range keys {
		champion := knowledge.Champions[key]
		if normalizeChampionIdentity(key) == normalized || normalizeChampionIdentity(champion.ID) == normalized || normalizeChampionIdentity(champion.Name) == normalized {
			return champion, true
		}
	}
	return Champion{}, false
}

func normalizeChampionIdentity(value string) string {
	var builder strings.Builder
	for _, character := range strings.ToLower(strings.TrimSpace(value)) {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			builder.WriteRune(character)
		}
	}
	return builder.String()
}
