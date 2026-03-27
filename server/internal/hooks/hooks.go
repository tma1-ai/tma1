// Package hooks installs the TMA1 hook script for Claude Code / Codex integration.
package hooks

import (
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

//go:embed tma1-hook.sh.tmpl
var shTemplate string

//go:embed tma1-hook.ps1.tmpl
var ps1Template string

// EnsureHookScript writes the TMA1 hook script to <dataDir>/hooks/.
// On Unix: tma1-hook.sh (bash + curl). On Windows: tma1-hook.ps1 (PowerShell).
// It is idempotent — the file is only rewritten if the content differs.
// Returns the absolute path to the script.
func EnsureHookScript(dataDir string, port int, logger *slog.Logger) (string, error) {
	dir := filepath.Join(dataDir, "hooks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create hooks dir: %w", err)
	}

	portStr := fmt.Sprintf("%d", port)

	if runtime.GOOS == "windows" {
		content := strings.ReplaceAll(ps1Template, "{{PORT}}", portStr)
		return writeScript(filepath.Join(dir, "tma1-hook.ps1"), content, logger)
	}

	content := strings.ReplaceAll(shTemplate, "{{PORT}}", portStr)
	return writeScript(filepath.Join(dir, "tma1-hook.sh"), content, logger)
}

func writeScript(path, content string, logger *slog.Logger) (string, error) {
	existing, err := os.ReadFile(path)
	if err == nil && string(existing) == content {
		return path, nil
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		return "", fmt.Errorf("write hook script: %w", err)
	}
	logger.Info("hook script installed", "path", path)
	return path, nil
}
