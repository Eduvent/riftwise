// Package riotapi provides the narrow, privacy-conscious Riot Account-V1 and
// Match-V5 boundary used by optional local enrichment. It never persists raw
// responses or credentials.
package riotapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"lolengine/internal/domain"
)

const redactedSecret = "[REDACTED]"

type Mode string

const (
	ModeDisabled Mode = "disabled"
	ModeDirect   Mode = "direct"
	ModeBackend  Mode = "backend"
)

func (mode Mode) Valid() bool {
	return mode == ModeDisabled || mode == ModeDirect || mode == ModeBackend
}

func ValidMatchType(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "ranked", "normal", "tourney", "tutorial":
		return true
	default:
		return false
	}
}

// Secret is an API credential that is deliberately redacted by every common
// formatting and JSON path. The raw value is only read inside the direct HTTP
// adapter when building X-Riot-Token.
type Secret string

func (Secret) String() string   { return redactedSecret }
func (Secret) GoString() string { return redactedSecret }
func (Secret) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, redactedSecret)
}
func (Secret) MarshalJSON() ([]byte, error) {
	return json.Marshal(redactedSecret)
}

// Region is an officially documented Match-V5/Account-V1 routing cluster.
type Region string

const (
	RegionAmericas Region = "americas"
	RegionAsia     Region = "asia"
	RegionEurope   Region = "europe"
	RegionSEA      Region = "sea"
)

func (region Region) Valid() bool {
	switch region {
	case RegionAmericas, RegionAsia, RegionEurope, RegionSEA:
		return true
	default:
		return false
	}
}

func ParseRegion(value string) (Region, bool) {
	region := Region(strings.ToLower(strings.TrimSpace(value)))
	return region, region.Valid()
}

func (region Region) Host() (string, bool) {
	if !region.Valid() {
		return "", false
	}
	return string(region) + ".api.riotgames.com", true
}

// Platform is a documented LoL platform routing identifier. The mapping is
// intentionally allowlisted; new Riot platforms must be added explicitly.
type Platform string

const (
	PlatformBR1  Platform = "BR1"
	PlatformEUN1 Platform = "EUN1"
	PlatformEUW1 Platform = "EUW1"
	PlatformJP1  Platform = "JP1"
	PlatformKR   Platform = "KR"
	PlatformLA1  Platform = "LA1"
	PlatformLA2  Platform = "LA2"
	PlatformME1  Platform = "ME1"
	PlatformNA1  Platform = "NA1"
	PlatformOC1  Platform = "OC1"
	PlatformTR1  Platform = "TR1"
	PlatformRU   Platform = "RU"
	PlatformPH2  Platform = "PH2"
	PlatformSG2  Platform = "SG2"
	PlatformTH2  Platform = "TH2"
	PlatformTW2  Platform = "TW2"
	PlatformVN2  Platform = "VN2"
)

func ParsePlatform(value string) (Platform, bool) {
	platform := Platform(strings.ToUpper(strings.TrimSpace(value)))
	_, ok := platform.Region()
	return platform, ok
}

func (platform Platform) Region() (Region, bool) {
	switch platform {
	case PlatformBR1, PlatformLA1, PlatformLA2, PlatformNA1:
		return RegionAmericas, true
	case PlatformJP1, PlatformKR:
		return RegionAsia, true
	case PlatformEUN1, PlatformEUW1, PlatformTR1, PlatformRU, PlatformME1:
		return RegionEurope, true
	case PlatformOC1, PlatformPH2, PlatformSG2, PlatformTH2, PlatformTW2, PlatformVN2:
		return RegionSEA, true
	default:
		return "", false
	}
}

// Account is the narrow Account-V1 response. Unicode and case are preserved.
type Account struct {
	PUUID    string `json:"puuid"`
	GameName string `json:"game_name"`
	TagLine  string `json:"tag_line"`
}

type MatchListQuery struct {
	Start     int
	Count     int
	StartTime int64
	EndTime   int64
	Queue     int
	Type      string
}

type RoleProvenance string

const (
	RoleUnknown                RoleProvenance = "unknown"
	RoleFromTeamPosition       RoleProvenance = "team_position"
	RoleFromIndividualPosition RoleProvenance = "individual_position"
	RoleFromLaneRole           RoleProvenance = "lane_role_fallback"
)

type OpponentProvenance string

const (
	OpponentUnknown        OpponentProvenance = "unknown"
	OpponentUniqueSameRole OpponentProvenance = "unique_same_role"
)

type FinalItem struct {
	Slot   int `json:"slot"`
	ItemID int `json:"item_id"`
}

// LinkedPlayerFacts contains only the explicitly linked participant and the
// optional champion name of one unambiguous same-role opponent. It never
// contains another participant's Riot ID or PUUID.
type LinkedPlayerFacts struct {
	ChampionName       string             `json:"champion_name"`
	Role               domain.Role        `json:"role,omitempty"`
	RoleProvenance     RoleProvenance     `json:"role_provenance"`
	TeamID             int                `json:"team_id"`
	Win                bool               `json:"win"`
	Kills              int                `json:"kills"`
	Deaths             int                `json:"deaths"`
	Assists            int                `json:"assists"`
	CreepScore         int                `json:"creep_score"`
	FinalItems         []FinalItem        `json:"final_items"`
	OpponentChampion   string             `json:"opponent_champion,omitempty"`
	OpponentProvenance OpponentProvenance `json:"opponent_provenance"`
}

// Match is the normalized privacy-minimized Match-V5 record.
type Match struct {
	ID              string            `json:"id"`
	Platform        Platform          `json:"platform"`
	QueueID         int               `json:"queue_id"`
	MapID           int               `json:"map_id"`
	Patch           string            `json:"patch"`
	StartedAt       time.Time         `json:"started_at"`
	EndedAt         time.Time         `json:"ended_at"`
	DurationSeconds int64             `json:"duration_seconds"`
	Player          LinkedPlayerFacts `json:"player"`
	ContentHash     string            `json:"content_hash"`
}

type ErrorKind string

const (
	ErrorBadRequest   ErrorKind = "bad_request"
	ErrorUnauthorized ErrorKind = "unauthorized"
	ErrorForbidden    ErrorKind = "forbidden"
	ErrorNotFound     ErrorKind = "not_found"
	ErrorRateLimited  ErrorKind = "rate_limited"
	ErrorServer       ErrorKind = "server_error"
	ErrorMalformed    ErrorKind = "malformed_response"
	ErrorTransport    ErrorKind = "transport"
	ErrorCancelled    ErrorKind = "cancelled"
)

// Error is deliberately body-free and credential-free.
type Error struct {
	Kind       ErrorKind
	Operation  string
	StatusCode int
	RetryAfter time.Duration
	Cause      error
}

func (err *Error) Error() string {
	if err == nil {
		return "<nil>"
	}
	message := fmt.Sprintf("riot api %s: %s", err.Operation, err.Kind)
	if err.StatusCode != 0 {
		message += fmt.Sprintf(" (HTTP %d)", err.StatusCode)
	}
	return message
}

func (err *Error) Unwrap() error { return err.Cause }

func KindOf(err error) ErrorKind {
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return apiErr.Kind
	}
	return ""
}
