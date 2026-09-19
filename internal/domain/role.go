package domain

import "strings"

// Role is the canonical position used by profile resolution. Unknown or
// missing roles intentionally remain invalid rather than being inferred from
// champion identity.
type Role string

const (
	RoleTop     Role = "top"
	RoleJungle  Role = "jungle"
	RoleMiddle  Role = "middle"
	RoleBottom  Role = "bottom"
	RoleSupport Role = "support"
)

// ParseRole normalizes the explicit values used by Live Client and manual
// callers. It never guesses a role from any other field.
func ParseRole(value string) (Role, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "top":
		return RoleTop, true
	case "jungle":
		return RoleJungle, true
	case "middle", "mid":
		return RoleMiddle, true
	case "bottom", "adc", "bot":
		return RoleBottom, true
	case "utility", "support", "sup":
		return RoleSupport, true
	default:
		return "", false
	}
}

// Valid reports whether role is one of the stable canonical positions.
func (role Role) Valid() bool {
	switch role {
	case RoleTop, RoleJungle, RoleMiddle, RoleBottom, RoleSupport:
		return true
	default:
		return false
	}
}
