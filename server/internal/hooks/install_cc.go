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
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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

// mkdirAll mirrors writeFile for directory creation.
func (i *ClaudeCodeInstaller) mkdirAll(path string, perm os.FileMode) error {
	if i.DryRun {
		if i.Logger != nil {
			i.Logger.Info("[dry-run] would mkdir -p", "path", path)
		}
		return nil
	}
	return os.MkdirAll(path, perm)
}

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

// syncEmbeddedTree walks an embed.FS rooted at `embedRoot`, mirroring
// each file under `destRoot`. Files whose on-disk content already
// matches the embedded version are left untouched (so reinstalls don't
// thrash mtimes). Honors DryRun via writeFile / mkdirAll.
func (i *ClaudeCodeInstaller) syncEmbeddedTree(src embed.FS, embedRoot, destRoot string) ([]string, error) {
	if err := i.mkdirAll(destRoot, 0o755); err != nil {
		return nil, err
	}
	var changed []string
	walkErr := fs.WalkDir(src, embedRoot, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(embedRoot, p)
		if err != nil {
			return err
		}
		want, err := src.ReadFile(p)
		if err != nil {
			return err
		}
		target := filepath.Join(destRoot, rel)
		if existing, err := os.ReadFile(target); err == nil && string(existing) == string(want) {
			return nil
		}
		if err := i.mkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := i.writeFile(target, want, 0o644); err != nil {
			return err
		}
		changed = append(changed, target)
		return nil
	})
	if walkErr != nil {
		return changed, walkErr
	}
	return changed, nil
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

	command := hookCommand(scriptPath)
	if !registerTMA1Hooks(existing, command) {
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

// writeFileAtomic writes data to path via a temp-file + rename so a crash or
// signal between truncate and full write can't leave the target half-written.
// Critical for ~/.claude.json (OAuth + project history) and ~/.claude/settings.json
// (hook registrations) — losing either silently breaks the user's CC install.
//
// The temp file lives in the same directory as the target so the rename is
// guaranteed to be on the same filesystem (atomic).
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tma1-write-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
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
	// Propagate non-default GreptimeDB port so the CC-spawned mcp-serve child
	// — which runs in CC's environment, not the parent tma1-server's — talks
	// to the same DB. Without this, a user who ran the server with
	// TMA1_GREPTIMEDB_HTTP_PORT=14555 would have the MCP child silently fall
	// back to 14000 and return empty results.
	if i.GreptimeDBHTTPPort != 0 && i.GreptimeDBHTTPPort != defaultGreptimeDBHTTPPort {
		desired["env"] = map[string]any{
			"TMA1_GREPTIMEDB_HTTP_PORT": strconv.Itoa(i.GreptimeDBHTTPPort),
		}
	}

	if cur, ok := servers["tma1"].(map[string]any); ok && mcpEntryEqual(cur, desired) {
		return cfgPath, false, nil
	}

	servers["tma1"] = desired
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

// tma1BinaryPath returns the absolute path to the tma1-server binary CC
// should spawn for `mcp-serve`. Prefer os.Executable() (the binary we're
// running right now is what the user just invoked), then fall back to the
// standard install location under DataDir/bin.
func tma1BinaryPath(dataDir string) (string, error) {
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			return resolved, nil
		}
		return exe, nil
	}
	name := "tma1-server"
	if runtime.GOOS == "windows" {
		name = "tma1-server.exe"
	}
	if dataDir == "" {
		return "", fmt.Errorf("cannot determine tma1-server path: no executable, no DataDir")
	}
	return filepath.Join(dataDir, "bin", name), nil
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

// hookCommand returns the shell command CC will invoke for each hook.
// Windows uses PowerShell with the .ps1 template; everything else uses bash.
func hookCommand(scriptPath string) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf(`powershell -ExecutionPolicy Bypass -File "%s"`, scriptPath)
	}
	return scriptPath
}

// registerTMA1Hooks ensures the given command is registered for the events
// TMA1 cares about. Returns true if the settings map mutated.
//
// Schema (CC settings.json):
//
//	{
//	  "hooks": {
//	    "<EventName>": [
//	      { "matcher": "", "hooks": [{ "type": "command", "command": "..." }] }
//	    ]
//	  }
//	}
//
// Two-pass matching avoids duplicate entries:
//  1. Look for an entry whose hooks[].command resolves to the same script
//     (after ~/ expansion). This catches old entries installed before we
//     added the `id` field, or entries the user hand-wrote.
//  2. Fall back to looking for an entry with `id="tma1"`.
//
// The matched entry is rewritten in place (canonical id + absolute path).
// Other entries are left alone — that's important: user-added hooks for the
// same event must keep working.
func registerTMA1Hooks(settings map[string]any, command string) bool {
	const tmaID = "tma1"

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}

	mutated := false
	// Events TMA1 registers itself. The matcher is "" (all tools) -- server-
	// side dispatch decides which event types deserve injection per phase.
	//
	// The list must cover EVERY native CC hook event whose payload the
	// server stores in tma1_hook_events or queries in anomaly rules.
	// Missing PreToolUse means R-stale-view never fires (no Read events),
	// tool counts under-count, current_focus stays empty, and follow-rate
	// validation can't tell whether the agent re-Read the listed file.
	// Lifecycle / subagent / notification events likewise feed the
	// session timeline + canvas; dropping them silently strips features.
	for _, event := range []string{
		"UserPromptSubmit",
		"PreToolUse",
		"PostToolUse",
		"SessionStart",
		"SessionEnd",
		"PreCompact",
		"Stop",
		"SubagentStop",
		"Notification",
	} {
		list, _ := hooks[event].([]any)
		idx := findEquivalentEntry(list, command, tmaID)

		entry := map[string]any{
			"matcher": "",
			"id":      tmaID,
			"hooks": []any{
				map[string]any{
					"type":    "command",
					"command": command,
				},
			},
		}

		switch {
		case idx >= 0:
			if !entryEqual(list[idx], entry) {
				list[idx] = entry
				hooks[event] = list
				mutated = true
			}
		default:
			list = append(list, entry)
			hooks[event] = list
			mutated = true
		}
	}

	if mutated {
		settings["hooks"] = hooks
	}
	return mutated
}

// findEquivalentEntry returns the index of an existing TMA1-equivalent entry,
// or -1. An entry counts as equivalent if either:
//   - its first hook's command (after ~/ expansion) equals `command`
//   - its id field equals `tmaID`
func findEquivalentEntry(list []any, command, tmaID string) int {
	resolved := expandHome(command)
	for i, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if id, _ := m["id"].(string); id == tmaID {
			return i
		}
		if firstCmd := entryCommand(m); firstCmd != "" && expandHome(firstCmd) == resolved {
			return i
		}
	}
	return -1
}

// entryCommand returns the first hook's command string from an entry, or "".
func entryCommand(entry map[string]any) string {
	hs, _ := entry["hooks"].([]any)
	if len(hs) == 0 {
		return ""
	}
	h, _ := hs[0].(map[string]any)
	if h == nil {
		return ""
	}
	cmd, _ := h["command"].(string)
	return cmd
}

// expandHome resolves a leading ~/ to $HOME so two paths pointing at the
// same script are recognized as equal regardless of how they were spelled.
func expandHome(p string) string {
	if !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[2:])
}

func entryEqual(a, b any) bool {
	am, _ := a.(map[string]any)
	bm, _ := b.(map[string]any)
	if am == nil || bm == nil {
		return false
	}
	// Compare only the fields we manage. User-extended fields are preserved
	// by virtue of being replaced wholesale only when the command differs.
	if id1, _ := am["id"].(string); id1 != bm["id"] {
		return false
	}
	if m1, _ := am["matcher"].(string); m1 != bm["matcher"] {
		return false
	}
	return entryCommand(am) == entryCommand(bm)
}

// readJSONFileStrict loads a JSON object map. Returns:
//   - (empty map, nil) when the file does not exist
//   - (parsed map, nil) when parse succeeds and the root is an object
//   - (nil, err) when the file exists but does not contain a JSON object
//
// Strict on purpose: callers like installSettings / installMCPServer write
// back to files that hold user-critical state. Silently treating a parse
// error as "empty" would corrupt those files.
func readJSONFileStrict(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if m == nil {
		return nil, fmt.Errorf("parse %s: root is not a JSON object", path)
	}
	return m, nil
}

// installInstructions appends a TMA1 block to CLAUDE.md (or AGENTS.md if
// CLAUDE.md is absent and AGENTS.md exists). Idempotent via marker matching.
func (i *ClaudeCodeInstaller) installInstructions(projectDir string) (string, bool, error) {
	target := chooseInstructionsFile(projectDir)
	existing, _ := os.ReadFile(target)

	const startMarker = "<!-- tma1:start -->"
	const endMarker = "<!-- tma1:end -->"

	desired := instructionsBlock(startMarker, endMarker)

	var newContent []byte
	if startIdx := indexOf(existing, []byte(startMarker)); startIdx >= 0 {
		endIdx := indexOf(existing, []byte(endMarker))
		if endIdx < 0 {
			endIdx = len(existing)
		} else {
			endIdx += len(endMarker)
		}
		newContent = append([]byte{}, existing[:startIdx]...)
		newContent = append(newContent, desired...)
		newContent = append(newContent, existing[endIdx:]...)
	} else {
		// Append block. Ensure there is a blank line before.
		newContent = append([]byte{}, existing...)
		if len(newContent) > 0 && newContent[len(newContent)-1] != '\n' {
			newContent = append(newContent, '\n')
		}
		if len(newContent) > 0 {
			newContent = append(newContent, '\n')
		}
		newContent = append(newContent, desired...)
		newContent = append(newContent, '\n')
	}

	if string(newContent) == string(existing) {
		return target, false, nil
	}
	if err := i.writeFile(target, newContent, 0o644); err != nil {
		return target, false, err
	}
	return target, true, nil
}

func chooseInstructionsFile(projectDir string) string {
	claudeMD := filepath.Join(projectDir, "CLAUDE.md")
	agentsMD := filepath.Join(projectDir, "AGENTS.md")
	if fileExists(claudeMD) {
		return claudeMD
	}
	if fileExists(agentsMD) {
		return agentsMD
	}
	return claudeMD
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func instructionsBlock(start, end string) []byte {
	return []byte(start + `
## TMA1 Context Layer

TMA1 thickens the Observe step in your reasoning loop. At the start of each
turn it injects a <tma1-context> block summarising the current session
(tool history, tokens, current focus, recent files). Use that block when
deciding what to do next.

**You should:**
- Read the <tma1-context> block (when present) before reasoning about the next action
- Call the MCP tool ` + "`get_session_state`" + ` if you need a fuller view of your prior tool calls
- Call ` + "`get_context_bundle`" + ` after compaction or when context feels stale
` + end)
}

// installGitignore appends ".tma1-context.md" to projectDir/.gitignore if
// missing. Tolerates an absent .gitignore (creates it) and an absent project
// (no-op).
func (i *ClaudeCodeInstaller) installGitignore(projectDir string) (string, bool, error) {
	path := filepath.Join(projectDir, ".gitignore")
	existing, _ := os.ReadFile(path)
	if containsLine(existing, ".tma1-context.md") {
		return path, false, nil
	}
	suffix := ".tma1-context.md\n"
	updated := existing
	if len(updated) > 0 && updated[len(updated)-1] != '\n' {
		updated = append(updated, '\n')
	}
	updated = append(updated, []byte(suffix)...)
	if err := i.writeFile(path, updated, 0o644); err != nil {
		return path, false, err
	}
	return path, true, nil
}

func containsLine(data []byte, line string) bool {
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) == line {
			return true
		}
	}
	return false
}

func indexOf(haystack, needle []byte) int {
	return strings.Index(string(haystack), string(needle))
}

func joinErrors(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0]
	}
	parts := make([]string, len(errs))
	for i, e := range errs {
		parts[i] = e.Error()
	}
	return fmt.Errorf("multiple install errors: %s", strings.Join(parts, "; "))
}
