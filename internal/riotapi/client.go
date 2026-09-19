package riotapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	rand "math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultTimeout    = 10 * time.Second
	defaultRetryAfter = time.Second
	defaultGateIdle   = 15 * time.Minute
	// A full day accommodates a legitimate daily quota reset while keeping
	// untrusted integer seconds far below time.Duration overflow.
	maximumRetryAfter = 24 * time.Hour
	maximumAttempts   = 3
	maximumBodyBytes  = 4 << 20
	maximumMatchIDLen = 128
)

// Service is the dependency used by match synchronization.
type Service interface {
	ResolveAccount(context.Context, Region, string, string) (Account, error)
	ListMatchIDs(context.Context, Region, string, MatchListQuery) ([]string, error)
	GetMatch(context.Context, Region, string, string) (Match, error)
}

type ClientConfig struct {
	Timeout      time.Duration
	MaxAttempts  int
	RetryAfter   time.Duration
	Now          func() time.Time
	Sleep        func(context.Context, time.Duration) error
	Jitter       func(time.Duration) time.Duration
	MaximumBytes int64
	RateGates    *RateGateRegistry
}

type Client struct {
	httpClient    *http.Client
	direct        bool
	secret        Secret
	backend       *url.URL
	maxAttempts   int
	retryAfter    time.Duration
	now           func() time.Time
	sleep         func(context.Context, time.Duration) error
	jitter        func(time.Duration) time.Duration
	maximumBytes  int64
	credentialKey string
	rateGates     *RateGateRegistry
}

func NewDirect(secret Secret, httpClient *http.Client, config ClientConfig) (*Client, error) {
	if strings.TrimSpace(string(secret)) == "" {
		return nil, errors.New("Riot API key is required for direct mode")
	}
	client := newClient(httpClient, config)
	client.direct = true
	client.secret = secret
	digest := sha256.Sum256([]byte(secret))
	client.credentialKey = hex.EncodeToString(digest[:])
	return client, nil
}

func NewBackend(rawURL string, httpClient *http.Client, config ClientConfig) (*Client, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("Riot backend URL must be an absolute HTTPS URL")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopback(parsed.Hostname())) {
		return nil, errors.New("Riot backend URL must use HTTPS except for explicit loopback development")
	}
	client := newClient(httpClient, config)
	client.backend = parsed
	client.credentialKey = "backend:" + parsed.Host
	return client, nil
}

func newClient(httpClient *http.Client, config ClientConfig) *Client {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	if config.Timeout <= 0 {
		config.Timeout = defaultTimeout
	}
	clone := *httpClient
	// Redirects are rejected rather than relying on net/http's cross-origin
	// header filtering. This makes the credential boundary explicit and also
	// protects against HTTPS-to-HTTP and same-host redirect surprises.
	clone.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	if clone.Timeout <= 0 {
		clone.Timeout = config.Timeout
	}
	maxAttempts := config.MaxAttempts
	if maxAttempts <= 0 || maxAttempts > maximumAttempts {
		maxAttempts = maximumAttempts
	}
	config.RetryAfter = boundedRetryAfter(config.RetryAfter)
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Sleep == nil {
		config.Sleep = sleepContext
	}
	if config.Jitter == nil {
		config.Jitter = func(base time.Duration) time.Duration {
			if base <= 0 {
				return 0
			}
			return time.Duration(rand.Int64N(int64(base/2) + 1))
		}
	}
	if config.MaximumBytes <= 0 || config.MaximumBytes > maximumBodyBytes {
		config.MaximumBytes = maximumBodyBytes
	}
	if config.RateGates == nil {
		config.RateGates = NewRateGateRegistry(defaultGateIdle)
	}
	return &Client{httpClient: &clone, maxAttempts: maxAttempts, retryAfter: config.RetryAfter, now: config.Now, sleep: config.Sleep, jitter: config.Jitter, maximumBytes: config.MaximumBytes, rateGates: config.RateGates}
}

func (client *Client) ResolveAccount(ctx context.Context, region Region, gameName, tagLine string) (Account, error) {
	gameName = strings.TrimSpace(gameName)
	tagLine = strings.TrimSpace(tagLine)
	if !region.Valid() || gameName == "" || tagLine == "" {
		return Account{}, &Error{Kind: ErrorBadRequest, Operation: "resolve account"}
	}
	path := "/riot/account/v1/accounts/by-riot-id/" + url.PathEscape(gameName) + "/" + url.PathEscape(tagLine)
	var account struct {
		PUUID    string `json:"puuid"`
		GameName string `json:"gameName"`
		TagLine  string `json:"tagLine"`
	}
	if err := client.getJSON(ctx, region, path, nil, "resolve account", &account); err != nil {
		return Account{}, err
	}
	if strings.TrimSpace(account.PUUID) == "" || strings.TrimSpace(account.GameName) == "" || strings.TrimSpace(account.TagLine) == "" {
		return Account{}, &Error{Kind: ErrorMalformed, Operation: "resolve account"}
	}
	return Account{PUUID: account.PUUID, GameName: account.GameName, TagLine: account.TagLine}, nil
}

func (client *Client) ListMatchIDs(ctx context.Context, region Region, puuid string, query MatchListQuery) ([]string, error) {
	if !region.Valid() || strings.TrimSpace(puuid) == "" || query.Start < 0 || query.Count < 1 || query.Count > 100 ||
		query.StartTime < 0 || query.EndTime < 0 || (query.StartTime > 0 && query.EndTime > 0 && query.StartTime > query.EndTime) ||
		query.Queue < 0 || !ValidMatchType(query.Type) {
		return nil, &Error{Kind: ErrorBadRequest, Operation: "list matches"}
	}
	values := make(url.Values)
	values.Set("start", strconv.Itoa(query.Start))
	values.Set("count", strconv.Itoa(query.Count))
	if query.StartTime > 0 {
		values.Set("startTime", strconv.FormatInt(query.StartTime, 10))
	}
	if query.EndTime > 0 {
		values.Set("endTime", strconv.FormatInt(query.EndTime, 10))
	}
	if query.Queue > 0 {
		values.Set("queue", strconv.Itoa(query.Queue))
	}
	if value := strings.TrimSpace(query.Type); value != "" {
		values.Set("type", strings.ToLower(value))
	}
	path := "/lol/match/v5/matches/by-puuid/" + url.PathEscape(puuid) + "/ids"
	maximumListBytes := int64(2 + query.Count*(maximumMatchIDLen+3))
	if maximumListBytes > client.maximumBytes {
		maximumListBytes = client.maximumBytes
	}
	payload, err := client.getBytesLimit(ctx, region, path, values, "list matches", maximumListBytes)
	if err != nil {
		return nil, err
	}
	ids, err := decodeMatchIDs(payload, query.Count)
	if err != nil {
		return nil, &Error{Kind: ErrorMalformed, Operation: "list matches", Cause: err}
	}
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if !validMatchID(id) {
			return nil, &Error{Kind: ErrorMalformed, Operation: "list matches"}
		}
		platform, ok := ParsePlatform(strings.SplitN(id, "_", 2)[0])
		platformRegion, regionOK := platform.Region()
		if !ok || !regionOK || platformRegion != region {
			return nil, &Error{Kind: ErrorMalformed, Operation: "list matches"}
		}
		result = append(result, id)
	}
	return result, nil
}

func (client *Client) GetMatch(ctx context.Context, region Region, matchID, linkedPUUID string) (Match, error) {
	matchID = strings.TrimSpace(matchID)
	linkedPUUID = strings.TrimSpace(linkedPUUID)
	if !region.Valid() || !validMatchID(matchID) || linkedPUUID == "" {
		return Match{}, &Error{Kind: ErrorBadRequest, Operation: "get match"}
	}
	path := "/lol/match/v5/matches/" + url.PathEscape(matchID)
	payload, err := client.getBytes(ctx, region, path, nil, "get match")
	if err != nil {
		return Match{}, err
	}
	match, err := NormalizeMatch(payload, matchID, linkedPUUID)
	if err != nil {
		return Match{}, &Error{Kind: ErrorMalformed, Operation: "get match", Cause: err}
	}
	matchRegion, ok := match.Platform.Region()
	if !ok || matchRegion != region {
		return Match{}, &Error{Kind: ErrorMalformed, Operation: "get match"}
	}
	return match, nil
}

func (client *Client) getJSON(ctx context.Context, region Region, path string, values url.Values, operation string, destination any) error {
	payload, err := client.getBytes(ctx, region, path, values, operation)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(destination); err != nil {
		return &Error{Kind: ErrorMalformed, Operation: operation, Cause: err}
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return &Error{Kind: ErrorMalformed, Operation: operation}
	}
	return nil
}

func (client *Client) getBytes(ctx context.Context, region Region, path string, values url.Values, operation string) ([]byte, error) {
	return client.getBytesLimit(ctx, region, path, values, operation, client.maximumBytes)
}

func (client *Client) getBytesLimit(ctx context.Context, region Region, path string, values url.Values, operation string, maximumBytes int64) ([]byte, error) {
	if maximumBytes <= 0 || maximumBytes > client.maximumBytes {
		maximumBytes = client.maximumBytes
	}
	for attempt := 1; attempt <= client.maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, &Error{Kind: ErrorCancelled, Operation: operation, Cause: err}
		}
		gateKey := client.credentialKey + ":" + string(region)
		if err := client.rateGates.wait(ctx, gateKey, client.now, client.sleep); err != nil {
			if ctx.Err() != nil {
				return nil, &Error{Kind: ErrorCancelled, Operation: operation, Cause: ctx.Err()}
			}
			return nil, &Error{Kind: ErrorTransport, Operation: operation, Cause: err}
		}
		requestURL, err := client.endpoint(region, path, values)
		if err != nil {
			return nil, &Error{Kind: ErrorBadRequest, Operation: operation, Cause: err}
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
		if err != nil {
			return nil, &Error{Kind: ErrorBadRequest, Operation: operation, Cause: err}
		}
		if client.direct {
			request.Header.Set("X-Riot-Token", string(client.secret))
		}
		response, err := client.httpClient.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return nil, &Error{Kind: ErrorCancelled, Operation: operation, Cause: ctx.Err()}
			}
			var networkErr net.Error
			if errors.As(err, &networkErr) && networkErr.Timeout() {
				return nil, &Error{Kind: ErrorTransport, Operation: operation, Cause: err}
			}
			return nil, &Error{Kind: ErrorTransport, Operation: operation, Cause: err}
		}
		payload, readErr := io.ReadAll(io.LimitReader(response.Body, maximumBytes+1))
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil {
			if ctx.Err() != nil {
				return nil, &Error{Kind: ErrorCancelled, Operation: operation, Cause: ctx.Err()}
			}
			cause := readErr
			if cause == nil {
				cause = closeErr
			}
			return nil, &Error{Kind: ErrorTransport, Operation: operation, StatusCode: response.StatusCode, Cause: cause}
		}
		if int64(len(payload)) > maximumBytes {
			return nil, &Error{Kind: ErrorMalformed, Operation: operation, StatusCode: response.StatusCode}
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return payload, nil
		}
		kind := errorKindForStatus(response.StatusCode)
		retryAfter := time.Duration(0)
		if response.StatusCode == http.StatusTooManyRequests {
			retryAfter = parseRetryAfter(response.Header.Get("Retry-After"), client.retryAfter)
			current := client.now()
			client.rateGates.advance(gateKey, current.Add(retryAfter), current)
		}
		apiErr := &Error{Kind: kind, Operation: operation, StatusCode: response.StatusCode, RetryAfter: retryAfter}
		if attempt >= client.maxAttempts || (kind != ErrorRateLimited && kind != ErrorServer) {
			return nil, apiErr
		}
		if kind == ErrorServer {
			delay := time.Duration(1<<(attempt-1))*100*time.Millisecond + client.jitter(100*time.Millisecond)
			if err := client.sleep(ctx, delay); err != nil {
				if ctx.Err() != nil {
					return nil, &Error{Kind: ErrorCancelled, Operation: operation, Cause: ctx.Err()}
				}
				return nil, &Error{Kind: ErrorTransport, Operation: operation, Cause: err}
			}
		}
	}
	return nil, &Error{Kind: ErrorServer, Operation: operation}
}

func (client *Client) endpoint(region Region, path string, values url.Values) (*url.URL, error) {
	if !region.Valid() {
		return nil, errors.New("invalid region")
	}
	var endpoint url.URL
	if client.direct {
		host, _ := region.Host()
		endpoint = url.URL{Scheme: "https", Host: host}
	} else {
		endpoint = *client.backend
		basePath := strings.TrimSuffix(endpoint.EscapedPath(), "/")
		path = basePath + path
		if values == nil {
			values = make(url.Values)
		}
		values.Set("region", string(region))
	}
	decoded, err := url.PathUnescape(path)
	if err != nil {
		return nil, err
	}
	endpoint.Path = decoded
	endpoint.RawPath = path
	endpoint.RawQuery = values.Encode()
	return &endpoint, nil
}

func errorKindForStatus(status int) ErrorKind {
	switch status {
	case 400:
		return ErrorBadRequest
	case 401:
		return ErrorUnauthorized
	case 403:
		return ErrorForbidden
	case 404:
		return ErrorNotFound
	case 429:
		return ErrorRateLimited
	default:
		if status >= 500 {
			return ErrorServer
		}
		return ErrorMalformed
	}
}

func parseRetryAfter(value string, fallback time.Duration) time.Duration {
	fallback = boundedRetryAfter(fallback)
	seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || seconds <= 0 {
		return fallback
	}
	maximumSeconds := int64(maximumRetryAfter / time.Second)
	if seconds >= maximumSeconds {
		return maximumRetryAfter
	}
	return time.Duration(seconds) * time.Second
}

func boundedRetryAfter(value time.Duration) time.Duration {
	if value <= 0 {
		return defaultRetryAfter
	}
	if value > maximumRetryAfter {
		return maximumRetryAfter
	}
	return value
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isLoopback(host string) bool {
	if strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validMatchID(value string) bool {
	if len(value) == 0 || len(value) > maximumMatchIDLen {
		return false
	}
	underscore := strings.LastIndexByte(value, '_')
	if underscore <= 0 || underscore == len(value)-1 {
		return false
	}
	for _, char := range value[:underscore] {
		if !(char >= 'A' && char <= 'Z') && !(char >= '0' && char <= '9') {
			return false
		}
	}
	for _, char := range value[underscore+1:] {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

type rateGate struct {
	notBefore time.Time
	lastUsed  time.Time
}

// RateGateRegistry is client-scoped by default. Callers that intentionally
// want several clients using the same credential to share 429 back-pressure
// can inject one registry into each ClientConfig.
type RateGateRegistry struct {
	mu      sync.Mutex
	gates   map[string]*rateGate
	idleTTL time.Duration
}

func NewRateGateRegistry(idleTTL time.Duration) *RateGateRegistry {
	if idleTTL <= 0 {
		idleTTL = defaultGateIdle
	}
	return &RateGateRegistry{gates: make(map[string]*rateGate), idleTTL: idleTTL}
}

func (registry *RateGateRegistry) gateLocked(key string, now time.Time) *rateGate {
	registry.evictLocked(now)
	gate := registry.gates[key]
	if gate == nil {
		gate = &rateGate{}
		registry.gates[key] = gate
	}
	gate.lastUsed = now
	return gate
}

func (registry *RateGateRegistry) evictLocked(now time.Time) {
	for key, gate := range registry.gates {
		if !gate.lastUsed.IsZero() && now.Sub(gate.lastUsed) >= registry.idleTTL && !gate.notBefore.After(now) {
			delete(registry.gates, key)
		}
	}
}

func (registry *RateGateRegistry) advance(key string, deadline, now time.Time) {
	registry.mu.Lock()
	gate := registry.gateLocked(key, now)
	if deadline.After(gate.notBefore) {
		gate.notBefore = deadline
	}
	registry.mu.Unlock()
}

func (registry *RateGateRegistry) wait(ctx context.Context, key string, now func() time.Time, sleep func(context.Context, time.Duration) error) error {
	for {
		current := now()
		registry.mu.Lock()
		gate := registry.gateLocked(key, current)
		remaining := gate.notBefore.Sub(current)
		registry.mu.Unlock()
		if remaining <= 0 {
			return nil
		}
		if err := sleep(ctx, remaining); err != nil {
			return err
		}
	}
}

// Len reports the current bounded registry size for deterministic tests and
// operational diagnostics; it never exposes credential-derived keys.
func (registry *RateGateRegistry) Len(now time.Time) int {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.evictLocked(now)
	return len(registry.gates)
}

func decodeMatchIDs(payload []byte, count int) ([]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('[') {
		return nil, errors.New("match ID response must be an array")
	}
	ids := make([]string, 0, count)
	for decoder.More() {
		if len(ids) >= count {
			return nil, errors.New("match ID response exceeds requested count")
		}
		var id string
		if err := decoder.Decode(&id); err != nil {
			return nil, err
		}
		if len(id) == 0 || len(id) > maximumMatchIDLen {
			return nil, errors.New("match ID exceeds supported length")
		}
		ids = append(ids, id)
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim(']') {
		return nil, errors.New("invalid match ID array")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, errors.New("trailing match ID data")
	}
	return ids, nil
}

var _ Service = (*Client)(nil)

// Compile-time guard against accidentally formatting a raw secret in an error.
var _ fmt.Stringer = Secret("")
