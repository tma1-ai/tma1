// Package hooks installs the TMA1 hook script for Claude Code / Codex integration.
package hooks

import (
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/tma1-ai/tma1/server/internal/relay"
)

//go:embed tma1-hook.sh.tmpl
var shTemplate string

//go:embed tma1-hook.ps1.tmpl
var ps1Template string

//go:embed tma1-hook-codex.sh.tmpl
var shTemplateCodex string

//go:embed tma1-hook-codex.ps1.tmpl
var ps1TemplateCodex string

// Adapter identifies which agent the hook script is wired to. The
// templates differ because each agent's hook protocol envelope is
// different (CC: raw stdout / Stop-JSON; Codex: hookSpecificOutput
// shape via ?envelope=codex on the server).
type Adapter string

const (
	AdapterClaudeCode Adapter = "claude-code"
	AdapterCodex      Adapter = "codex"
)

// EnsureHookScript writes the TMA1 hook script for `AdapterClaudeCode`
// to <dataDir>/hooks/. Idempotent. Returns the absolute path.
// Kept for backwards compatibility with main.go's startup wiring; new
// adapter installers go through EnsureHookScriptFor.
func EnsureHookScript(dataDir string, port int, logger *slog.Logger) (string, error) {
	return EnsureHookScriptFor(AdapterClaudeCode, dataDir, port, logger)
}

// EnsureHookScriptFor writes the hook script for the requested adapter
// to <dataDir>/hooks/. Unix gets a `.sh`, Windows gets a `.ps1`. The
// content is the embedded template with `{{PORT}}` and `{{ROLE}}`
// substituted. Idempotent — the file is only rewritten if its content
// differs.
func EnsureHookScriptFor(adapter Adapter, dataDir string, port int, logger *slog.Logger) (string, error) {
	dir := filepath.Join(dataDir, "hooks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create hooks dir: %w", err)
	}

	portStr := fmt.Sprintf("%d", port)
	tmpl, name := selectHookTemplate(adapter)
	content := strings.ReplaceAll(tmpl, "{{PORT}}", portStr)
	content = strings.ReplaceAll(content, "{{ROLE}}", defaultRole(adapter))
	return writeScript(filepath.Join(dir, name), content, logger)
}

// defaultRole maps an adapter to its relay role: Claude Code drives the
// workflow (driver), Codex reviews (reviewer). Baked into the hook
// script as the {{ROLE}} default and overridable at runtime via the
// TMA1_RELAY_ROLE env in the agent's shell. Deriving from the adapter
// (rather than an install flag) keeps the server-startup EnsureHookScript
// and the installer writing identical script content for a given adapter,
// so neither clobbers the other.
func defaultRole(adapter Adapter) string {
	if adapter == AdapterCodex {
		return relay.RoleReviewer
	}
	return relay.RoleDriver
}

// relayEnv returns the MCP-child env entries the relay handoff tool needs:
// the parent server port to POST signals to, the agent's role, and the
// shared signal-auth token. Merged into each adapter's MCP server env.
func relayEnv(dataDir string, port int, role string, dryRun bool) map[string]any {
	env := map[string]any{
		"TMA1_PORT":       strconv.Itoa(port),
		"TMA1_RELAY_ROLE": role,
	}
	var tok string
	if dryRun {
		// Dry-run must not create the token file; only surface an existing one.
		tok, _ = relay.LoadToken(dataDir)
	} else if t, err := relay.LoadOrCreateToken(dataDir); err == nil {
		tok = t
	}
	if tok != "" {
		env["TMA1_RELAY_TOKEN"] = tok
	}
	return env
}

// HookScriptPath returns the CC hook script path EnsureHookScript
// would write to, without touching disk. Used by the dry-run install
// path.
func HookScriptPath(dataDir string) string {
	return HookScriptPathFor(AdapterClaudeCode, dataDir)
}

// HookScriptPathFor returns the would-be hook script path for the
// requested adapter without touching disk.
func HookScriptPathFor(adapter Adapter, dataDir string) string {
	_, name := selectHookTemplate(adapter)
	return filepath.Join(dataDir, "hooks", name)
}

// selectHookTemplate picks the (template, filename) pair for the
// adapter + OS combination. Unknown adapters fall back to CC so a
// future caller doesn't crash.
func selectHookTemplate(adapter Adapter) (string, string) {
	if adapter == AdapterCodex {
		if runtime.GOOS == "windows" {
			return ps1TemplateCodex, "tma1-hook-codex.ps1"
		}
		return shTemplateCodex, "tma1-hook-codex.sh"
	}
	if runtime.GOOS == "windows" {
		return ps1Template, "tma1-hook.ps1"
	}
	return shTemplate, "tma1-hook.sh"
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
