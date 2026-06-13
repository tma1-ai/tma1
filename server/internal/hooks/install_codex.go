// Package hooks (install_codex.go) installs the TMA1 adapter for OpenAI
// Codex CLI.
//
// Codex-native targets (different from CC by design — Codex uses TOML
// config, a different hook stdin/stdout protocol, and an `~/.agents/skills/`
// directory):
//
//   - `~/.codex/config.toml` `[mcp_servers.tma1]` table  → MCP stdio
//     server registration. Atomic TOML merge so we never lose unrelated
//     user-managed servers / settings.
//   - `~/.codex/hooks.json`                              → hook command
//     registrations for SessionStart / PreToolUse / PostToolUse /
//     UserPromptSubmit / Stop. JSON merge is the same pattern used for
//     CC's settings.json, just at a different path.
//   - `~/.agents/skills/tma1-peer/`                       → tma1-peer
//     skill (Codex's standard skills directory).
//   - `<project>/AGENTS.md` `<!-- tma1:start -->` block  → reused from
//     install_shared.go.
//   - `<project>/.gitignore` `.tma1-context.md` entry    → reused from
//     install_shared.go.
//
// Idempotent. All writes routed through the DryRun-aware sink so
// `--dry-run` is genuinely side-effect free.
package hooks

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"

	"github.com/BurntSushi/toml"
)

//go:embed all:codex-skills
var embeddedCodexSkills embed.FS

// CodexInstaller knows how to set up TMA1 hooks + MCP server + skill
// for OpenAI Codex CLI. Mirrors ClaudeCodeInstaller's surface so the
// CLI's runInstall() can dispatch on the adapter name without caring
// about adapter-specific internals.
type CodexInstaller struct {
	DataDir            string // ~/.tma1
	Port               int    // tma1-server HTTP port
	GreptimeDBHTTPPort int    // GreptimeDB HTTP port; persisted to MCP env when != default
	ProjectDir         string // project root (AGENTS.md + .gitignore lives here)
	Logger             *slog.Logger
	// DryRun prints what would change without writing anything to disk.
	// Every write goes through writeFile, so this gates the whole pipeline.
	DryRun bool
}

// codexHookEvents are the Codex hook event names we register.
//
// Mapping vs the server-side generateInjection events:
//
//   - SessionStart      → injects orientation context
//   - UserPromptSubmit  → injects per-turn digest
//   - PostToolUse       → injects per-tool anomaly notes (rare)
//   - Stop              → blocks on unresolved HIGH-severity anomalies
//   - PreToolUse        → telemetry-only; Codex does not consume
//     `additionalContext` from PreToolUse hooks
//     per developers.openai.com/codex/hooks +
//     openai/codex#19385. We still register it so
//     the anomaly rules that query
//     `event_type = 'PreToolUse'` (file edits,
//     Bash commands) get the event stream.
//
// PreCompact is deliberately NOT registered: it doesn't exist in
// Codex's hook catalogue (SessionStart / PreToolUse / PermissionRequest
// / PostToolUse / UserPromptSubmit / Stop are the documented six),
// so Codex would never fire it. The server-side PreCompact handler
// in generateInjection is exercised only by Claude Code.
//
// PermissionRequest could be added later for permission-decision
// telemetry but currently has no injection consumer + no anomaly
// rule, so we keep the registered set minimal.
var codexHookEvents = []string{
	"SessionStart",
	"PreToolUse",
	"PostToolUse",
	"UserPromptSubmit",
	"Stop",
}

func (i *CodexInstaller) writeFile(path string, data []byte, perm os.FileMode) error {
	if i.DryRun {
		if i.Logger != nil {
			i.Logger.Info("[dry-run] would write", "path", path, "bytes", len(data))
		}
		return nil
	}
	return writeFileAtomic(path, data, perm)
}

func (i *CodexInstaller) mkdirAll(path string, perm os.FileMode) error {
	if i.DryRun {
		if i.Logger != nil {
			i.Logger.Info("[dry-run] would mkdir -p", "path", path)
		}
		return nil
	}
	return os.MkdirAll(path, perm)
}

func (i *CodexInstaller) dryRun() bool            { return i.DryRun }
func (i *CodexInstaller) getLogger() *slog.Logger { return i.Logger }

// Install performs all Codex installation steps. Mirrors the
// ClaudeCodeInstaller.Install layout so the report is recognisable
// between the two adapters.
func (i *CodexInstaller) Install() (InstallReport, error) {
	var rep InstallReport
	var errs []error

	// 1. Hook script (~/.tma1/hooks/tma1-hook-codex.{sh,ps1}).
	var scriptPath string
	if i.DryRun {
		scriptPath = HookScriptPathFor(AdapterCodex, i.DataDir)
		if i.Logger != nil {
			i.Logger.Info("[dry-run] would write hook script", "path", scriptPath)
		}
	} else {
		p, err := EnsureHookScriptFor(AdapterCodex, i.DataDir, i.Port, i.Logger)
		if err != nil {
			errs = append(errs, fmt.Errorf("hook script: %w", err))
		}
		scriptPath = p
	}
	rep.HookScript = scriptPath

	// 2. Register hooks in ~/.codex/hooks.json. Skipped when scriptPath
	// is empty -- registering an empty command would silently break
	// Codex's hook chain (codex would run "" per event and fail).
	if scriptPath != "" {
		settingsPath, changed, err := i.installHooksConfig(scriptPath)
		if err != nil {
			errs = append(errs, fmt.Errorf("hooks.json: %w", err))
		}
		rep.SettingsPath = settingsPath
		if changed {
			rep.Changed = append(rep.Changed, settingsPath+" (hook registration)")
		}
	}

	// 3. Project-level instructions + .gitignore (shared with CC, same
	// AGENTS.md block so a project with both adapters installed gets
	// one block, not two).
	if i.ProjectDir != "" {
		// Codex's primary instructions file is AGENTS.md. The shared
		// chooser (chooseInstructionsFile) creates AGENTS.md if it
		// doesn't exist — there is deliberately NO fallback to
		// CLAUDE.md, because Codex doesn't scan CLAUDE.md and the
		// block would be invisible there. See
		// install_shared.go::chooseInstructionsFile for the full rule.
		instrPath, changed, err := installInstructions(i, i.ProjectDir, "AGENTS.md")
		if err != nil {
			errs = append(errs, fmt.Errorf("instructions: %w", err))
		}
		rep.InstructionsPath = instrPath
		if changed {
			rep.Changed = append(rep.Changed, instrPath+" (tma1 block)")
		}

		gi, changed, err := installGitignore(i, i.ProjectDir)
		if err != nil {
			errs = append(errs, fmt.Errorf("gitignore: %w", err))
		}
		rep.GitignorePath = gi
		if changed {
			rep.Changed = append(rep.Changed, gi+" (.tma1-context.md entry)")
		}
	}

	// 4. Skill into ~/.agents/skills/.
	skillPaths, skillErr := i.installSkill()
	if skillErr != nil {
		errs = append(errs, fmt.Errorf("skill: %w", skillErr))
	}
	rep.SkillPaths = skillPaths
	for _, p := range skillPaths {
		rep.Changed = append(rep.Changed, p+" (skill)")
	}

	// 5. MCP server entry in ~/.codex/config.toml.
	mcpPath, mcpChanged, mcpErr := i.installMCPServer()
	if mcpErr != nil {
		errs = append(errs, fmt.Errorf("mcp config: %w", mcpErr))
	}
	rep.MCPConfigPath = mcpPath
	if mcpChanged {
		rep.Changed = append(rep.Changed, mcpPath+" (mcp_servers.tma1)")
	}

	if len(errs) > 0 {
		return rep, joinErrors(errs)
	}
	return rep, nil
}

// installSkill copies the embedded codex-skills tree into
// ~/.agents/skills/. Idempotent: identical content is left alone.
// Stale-sweep scoped to the hookOwnerPrefix owner so the user's
// personal skills in the same directory (humanizer, find-skills,
// etc.) are never touched.
func (i *CodexInstaller) installSkill() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dest := filepath.Join(home, ".agents", "skills")
	return syncEmbeddedTree(i, embeddedCodexSkills, "codex-skills", dest, hookOwnerPrefix)
}

// installHooksConfig writes the Codex hook registration into
// ~/.codex/hooks.json. We prefer the standalone hooks.json path over
// inlining `[hooks]` in config.toml -- a focused file is easier to
// merge without touching unrelated user settings, and Codex's loader
// accepts both per the docs.
//
// Schema (matches developers.openai.com/codex/hooks):
//
//	{
//	  "hooks": {
//	    "<EventName>": [
//	      { "matcher": "", "id": "tma1",
//	        "hooks": [{ "type": "command", "command": "...", "timeout": 1 }] }
//	    ]
//	  }
//	}
//
// Idempotent: the `id: "tma1"` marker lets us find + rewrite our own
// entry in place rather than appending duplicates. User-added entries
// for the same event are preserved.
func (i *CodexInstaller) installHooksConfig(scriptPath string) (string, bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false, err
	}
	cfgPath := filepath.Join(home, ".codex", "hooks.json")
	if err := i.mkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		return cfgPath, false, err
	}

	existing, err := readJSONFileStrict(cfgPath)
	if err != nil {
		return cfgPath, false, fmt.Errorf("refusing to overwrite %s: %w", cfgPath, err)
	}

	command := wrapHookCommand(scriptPath)
	if !registerTMA1HookEntries(existing, codexHookEvents, command) {
		return cfgPath, false, nil
	}

	out, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return cfgPath, false, err
	}
	if err := i.writeFile(cfgPath, append(out, '\n'), 0o644); err != nil {
		return cfgPath, false, err
	}
	return cfgPath, true, nil
}

// installMCPServer registers `tma1` as an MCP stdio server under
// `[mcp_servers.tma1]` in `~/.codex/config.toml`. Idempotent: only
// writes when the desired entry differs from disk. **Refuses to
// overwrite** on parse error -- config.toml carries the user's
// permission decisions, model preferences, profiles, etc.
func (i *CodexInstaller) installMCPServer() (string, bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false, err
	}
	cfgPath := filepath.Join(home, ".codex", "config.toml")
	if err := i.mkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		return cfgPath, false, err
	}

	existing, err := readTOMLFileStrict(cfgPath)
	if err != nil {
		return cfgPath, false, fmt.Errorf("refusing to overwrite %s: %w", cfgPath, err)
	}

	binary, err := tma1BinaryPath(i.DataDir)
	if err != nil {
		return cfgPath, false, err
	}

	mcpServers, _ := existing["mcp_servers"].(map[string]any)
	if mcpServers == nil {
		mcpServers = map[string]any{}
	}

	desired := map[string]any{
		"command": binary,
		"args":    []any{"mcp-serve"},
	}
	// TMA1_MCP_CALLER tells the spawned mcp-serve which agent invoked
	// it. get_peer_sessions uses this to exclude the caller's own
	// sessions when agent_source is empty.
	env := map[string]any{
		"TMA1_MCP_CALLER": "codex",
	}
	// Propagate non-default GreptimeDB port so the Codex-spawned MCP
	// child talks to the same DB as the parent tma1-server. Without
	// this a user running with TMA1_GREPTIMEDB_HTTP_PORT=14555 would
	// have the MCP child silently fall back to 14000 and return empty
	// results.
	if i.GreptimeDBHTTPPort != 0 && i.GreptimeDBHTTPPort != defaultGreptimeDBHTTPPort {
		env["TMA1_GREPTIMEDB_HTTP_PORT"] = strconv.Itoa(i.GreptimeDBHTTPPort)
	}
	// Relay: parent port + role + signal token so the MCP child's
	// tma1_handoff tool can reach /api/relay/signal.
	for k, v := range relayEnv(i.DataDir, i.Port, "reviewer", i.DryRun) {
		env[k] = v
	}
	desired["env"] = env

	if cur, ok := mcpServers[hookOwnerID].(map[string]any); ok && mcpEntryEqual(cur, desired) {
		return cfgPath, false, nil
	}

	mcpServers[hookOwnerID] = desired
	existing["mcp_servers"] = mcpServers

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(existing); err != nil {
		return cfgPath, false, err
	}
	if err := i.writeFile(cfgPath, buf.Bytes(), 0o644); err != nil {
		return cfgPath, false, err
	}
	return cfgPath, true, nil
}

// readTOMLFileStrict mirrors readJSONFileStrict for TOML:
//   - (empty map, nil) when the file does not exist
//   - (parsed map, nil) when parse succeeds
//   - (nil, err) when the file exists but doesn't parse
//
// Strict on purpose -- ~/.codex/config.toml carries user-managed
// state we must not corrupt.
func readTOMLFileStrict(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := toml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return m, nil
}
