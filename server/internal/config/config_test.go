package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	// Clear all TMA1_ env vars to test defaults.
	for _, key := range []string{
		"TMA1_HOST", "TMA1_PORT", "TMA1_DATA_DIR", "TMA1_GREPTIMEDB_VERSION",
		"TMA1_GREPTIMEDB_HTTP_PORT", "TMA1_GREPTIMEDB_GRPC_PORT", "TMA1_GREPTIMEDB_MYSQL_PORT", "TMA1_LOG_LEVEL",
	} {
		t.Setenv(key, "")
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	home, _ := os.UserHomeDir()
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"Host", cfg.Host, "127.0.0.1"},
		{"Port", cfg.Port, "14318"},
		{"DataDir", cfg.DataDir, filepath.Join(home, ".tma1")},
		{"GreptimeDBVersion", cfg.GreptimeDBVersion, defaultGreptimeDBVersion},
		{"LogLevel", cfg.LogLevel, "info"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %q, want %q", tt.got, tt.want)
			}
		})
	}

	intTests := []struct {
		name string
		got  int
		want int
	}{
		{"GreptimeDBHTTPPort", cfg.GreptimeDBHTTPPort, 14000},
		{"GreptimeDBGRPCPort", cfg.GreptimeDBGRPCPort, 14001},
		{"GreptimeDBMySQLPort", cfg.GreptimeDBMySQLPort, 14002},
	}
	for _, tt := range intTests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %d, want %d", tt.got, tt.want)
			}
		})
	}
}

func TestLoadEnvOverrides(t *testing.T) {
	t.Setenv("TMA1_HOST", "0.0.0.0")
	t.Setenv("TMA1_PORT", "9999")
	t.Setenv("TMA1_DATA_DIR", "/tmp/tma1-test")
	t.Setenv("TMA1_GREPTIMEDB_VERSION", "v0.12.0")
	t.Setenv("TMA1_GREPTIMEDB_HTTP_PORT", "5000")
	t.Setenv("TMA1_GREPTIMEDB_GRPC_PORT", "5001")
	t.Setenv("TMA1_GREPTIMEDB_MYSQL_PORT", "5002")
	t.Setenv("TMA1_LOG_LEVEL", "debug")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	tests := []struct {
		name string
		got  string
		want string
	}{
		{"Host", cfg.Host, "0.0.0.0"},
		{"Port", cfg.Port, "9999"},
		{"DataDir", cfg.DataDir, "/tmp/tma1-test"},
		{"GreptimeDBVersion", cfg.GreptimeDBVersion, "v0.12.0"},
		{"LogLevel", cfg.LogLevel, "debug"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %q, want %q", tt.got, tt.want)
			}
		})
	}

	intTests := []struct {
		name string
		got  int
		want int
	}{
		{"GreptimeDBHTTPPort", cfg.GreptimeDBHTTPPort, 5000},
		{"GreptimeDBGRPCPort", cfg.GreptimeDBGRPCPort, 5001},
		{"GreptimeDBMySQLPort", cfg.GreptimeDBMySQLPort, 5002},
	}
	for _, tt := range intTests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %d, want %d", tt.got, tt.want)
			}
		})
	}
}

func TestEnvIntInvalidFallback(t *testing.T) {
	t.Setenv("TMA1_GREPTIMEDB_HTTP_PORT", "not-a-number")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.GreptimeDBHTTPPort != 14000 {
		t.Errorf("expected fallback 14000 for invalid int, got %d", cfg.GreptimeDBHTTPPort)
	}
}

// install.sh lays down the binary before tma1-server ever runs; on drift the
// server replaces it on first start — the churn pinning exists to prevent.
func TestInstallShPinMatchesDefault(t *testing.T) {
	const script = "../../../site/public/install.sh"
	data, err := os.ReadFile(script)
	if err != nil {
		t.Skipf("install.sh not readable from this checkout: %v", err)
	}

	want := `TMA1_GREPTIMEDB_VERSION="${TMA1_GREPTIMEDB_VERSION:-` + defaultGreptimeDBVersion + `}"`
	if !strings.Contains(string(data), want) {
		t.Errorf("install.sh does not pin %s; expected to find:\n\t%s",
			defaultGreptimeDBVersion, want)
	}
}
