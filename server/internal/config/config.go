package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// Pinned rather than "latest" because GitHub's /releases/latest redirect skips
// pre-releases: "latest" would resolve to an older stable tag and re-download it
// on every start. Mirrored in site/public/install.sh.
const defaultGreptimeDBVersion = "v1.2.0-beta.2"

// Config holds all runtime configuration for tma1-server.
type Config struct {
	// Host is the address tma1-server binds to (default 127.0.0.1).
	Host string

	// Port tma1-server listens on.
	Port string

	// DataDir is where GreptimeDB binary and data are stored (~/.tma1).
	DataDir string

	// GreptimeDB version to download ("latest" or an exact tag).
	GreptimeDBVersion string

	// GreptimeDB HTTP API port (used for SQL queries, health checks, and OTLP ingestion).
	GreptimeDBHTTPPort int

	// GreptimeDB gRPC port.
	GreptimeDBGRPCPort int

	// GreptimeDB MySQL port (used for direct SQL connections).
	GreptimeDBMySQLPort int

	// LogLevel: debug, info, warn, error.
	LogLevel string

	// DataTTL is the default TTL for auto-created tables (e.g. "60d", "30d").
	DataTTL string

	// LLMAPIKey is the API key for the LLM provider (optional, enables prompt evaluation).
	LLMAPIKey string

	// LLMProvider is the LLM provider: "anthropic" or "openai" (default "anthropic").
	LLMProvider string

	// LLMModel overrides the default model for the LLM provider.
	LLMModel string

	// QueryConcurrency caps the number of in-flight SQL queries from the dashboard.
	// Excess queries queue client-side. Default 4. Lower this if GreptimeDB hits
	// memory limits on wide ranges (30d).
	QueryConcurrency int
}

// Load reads config from environment variables, with sensible defaults.
func Load() (*Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("config: cannot determine home dir: %w", err)
	}

	cfg := &Config{
		Host:                env("TMA1_HOST", "127.0.0.1"),
		Port:                env("TMA1_PORT", "14318"),
		DataDir:             env("TMA1_DATA_DIR", filepath.Join(home, ".tma1")),
		GreptimeDBVersion:   env("TMA1_GREPTIMEDB_VERSION", defaultGreptimeDBVersion),
		GreptimeDBHTTPPort:  envInt("TMA1_GREPTIMEDB_HTTP_PORT", 14000),
		GreptimeDBGRPCPort:  envInt("TMA1_GREPTIMEDB_GRPC_PORT", 14001),
		GreptimeDBMySQLPort: envInt("TMA1_GREPTIMEDB_MYSQL_PORT", 14002),
		LogLevel:            env("TMA1_LOG_LEVEL", "info"),
		DataTTL:             env("TMA1_DATA_TTL", "60d"),
		LLMAPIKey:           env("TMA1_LLM_API_KEY", ""),
		LLMProvider:         env("TMA1_LLM_PROVIDER", "anthropic"),
		LLMModel:            env("TMA1_LLM_MODEL", ""),
		QueryConcurrency:    envInt("TMA1_QUERY_CONCURRENCY", 4),
	}

	return cfg, nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}
