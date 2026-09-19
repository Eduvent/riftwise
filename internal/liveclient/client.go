package liveclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"lolengine/internal/domain"
)

const (
	allGameDataPath = "/liveclientdata/allgamedata"
	maxResponseSize = 2 << 20
)

// ErrGameUnavailable identifies a valid HTTP response without active game data.
var ErrGameUnavailable = errors.New("live game data unavailable")

// ErrInvalidPayload identifies a successful response that is not valid Live Client game data.
var ErrInvalidPayload = errors.New("invalid live client payload")

// ErrTransport identifies a failure to exchange a complete HTTP response.
var ErrTransport = errors.New("live client transport failure")

// ErrTimeout identifies a transport failure caused by a deadline.
var ErrTimeout = errors.New("live client timeout")

type fetchError struct {
	class domain.FetchErrorClass
	op    string
	err   error
}

func (err *fetchError) Error() string {
	if err == nil {
		return ""
	}
	return err.op + ": " + err.err.Error()
}

func (err *fetchError) Unwrap() error { return err.err }

func (err *fetchError) FetchErrorClass() domain.FetchErrorClass { return err.class }

func classifiedError(class domain.FetchErrorClass, op string, marker, cause error) error {
	if cause == nil {
		cause = marker
	} else if marker != nil {
		cause = errors.Join(marker, cause)
	}
	return &fetchError{class: class, op: op, err: cause}
}

// Client reads and normalizes Riot Live Client Data from the local game process.
type Client struct {
	endpoint   *url.URL
	httpClient *http.Client
}

// New creates a client for a loopback Live Client endpoint. Riot serves this endpoint
// with a self-signed certificate, so certificate verification is disabled only after
// the configured URL has been proven to target loopback. Redirects cannot leave loopback.
func New(baseURL string) (*Client, error) {
	base, err := validateLoopbackURL(baseURL)
	if err != nil {
		return nil, err
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // Riot's loopback Live Client endpoint uses a self-signed certificate.
	}
	dialer := &net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}
	transport.DialContext = newLoopbackDialContext(net.DefaultResolver, dialer.DialContext)

	httpClient := &http.Client{
		Timeout:   3 * time.Second,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many redirects")
			}
			if _, err := validateLoopbackURL(req.URL.String()); err != nil {
				return fmt.Errorf("reject live client redirect: %w", err)
			}
			return nil
		},
	}

	return newWithParsedBase(base, httpClient), nil
}

// newWithHTTPClient creates a loopback adapter with a caller-provided HTTP client
// for deterministic package tests without exposing an unhardened production path.
func newWithHTTPClient(baseURL string, httpClient *http.Client) (*Client, error) {
	base, err := validateLoopbackURL(baseURL)
	if err != nil {
		return nil, err
	}
	if httpClient == nil {
		return nil, errors.New("HTTP client must not be nil")
	}
	return newWithParsedBase(base, httpClient), nil
}

func newWithParsedBase(base *url.URL, httpClient *http.Client) *Client {
	endpoint := *base
	endpoint.Path = allGameDataPath
	endpoint.RawPath = ""
	endpoint.RawQuery = ""
	endpoint.Fragment = ""
	return &Client{endpoint: &endpoint, httpClient: httpClient}
}

// Fetch returns a domain-normalized game state.
func (c *Client) Fetch(ctx context.Context) (domain.GameState, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint.String(), nil)
	if err != nil {
		return domain.GameState{}, fmt.Errorf("create live client request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		class, marker := domain.FetchTransport, ErrTransport
		var networkError net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &networkError) && networkError.Timeout()) {
			class, marker = domain.FetchTimeout, ErrTimeout
		}
		return domain.GameState{}, classifiedError(class, "fetch live client data", marker, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return domain.GameState{}, classifiedError(domain.FetchGameUnavailable, "fetch live client data", ErrGameUnavailable, fmt.Errorf("HTTP %d", resp.StatusCode))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		class, marker := domain.FetchTransport, ErrTransport
		var networkError net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &networkError) && networkError.Timeout()) {
			class, marker = domain.FetchTimeout, ErrTimeout
		}
		return domain.GameState{}, classifiedError(class, "read live client data", marker, err)
	}
	if len(body) > maxResponseSize {
		return domain.GameState{}, classifiedError(domain.FetchInvalidPayload, "read live client data", ErrInvalidPayload, fmt.Errorf("response exceeds %d bytes", maxResponseSize))
	}

	var payload allGameDataResponse
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&payload); err != nil {
		return domain.GameState{}, classifiedError(domain.FetchInvalidPayload, "decode live client response", ErrInvalidPayload, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return domain.GameState{}, classifiedError(domain.FetchInvalidPayload, "decode live client response", ErrInvalidPayload, errors.New("trailing JSON value"))
		}
		return domain.GameState{}, classifiedError(domain.FetchInvalidPayload, "decode live client response", ErrInvalidPayload, fmt.Errorf("trailing data: %w", err))
	}
	if err := payload.validate(); err != nil {
		return domain.GameState{}, classifiedError(domain.FetchInvalidPayload, "validate live client response", ErrInvalidPayload, err)
	}
	return normalize(payload), nil
}

func validateLoopbackURL(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, fmt.Errorf("parse live client URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("live client URL must use http or https")
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("live client URL must not contain user information")
	}
	hostname := parsed.Hostname()
	if hostname == "" {
		return nil, fmt.Errorf("live client URL must include a host")
	}
	if !isLoopbackHost(hostname) {
		return nil, fmt.Errorf("live client URL must target loopback, got %q", hostname)
	}
	return parsed, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

type ipResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type dialContextFunc func(context.Context, string, string) (net.Conn, error)

func newLoopbackDialContext(resolver ipResolver, dial dialContextFunc) dialContextFunc {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("parse dial address %q: %w", address, err)
		}

		var addresses []net.IPAddr
		if ip := net.ParseIP(host); ip != nil {
			addresses = []net.IPAddr{{IP: ip}}
		} else {
			addresses, err = resolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("resolve loopback host %q: %w", host, err)
			}
		}
		if len(addresses) == 0 {
			return nil, fmt.Errorf("loopback host %q resolved to no addresses", host)
		}
		for _, candidate := range addresses {
			if candidate.IP == nil || !candidate.IP.IsLoopback() {
				return nil, fmt.Errorf("host %q resolved to non-loopback address %q", host, candidate.IP)
			}
		}

		var dialErrors []error
		for _, candidate := range addresses {
			if network == "tcp4" && candidate.IP.To4() == nil {
				continue
			}
			if network == "tcp6" && candidate.IP.To4() != nil {
				continue
			}
			resolvedAddress := net.JoinHostPort(candidate.IP.String(), port)
			connection, err := dial(ctx, network, resolvedAddress)
			if err == nil {
				return connection, nil
			}
			dialErrors = append(dialErrors, fmt.Errorf("dial %s: %w", resolvedAddress, err))
			if ctx.Err() != nil {
				break
			}
		}
		if len(dialErrors) == 0 {
			return nil, fmt.Errorf("host %q has no addresses compatible with %s", host, network)
		}
		return nil, errors.Join(dialErrors...)
	}
}

type allGameDataResponse struct {
	ActivePlayer *activePlayerResponse `json:"activePlayer"`
	AllPlayers   []playerResponse      `json:"allPlayers"`
	Events       *eventsResponse       `json:"events"`
	GameData     *gameDataResponse     `json:"gameData"`
}

type eventsResponse struct {
	Events []json.RawMessage `json:"Events"`
}

type activePlayerResponse struct {
	CurrentGold  float64 `json:"currentGold"`
	Level        int     `json:"level"`
	RiotID       string  `json:"riotId"`
	GameName     string  `json:"riotIdGameName"`
	TagLine      string  `json:"riotIdTagLine"`
	SummonerName string  `json:"summonerName"`
}

type playerResponse struct {
	ChampionName string         `json:"championName"`
	Items        []itemResponse `json:"items"`
	Level        int            `json:"level"`
	RiotID       string         `json:"riotId"`
	GameName     string         `json:"riotIdGameName"`
	TagLine      string         `json:"riotIdTagLine"`
	Scores       *scoreResponse `json:"scores"`
	SummonerName string         `json:"summonerName"`
	Team         string         `json:"team"`
	Position     string         `json:"position"`
	IsDead       bool           `json:"isDead"`
	RespawnTimer float64        `json:"respawnTimer"`
}

type itemResponse struct {
	Count       int    `json:"count"`
	DisplayName string `json:"displayName"`
	ItemID      int    `json:"itemID"`
	Price       int    `json:"price"`
	Slot        int    `json:"slot"`
}

type scoreResponse struct {
	Assists    int `json:"assists"`
	CreepScore int `json:"creepScore"`
	Deaths     int `json:"deaths"`
	Kills      int `json:"kills"`
}

type gameDataResponse struct {
	GameMode    string  `json:"gameMode"`
	GameTime    float64 `json:"gameTime"`
	GameVersion string  `json:"gameVersion"`
	MapNumber   int     `json:"mapNumber"`
	MapName     string  `json:"mapName"`
}

func (payload allGameDataResponse) validate() error {
	if payload.ActivePlayer == nil {
		return errors.New("activePlayer is required")
	}
	if payload.GameData == nil {
		return errors.New("gameData is required")
	}
	if strings.TrimSpace(payload.GameData.GameMode) == "" {
		return errors.New("gameData.gameMode is required")
	}
	if payload.GameData.MapNumber <= 0 {
		return errors.New("gameData.mapNumber must be positive")
	}
	if len(payload.AllPlayers) == 0 {
		return errors.New("allPlayers must contain participants")
	}
	if payload.ActivePlayer.Level <= 0 {
		return errors.New("activePlayer.level must be positive")
	}

	activeIdentity := normalizeIdentity(
		payload.ActivePlayer.RiotID,
		payload.ActivePlayer.GameName,
		payload.ActivePlayer.TagLine,
		payload.ActivePlayer.SummonerName,
	)
	if activeIdentity.RiotID == "" && activeIdentity.SummonerName == "" {
		return errors.New("activePlayer identity is required")
	}
	activeMatched := false
	for index, player := range payload.AllPlayers {
		playerIdentity := normalizeIdentity(player.RiotID, player.GameName, player.TagLine, player.SummonerName)
		if playerIdentity.RiotID == "" && playerIdentity.SummonerName == "" {
			return fmt.Errorf("allPlayers[%d] identity is required", index)
		}
		if strings.TrimSpace(player.ChampionName) == "" {
			return fmt.Errorf("allPlayers[%d].championName is required", index)
		}
		if strings.TrimSpace(player.Team) == "" {
			return fmt.Errorf("allPlayers[%d].team is required", index)
		}
		if player.Level <= 0 {
			return fmt.Errorf("allPlayers[%d].level must be positive", index)
		}
		if player.Scores == nil {
			return fmt.Errorf("allPlayers[%d].scores is required", index)
		}
		if player.Items == nil {
			return fmt.Errorf("allPlayers[%d].items is required", index)
		}
		if identitiesMatch(activeIdentity, playerIdentity) {
			activeMatched = true
		}
	}
	if !activeMatched {
		return errors.New("activePlayer does not match an allPlayers participant")
	}
	return nil
}

func normalize(payload allGameDataResponse) domain.GameState {
	players := make([]domain.Player, 0, len(payload.AllPlayers))
	for _, player := range payload.AllPlayers {
		players = append(players, normalizePlayer(player))
	}

	activeIdentity := normalizeIdentity(
		payload.ActivePlayer.RiotID,
		payload.ActivePlayer.GameName,
		payload.ActivePlayer.TagLine,
		payload.ActivePlayer.SummonerName,
	)
	active := domain.ActivePlayer{
		Identity:    activeIdentity,
		Level:       payload.ActivePlayer.Level,
		CurrentGold: payload.ActivePlayer.CurrentGold,
		Items:       []domain.Item{},
	}

	for _, player := range players {
		if identitiesMatch(activeIdentity, player.Identity) {
			active.Identity = mergeIdentity(active.Identity, player.Identity)
			active.ChampionName = player.ChampionName
			active.Team = player.Team
			active.Position = player.Position
			active.Score = player.Score
			active.Items = cloneItems(player.Items)
			active.IsDead = player.IsDead
			active.RespawnTimer = player.RespawnTimer
			if active.Level == 0 {
				active.Level = player.Level
			}
			break
		}
	}

	return domain.GameState{
		GameTimeSeconds: payload.GameData.GameTime,
		GameMode:        payload.GameData.GameMode,
		GameVersion:     payload.GameData.GameVersion,
		MapNumber:       payload.GameData.MapNumber,
		MapName:         payload.GameData.MapName,
		ActivePlayer:    active,
		Players:         players,
		SourceEvents:    normalizeSourceEvents(payload.Events),
	}
}

func normalizePlayer(player playerResponse) domain.Player {
	items := make([]domain.Item, 0, len(player.Items))
	for _, item := range player.Items {
		items = append(items, domain.Item{
			ID:          item.ItemID,
			DisplayName: item.DisplayName,
			Count:       item.Count,
			Price:       item.Price,
			Slot:        item.Slot,
		})
	}
	return domain.Player{
		Identity:     normalizeIdentity(player.RiotID, player.GameName, player.TagLine, player.SummonerName),
		ChampionName: player.ChampionName,
		Team:         player.Team,
		Position:     player.Position,
		Level:        player.Level,
		Score: domain.Score{
			Kills:      player.Scores.Kills,
			Deaths:     player.Scores.Deaths,
			Assists:    player.Scores.Assists,
			CreepScore: player.Scores.CreepScore,
		},
		Items:        items,
		IsDead:       player.IsDead,
		RespawnTimer: player.RespawnTimer,
	}
}

func normalizeSourceEvents(source *eventsResponse) []domain.SourceEvent {
	if source == nil || len(source.Events) == 0 {
		return nil
	}
	byKey := make(map[string]domain.SourceEvent, len(source.Events))
	for _, raw := range source.Events {
		var envelope struct {
			ID       int64   `json:"EventID"`
			Name     string  `json:"EventName"`
			GameTime float64 `json:"EventTime"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil || strings.TrimSpace(envelope.Name) == "" {
			continue
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			continue
		}
		delete(fields, "EventID")
		delete(fields, "EventName")
		delete(fields, "EventTime")
		payload, _ := json.Marshal(fields)
		key := ""
		if envelope.ID != 0 {
			key = fmt.Sprintf("source:%d", envelope.ID)
		} else {
			canonical, _ := json.Marshal(struct {
				Name     string          `json:"name"`
				GameTime float64         `json:"game_time"`
				Payload  json.RawMessage `json:"payload"`
			}{Name: strings.TrimSpace(envelope.Name), GameTime: envelope.GameTime, Payload: payload})
			digest := sha256.Sum256(canonical)
			key = "source-hash:" + hex.EncodeToString(digest[:8])
		}
		candidate := domain.SourceEvent{
			ID: envelope.ID, Name: strings.TrimSpace(envelope.Name), GameTimeSeconds: envelope.GameTime,
			DedupeKey: key, Payload: payload,
		}
		previous, exists := byKey[key]
		candidateCanonical, _ := json.Marshal(candidate)
		previousCanonical, _ := json.Marshal(previous)
		if !exists || bytes.Compare(candidateCanonical, previousCanonical) < 0 {
			byKey[key] = candidate
		}
	}
	events := make([]domain.SourceEvent, 0, len(byKey))
	for _, event := range byKey {
		events = append(events, event)
	}
	sort.Slice(events, func(left, right int) bool {
		if events[left].ID != events[right].ID {
			return events[left].ID < events[right].ID
		}
		if events[left].GameTimeSeconds != events[right].GameTimeSeconds {
			return events[left].GameTimeSeconds < events[right].GameTimeSeconds
		}
		if events[left].Name != events[right].Name {
			return events[left].Name < events[right].Name
		}
		return events[left].DedupeKey < events[right].DedupeKey
	})
	return events
}

func normalizeIdentity(riotID, gameName, tagLine, summonerName string) domain.PlayerIdentity {
	identity := domain.PlayerIdentity{
		RiotID:       riotID,
		GameName:     gameName,
		TagLine:      tagLine,
		SummonerName: summonerName,
	}
	if identity.RiotID == "" && identity.GameName != "" && identity.TagLine != "" {
		identity.RiotID = identity.GameName + "#" + identity.TagLine
	}
	if identity.RiotID != "" && (identity.GameName == "" || identity.TagLine == "") {
		if separator := strings.LastIndex(identity.RiotID, "#"); separator > 0 && separator < len(identity.RiotID)-1 {
			if identity.GameName == "" {
				identity.GameName = identity.RiotID[:separator]
			}
			if identity.TagLine == "" {
				identity.TagLine = identity.RiotID[separator+1:]
			}
		}
	}
	return identity
}

func identitiesMatch(left, right domain.PlayerIdentity) bool {
	if left.RiotID != "" && right.RiotID != "" && strings.EqualFold(left.RiotID, right.RiotID) {
		return true
	}
	if left.GameName != "" && left.TagLine != "" && right.GameName != "" && right.TagLine != "" {
		return strings.EqualFold(left.GameName, right.GameName) && strings.EqualFold(left.TagLine, right.TagLine)
	}
	return left.SummonerName != "" && right.SummonerName != "" && strings.EqualFold(left.SummonerName, right.SummonerName)
}

func mergeIdentity(primary, fallback domain.PlayerIdentity) domain.PlayerIdentity {
	if primary.RiotID == "" {
		primary.RiotID = fallback.RiotID
	}
	if primary.GameName == "" {
		primary.GameName = fallback.GameName
	}
	if primary.TagLine == "" {
		primary.TagLine = fallback.TagLine
	}
	if primary.SummonerName == "" {
		primary.SummonerName = fallback.SummonerName
	}
	return primary
}

func cloneItems(items []domain.Item) []domain.Item {
	return append([]domain.Item(nil), items...)
}
