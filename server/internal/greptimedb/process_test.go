package greptimedb

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var testLogger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

func TestEnsureDefaultConfigFileWritesTemplate(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()

	configPath, err := ensureDefaultConfigFile(dataDir, testLogger)
	if err != nil {
		t.Fatalf("ensureDefaultConfigFile() error = %v", err)
	}

	if got, want := filepath.Base(configPath), defaultConfigFileName; got != want {
		t.Fatalf("config file name = %q, want %q", got, want)
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", configPath, err)
	}

	for _, snippet := range []string{
		`enable_telemetry = false`,
		`max_concurrent_queries = 4`,
		`bind_addr = "127.0.0.1:14001"`,
		`[postgres]`,
		`enable = false`,
		`[influxdb]`,
		`[jaeger]`,
		`[prom_store]`,
		`with_metric_engine = true`,
		`memory_pool_size = "512MB"`,
		`scan_memory_limit = "512MB"`,
		`# tma1-config-version: 2`,
	} {
		if !strings.Contains(string(content), snippet) {
			t.Fatalf("default config missing %q", snippet)
		}
	}
}

func TestEnsureDefaultConfigFilePreservesCurrentVersion(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	configDir := filepath.Join(dataDir, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	configPath := filepath.Join(configDir, defaultConfigFileName)
	want := []byte("# tma1-config-version: 2\ncustom = true\n")
	if err := os.WriteFile(configPath, want, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	gotPath, err := ensureDefaultConfigFile(dataDir, testLogger)
	if err != nil {
		t.Fatalf("ensureDefaultConfigFile() error = %v", err)
	}
	if gotPath != configPath {
		t.Fatalf("configPath = %q, want %q", gotPath, configPath)
	}

	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("config content = %q, want %q (should not be modified)", string(got), string(want))
	}
}

func TestConfigMigrationV1ToV2(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	configDir := filepath.Join(dataDir, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	// Simulate a v1 config (no version line, old memory values).
	v1Config := `# TMA1 default GreptimeDB configuration.
[query]
memory_pool_size = "128MB"

[[region_engine]]
[region_engine.mito]
scan_memory_limit = "128MB"
`
	configPath := filepath.Join(configDir, defaultConfigFileName)
	if err := os.WriteFile(configPath, []byte(v1Config), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := ensureDefaultConfigFile(dataDir, testLogger)
	if err != nil {
		t.Fatalf("ensureDefaultConfigFile() error = %v", err)
	}

	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	content := string(got)
	if !strings.Contains(content, `memory_pool_size = "512MB"`) {
		t.Fatal("migration did not upgrade memory_pool_size")
	}
	if !strings.Contains(content, `scan_memory_limit = "512MB"`) {
		t.Fatal("migration did not upgrade scan_memory_limit")
	}
	if !strings.Contains(content, "# tma1-config-version: 2") {
		t.Fatal("migration did not set version")
	}
}

func TestConfigMigrationSkipsCustomValues(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	configDir := filepath.Join(dataDir, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	// User already customized to 256MB — migration should not override.
	v1Custom := `# TMA1 default GreptimeDB configuration.
[query]
memory_pool_size = "256MB"

[[region_engine]]
[region_engine.mito]
scan_memory_limit = "256MB"
`
	configPath := filepath.Join(configDir, defaultConfigFileName)
	if err := os.WriteFile(configPath, []byte(v1Custom), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := ensureDefaultConfigFile(dataDir, testLogger)
	if err != nil {
		t.Fatalf("ensureDefaultConfigFile() error = %v", err)
	}

	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	content := string(got)
	if !strings.Contains(content, `memory_pool_size = "256MB"`) {
		t.Fatal("migration should not override user-customized memory_pool_size")
	}
	if !strings.Contains(content, `scan_memory_limit = "256MB"`) {
		t.Fatal("migration should not override user-customized scan_memory_limit")
	}
	// Version should still be bumped even though values weren't changed.
	if !strings.Contains(content, "# tma1-config-version: 2") {
		t.Fatal("migration did not set version")
	}
}

func TestConfigVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    int
	}{
		{"no version line", "# TMA1 config\nfoo = bar\n", 1},
		{"version 1", "# tma1-config-version: 1\nfoo = bar\n", 1},
		{"version 2", "# tma1-config-version: 2\nfoo = bar\n", 2},
		{"version 99", "# tma1-config-version: 99\n", 99},
		{"empty file", "", 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := configVersion([]byte(tt.content)); got != tt.want {
				t.Fatalf("configVersion() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestStartArgsIncludeConfigFile(t *testing.T) {
	t.Parallel()

	cfg := Config{
		HTTPPort:  14000,
		GRPCPort:  14001,
		MySQLPort: 14002,
	}

	args := startArgs(cfg, "/tmp/tma1/data", "/tmp/tma1/config/standalone.toml")
	want := []string{
		"standalone",
		"start",
		"-c",
		"/tmp/tma1/config/standalone.toml",
		"--data-home",
		"/tmp/tma1/data",
		"--http-addr=127.0.0.1:14000",
		"--rpc-bind-addr=127.0.0.1:14001",
		"--mysql-addr=127.0.0.1:14002",
	}

	if len(args) != len(want) {
		t.Fatalf("len(args) = %d, want %d", len(args), len(want))
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args[%d] = %q, want %q", i, args[i], want[i])
		}
	}
}
