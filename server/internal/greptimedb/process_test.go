package greptimedb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureDefaultConfigFileWritesTemplate(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()

	configPath, err := ensureDefaultConfigFile(dataDir)
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
		`memory_pool_size = "128MB"`,
	} {
		if !strings.Contains(string(content), snippet) {
			t.Fatalf("default config missing %q", snippet)
		}
	}
}

func TestEnsureDefaultConfigFilePreservesExistingFile(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	configDir := filepath.Join(dataDir, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	configPath := filepath.Join(configDir, defaultConfigFileName)
	want := []byte("custom = true\n")
	if err := os.WriteFile(configPath, want, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	gotPath, err := ensureDefaultConfigFile(dataDir)
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
		t.Fatalf("config content = %q, want %q", string(got), string(want))
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
