package riotapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"lolengine/internal/domain"
)

func TestSecretNeverRevealsValue(t *testing.T) {
	secret := Secret("RGAPI-super-secret")
	if got := fmt.Sprint(secret); strings.Contains(got, "super-secret") || got != redactedSecret {
		t.Fatalf("String() = %q", got)
	}
	if got := fmt.Sprintf("%#v", secret); strings.Contains(got, "super-secret") || got != redactedSecret {
		t.Fatalf("GoString() = %q", got)
	}
	for _, got := range []string{fmt.Sprintf("%s", secret), fmt.Sprintf("%q", secret), fmt.Sprintf("%x", secret), fmt.Sprintf("%+v", secret)} {
		if strings.Contains(got, "super-secret") || !strings.Contains(got, "REDACTED") {
			t.Fatalf("formatted secret = %q", got)
		}
	}
	payload, err := json.Marshal(secret)
	if err != nil || string(payload) != `"[REDACTED]"` {
		t.Fatalf("MarshalJSON() = %s, %v", payload, err)
	}
}

func TestPlatformRoutingIsExplicit(t *testing.T) {
	tests := []struct {
		platform Platform
		region   Region
	}{
		{PlatformNA1, RegionAmericas}, {PlatformBR1, RegionAmericas},
		{PlatformKR, RegionAsia}, {PlatformJP1, RegionAsia},
		{PlatformEUW1, RegionEurope}, {PlatformTR1, RegionEurope}, {PlatformME1, RegionEurope},
		{PlatformOC1, RegionSEA}, {PlatformSG2, RegionSEA}, {PlatformVN2, RegionSEA},
	}
	for _, test := range tests {
		if got, ok := test.platform.Region(); !ok || got != test.region {
			t.Errorf("%s.Region() = %s, %v", test.platform, got, ok)
		}
	}
	if _, ok := Platform("PBE1").Region(); ok {
		t.Fatal("undocumented platform was accepted")
	}
}

func TestOfficialOC1AndME1RegionalHosts(t *testing.T) {
	tests := []struct {
		name     string
		region   Region
		matchID  string
		wantHost string
	}{
		{name: "OC1 uses SEA", region: RegionSEA, matchID: "OC1_1", wantHost: "sea.api.riotgames.com"},
		{name: "ME1 uses Europe", region: RegionEurope, matchID: "ME1_1", wantHost: "europe.api.riotgames.com"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, err := NewDirect("route-key", &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.URL.Host != test.wantHost {
					t.Fatalf("host = %q", request.URL.Host)
				}
				return jsonResponse(http.StatusOK, `["`+test.matchID+`"]`, nil), nil
			})}, ClientConfig{})
			if err != nil {
				t.Fatal(err)
			}
			ids, err := client.ListMatchIDs(context.Background(), test.region, "self", MatchListQuery{Count: 1})
			if err != nil || len(ids) != 1 || ids[0] != test.matchID {
				t.Fatalf("ids = %v, %v", ids, err)
			}
		})
	}
}

func TestDirectClientRejectsRedirectsWithoutForwardingCredential(t *testing.T) {
	var requests []*http.Request
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request.Clone(request.Context()))
		return &http.Response{
			StatusCode: http.StatusTemporaryRedirect,
			Header:     http.Header{"Location": []string{"http://attacker.invalid/steal"}},
			Body:       io.NopCloser(strings.NewReader(`{}`)),
			Request:    request,
		}, nil
	})
	client, err := NewDirect("redirect-secret", &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		t.Fatal("caller redirect policy must not override the credential boundary")
		return nil
	}}, ClientConfig{MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.ResolveAccount(context.Background(), RegionAmericas, "name", "tag")
	if KindOf(err) != ErrorMalformed || len(requests) != 1 || requests[0].URL.Host != "americas.api.riotgames.com" || requests[0].Header.Get("X-Riot-Token") != "redirect-secret" {
		t.Fatalf("redirect boundary requests=%d err=%v", len(requests), err)
	}

	requests = nil
	transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request.Clone(request.Context()))
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{"https://americas.api.riotgames.com/other"}},
			Body:       io.NopCloser(strings.NewReader(`{}`)),
			Request:    request,
		}, nil
	})
	client.httpClient.Transport = transport
	_, err = client.ResolveAccount(context.Background(), RegionAmericas, "name", "tag")
	if KindOf(err) != ErrorMalformed || len(requests) != 1 {
		t.Fatalf("same-host redirect followed: requests=%d err=%v", len(requests), err)
	}
}

func TestDirectClientHostsEscapesQueriesAndOnlyUsesRiotToken(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host != "americas.api.riotgames.com" {
			t.Fatalf("host = %q", request.URL.Host)
		}
		if request.Header.Get("X-Riot-Token") != "secret" || len(request.Header) != 1 {
			t.Fatalf("headers = %#v", request.Header)
		}
		if !strings.Contains(request.URL.EscapedPath(), "Jos%C3%A9%20%2F%20%F0%9F%8E%AE") || !strings.HasSuffix(request.URL.EscapedPath(), "/%23LAS") {
			t.Fatalf("escaped path = %q", request.URL.EscapedPath())
		}
		return jsonResponse(http.StatusOK, `{"puuid":"p-1","gameName":"José / 🎮","tagLine":"#LAS"}`, nil), nil
	})
	client, err := NewDirect(Secret("secret"), &http.Client{Transport: transport}, ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	account, err := client.ResolveAccount(context.Background(), RegionAmericas, "José / 🎮", "#LAS")
	if err != nil || account.GameName != "José / 🎮" || account.TagLine != "#LAS" {
		t.Fatalf("ResolveAccount() = %#v, %v", account, err)
	}

	transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		query := request.URL.Query()
		if query.Get("start") != "7" || query.Get("count") != "12" || query.Get("startTime") != "100" || query.Get("endTime") != "200" || query.Get("queue") != "420" || query.Get("type") != "ranked" {
			t.Fatalf("query = %v", query)
		}
		return jsonResponse(http.StatusOK, `["NA1_2","NA1_1"]`, nil), nil
	})
	client.httpClient.Transport = transport
	ids, err := client.ListMatchIDs(context.Background(), RegionAmericas, "p / ü", MatchListQuery{Start: 7, Count: 12, StartTime: 100, EndTime: 200, Queue: 420, Type: "ranked"})
	if err != nil || len(ids) != 2 || ids[0] != "NA1_2" {
		t.Fatalf("ListMatchIDs() = %v, %v", ids, err)
	}
}

func TestHTTPErrorKindsNeverContainResponseBody(t *testing.T) {
	tests := []struct {
		status int
		kind   ErrorKind
	}{
		{400, ErrorBadRequest}, {401, ErrorUnauthorized}, {403, ErrorForbidden},
		{404, ErrorNotFound}, {429, ErrorRateLimited}, {500, ErrorServer},
	}
	for _, test := range tests {
		t.Run(fmt.Sprint(test.status), func(t *testing.T) {
			client, err := NewDirect("secret", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return jsonResponse(test.status, `{"secret":"must-not-leak"}`, http.Header{"Retry-After": []string{"0"}}), nil
			})}, ClientConfig{MaxAttempts: 1})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.ResolveAccount(context.Background(), RegionAmericas, "name", "tag")
			var apiErr *Error
			if !errors.As(err, &apiErr) || apiErr.Kind != test.kind || strings.Contains(err.Error(), "must-not-leak") {
				t.Fatalf("error = %#v", err)
			}
		})
	}
}

func TestTransportTimeoutMalformedPayloadAndQueryValidation(t *testing.T) {
	client, err := NewDirect("timeout-key", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, timeoutError{}
	})}, ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ResolveAccount(context.Background(), RegionAmericas, "name", "tag"); KindOf(err) != ErrorTransport {
		t.Fatalf("timeout kind = %v", err)
	}

	client, _ = NewDirect("malformed-key", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"puuid":`, nil), nil
	})}, ClientConfig{})
	if _, err := client.ResolveAccount(context.Background(), RegionAmericas, "name", "tag"); KindOf(err) != ErrorMalformed {
		t.Fatalf("malformed kind = %v", err)
	}
	if _, err := client.ListMatchIDs(context.Background(), RegionAmericas, "p", MatchListQuery{Count: 101}); KindOf(err) != ErrorBadRequest {
		t.Fatalf("unbounded count = %v", err)
	}
	if _, err := client.ListMatchIDs(context.Background(), RegionAmericas, "p", MatchListQuery{Count: 1, Type: "invented"}); KindOf(err) != ErrorBadRequest {
		t.Fatalf("invalid type = %v", err)
	}
}

func TestHTTPClientAndBodyReadTimeoutsAreTransportErrors(t *testing.T) {
	tests := []struct {
		name      string
		transport http.RoundTripper
	}{
		{
			name: "client deadline with live caller context",
			transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, fmt.Errorf("request deadline: %w", context.DeadlineExceeded)
			}),
		},
		{
			name: "body read deadline",
			transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: timeoutBody{}, Request: request}, nil
			}),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, err := NewDirect("timeout-kind", &http.Client{Transport: test.transport}, ClientConfig{MaxAttempts: 1})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.ResolveAccount(context.Background(), RegionAmericas, "name", "tag")
			var apiErr *Error
			if !errors.As(err, &apiErr) || apiErr.Kind != ErrorTransport || apiErr.Cause == nil {
				t.Fatalf("timeout = %#v", err)
			}
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client, _ := NewDirect("caller-cancel", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, context.Canceled
	})}, ClientConfig{})
	if _, err := client.ResolveAccount(ctx, RegionAmericas, "name", "tag"); KindOf(err) != ErrorCancelled || !errors.Is(err, context.Canceled) {
		t.Fatalf("caller cancellation = %v", err)
	}

	client, _ = NewDirect("retry-sleep-timeout", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusServiceUnavailable, `{}`, nil), nil
	})}, ClientConfig{MaxAttempts: 2, Sleep: func(context.Context, time.Duration) error { return context.DeadlineExceeded }, Jitter: func(time.Duration) time.Duration { return 0 }})
	if _, err := client.ResolveAccount(context.Background(), RegionAmericas, "name", "tag"); KindOf(err) != ErrorTransport || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("internal wait timeout = %v", err)
	}
}

func TestMatchIDListRejectsOverCountAndOversizedIDs(t *testing.T) {
	responses := []string{`["NA1_1","NA1_2"]`, `["NA1_` + strings.Repeat("9", maximumMatchIDLen) + `"]`}
	for _, response := range responses {
		client, err := NewDirect("bounded-list", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, response, nil), nil
		})}, ClientConfig{})
		if err != nil {
			t.Fatal(err)
		}
		if ids, err := client.ListMatchIDs(context.Background(), RegionAmericas, "self", MatchListQuery{Count: 1}); KindOf(err) != ErrorMalformed || len(ids) != 0 {
			t.Fatalf("response accepted: ids=%v err=%v", ids, err)
		}
	}
}

func TestMatchIDListPreservesSourceCardinalityForCursorProgress(t *testing.T) {
	client, err := NewDirect("cursor-cardinality", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `["NA1_1","NA1_1"]`, nil), nil
	})}, ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ids, err := client.ListMatchIDs(context.Background(), RegionAmericas, "self", MatchListQuery{Count: 2})
	if err != nil || len(ids) != 2 || ids[0] != ids[1] {
		t.Fatalf("source cardinality = %v, %v", ids, err)
	}
}

func TestRateLimitInvalidRetryAfterUsesFallback(t *testing.T) {
	now := time.Unix(2000, 0)
	var sleeps []time.Duration
	calls := 0
	client, err := NewDirect("fallback-key", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return jsonResponse(429, `{}`, http.Header{"Retry-After": []string{"not-an-integer"}}), nil
		}
		return jsonResponse(200, `{"puuid":"p","gameName":"n","tagLine":"t"}`, nil), nil
	})}, ClientConfig{
		MaxAttempts: 2, RetryAfter: 3 * time.Second, Now: func() time.Time { return now },
		Sleep: func(_ context.Context, delay time.Duration) error {
			sleeps = append(sleeps, delay)
			now = now.Add(delay)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ResolveAccount(context.Background(), RegionAmericas, "n", "t"); err != nil {
		t.Fatal(err)
	}
	if len(sleeps) != 1 || sleeps[0] != 3*time.Second {
		t.Fatalf("fallback sleeps = %v", sleeps)
	}
}

func TestRetryAfterIsPositiveAndBoundedBeforeDurationConversion(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{name: "valid seconds", value: "7", want: 7 * time.Second},
		{name: "zero uses fallback", value: "0", want: 3 * time.Second},
		{name: "negative uses fallback", value: "-1", want: 3 * time.Second},
		{name: "integer overflow uses fallback", value: "999999999999999999999999", want: 3 * time.Second},
		{name: "max int clamps before multiply", value: "9223372036854775807", want: maximumRetryAfter},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := parseRetryAfter(test.value, 3*time.Second); got != test.want || got <= 0 || got > maximumRetryAfter {
				t.Fatalf("parseRetryAfter(%q) = %v, want %v", test.value, got, test.want)
			}
		})
	}
	if got := parseRetryAfter("invalid", maximumRetryAfter+time.Hour); got != maximumRetryAfter {
		t.Fatalf("oversized configured fallback = %v, want %v", got, maximumRetryAfter)
	}
}

func TestOverflowRetryAfterCannotBypassConcurrentSharedGate(t *testing.T) {
	now := time.Unix(7000, 0)
	var clockMu sync.Mutex
	clock := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}
	registry := NewRateGateRegistry(time.Hour)
	seed, _ := NewDirect("overflow-shared", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusTooManyRequests, `{}`, http.Header{"Retry-After": []string{"9223372036854775807"}}), nil
	})}, ClientConfig{MaxAttempts: 1, Now: clock, RateGates: registry})
	_, err := seed.ResolveAccount(context.Background(), RegionAmericas, "n", "t")
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Kind != ErrorRateLimited || apiErr.RetryAfter != maximumRetryAfter {
		t.Fatalf("seed rate limit = %#v", err)
	}

	waits := make(chan time.Duration, 2)
	release := make(chan struct{})
	sleep := func(ctx context.Context, delay time.Duration) error {
		waits <- delay
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
			return nil
		}
	}
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"puuid":"p","gameName":"n","tagLine":"t"}`, nil), nil
	})
	first, _ := NewDirect("overflow-shared", &http.Client{Transport: transport}, ClientConfig{MaxAttempts: 1, Now: clock, Sleep: sleep, RateGates: registry})
	second, _ := NewDirect("overflow-shared", &http.Client{Transport: transport}, ClientConfig{MaxAttempts: 1, Now: clock, Sleep: sleep, RateGates: registry})
	errorsCh := make(chan error, 2)
	for _, client := range []*Client{first, second} {
		go func(client *Client) {
			_, callErr := client.ResolveAccount(context.Background(), RegionAmericas, "n", "t")
			errorsCh <- callErr
		}(client)
	}
	for range 2 {
		if delay := <-waits; delay != maximumRetryAfter {
			t.Fatalf("shared overflow gate delay = %v, want %v", delay, maximumRetryAfter)
		}
	}
	clockMu.Lock()
	now = now.Add(maximumRetryAfter)
	clockMu.Unlock()
	close(release)
	for range 2 {
		if callErr := <-errorsCh; callErr != nil {
			t.Fatal(callErr)
		}
	}
}

func TestRetriesServerErrorsAtMostThreeAndCancellationStopsWait(t *testing.T) {
	var calls int
	client, err := NewDirect("secret", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return jsonResponse(http.StatusServiceUnavailable, `{}`, nil), nil
	})}, ClientConfig{MaxAttempts: 9, Sleep: func(context.Context, time.Duration) error { return nil }, Jitter: func(time.Duration) time.Duration { return 0 }})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.ResolveAccount(context.Background(), RegionAmericas, "name", "tag")
	if calls != 3 || KindOf(err) != ErrorServer {
		t.Fatalf("calls=%d err=%v", calls, err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.ResolveAccount(cancelled, RegionAmericas, "name", "tag")
	if KindOf(err) != ErrorCancelled {
		t.Fatalf("cancel error = %v", err)
	}
}

func TestRateLimitGateIsSharedByCredentialAndRegion(t *testing.T) {
	now := time.Unix(1000, 0)
	var mu sync.Mutex
	var sleeps []time.Duration
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	sleep := func(_ context.Context, duration time.Duration) error {
		mu.Lock()
		sleeps = append(sleeps, duration)
		now = now.Add(duration)
		mu.Unlock()
		return nil
	}
	firstCalls := 0
	registry := NewRateGateRegistry(time.Minute)
	first, _ := NewDirect("shared-key", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		firstCalls++
		if firstCalls == 1 {
			return jsonResponse(429, `{}`, http.Header{"Retry-After": []string{"2"}}), nil
		}
		return jsonResponse(200, `{"puuid":"p","gameName":"n","tagLine":"t"}`, nil), nil
	})}, ClientConfig{MaxAttempts: 2, Now: clock, Sleep: sleep, Jitter: func(time.Duration) time.Duration { return 0 }, RateGates: registry})
	second, _ := NewDirect("shared-key", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(200, `{"puuid":"p","gameName":"n","tagLine":"t"}`, nil), nil
	})}, ClientConfig{MaxAttempts: 1, Now: clock, Sleep: sleep, RateGates: registry})
	if _, err := first.ResolveAccount(context.Background(), RegionAmericas, "n", "t"); err != nil {
		t.Fatal(err)
	}
	if _, err := second.ResolveAccount(context.Background(), RegionAmericas, "n", "t"); err != nil {
		t.Fatal(err)
	}
	if len(sleeps) == 0 || sleeps[0] != 2*time.Second {
		t.Fatalf("sleeps = %v", sleeps)
	}
}

func TestRateGateRegistriesAreIsolatedAndEvictIdleEntries(t *testing.T) {
	now := time.Unix(5000, 0)
	firstRegistry := NewRateGateRegistry(time.Second)
	secondRegistry := NewRateGateRegistry(time.Second)
	firstRegistry.advance("credential:americas", now.Add(time.Minute), now)
	if firstRegistry.Len(now) != 1 || secondRegistry.Len(now) != 0 {
		t.Fatalf("registry isolation first=%d second=%d", firstRegistry.Len(now), secondRegistry.Len(now))
	}
	if firstRegistry.Len(now.Add(30*time.Second)) != 1 {
		t.Fatal("active future gate evicted")
	}
	if firstRegistry.Len(now.Add(2*time.Minute)) != 0 {
		t.Fatal("expired idle gate retained")
	}
}

func TestDefaultClientsDoNotShareImmortalRateGateState(t *testing.T) {
	now := time.Unix(5500, 0)
	first, _ := NewDirect("same-key", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusTooManyRequests, `{}`, http.Header{"Retry-After": []string{"60"}}), nil
	})}, ClientConfig{MaxAttempts: 1, Now: func() time.Time { return now }})
	if _, err := first.ResolveAccount(context.Background(), RegionAmericas, "n", "t"); KindOf(err) != ErrorRateLimited {
		t.Fatalf("first rate limit = %v", err)
	}
	second, _ := NewDirect("same-key", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"puuid":"p","gameName":"n","tagLine":"t"}`, nil), nil
	})}, ClientConfig{MaxAttempts: 1, Now: func() time.Time { return now }, Sleep: func(context.Context, time.Duration) error {
		t.Fatal("independent client inherited another client's gate")
		return nil
	}})
	if _, err := second.ResolveAccount(context.Background(), RegionAmericas, "n", "t"); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentClientsExplicitlyShareInjectedRateGate(t *testing.T) {
	now := time.Unix(6000, 0)
	var clockMu sync.Mutex
	clock := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}
	waiting := make(chan struct{}, 2)
	release := make(chan struct{})
	sleep := func(ctx context.Context, _ time.Duration) error {
		waiting <- struct{}{}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
			return nil
		}
	}
	registry := NewRateGateRegistry(time.Minute)
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(200, `{"puuid":"p","gameName":"n","tagLine":"t"}`, nil), nil
	})
	first, _ := NewDirect("concurrent-shared", &http.Client{Transport: transport}, ClientConfig{Now: clock, Sleep: sleep, RateGates: registry})
	second, _ := NewDirect("concurrent-shared", &http.Client{Transport: transport}, ClientConfig{Now: clock, Sleep: sleep, RateGates: registry})
	registry.advance(first.credentialKey+":"+string(RegionAmericas), now.Add(2*time.Second), now)
	errorsCh := make(chan error, 2)
	for _, client := range []*Client{first, second} {
		go func(client *Client) {
			_, err := client.ResolveAccount(context.Background(), RegionAmericas, "n", "t")
			errorsCh <- err
		}(client)
	}
	<-waiting
	<-waiting
	clockMu.Lock()
	now = now.Add(2 * time.Second)
	clockMu.Unlock()
	close(release)
	for range 2 {
		if err := <-errorsCh; err != nil {
			t.Fatal(err)
		}
	}
}

func TestNormalizeMatchDurationRoleAndItems(t *testing.T) {
	payload := []byte(`{
		"metadata":{"matchId":"NA1_123","participants":["self","other"]},
		"info":{"gameCreation":1000000,"gameStartTimestamp":1100000,"gameEndTimestamp":0,"gameDuration":1800000,"gameVersion":"11.19.401.7438","queueId":420,"mapId":11,"platformId":"NA1",
		"participants":[
			{"puuid":"self","championName":"Garen","teamId":100,"win":true,"kills":7,"deaths":2,"assists":5,"totalMinionsKilled":180,"neutralMinionsKilled":12,"teamPosition":"","individualPosition":"TOP","lane":"TOP","role":"SOLO","item0":3078,"item1":3006,"item2":0,"item3":3071,"item4":0,"item5":0,"item6":3340},
			{"puuid":"other","championName":"Darius","teamId":200,"teamPosition":"TOP"}
		]}}
	`)
	match, err := NormalizeMatch(payload, "NA1_123", "self")
	if err != nil {
		t.Fatal(err)
	}
	if match.DurationSeconds != 1800 || match.EndedAt.UnixMilli() != 1100000+1800000 || match.Patch != "11.19" {
		t.Fatalf("timing = %#v", match)
	}
	if match.Player.Role != "top" || match.Player.RoleProvenance != RoleFromIndividualPosition || match.Player.CreepScore != 192 || len(match.Player.FinalItems) != 4 || match.Player.FinalItems[3].Slot != 6 {
		t.Fatalf("player = %#v", match.Player)
	}
	if match.Player.OpponentChampion != "Darius" || match.Player.OpponentProvenance != OpponentUniqueSameRole {
		t.Fatalf("opponent = %#v", match.Player)
	}
}

func TestNormalizeMatchRejectsMissingLinkedParticipantEvidence(t *testing.T) {
	payload := []byte(`{"metadata":{"matchId":"NA1_123","participants":["someone-else"]},"info":{"gameStartTimestamp":1000,"gameDuration":1800,"gameVersion":"16.18.1","platformId":"NA1","participants":[{"puuid":"self","championName":"Garen"}]}}`)
	if _, err := NormalizeMatch(payload, "NA1_123", "self"); err == nil {
		t.Fatal("missing linked metadata evidence was accepted")
	}
}

func TestNormalizeModernDurationAndAuthoritativeTeamPosition(t *testing.T) {
	payload := []byte(`{
		"metadata":{"matchId":"EUW1_42","participants":["self"]},
		"info":{"gameStartTimestamp":1000000,"gameEndTimestamp":2800000,"gameDuration":1800,"gameVersion":"16.18.1","platformId":"EUW1","queueId":420,"mapId":11,
		"participants":[{"puuid":"self","championName":"Ahri","teamId":100,"teamPosition":"MIDDLE","individualPosition":"TOP","item0":6655}]}}
	`)
	match, err := NormalizeMatch(payload, "EUW1_42", "self")
	if err != nil {
		t.Fatal(err)
	}
	if match.DurationSeconds != 1800 || match.EndedAt.UnixMilli() != 2800000 || match.Player.Role != domain.RoleMiddle || match.Player.RoleProvenance != RoleFromTeamPosition {
		t.Fatalf("modern match = %#v", match)
	}
}

func TestBackendRequiresHTTPSExceptLoopback(t *testing.T) {
	if _, err := NewBackend("http://example.com", nil, ClientConfig{}); err == nil {
		t.Fatal("insecure public backend accepted")
	}
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("X-Riot-Token") != "" {
			t.Fatal("backend adapter sent Riot token")
		}
		return jsonResponse(http.StatusOK, `{"puuid":"p","gameName":"n","tagLine":"t"}`, nil), nil
	})}
	client, err := NewBackend("http://127.0.0.1:7331", httpClient, ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ResolveAccount(context.Background(), RegionAmericas, "n", "t"); err != nil {
		t.Fatal(err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

type timeoutError struct{}

func (timeoutError) Error() string   { return "simulated timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

type timeoutBody struct{}

func (timeoutBody) Read([]byte) (int, error) { return 0, timeoutError{} }
func (timeoutBody) Close() error             { return nil }

func jsonResponse(status int, body string, headers http.Header) *http.Response {
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Set("Content-Type", "application/json")
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: headers}
}
