//go:build riotlive

package riotapi_test

import (
	"context"
	"os"
	"testing"
	"time"

	"lolengine/internal/riotapi"
)

// TestLiveSmoke is intentionally excluded from ordinary test runs. It reads a
// private developer key only from the environment, prints no response data,
// and performs the minimum resolve/list/detail sequence.
func TestLiveSmoke(t *testing.T) {
	if os.Getenv("LOL_ENGINE_RIOT_LIVE_TEST") != "1" {
		t.Skip("set LOL_ENGINE_RIOT_LIVE_TEST=1 for an explicit credentialed smoke test")
	}
	region, ok := riotapi.ParseRegion(os.Getenv("LOL_ENGINE_RIOT_REGION"))
	if !ok {
		t.Fatal("LOL_ENGINE_RIOT_REGION must be a documented regional route")
	}
	gameName, tagLine := os.Getenv("LOL_ENGINE_RIOT_TEST_GAME_NAME"), os.Getenv("LOL_ENGINE_RIOT_TEST_TAG_LINE")
	key := riotapi.Secret(os.Getenv("LOL_ENGINE_RIOT_API_KEY"))
	if gameName == "" || tagLine == "" || os.Getenv("LOL_ENGINE_RIOT_API_KEY") == "" {
		t.Fatal("live smoke requires the API key and explicit test Riot ID environment variables")
	}
	client, err := riotapi.NewDirect(key, nil, riotapi.ClientConfig{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal("configure live client:", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	account, err := client.ResolveAccount(ctx, region, gameName, tagLine)
	if err != nil {
		t.Fatal("resolve linked account:", err)
	}
	ids, err := client.ListMatchIDs(ctx, region, account.PUUID, riotapi.MatchListQuery{Start: 0, Count: 1, EndTime: time.Now().Unix()})
	if err != nil {
		t.Fatal("list one match:", err)
	}
	if len(ids) == 0 {
		t.Skip("linked account has no match in the bounded default list")
	}
	if _, err := client.GetMatch(ctx, region, ids[0], account.PUUID); err != nil {
		t.Fatal("normalize one match:", err)
	}
}
