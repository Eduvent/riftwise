package config

import (
	"testing"
	"time"

	"lolengine/internal/domain"
	"lolengine/internal/riotapi"
)

func TestLoadFromEnvDefaults(t *testing.T) {
	t.Setenv(EnvListenAddress, "")
	t.Setenv(EnvLiveClientURL, "")
	t.Setenv(EnvPollInterval, "")
	t.Setenv(EnvStaleAfter, "")
	t.Setenv(EnvEndAfter, "")
	t.Setenv(EnvDBPath, "")
	t.Setenv(EnvDDragonURL, "")
	t.Setenv(EnvDDragonLocale, "")
	t.Setenv(EnvDDragonTimeout, "")
	t.Setenv(EnvDDragonSync, "")
	t.Setenv(EnvRulesDir, "")
	t.Setenv(EnvGenericEnabled, "")
	t.Setenv(EnvGenericRulesDir, "")
	t.Setenv(EnvStrategy, "")
	t.Setenv(EnvRiotMode, "")
	t.Setenv(EnvRiotRegion, "")
	t.Setenv(EnvRiotAPIKey, "")
	t.Setenv(EnvRiotBackendURL, "")
	t.Setenv(EnvRiotTimeout, "")
	t.Setenv(EnvRiotConcurrency, "")
	t.Setenv(EnvRiotPageSize, "")
	t.Setenv(EnvRiotMaxMatches, "")
	t.Setenv(EnvRiotAutoSync, "")
	t.Setenv(EnvRiotRawStorage, "")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv() error = %v", err)
	}
	if cfg.ListenAddress != "127.0.0.1:8080" {
		t.Errorf("ListenAddress = %q", cfg.ListenAddress)
	}
	if cfg.LiveClientURL != "https://127.0.0.1:2999" {
		t.Errorf("LiveClientURL = %q", cfg.LiveClientURL)
	}
	if cfg.PollInterval != 2*time.Second || cfg.StaleAfter != 10*time.Second || cfg.EndAfter != 90*time.Second {
		t.Errorf("timings = %v / %v / %v", cfg.PollInterval, cfg.StaleAfter, cfg.EndAfter)
	}
	if cfg.DBPath != "data/lol_engine.db" || cfg.DDragonLocale != "es_MX" || !cfg.DDragonSync {
		t.Errorf("Data Dragon/storage defaults = %#v", cfg)
	}
	if cfg.RulesDir != "rules/garen/v2" || cfg.Strategy != domain.StrategyBalanced {
		t.Errorf("rules defaults = %#v", cfg)
	}
	if !cfg.GenericEnabled || cfg.GenericRulesDir != "rules/generic/v1" {
		t.Errorf("generic defaults = %#v", cfg)
	}
	if cfg.RiotMode != riotapi.ModeDisabled || cfg.RiotConcurrency != 4 || cfg.RiotPageSize != 20 || cfg.RiotMaxMatches != 100 || cfg.RiotAutoSync || cfg.RiotRawStorage {
		t.Errorf("Riot defaults = %#v", cfg)
	}
}

func TestRiotConfigurationIsExplicitAndBounded(t *testing.T) {
	t.Setenv(EnvRiotMode, "direct")
	t.Setenv(EnvRiotRegion, "americas")
	t.Setenv(EnvRiotAPIKey, "RGAPI-secret")
	t.Setenv(EnvRiotTimeout, "3s")
	t.Setenv(EnvRiotConcurrency, "8")
	t.Setenv(EnvRiotPageSize, "100")
	t.Setenv(EnvRiotMaxMatches, "250")
	t.Setenv(EnvRiotAutoSync, "true")
	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RiotMode != riotapi.ModeDirect || cfg.RiotRegion != riotapi.RegionAmericas || string(cfg.RiotAPIKey) != "RGAPI-secret" || cfg.RiotTimeout != 3*time.Second || cfg.RiotConcurrency != 8 || cfg.RiotPageSize != 100 || cfg.RiotMaxMatches != 250 || !cfg.RiotAutoSync {
		t.Fatalf("Riot config = %#v", cfg)
	}

	t.Setenv(EnvRiotAPIKey, "")
	if _, err := LoadFromEnv(); err == nil {
		t.Fatal("direct mode without a key was accepted")
	}
	t.Setenv(EnvRiotAPIKey, "RGAPI-secret")
	t.Setenv(EnvRiotRegion, "")
	if _, err := LoadFromEnv(); err == nil {
		t.Fatal("direct mode without an explicit region was accepted")
	}
	t.Setenv(EnvRiotRegion, "americas")
	t.Setenv(EnvRiotMode, "backend")
	t.Setenv(EnvRiotBackendURL, "http://example.com")
	if _, err := LoadFromEnv(); err == nil {
		t.Fatal("non-loopback HTTP backend was accepted")
	}
	t.Setenv(EnvRiotBackendURL, "https://riot-proxy.example.com")
	if _, err := LoadFromEnv(); err != nil {
		t.Fatalf("HTTPS backend rejected: %v", err)
	}
}

func TestRiotConfigurationRejectsRawStorageAndOutOfBounds(t *testing.T) {
	tests := []struct{ name, env, value string }{
		{"raw storage", EnvRiotRawStorage, "true"},
		{"concurrency", EnvRiotConcurrency, "9"},
		{"page size", EnvRiotPageSize, "101"},
		{"max matches", EnvRiotMaxMatches, "0"},
		{"too many matches", EnvRiotMaxMatches, "1001"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(test.env, test.value)
			if _, err := LoadFromEnv(); err == nil {
				t.Fatal("invalid Riot config was accepted")
			}
		})
	}
}

func TestLoadFromEnvOverrides(t *testing.T) {
	t.Setenv(EnvListenAddress, "[::1]:9090")
	t.Setenv(EnvLiveClientURL, "http://localhost:2999")
	t.Setenv(EnvPollInterval, "750ms")
	t.Setenv(EnvStaleAfter, "3s")
	t.Setenv(EnvEndAfter, "30s")
	t.Setenv(EnvDBPath, "/tmp/lol-engine-test.db")
	t.Setenv(EnvDDragonURL, "http://127.0.0.1:9999")
	t.Setenv(EnvDDragonLocale, "es_ES")
	t.Setenv(EnvDDragonTimeout, "2s")
	t.Setenv(EnvDDragonSync, "false")
	t.Setenv(EnvRulesDir, "/tmp/garen-rules")
	t.Setenv(EnvGenericEnabled, "false")
	t.Setenv(EnvGenericRulesDir, "/tmp/generic-rules")
	t.Setenv(EnvStrategy, "split_push")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv() error = %v", err)
	}
	if cfg.ListenAddress != "[::1]:9090" || cfg.PollInterval != 750*time.Millisecond || cfg.StaleAfter != 3*time.Second || cfg.EndAfter != 30*time.Second {
		t.Fatalf("config = %#v", cfg)
	}
	if cfg.DBPath != "/tmp/lol-engine-test.db" || cfg.DDragonLocale != "es_ES" || cfg.DDragonTimeout != 2*time.Second || cfg.DDragonSync {
		t.Fatalf("extended config = %#v", cfg)
	}
	if cfg.RulesDir != "/tmp/garen-rules" || cfg.Strategy != domain.StrategySplitPush {
		t.Fatalf("rules config = %#v", cfg)
	}
	if cfg.GenericEnabled || cfg.GenericRulesDir != "/tmp/generic-rules" {
		t.Fatalf("generic config = %#v", cfg)
	}
}

func TestValidateRejectsUnsafeOrInvalidConfiguration(t *testing.T) {
	valid := Config{
		ListenAddress:   "127.0.0.1:8080",
		LiveClientURL:   "https://127.0.0.1:2999",
		PollInterval:    time.Second,
		StaleAfter:      5 * time.Second,
		EndAfter:        time.Minute,
		DBPath:          "data/test.db",
		DDragonBaseURL:  "https://ddragon.leagueoflegends.com",
		DDragonLocale:   "es_MX",
		DDragonTimeout:  5 * time.Second,
		RulesDir:        "rules/garen/v2",
		GenericEnabled:  true,
		GenericRulesDir: "rules/generic/v1",
		Strategy:        domain.StrategyBalanced,
	}
	tests := []struct {
		name   string
		change func(*Config)
	}{
		{name: "all interfaces", change: func(cfg *Config) { cfg.ListenAddress = "0.0.0.0:8080" }},
		{name: "non-loopback", change: func(cfg *Config) { cfg.ListenAddress = "192.168.1.10:8080" }},
		{name: "missing port", change: func(cfg *Config) { cfg.ListenAddress = "127.0.0.1" }},
		{name: "invalid port", change: func(cfg *Config) { cfg.ListenAddress = "127.0.0.1:70000" }},
		{name: "empty live URL", change: func(cfg *Config) { cfg.LiveClientURL = "" }},
		{name: "zero polling", change: func(cfg *Config) { cfg.PollInterval = 0 }},
		{name: "zero stale threshold", change: func(cfg *Config) { cfg.StaleAfter = 0 }},
		{name: "end threshold not conservative", change: func(cfg *Config) { cfg.EndAfter = cfg.StaleAfter }},
		{name: "empty database path", change: func(cfg *Config) { cfg.DBPath = "" }},
		{name: "invalid Data Dragon URL", change: func(cfg *Config) { cfg.DDragonBaseURL = "file:///tmp/data" }},
		{name: "empty Data Dragon locale", change: func(cfg *Config) { cfg.DDragonLocale = "" }},
		{name: "zero Data Dragon timeout", change: func(cfg *Config) { cfg.DDragonTimeout = 0 }},
		{name: "empty rules directory", change: func(cfg *Config) { cfg.RulesDir = "" }},
		{name: "empty generic rules directory", change: func(cfg *Config) { cfg.GenericRulesDir = "" }},
		{name: "invalid strategy", change: func(cfg *Config) { cfg.Strategy = "roam_only" }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := valid
			test.change(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("Validate() unexpectedly succeeded")
			}
		})
	}

	disabled := valid
	disabled.GenericEnabled = false
	disabled.GenericRulesDir = ""
	if err := disabled.Validate(); err != nil {
		t.Fatalf("disabled generic routing should not require a profile directory: %v", err)
	}
}

func TestLoadFromEnvRejectsInvalidGenericEnabled(t *testing.T) {
	t.Setenv(EnvGenericEnabled, "sometimes")
	if _, err := LoadFromEnv(); err == nil {
		t.Fatal("LoadFromEnv() unexpectedly accepted invalid generic flag")
	}
}
