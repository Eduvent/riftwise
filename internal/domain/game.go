package domain

import "time"

// TrackerStatus describes the backend's view of the local League client.
type TrackerStatus string

const (
	StatusWaitingForGame TrackerStatus = "waiting_for_game"
	StatusInGame         TrackerStatus = "in_game"
	StatusStale          TrackerStatus = "stale"
)

// PlayerIdentity contains the stable identity fields exposed by the Live Client API.
type PlayerIdentity struct {
	RiotID       string `json:"riot_id,omitempty"`
	GameName     string `json:"game_name,omitempty"`
	TagLine      string `json:"tag_line,omitempty"`
	SummonerName string `json:"summoner_name,omitempty"`
}

// Score is the minimum scoreboard data needed by the recommendation engine.
type Score struct {
	Kills      int `json:"kills"`
	Deaths     int `json:"deaths"`
	Assists    int `json:"assists"`
	CreepScore int `json:"creep_score"`
}

// Item is an item stack currently held by a player.
type Item struct {
	ID          int    `json:"id"`
	DisplayName string `json:"display_name"`
	Count       int    `json:"count"`
	Price       int    `json:"price"`
	Slot        int    `json:"slot"`
}

// Player is a normalized participant independent of Riot's response schema.
type Player struct {
	Identity     PlayerIdentity `json:"identity"`
	ChampionName string         `json:"champion_name"`
	Team         string         `json:"team"`
	Position     string         `json:"position,omitempty"`
	Level        int            `json:"level"`
	Score        Score          `json:"score"`
	Items        []Item         `json:"items"`
	IsDead       bool           `json:"is_dead"`
	RespawnTimer float64        `json:"respawn_timer"`
}

// ActivePlayer adds private local-player data that is not available for every participant.
type ActivePlayer struct {
	Identity     PlayerIdentity `json:"identity"`
	ChampionName string         `json:"champion_name"`
	Team         string         `json:"team"`
	Position     string         `json:"position,omitempty"`
	Level        int            `json:"level"`
	CurrentGold  float64        `json:"current_gold"`
	Score        Score          `json:"score"`
	Items        []Item         `json:"items"`
	IsDead       bool           `json:"is_dead"`
	RespawnTimer float64        `json:"respawn_timer"`
}

// GameState is the normalized state consumed by future analysis and recommendation packages.
type GameState struct {
	GameTimeSeconds float64       `json:"game_time_seconds"`
	GameMode        string        `json:"game_mode"`
	GameVersion     string        `json:"game_version,omitempty"`
	MapNumber       int           `json:"map_number,omitempty"`
	MapName         string        `json:"map_name,omitempty"`
	ActivePlayer    ActivePlayer  `json:"active_player"`
	Players         []Player      `json:"players"`
	SourceEvents    []SourceEvent `json:"source_events,omitempty"`
}

// Snapshot is the observable tracker state returned by HTTP and SSE.
type Snapshot struct {
	Status           TrackerStatus   `json:"status"`
	Game             *GameState      `json:"game,omitempty"`
	UpdatedAt        time.Time       `json:"updated_at"`
	LastSuccessAt    *time.Time      `json:"last_success_at,omitempty"`
	LastError        string          `json:"last_error,omitempty"`
	Revision         uint64          `json:"revision"`
	DecisionRevision uint64          `json:"decision_revision"`
	Connection       ConnectionState `json:"connection"`
	Session          *MatchSession   `json:"session,omitempty"`
	RecentEvents     []SemanticEvent `json:"recent_events,omitempty"`
}
