package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"lolengine/internal/domain"
	"lolengine/internal/riotapi"
)

const (
	EnvListenAddress   = "LOL_ENGINE_LISTEN_ADDR"
	EnvLiveClientURL   = "LOL_ENGINE_LIVE_CLIENT_URL"
	EnvPollInterval    = "LOL_ENGINE_POLL_INTERVAL"
	EnvStaleAfter      = "LOL_ENGINE_STALE_AFTER"
	EnvEndAfter        = "LOL_ENGINE_END_AFTER"
	EnvDBPath          = "LOL_ENGINE_DB_PATH"
	EnvDDragonURL      = "LOL_ENGINE_DDRAGON_BASE_URL"
	EnvDDragonLocale   = "LOL_ENGINE_DDRAGON_LOCALE"
	EnvDDragonTimeout  = "LOL_ENGINE_DDRAGON_TIMEOUT"
	EnvDDragonSync     = "LOL_ENGINE_DDRAGON_SYNC"
	EnvRulesDir        = "LOL_ENGINE_RULES_DIR"
	EnvGenericEnabled  = "LOL_ENGINE_GENERIC_ENABLED"
	EnvGenericRulesDir = "LOL_ENGINE_GENERIC_RULES_DIR"
	EnvStrategy        = "LOL_ENGINE_STRATEGY"
	EnvRiotMode        = "LOL_ENGINE_RIOT_MODE"
	EnvRiotRegion      = "LOL_ENGINE_RIOT_REGION"
	EnvRiotAPIKey      = "LOL_ENGINE_RIOT_API_KEY"
	EnvRiotBackendURL  = "LOL_ENGINE_RIOT_BACKEND_URL"
	EnvRiotTimeout     = "LOL_ENGINE_RIOT_TIMEOUT"
	EnvRiotConcurrency = "LOL_ENGINE_RIOT_CONCURRENCY"
	EnvRiotPageSize    = "LOL_ENGINE_RIOT_PAGE_SIZE"
	EnvRiotMaxMatches  = "LOL_ENGINE_RIOT_MAX_MATCHES_PER_SYNC"
	EnvRiotAutoSync    = "LOL_ENGINE_RIOT_AUTO_SYNC"
	EnvRiotRawStorage  = "LOL_ENGINE_RIOT_RAW_STORAGE"
)

// Config contains process-level settings loaded from environment variables.
type Config struct {
	ListenAddress   string
	LiveClientURL   string
	PollInterval    time.Duration
	StaleAfter      time.Duration
	EndAfter        time.Duration
	DBPath          string
	DDragonBaseURL  string
	DDragonLocale   string
	DDragonTimeout  time.Duration
	DDragonSync     bool
	RulesDir        string
	GenericEnabled  bool
	GenericRulesDir string
	Strategy        domain.Strategy
	RiotMode        riotapi.Mode
	RiotRegion      riotapi.Region
	RiotAPIKey      riotapi.Secret
	RiotBackendURL  string
	RiotTimeout     time.Duration
	RiotConcurrency int
	RiotPageSize    int
	RiotMaxMatches  int
	RiotAutoSync    bool
	RiotRawStorage  bool
}

// LoadFromEnv loads configuration with safe localhost defaults.
func LoadFromEnv() (Config, error) {
	cfg := Config{
		ListenAddress:   envOrDefault(EnvListenAddress, "127.0.0.1:8080"),
		LiveClientURL:   envOrDefault(EnvLiveClientURL, "https://127.0.0.1:2999"),
		PollInterval:    2 * time.Second,
		StaleAfter:      10 * time.Second,
		EndAfter:        90 * time.Second,
		DBPath:          envOrDefault(EnvDBPath, "data/lol_engine.db"),
		DDragonBaseURL:  envOrDefault(EnvDDragonURL, "https://ddragon.leagueoflegends.com"),
		DDragonLocale:   envOrDefault(EnvDDragonLocale, "es_MX"),
		DDragonTimeout:  5 * time.Second,
		DDragonSync:     true,
		RulesDir:        envOrDefault(EnvRulesDir, "rules/garen/v2"),
		GenericEnabled:  true,
		GenericRulesDir: envOrDefault(EnvGenericRulesDir, "rules/generic/v1"),
		Strategy:        domain.Strategy(strings.ToLower(envOrDefault(EnvStrategy, string(domain.StrategyBalanced)))),
		RiotMode:        riotapi.Mode(strings.ToLower(envOrDefault(EnvRiotMode, string(riotapi.ModeDisabled)))),
		RiotAPIKey:      riotapi.Secret(strings.TrimSpace(os.Getenv(EnvRiotAPIKey))),
		RiotBackendURL:  strings.TrimSpace(os.Getenv(EnvRiotBackendURL)),
		RiotTimeout:     10 * time.Second,
		RiotConcurrency: 4,
		RiotPageSize:    20,
		RiotMaxMatches:  100,
	}
	if raw := strings.TrimSpace(os.Getenv(EnvRiotRegion)); raw != "" {
		cfg.RiotRegion = riotapi.Region(strings.ToLower(raw))
	}

	var err error
	if raw := strings.TrimSpace(os.Getenv(EnvPollInterval)); raw != "" {
		cfg.PollInterval, err = time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parse %s: %w", EnvPollInterval, err)
		}
	}
	if raw := strings.TrimSpace(os.Getenv(EnvStaleAfter)); raw != "" {
		cfg.StaleAfter, err = time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parse %s: %w", EnvStaleAfter, err)
		}
	}
	if raw := strings.TrimSpace(os.Getenv(EnvEndAfter)); raw != "" {
		cfg.EndAfter, err = time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parse %s: %w", EnvEndAfter, err)
		}
	}
	if raw := strings.TrimSpace(os.Getenv(EnvDDragonTimeout)); raw != "" {
		cfg.DDragonTimeout, err = time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parse %s: %w", EnvDDragonTimeout, err)
		}
	}
	if raw := strings.TrimSpace(os.Getenv(EnvDDragonSync)); raw != "" {
		cfg.DDragonSync, err = strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parse %s: %w", EnvDDragonSync, err)
		}
	}
	if raw := strings.TrimSpace(os.Getenv(EnvGenericEnabled)); raw != "" {
		cfg.GenericEnabled, err = strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parse %s: %w", EnvGenericEnabled, err)
		}
	}
	if raw := strings.TrimSpace(os.Getenv(EnvRiotTimeout)); raw != "" {
		cfg.RiotTimeout, err = time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parse %s: %w", EnvRiotTimeout, err)
		}
	}
	if raw := strings.TrimSpace(os.Getenv(EnvRiotConcurrency)); raw != "" {
		cfg.RiotConcurrency, err = strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parse %s: %w", EnvRiotConcurrency, err)
		}
		if cfg.RiotConcurrency < 1 || cfg.RiotConcurrency > 8 {
			return Config{}, fmt.Errorf("%s must be between 1 and 8", EnvRiotConcurrency)
		}
	}
	if raw := strings.TrimSpace(os.Getenv(EnvRiotPageSize)); raw != "" {
		cfg.RiotPageSize, err = strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parse %s: %w", EnvRiotPageSize, err)
		}
		if cfg.RiotPageSize < 1 || cfg.RiotPageSize > 100 {
			return Config{}, fmt.Errorf("%s must be between 1 and 100", EnvRiotPageSize)
		}
	}
	if raw := strings.TrimSpace(os.Getenv(EnvRiotMaxMatches)); raw != "" {
		cfg.RiotMaxMatches, err = strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parse %s: %w", EnvRiotMaxMatches, err)
		}
		if cfg.RiotMaxMatches < 1 || cfg.RiotMaxMatches > 1000 {
			return Config{}, fmt.Errorf("%s must be between 1 and 1000", EnvRiotMaxMatches)
		}
	}
	if raw := strings.TrimSpace(os.Getenv(EnvRiotAutoSync)); raw != "" {
		cfg.RiotAutoSync, err = strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parse %s: %w", EnvRiotAutoSync, err)
		}
	}
	if raw := strings.TrimSpace(os.Getenv(EnvRiotRawStorage)); raw != "" {
		cfg.RiotRawStorage, err = strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parse %s: %w", EnvRiotRawStorage, err)
		}
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate rejects unsafe bindings and invalid timing values.
func (c Config) Validate() error {
	host, port, err := net.SplitHostPort(c.ListenAddress)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", c.ListenAddress, err)
	}
	if !isLoopbackHost(host) {
		return fmt.Errorf("listen address must use a loopback host, got %q", host)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("listen address has invalid port %q", port)
	}
	if strings.TrimSpace(c.LiveClientURL) == "" {
		return fmt.Errorf("live client URL must not be empty")
	}
	if c.PollInterval <= 0 {
		return fmt.Errorf("poll interval must be greater than zero")
	}
	if c.StaleAfter <= 0 {
		return fmt.Errorf("stale threshold must be greater than zero")
	}
	if c.EndAfter <= c.StaleAfter {
		return fmt.Errorf("end threshold must be greater than stale threshold")
	}
	if strings.TrimSpace(c.DBPath) == "" {
		return fmt.Errorf("database path must not be empty")
	}
	parsedURL, err := url.Parse(strings.TrimSpace(c.DDragonBaseURL))
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Hostname() == "" {
		return fmt.Errorf("Data Dragon base URL must be an absolute HTTP(S) URL")
	}
	if strings.TrimSpace(c.DDragonLocale) == "" {
		return fmt.Errorf("Data Dragon locale must not be empty")
	}
	if c.DDragonTimeout <= 0 {
		return fmt.Errorf("Data Dragon timeout must be greater than zero")
	}
	if strings.TrimSpace(c.RulesDir) == "" {
		return fmt.Errorf("rules directory must not be empty")
	}
	if c.GenericEnabled && strings.TrimSpace(c.GenericRulesDir) == "" {
		return fmt.Errorf("generic rules directory must not be empty")
	}
	if !c.Strategy.Valid() {
		return fmt.Errorf("strategy must be balanced, split_push, or teamfight")
	}
	mode := c.RiotMode
	if mode == "" {
		mode = riotapi.ModeDisabled
	}
	if !mode.Valid() {
		return fmt.Errorf("%s must be disabled, direct, or backend", EnvRiotMode)
	}
	if c.RiotRawStorage {
		return fmt.Errorf("%s=true is unsupported: raw Riot payload storage is prohibited", EnvRiotRawStorage)
	}
	if c.RiotConcurrency != 0 && (c.RiotConcurrency < 1 || c.RiotConcurrency > 8) {
		return fmt.Errorf("%s must be between 1 and 8", EnvRiotConcurrency)
	}
	if c.RiotPageSize != 0 && (c.RiotPageSize < 1 || c.RiotPageSize > 100) {
		return fmt.Errorf("%s must be between 1 and 100", EnvRiotPageSize)
	}
	if c.RiotMaxMatches < 0 || c.RiotMaxMatches > 1000 || (mode != riotapi.ModeDisabled && c.RiotMaxMatches < 1) {
		return fmt.Errorf("%s must be between 1 and 1000", EnvRiotMaxMatches)
	}
	if mode != riotapi.ModeDisabled {
		if !c.RiotRegion.Valid() {
			return fmt.Errorf("%s must be americas, asia, europe, or sea when Riot enrichment is enabled", EnvRiotRegion)
		}
		if c.RiotTimeout <= 0 {
			return fmt.Errorf("%s must be greater than zero", EnvRiotTimeout)
		}
	}
	if mode == riotapi.ModeDirect && strings.TrimSpace(string(c.RiotAPIKey)) == "" {
		return fmt.Errorf("%s is required in private direct mode", EnvRiotAPIKey)
	}
	if mode == riotapi.ModeBackend {
		parsed, err := url.Parse(strings.TrimSpace(c.RiotBackendURL))
		if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("%s must be an absolute HTTPS URL", EnvRiotBackendURL)
		}
		if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())) {
			return fmt.Errorf("%s must use HTTPS except for loopback development", EnvRiotBackendURL)
		}
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
