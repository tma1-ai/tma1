package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// Config holds all runtime configuration for tma1-server.
type Config struct {
	// Host is the address tma1-server binds to (default 127.0.0.1).
	Host string

	// Port tma1-server listens on.
	Port string

	// DataDir is where GreptimeDB binary and data are stored (~/.tma1).
	DataDir string

	// GreptimeDB version to download ("latest" or "v0.x.y").
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
		GreptimeDBVersion:   env("TMA1_GREPTIMEDB_VERSION", "latest"),
		GreptimeDBHTTPPort:  envInt("TMA1_GREPTIMEDB_HTTP_PORT", 14000),
		GreptimeDBGRPCPort:  envInt("TMA1_GREPTIMEDB_GRPC_PORT", 14001),
		GreptimeDBMySQLPort: envInt("TMA1_GREPTIMEDB_MYSQL_PORT", 14002),
		LogLevel:            env("TMA1_LOG_LEVEL", "info"),
		DataTTL:             env("TMA1_DATA_TTL", "60d"),
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
