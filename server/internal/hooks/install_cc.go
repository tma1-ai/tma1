// Package hooks (install_cc.go) installs TMA1 integration for Claude Code:
//   - registers hook events in ~/.claude/settings.json
//   - appends a <!-- tma1:start --> block to CLAUDE.md / AGENTS.md in the
//     target project (so CC reads about TMA1 every session)
//   - adds .tma1-context.md to the project .gitignore
//   - installs tma1-peer slash-command skill into ~/.claude/skills/
//
// All operations are idempotent. Repeated installs only rewrite a file when
// its desired content differs from disk.
package hooks

import (
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
)

//go:embed all:skills
var embeddedSkills embed.FS

//go:embed all:commands
var embeddedCommands embed.FS

// ClaudeCodeInstaller knows how to set up TMA1 hooks for Claude Code.
type ClaudeCodeInstaller struct {
	DataDir            string // ~/.tma1
	Port               int    // tma1-server HTTP port
	GreptimeDBHTTPPort int    // GreptimeDB HTTP port; only persisted to MCP env when != default
	ProjectDir         string // project root (where CLAUDE.md / .gitignore live)
	Logger             *slog.Logger
	// DryRun prints what would change without writing anything to disk.
	// Every settings.json / .claude.json / skill / command / instruction
	// / .gitignore write goes through writeFile, so toggling this gates
	// the whole install pipeline.
	DryRun bool
}

// defaultGreptimeDBHTTPPort mirrors config.envInt("TMA1_GREPTIMEDB_HTTP_PORT", 14000).
// Kept as a constant so installMCPServer can decide whether the user's
// configured port deserves an explicit env override in the MCP entry.
const defaultGreptimeDBHTTPPort = 14000

var claudeCodeHookEvents = []string{
	"SessionStart",
	"SessionEnd",
	"PreToolUse",
	"PostToolUse",
	"PostToolUseFailure",
	"UserPromptSubmit",
	"SubagentStart",
	"SubagentStop",
	"Notification",
	"Stop",
	"PreCompact",
	"PostCompact",
	"PermissionRequest",
	"PermissionDenied",
	"TaskCreated",
	"TaskCompleted",
	"FileChanged",
	"CwdChanged",
	"InstructionsLoaded",
	"Elicitation",
	"ElicitationResult",
	"WorktreeCreate",
	"WorktreeRemove",
	"StopFailure",
	"Setup",
	"TeammateIdle",
	"ConfigChange",
}

// Install runs all three steps. Returns the list of touched paths and the
// first error encountered (subsequent steps are still attempted on failure
// so partial installs don't leave the user stuck).
type InstallReport struct {
	HookScript       string
	SettingsPath     string
	InstructionsPath string
	GitignorePath    string
	MCPConfigPath    string   // ~/.claude.json (or wherever mcpServers got registered)
	SkillPaths       []string // skill files installed under ~/.claude/skills/
	CommandPaths     []string // slash-command files installed under ~/.claude/commands/
	Changed          []string // human-readable summary of what changed
}

// writeFile is the DryRun-aware sink for every install-time file write.
// On DryRun it logs the intended action and returns nil; otherwise it
// delegates to writeFileAtomic (temp + rename) so partial writes can't
// corrupt files holding user-critical state.
func (i *ClaudeCodeInstaller) writeFile(path string, data []byte, perm os.FileMode) error {
	if i.DryRun {
		if i.Logger != nil {
			i.Logger.Info("[dry-run] would write", "path", path, "bytes", len(data))
		}
		return nil
	}
	return writeFileAtomic(path, data, perm)
}

func (i *ClaudeCodeInstaller) mkdirAll(path string, perm os.FileMode) error {
	if i.DryRun {
		if i.Logger != nil {
			i.Logger.Info("[dry-run] would mkdir -p", "path", path)
		}
		return nil
	}
	return os.MkdirAll(path, perm)
}

// dryRun / getLogger satisfy the installSink interface so the helpers
// in install_shared.go can route DryRun + structured logging through
// this installer.
func (i *ClaudeCodeInstaller) dryRun() bool          { return i.DryRun }
func (i *ClaudeCodeInstaller) getLogger() *slog.Logger { return i.Logger }

// Install performs all installation steps. Errors from any step are joined
// but not fatal — partial success is reported so the user can act.
func (i *ClaudeCodeInstaller) Install() (InstallReport, error) {
	var rep InstallReport
	var errs []error

	// 1. Ensure hook script exists. DryRun computes the would-be path
	// without writing -- EnsureHookScript creates ~/.tma1/hooks/ and
	// emits the script via its own paths, neither of which run through
	// the DryRun-aware writeFile/mkdirAll sinks. Honour DryRun here
	// so "--dry-run" is actually side-effect free as advertised.
	var scriptPath string
	if i.DryRun {
		scriptPath = HookScriptPath(i.DataDir)
		if i.Logger != nil {
			i.Logger.Info("[dry-run] would write hook script", "path", scriptPath)
		}
	} else {
		p, err := EnsureHookScript(i.DataDir, i.Port, i.Logger)
		if err != nil {
			errs = append(errs, fmt.Errorf("hook script: %w", err))
		}
		scriptPath = p
	}
	rep.HookScript = scriptPath

	// 2. Register hooks in ~/.claude/settings.json.
	// Skip if scriptPath is empty — registering an empty command would silently
	// break CC's hook chain (CC would run `""` per event and fail every turn).
	if scriptPath != "" {
		settingsPath, changed, err := i.installSettings(scriptPath)
		if err != nil {
			errs = append(errs, fmt.Errorf("settings.json: %w", err))
		}
		rep.SettingsPath = settingsPath
		if changed {
			rep.Changed = append(rep.Changed, settingsPath+" (hook registration)")
		}
	}

	// 3. Append <!-- tma1:start --> block to CLAUDE.md/AGENTS.md.
	if i.ProjectDir != "" {
		instrPath, changed, err := i.installInstructions(i.ProjectDir)
		if err != nil {
			errs = append(errs, fmt.Errorf("instructions: %w", err))
		}
		rep.InstructionsPath = instrPath
		if changed {
			rep.Changed = append(rep.Changed, instrPath+" (tma1 block)")
		}

		// 4. Add .tma1-context.md to .gitignore (best-effort).
		gi, changed, err := i.installGitignore(i.ProjectDir)
		if err != nil {
			errs = append(errs, fmt.Errorf("gitignore: %w", err))
		}
		rep.GitignorePath = gi
		if changed {
			rep.Changed = append(rep.Changed, gi+" (.tma1-context.md entry)")
		}
	}

	// 5. Install the tma1-peer slash-command into both `~/.claude/commands/`
	// (CC's native location for `/<name> args` invocations) and the legacy
	// `~/.claude/skills/` path. Plan §Phase 1.6 prefers the command form
	// because it survives independently of skill autoload heuristics; the
	// skill stays as a fallback for CC versions whose commands/ directory
	// doesn't accept arguments.
	skillPaths, skillErr := i.installSkills()
	if skillErr != nil {
		errs = append(errs, fmt.Errorf("skills: %w", skillErr))
	}
	rep.SkillPaths = skillPaths
	for _, p := range skillPaths {
		rep.Changed = append(rep.Changed, p+" (skill)")
	}

	commandPaths, commandErr := i.installCommands()
	if commandErr != nil {
		errs = append(errs, fmt.Errorf("commands: %w", commandErr))
	}
	rep.CommandPaths = commandPaths
	for _, p := range commandPaths {
		rep.Changed = append(rep.Changed, p+" (command)")
	}

	// 6. Register tma1 as an MCP server in ~/.claude.json so CC can pull
	//    perception data (get_context_bundle, get_session_state, …) on demand.
	mcpPath, mcpChanged, mcpErr := i.installMCPServer()
	if mcpErr != nil {
		errs = append(errs, fmt.Errorf("mcp config: %w", mcpErr))
	}
	rep.MCPConfigPath = mcpPath
	if mcpChanged {
		rep.Changed = append(rep.Changed, mcpPath+" (mcpServers.tma1)")
	}

	if len(errs) > 0 {
		return rep, joinErrors(errs)
	}
	return rep, nil
}

// installSkills copies the embedded skill tree into ~/.claude/skills/.
// Returns the list of newly written / updated skill files.
// Idempotent — files identical to the embedded content are left alone.
func (i *ClaudeCodeInstaller) installSkills() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	skillsDest := filepath.Join(home, ".claude", "skills")
	return i.syncEmbeddedTree(embeddedSkills, "skills", skillsDest)
}

// installCommands copies the embedded slash-command tree into
// ~/.claude/commands/. Same shape as installSkills — separate function
// so the InstallReport can attribute changes to the right channel
// (commands vs skills) and so dropping support for either side later
// is a one-call removal.
func (i *ClaudeCodeInstaller) installCommands() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	cmdsDest := filepath.Join(home, ".claude", "commands")
	return i.syncEmbeddedTree(embeddedCommands, "commands", cmdsDest)
}

// syncEmbeddedTree is a thin wrapper over the shared helper so the
// existing call sites + tests keep their method-shaped invocation.
// Stale-sweep is scoped to the hookOwnerPrefix owner so user-installed
// skills / commands sitting alongside ours in ~/.claude/{skills,
// commands}/ are never touched.
func (i *ClaudeCodeInstaller) syncEmbeddedTree(src embed.FS, embedRoot, destRoot string) ([]string, error) {
	return syncEmbeddedTree(i, src, embedRoot, destRoot, hookOwnerPrefix)
}

// installSettings updates ~/.claude/settings.json to register UserPromptSubmit
// and Stop hooks. Returns the resolved path, whether the file changed, and any
// error.
//
// Bails out on JSON parse errors rather than overwriting — settings.json may
// contain user customisations we'd otherwise destroy silently.
func (i *ClaudeCodeInstaller) installSettings(scriptPath string) (string, bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false, err
	}
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if err := i.mkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return settingsPath, false, err
	}

	existing, err := readJSONFileStrict(settingsPath)
	if err != nil {
		return settingsPath, false, fmt.Errorf("refusing to overwrite %s: %w", settingsPath, err)
	}

	command := wrapHookCommand(scriptPath)
	if !registerTMA1HookEntries(existing, claudeCodeHookEvents, command, nil) {
		return settingsPath, false, nil
	}

	out, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return settingsPath, false, err
	}
	if err := i.writeFile(settingsPath, append(out, '\n'), 0o644); err != nil {
		return settingsPath, false, err
	}
	return settingsPath, true, nil
}

// installMCPServer registers `tma1` as an MCP stdio server under the
// top-level `mcpServers` object in ~/.claude.json. Idempotent: only writes
// when the desired entry differs from what's on disk.
//
// Crucially, this file holds CC's own state (OAuth, project history, other
// MCP servers like slack/greptimedb). A parse error must NOT cause us to
// truncate it — we abort instead.
func (i *ClaudeCodeInstaller) installMCPServer() (string, bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false, err
	}
	cfgPath := filepath.Join(home, ".claude.json")

	existing, err := readJSONFileStrict(cfgPath)
	if err != nil {
		return cfgPath, false, fmt.Errorf("refusing to overwrite %s: %w", cfgPath, err)
	}

	binary, err := tma1BinaryPath(i.DataDir)
	if err != nil {
		return cfgPath, false, err
	}

	servers, _ := existing["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}

	desired := map[string]any{
		"type":    "stdio",
		"command": binary,
		"args":    []any{"mcp-serve"},
	}
	// TMA1_MCP_CALLER tells the spawned mcp-serve which agent invoked
	// it. get_peer_sessions uses this to exclude the caller's own
	// sessions when agent_source is empty — without it, CC's
	// `/tma1-peer` would surface CC's own backlog as "peers".
	env := map[string]any{
		"TMA1_MCP_CALLER": "claude_code",
	}
	// Propagate non-default GreptimeDB port so the CC-spawned mcp-serve child
	// — which runs in CC's environment, not the parent tma1-server's — talks
	// to the same DB. Without this, a user who ran the server with
	// TMA1_GREPTIMEDB_HTTP_PORT=14555 would have the MCP child silently fall
	// back to 14000 and return empty results.
	if i.GreptimeDBHTTPPort != 0 && i.GreptimeDBHTTPPort != defaultGreptimeDBHTTPPort {
		env["TMA1_GREPTIMEDB_HTTP_PORT"] = strconv.Itoa(i.GreptimeDBHTTPPort)
	}
	desired["env"] = env

	if cur, ok := servers[hookOwnerID].(map[string]any); ok && mcpEntryEqual(cur, desired) {
		return cfgPath, false, nil
	}

	servers[hookOwnerID] = desired
	existing["mcpServers"] = servers

	out, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return cfgPath, false, err
	}
	if err := i.writeFile(cfgPath, append(out, '\n'), 0o644); err != nil {
		return cfgPath, false, err
	}
	return cfgPath, true, nil
}

// mcpEntryEqual compares two MCP server entries on the fields we manage.
// args and env are compared via JSON round-trip so int/float64 weirdness
// from map[string]any doesn't trigger false negatives.
//
// env must be part of the comparison: otherwise an installer rerun with a
// different GreptimeDBHTTPPort would silently keep the stale entry.
func mcpEntryEqual(a, b map[string]any) bool {
	at, _ := a["type"].(string)
	bt, _ := b["type"].(string)
	if at != bt {
		return false
	}
	ac, _ := a["command"].(string)
	bc, _ := b["command"].(string)
	if ac != bc {
		return false
	}
	aj, _ := json.Marshal(a["args"])
	bj, _ := json.Marshal(b["args"])
	if string(aj) != string(bj) {
		return false
	}
	ae, _ := json.Marshal(a["env"])
	be, _ := json.Marshal(b["env"])
	return string(ae) == string(be)
}

// installInstructions / installGitignore are thin method wrappers
// around the shared helpers in install_shared.go so existing callers
// (Install() and the tests) keep their method-shaped invocation.
// CC prefers CLAUDE.md as the instructions file -- it's Claude
// Code's canonical name; AGENTS.md is the fallback when CLAUDE.md
// is absent.
func (i *ClaudeCodeInstaller) installInstructions(projectDir string) (string, bool, error) {
	return installInstructions(i, projectDir, "CLAUDE.md")
}

func (i *ClaudeCodeInstaller) installGitignore(projectDir string) (string, bool, error) {
	return installGitignore(i, projectDir)
}
