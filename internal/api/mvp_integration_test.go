package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lolengine/internal/catalog"
	"lolengine/internal/ddragon"
	"lolengine/internal/domain"
	"lolengine/internal/itemization"
	"lolengine/internal/recommendation"
	"lolengine/internal/state"
	"lolengine/internal/storage"
	"lolengine/internal/tracker"
)

func TestMVPFlowFromLiveStateThroughCachedRecommendationAPI(t *testing.T) {
	ctx := context.Background()
	database, err := storage.Open(ctx, filepath.Join(t.TempDir(), "lol-engine.db"))
	if err != nil {
		t.Fatalf("storage.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	dataDragonHTTP := &http.Client{Transport: integrationRoundTripper(func(request *http.Request) (*http.Response, error) {
		var body string
		switch request.URL.Path {
		case "/api/versions.json":
			body = `["16.18.1"]`
		case "/cdn/16.18.1/data/es_MX/item.json":
			body = integrationItems
		case "/cdn/16.18.1/data/en_US/championFull.json":
			body = integrationChampions
		default:
			t.Fatalf("unexpected Data Dragon path %q", request.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})}
	dataDragon, err := ddragon.New("https://ddragon.test", dataDragonHTTP, ddragon.Config{Timeout: time.Second})
	if err != nil {
		t.Fatalf("ddragon.New() error = %v", err)
	}
	catalogs, err := catalog.NewManager(dataDragon, database, "es_MX")
	if err != nil {
		t.Fatalf("catalog.NewManager() error = %v", err)
	}
	if err := catalogs.Initialize(ctx, true); err != nil {
		t.Fatalf("catalog Initialize() error = %v", err)
	}

	gameStore := state.NewStore(domain.Snapshot{Status: domain.StatusWaitingForGame})
	gameTracker, err := tracker.New(integrationFetcher{game: integrationGame()}, gameStore, tracker.Config{
		PollInterval: time.Hour,
		StaleAfter:   2 * time.Hour,
	})
	if err != nil {
		t.Fatalf("tracker.New() error = %v", err)
	}
	_, gameUpdates, unsubscribeGame := gameStore.Subscribe(1)
	defer unsubscribeGame()
	trackerContext, cancelTracker := context.WithCancel(ctx)
	go gameTracker.Run(trackerContext)
	select {
	case <-gameUpdates:
		cancelTracker()
	case <-time.After(time.Second):
		cancelTracker()
		t.Fatal("fake Live Client state was not tracked")
	}

	coordinator, err := recommendation.NewCoordinator(gameStore, catalogs, itemization.Engine{}, database, recommendation.Config{})
	if err != nil {
		t.Fatalf("recommendation.NewCoordinator() error = %v", err)
	}
	if _, err := coordinator.Recalculate(ctx); err != nil {
		t.Fatalf("coordinator.Recalculate() error = %v", err)
	}
	handler := New(gameStore, Config{Recommendations: coordinator})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/dashboard", nil)
	request.Host = "127.0.0.1:8080"
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("dashboard status = %d, body = %s", response.Code, response.Body.String())
	}
	var dashboard recommendation.DashboardSnapshot
	if err := json.NewDecoder(response.Body).Decode(&dashboard); err != nil {
		t.Fatalf("decode dashboard: %v", err)
	}
	if !dashboard.Catalog.Ready || dashboard.Catalog.Patch != "16.18.1" {
		t.Fatalf("catalog status = %#v", dashboard.Catalog)
	}
	if dashboard.Recommendation.Status != itemization.StatusReady || dashboard.Recommendation.TargetItem == nil || dashboard.Recommendation.TargetItem.ID != 4401 {
		t.Fatalf("recommendation = %#v", dashboard.Recommendation)
	}
	if _, err := database.LoadRecommendation(ctx); err != nil {
		t.Fatalf("persisted recommendation unavailable: %v", err)
	}
}

type integrationRoundTripper func(*http.Request) (*http.Response, error)

func (function integrationRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type integrationFetcher struct {
	game domain.GameState
}

func (fetcher integrationFetcher) Fetch(context.Context) (domain.GameState, error) {
	return fetcher.game, nil
}

func integrationGame() domain.GameState {
	return domain.GameState{
		GameMode: "CLASSIC", GameVersion: "16.18.99.1", MapNumber: 11, MapName: "Summoner's Rift",
		ActivePlayer: domain.ActivePlayer{
			Identity: domain.PlayerIdentity{RiotID: "Player#LAS"}, ChampionName: "Garen", Team: "ORDER", Position: "TOP",
			Level: 10, CurrentGold: 500, Score: domain.Score{CreepScore: 90},
		},
		Players: []domain.Player{{
			Identity: domain.PlayerIdentity{RiotID: "Mage#LAS"}, ChampionName: "Ahri", Team: "CHAOS", Position: "TOP",
			Level: 10, Score: domain.Score{Kills: 3, CreepScore: 90}, Items: []domain.Item{},
		}},
	}
}

const integrationItems = `{
  "type":"item","version":"16.18.1","data":{
    "1028":{"name":"Cristal de rubí","description":"Vida","plaintext":"Vida","from":[],"into":["4401","3065"],"tags":["Health"],"maps":{"11":true},"gold":{"base":400,"purchasable":true,"total":400,"sell":280},"stats":{"FlatHPPoolMod":150},"image":{"full":"1028.png"}},
    "1057":{"name":"Capa negatrón","description":"RM","plaintext":"RM","from":[],"into":["4401","3065"],"tags":["SpellBlock"],"maps":{"11":true},"gold":{"base":850,"purchasable":true,"total":850,"sell":595},"stats":{"FlatSpellBlockMod":40},"image":{"full":"1057.png"}},
    "4401":{"name":"Fuerza de la naturaleza","description":"RM","plaintext":"RM","from":["1057","1028"],"into":[],"tags":["SpellBlock","Health"],"maps":{"11":true},"gold":{"base":1550,"purchasable":true,"total":2800,"sell":1960},"stats":{"FlatSpellBlockMod":55},"image":{"full":"4401.png"}},
    "3065":{"name":"Rostro espiritual","description":"RM","plaintext":"RM","from":["1057","1028"],"into":[],"tags":["SpellBlock","Health"],"maps":{"11":true},"gold":{"base":1650,"purchasable":true,"total":2900,"sell":2030},"stats":{"FlatSpellBlockMod":50},"image":{"full":"3065.png"}}
  }
}`

const integrationChampions = `{
  "type":"champion","version":"16.18.1","data":{
    "Garen":{"id":"Garen","key":"86","name":"Garen","tags":["Fighter","Tank"],"passive":{"name":"Perseverance","description":"Regenerates health."},"spells":[{"name":"Judgment","description":"Physical damage.","tooltip":"<physicalDamage>damage</physicalDamage>"}]},
    "Ahri":{"id":"Ahri","key":"103","name":"Ahri","tags":["Mage"],"passive":{"name":"Essence Theft","description":"Heals."},"spells":[{"name":"Orb","description":"Magic damage.","tooltip":"<magicDamage>damage</magicDamage>"}]}
  }
}`
