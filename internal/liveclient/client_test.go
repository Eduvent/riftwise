package liveclient

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"lolengine/internal/domain"
)

const sampleGameData = `{
  "activePlayer": {
    "currentGold": 1234.5,
    "level": 11,
    "riotId": "Eduventura#LAS"
  },
  "allPlayers": [
    {
      "championName": "Garen",
      "items": [
        {"count": 1, "displayName": "Stridebreaker", "itemID": 6631, "price": 3300, "slot": 0}
      ],
      "level": 11,
      "riotIdGameName": "Eduventura",
      "riotIdTagLine": "LAS",
      "scores": {"assists": 3, "creepScore": 121, "deaths": 2, "kills": 5},
      "summonerName": "Eduventura",
	  "team": "ORDER",
	  "position": "TOP"
    },
    {
      "championName": "Poppy",
      "items": [],
      "level": 10,
      "riotId": "Opponent#LAS",
      "scores": {"assists": 1, "creepScore": 105, "deaths": 4, "kills": 1},
      "team": "CHAOS"
    }
  ],
	"gameData": {"gameMode": "CLASSIC", "gameTime": 754.25, "gameVersion": "16.18.123.456", "mapNumber": 11, "mapName": "Map11"}
}`

func TestFetchNormalizesGameState(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != allGameDataPath {
			t.Fatalf("path = %q, want %q", request.URL.Path, allGameDataPath)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(sampleGameData)),
			Request:    request,
		}, nil
	})}

	client, err := newWithHTTPClient("http://127.0.0.1:2999", httpClient)
	if err != nil {
		t.Fatalf("newWithHTTPClient() error = %v", err)
	}
	game, err := client.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}

	if game.GameMode != "CLASSIC" || game.GameTimeSeconds != 754.25 {
		t.Fatalf("game metadata = %#v", game)
	}
	if game.GameVersion != "16.18.123.456" || game.MapNumber != 11 || game.MapName != "Map11" {
		t.Errorf("extended game metadata = %#v", game)
	}
	if len(game.Players) != 2 {
		t.Fatalf("len(Players) = %d, want 2", len(game.Players))
	}
	active := game.ActivePlayer
	if active.Identity.RiotID != "Eduventura#LAS" || active.Identity.GameName != "Eduventura" || active.Identity.TagLine != "LAS" {
		t.Errorf("active identity = %#v", active.Identity)
	}
	if active.ChampionName != "Garen" || active.Team != "ORDER" || active.Position != "TOP" || active.Level != 11 {
		t.Errorf("active player metadata = %#v", active)
	}
	if active.CurrentGold != 1234.5 {
		t.Errorf("CurrentGold = %v, want 1234.5", active.CurrentGold)
	}
	if active.Score.Kills != 5 || active.Score.Deaths != 2 || active.Score.Assists != 3 || active.Score.CreepScore != 121 {
		t.Errorf("active score = %#v", active.Score)
	}
	if len(active.Items) != 1 || active.Items[0].ID != 6631 || active.Items[0].DisplayName != "Stridebreaker" {
		t.Errorf("active items = %#v", active.Items)
	}
	if game.Players[1].ChampionName != "Poppy" || game.Players[1].Team != "CHAOS" {
		t.Errorf("opponent = %#v", game.Players[1])
	}
}

func TestFetchNormalizesDeathStateAndDeduplicatedEvents(t *testing.T) {
	body := strings.Replace(sampleGameData,
		`"riotIdGameName": "Eduventura",`,
		`"isDead": true, "respawnTimer": 12.5, "riotIdGameName": "Eduventura",`, 1)
	body = strings.Replace(body,
		`"gameData": {"gameMode": "CLASSIC"`,
		`"events":{"Events":[
		  {"EventID":7,"EventName":"ChampionKill","EventTime":753.5,"VictimName":"Garen"},
		  {"EventID":1,"EventName":"GameStart","EventTime":0},
		  {"EventID":7,"EventName":"ChampionKill","EventTime":753.5,"VictimName":"Garen"}
		]},
		"gameData": {"gameMode": "CLASSIC"`, 1)
	client := clientReturningBody(t, body)

	game, err := client.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if !game.ActivePlayer.IsDead || game.ActivePlayer.RespawnTimer != 12.5 || !game.Players[0].IsDead {
		t.Fatalf("death state = active %#v, player %#v", game.ActivePlayer, game.Players[0])
	}
	if len(game.SourceEvents) != 2 || game.SourceEvents[0].ID != 1 || game.SourceEvents[1].ID != 7 {
		t.Fatalf("source events = %#v", game.SourceEvents)
	}
	if game.SourceEvents[1].DedupeKey != "source:7" || !strings.Contains(string(game.SourceEvents[1].Payload), `"VictimName":"Garen"`) {
		t.Fatalf("normalized source event = %#v", game.SourceEvents[1])
	}
}

func TestFetchDeterministicallyResolvesConflictingDuplicateEvents(t *testing.T) {
	events := func(first, second string) string {
		return strings.Replace(sampleGameData,
			`"gameData": {"gameMode": "CLASSIC"`,
			`"events":{"Events":[`+first+`,`+second+`]},"gameData": {"gameMode": "CLASSIC"`, 1)
	}
	alpha := `{"EventID":7,"EventName":"Alpha","EventTime":20,"Value":"same"}`
	zeta := `{"EventID":7,"EventName":"Zeta","EventTime":20,"Value":"same"}`

	forward, err := clientReturningBody(t, events(zeta, alpha)).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch(forward) error = %v", err)
	}
	reversed, err := clientReturningBody(t, events(alpha, zeta)).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch(reversed) error = %v", err)
	}
	if len(forward.SourceEvents) != 1 || len(reversed.SourceEvents) != 1 ||
		forward.SourceEvents[0].Name != reversed.SourceEvents[0].Name {
		t.Fatalf("duplicate normalization depends on input order: forward=%#v reversed=%#v", forward.SourceEvents, reversed.SourceEvents)
	}
}

func TestFetchClassifiesFailures(t *testing.T) {
	tests := []struct {
		name      string
		client    *Client
		wantClass domain.FetchErrorClass
	}{
		{
			name: "unavailable",
			client: mustTestClient(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader("no game")), Header: make(http.Header), Request: request}, nil
			})),
			wantClass: domain.FetchGameUnavailable,
		},
		{
			name: "transport",
			client: mustTestClient(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("connection refused")
			})),
			wantClass: domain.FetchTransport,
		},
		{
			name: "timeout",
			client: mustTestClient(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, context.DeadlineExceeded
			})),
			wantClass: domain.FetchTimeout,
		},
		{
			name: "body timeout",
			client: mustTestClient(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       timeoutReadCloser{},
					Request:    request,
				}, nil
			})),
			wantClass: domain.FetchTimeout,
		},
		{
			name:      "invalid payload",
			client:    clientReturningBody(t, `{}`),
			wantClass: domain.FetchInvalidPayload,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.client.Fetch(context.Background())
			if err == nil || domain.FetchClassOf(err) != tt.wantClass {
				t.Fatalf("Fetch() error = %v, class = %q, want %q", err, domain.FetchClassOf(err), tt.wantClass)
			}
		})
	}
}

type timeoutReadCloser struct{}

func (timeoutReadCloser) Read([]byte) (int, error) { return 0, timeoutError{} }
func (timeoutReadCloser) Close() error             { return nil }

type timeoutError struct{}

func (timeoutError) Error() string   { return "body read timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func TestFetchAcceptsMissingGameVersion(t *testing.T) {
	body := strings.Replace(
		sampleGameData,
		`"gameTime": 754.25, "gameVersion": "16.18.123.456",`,
		`"gameTime": 754.25,`,
		1,
	)
	client := clientReturningBody(t, body)

	game, err := client.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if game.GameVersion != "" {
		t.Fatalf("GameVersion = %q, want empty when Riot omits it", game.GameVersion)
	}
	if game.GameMode != "CLASSIC" || game.MapNumber != 11 || game.ActivePlayer.ChampionName != "Garen" {
		t.Fatalf("normalized game = %#v", game)
	}
}

func TestNewConfiguresSelfSignedTLSOnlyAfterLoopbackValidation(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://192.0.2.1:8888")
	client, err := New("https://127.0.0.1:2999")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	transport, ok := client.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.httpClient.Transport)
	}
	if transport.TLSClientConfig == nil || !transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("loopback HTTPS transport does not permit Riot's self-signed certificate")
	}
	if transport.TLSClientConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("TLS minimum = %d, want TLS 1.2", transport.TLSClientConfig.MinVersion)
	}
	if transport.Proxy != nil {
		t.Fatal("loopback transport unexpectedly uses an environment proxy")
	}
}

func TestNewRejectsUnsafeURLs(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{name: "non-loopback host", url: "https://example.com:2999"},
		{name: "unsupported scheme", url: "file://127.0.0.1/tmp/data"},
		{name: "missing host", url: "https:///liveclientdata"},
		{name: "userinfo", url: "https://user@127.0.0.1:2999"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(test.url); err == nil {
				t.Fatalf("New(%q) unexpectedly succeeded", test.url)
			}
		})
	}
}

func TestFetchReportsUnavailableGame(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("not running")),
			Request:    request,
		}, nil
	})}
	client, err := newWithHTTPClient("http://127.0.0.1:2999", httpClient)
	if err != nil {
		t.Fatalf("newWithHTTPClient() error = %v", err)
	}
	_, err = client.Fetch(context.Background())
	if !errors.Is(err, ErrGameUnavailable) {
		t.Fatalf("Fetch() error = %v, want ErrGameUnavailable", err)
	}
}

func TestFetchRejectsInvalidSuccessfulPayloads(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "null", body: "null"},
		{name: "empty object", body: "{}"},
		{name: "unrelated object", body: `{"message":"ok"}`},
		{name: "trailing JSON", body: sampleGameData + "\n{}"},
		{name: "missing game mode", body: strings.Replace(sampleGameData, `"gameMode": "CLASSIC"`, `"gameMode": ""`, 1)},
		{name: "invalid map number", body: strings.Replace(sampleGameData, `"mapNumber": 11`, `"mapNumber": 0`, 1)},
		{name: "participant missing score and items", body: `{
			"activePlayer":{"currentGold":500,"level":1,"riotId":"Player#LAS"},
			"allPlayers":[{"championName":"Garen","level":1,"riotId":"Player#LAS","team":"ORDER"}],
			"gameData":{"gameMode":"CLASSIC","gameTime":0,"gameVersion":"16.18.1","mapNumber":11}
		}`},
		{name: "missing active participant", body: `{
			"activePlayer":{"currentGold":500,"level":1,"riotId":"Missing#LAS"},
			"allPlayers":[{"championName":"Garen","items":[],"level":1,"riotId":"Other#LAS","scores":{},"team":"ORDER"}],
			"gameData":{"gameMode":"CLASSIC","gameTime":0,"gameVersion":"16.18.1","mapNumber":11}
		}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := clientReturningBody(t, test.body)
			_, err := client.Fetch(context.Background())
			if !errors.Is(err, ErrInvalidPayload) {
				t.Fatalf("Fetch() error = %v, want ErrInvalidPayload", err)
			}
		})
	}
}

func TestFetchRejectsOversizedPayload(t *testing.T) {
	client := clientReturningBody(t, strings.Repeat(" ", maxResponseSize+1))
	_, err := client.Fetch(context.Background())
	if !errors.Is(err, ErrInvalidPayload) || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Fetch() error = %v, want oversized ErrInvalidPayload", err)
	}
}

func TestFetchAcceptsTrailingWhitespace(t *testing.T) {
	client := clientReturningBody(t, sampleGameData+"\n\t ")
	if _, err := client.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
}

func TestLoopbackDialRejectsUnsafeResolution(t *testing.T) {
	tests := []struct {
		name      string
		addresses []net.IPAddr
	}{
		{name: "external only", addresses: []net.IPAddr{{IP: net.ParseIP("203.0.113.10")}}},
		{name: "mixed loopback and external", addresses: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}, {IP: net.ParseIP("203.0.113.10")}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dialCalled := false
			dial := newLoopbackDialContext(staticResolver{addresses: test.addresses}, func(context.Context, string, string) (net.Conn, error) {
				dialCalled = true
				return nil, errors.New("unexpected dial")
			})
			if _, err := dial(context.Background(), "tcp", "localhost.:2999"); err == nil || !strings.Contains(err.Error(), "non-loopback") {
				t.Fatalf("dial error = %v, want non-loopback rejection", err)
			}
			if dialCalled {
				t.Fatal("network dial occurred before unsafe resolution was rejected")
			}
		})
	}
}

func TestLoopbackDialUsesValidatedNumericAddress(t *testing.T) {
	dialFailure := errors.New("dial stopped by test")
	var dialedAddress string
	dial := newLoopbackDialContext(
		staticResolver{addresses: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}},
		func(_ context.Context, network, address string) (net.Conn, error) {
			if network != "tcp" {
				t.Fatalf("network = %q, want tcp", network)
			}
			dialedAddress = address
			return nil, dialFailure
		},
	)
	_, err := dial(context.Background(), "tcp", "LOCALHOST:2999")
	if !errors.Is(err, dialFailure) {
		t.Fatalf("dial error = %v, want wrapped dial failure", err)
	}
	if dialedAddress != "127.0.0.1:2999" {
		t.Fatalf("dialed address = %q, want numeric loopback", dialedAddress)
	}
}

func clientReturningBody(t *testing.T, body string) *Client {
	t.Helper()
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})}
	client, err := newWithHTTPClient("http://127.0.0.1:2999", httpClient)
	if err != nil {
		t.Fatalf("newWithHTTPClient() error = %v", err)
	}
	return client
}

func mustTestClient(t *testing.T, transport http.RoundTripper) *Client {
	t.Helper()
	client, err := newWithHTTPClient("http://127.0.0.1:2999", &http.Client{Transport: transport})
	if err != nil {
		t.Fatalf("newWithHTTPClient() error = %v", err)
	}
	return client
}

type staticResolver struct {
	addresses []net.IPAddr
	err       error
}

func (resolver staticResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return resolver.addresses, resolver.err
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
