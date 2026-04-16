package greptimedb

import (
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const defaultConfigFileName = "standalone.toml"

// currentConfigVersion is bumped whenever standalone-default.toml changes
// in a way that existing user configs should be migrated.
const currentConfigVersion = 3

//go:embed standalone-default.toml
var defaultConfig []byte

// configMigration describes a single version upgrade step.
type configMigration struct {
	version int                  // target version after this migration
	apply   func([]byte) []byte // transform config content
}

// configMigrations is the ordered list of upgrade steps.
// Each entry upgrades from version N-1 to N.
var configMigrations = []configMigration{
	{2, migrateV1ToV2},
	{3, migrateV2ToV3},
}

// migrateV1ToV2 raises memory limits from 128MB to 512MB.
// Uses exact string replacement so user-customized values are preserved.
func migrateV1ToV2(data []byte) []byte {
	s := string(data)
	s = strings.Replace(s, `memory_pool_size = "128MB"`, `memory_pool_size = "512MB"`, 1)
	s = strings.Replace(s, `scan_memory_limit = "128MB"`, `scan_memory_limit = "512MB"`, 1)
	return []byte(s)
}

// migrateV2ToV3 raises compaction memory limit to match query memory limits.
func migrateV2ToV3(data []byte) []byte {
	s := string(data)
	s = strings.Replace(s, `experimental_compaction_memory_limit = "64MB"`, `experimental_compaction_memory_limit = "512MB"`, 1)
	return []byte(s)
}

// configVersion extracts the version number from a "# tma1-config-version: N" comment.
// Returns 1 if no version line is found (pre-versioning configs).
func configVersion(content []byte) int {
	firstLine, _, _ := strings.Cut(string(content), "\n")
	const prefix = "# tma1-config-version: "
	if strings.HasPrefix(firstLine, prefix) {
		if v, err := strconv.Atoi(strings.TrimSpace(firstLine[len(prefix):])); err == nil && v > 0 {
			return v
		}
	}
	return 1
}

// setConfigVersion inserts or updates the version comment at the top of the config.
func setConfigVersion(data []byte, version int) []byte {
	s := string(data)
	versionLine := fmt.Sprintf("# tma1-config-version: %d\n", version)

	const prefix = "# tma1-config-version: "
	if strings.HasPrefix(s, prefix) {
		// Replace existing version line.
		idx := strings.IndexByte(s, '\n')
		if idx >= 0 {
			return []byte(versionLine + s[idx+1:])
		}
		// Version line is the entire file (no newline) — just replace it.
		return []byte(versionLine)
	}
	// Prepend version line.
	return []byte(versionLine + s)
}

func ensureDefaultConfigFile(dataDir string, logger *slog.Logger) (string, error) {
	configDir := filepath.Join(dataDir, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return "", fmt.Errorf("greptimedb: create config dir: %w", err)
	}

	configPath := filepath.Join(configDir, defaultConfigFileName)

	// Fresh install: write default config.
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if err := os.WriteFile(configPath, defaultConfig, 0o644); err != nil {
			return "", fmt.Errorf("greptimedb: write default config: %w", err)
		}
		return configPath, nil
	} else if err != nil {
		return "", fmt.Errorf("greptimedb: stat config file: %w", err)
	}

	// Existing config: check version and apply migrations if needed.
	content, err := os.ReadFile(configPath) //nolint:gosec
	if err != nil {
		logger.Warn("greptimedb: cannot read config for migration, using as-is", "error", err)
		return configPath, nil
	}

	oldVersion := configVersion(content)
	if oldVersion >= currentConfigVersion {
		return configPath, nil
	}

	// Apply migrations sequentially.
	migrated := content
	for _, m := range configMigrations {
		if m.version > oldVersion {
			migrated = m.apply(migrated)
		}
	}
	migrated = setConfigVersion(migrated, currentConfigVersion)

	if err := atomicWriteFile(configPath, migrated, 0o644); err != nil {
		logger.Warn("greptimedb: config migration write failed, using old config", "error", err)
		return configPath, nil
	}

	logger.Info("greptimedb: config upgraded", "from", oldVersion, "to", currentConfigVersion)
	return configPath, nil
}

// atomicWriteFile writes data to a temp file in the same directory, then renames
// it to the target path. This prevents corruption if the process crashes mid-write.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tma1-config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	if n, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write temp file: %w", err)
	} else if n != len(data) {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write temp file: short write: wrote %d of %d bytes", n, len(data))
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}

func startArgs(cfg Config, dataPath, configPath string) []string {
	return []string{
		"standalone", "start",
		"-c", configPath,
		"--data-home", dataPath,
		fmt.Sprintf("--http-addr=127.0.0.1:%d", cfg.HTTPPort),
		fmt.Sprintf("--rpc-bind-addr=127.0.0.1:%d", cfg.GRPCPort),
		fmt.Sprintf("--mysql-addr=127.0.0.1:%d", cfg.MySQLPort),
		// OTLP endpoint is part of the HTTP server in GreptimeDB standalone.
		// The HTTP port serves both the API and OTLP (/v1/otlp).
	}
}
