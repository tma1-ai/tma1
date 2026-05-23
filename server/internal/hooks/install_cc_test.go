package hooks

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallEndToEndIdempotent(t *testing.T) {
	// Sandbox a fake HOME + project dir so we don't touch the user's real settings.
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(t.TempDir(), "myproj")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}

	dataDir := filepath.Join(home, ".tma1")

	inst := &ClaudeCodeInstaller{
		DataDir:    dataDir,
		Port:       14318,
		ProjectDir: project,
		Logger:     slog.Default(),
	}

	rep, err := inst.Install()
	if err != nil {
		t.Fatalf("first install: %v", err)
	}

	// settings.json must register all three events with the right command.
	raw, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse settings.json: %v", err)
	}
	hookSection, _ := m["hooks"].(map[string]any)
	for _, event := range []string{"UserPromptSubmit", "Stop", "PostToolUse", "SessionStart", "PreCompact"} {
		entries, _ := hookSection[event].([]any)
		if len(entries) == 0 {
			t.Errorf("settings.json missing %q hooks", event)
			continue
		}
		first, _ := entries[0].(map[string]any)
		if id, _ := first["id"].(string); id != "tma1" {
			t.Errorf("%s entry missing id=tma1, got %v", event, first)
		}
	}

	// CLAUDE.md must contain the start/end markers exactly once.
	claude, err := os.ReadFile(filepath.Join(project, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	if strings.Count(string(claude), "<!-- tma1:start -->") != 1 {
		t.Errorf("CLAUDE.md should contain exactly one start marker, got %d", strings.Count(string(claude), "<!-- tma1:start -->"))
	}

	// .gitignore must contain .tma1-context.md.
	gi, _ := os.ReadFile(filepath.Join(project, ".gitignore"))
	if !strings.Contains(string(gi), ".tma1-context.md") {
		t.Errorf(".gitignore missing entry, got %q", gi)
	}

	// tma1-peer skill must be installed under ~/.claude/skills/.
	skillPath := filepath.Join(home, ".claude", "skills", "tma1-peer", "SKILL.md")
	skillContent, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("tma1-peer skill not installed: %v", err)
	}
	if !strings.Contains(string(skillContent), "tma1-peer") {
		t.Errorf("skill file present but content unexpected: %q", string(skillContent[:200]))
	}

	if len(rep.Changed) == 0 {
		t.Error("first install should report changes")
	}

	// Second install must be a no-op.
	rep2, err := inst.Install()
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if len(rep2.Changed) != 0 {
		t.Errorf("second install should be no-op, got changes: %v", rep2.Changed)
	}

	// CLAUDE.md still has exactly one tma1 block.
	claude, _ = os.ReadFile(filepath.Join(project, "CLAUDE.md"))
	if c := strings.Count(string(claude), "<!-- tma1:start -->"); c != 1 {
		t.Errorf("after second install, expected 1 tma1 block, got %d", c)
	}
}

func TestInstallPreservesExistingSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	settingsDir := filepath.Join(home, ".claude")
	_ = os.MkdirAll(settingsDir, 0o755)
	existing := map[string]any{
		"theme": "dark",
		"hooks": map[string]any{
			"UserPromptSubmit": []any{
				map[string]any{
					"matcher": "",
					"id":      "user-script",
					"hooks": []any{
						map[string]any{"type": "command", "command": "/usr/local/bin/my-hook"},
					},
				},
			},
		},
	}
	raw, _ := json.MarshalIndent(existing, "", "  ")
	_ = os.WriteFile(filepath.Join(settingsDir, "settings.json"), raw, 0o644)

	project := filepath.Join(t.TempDir(), "p")
	_ = os.MkdirAll(project, 0o755)

	inst := &ClaudeCodeInstaller{
		DataDir:    filepath.Join(home, ".tma1"),
		Port:       14318,
		ProjectDir: project,
		Logger:     slog.Default(),
	}
	if _, err := inst.Install(); err != nil {
		t.Fatalf("install: %v", err)
	}

	raw, _ = os.ReadFile(filepath.Join(settingsDir, "settings.json"))
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got["theme"] != "dark" {
		t.Errorf("theme key wiped: %+v", got)
	}
	entries, _ := got["hooks"].(map[string]any)["UserPromptSubmit"].([]any)
	if len(entries) != 2 {
		t.Fatalf("expected user + tma1 entries, got %d", len(entries))
	}
}

func TestRegisterTMA1HooksReplacesOnCommandChange(t *testing.T) {
	settings := map[string]any{
		"hooks": map[string]any{
			"UserPromptSubmit": []any{
				map[string]any{
					"id":      "tma1",
					"matcher": "",
					"hooks": []any{
						map[string]any{"type": "command", "command": "/old/path/tma1-hook.sh"},
					},
				},
			},
		},
	}
	if !registerTMA1HookEntries(settings, claudeCodeHookEvents, "/new/path/tma1-hook.sh") {
		t.Fatal("expected mutation when command changes")
	}
	if registerTMA1HookEntries(settings, claudeCodeHookEvents, "/new/path/tma1-hook.sh") {
		t.Error("expected no-op on second call with same command")
	}
}

func TestInstallMCPServerIdempotentAndPreservesPeers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Pre-seed ~/.claude.json with an unrelated MCP server (mimics Dennis's
	// real config with slack/greptimedb already registered) plus an
	// unrelated top-level key (CC stores tons of state in this file).
	pre := map[string]any{
		"userID": "abc-123",
		"mcpServers": map[string]any{
			"slack": map[string]any{
				"type":    "stdio",
				"command": "npx",
				"args":    []any{"slack-mcp-server@latest"},
			},
		},
	}
	raw, _ := json.MarshalIndent(pre, "", "  ")
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	inst := &ClaudeCodeInstaller{
		DataDir: filepath.Join(home, ".tma1"),
		Port:    14318,
		Logger:  slog.Default(),
	}
	path, changed, err := inst.installMCPServer()
	if err != nil {
		t.Fatalf("first installMCPServer: %v", err)
	}
	if !changed {
		t.Error("first install should report changed=true")
	}

	got := readClaudeConfig(t, path)
	if got["userID"] != "abc-123" {
		t.Errorf("userID wiped: %+v", got)
	}
	servers, _ := got["mcpServers"].(map[string]any)
	if _, ok := servers["slack"]; !ok {
		t.Errorf("slack peer wiped: %+v", servers)
	}
	tma1, ok := servers["tma1"].(map[string]any)
	if !ok {
		t.Fatalf("tma1 entry missing: %+v", servers)
	}
	if tma1["type"] != "stdio" {
		t.Errorf("tma1 type not stdio: %v", tma1["type"])
	}
	args, _ := tma1["args"].([]any)
	if len(args) != 1 || args[0] != "mcp-serve" {
		t.Errorf("tma1 args wrong: %v", tma1["args"])
	}

	// Second call must be a no-op.
	_, changed2, err := inst.installMCPServer()
	if err != nil {
		t.Fatalf("second installMCPServer: %v", err)
	}
	if changed2 {
		t.Error("second install should be no-op")
	}
}

func TestInstallMCPServerRefusesMalformedFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Garbage instead of JSON. We must NOT overwrite this — it could be a
	// transiently corrupted file holding the user's real CC state.
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	inst := &ClaudeCodeInstaller{
		DataDir: filepath.Join(home, ".tma1"),
		Port:    14318,
		Logger:  slog.Default(),
	}
	_, _, err := inst.installMCPServer()
	if err == nil {
		t.Fatal("expected error on malformed JSON, got nil")
	}
	// File must be untouched.
	raw, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
	if string(raw) != "{ not json" {
		t.Errorf("file was modified despite parse error: %q", string(raw))
	}
}

func TestInstallMCPServerPersistsCustomGreptimeDBPort(t *testing.T) {
	// When the user runs tma1-server with TMA1_GREPTIMEDB_HTTP_PORT=14555,
	// the CC-spawned mcp-serve child won't inherit that env. The installer
	// must persist the port in the MCP entry's env so the child reads the
	// same DB the parent server uses.
	home := t.TempDir()
	t.Setenv("HOME", home)

	inst := &ClaudeCodeInstaller{
		DataDir:            filepath.Join(home, ".tma1"),
		Port:               14318,
		GreptimeDBHTTPPort: 14555,
		Logger:             slog.Default(),
	}
	path, changed, err := inst.installMCPServer()
	if err != nil {
		t.Fatalf("installMCPServer: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true on first install with custom port")
	}
	got := readClaudeConfig(t, path)
	servers, _ := got["mcpServers"].(map[string]any)
	tma1, ok := servers["tma1"].(map[string]any)
	if !ok {
		t.Fatalf("tma1 entry missing: %+v", servers)
	}
	env, ok := tma1["env"].(map[string]any)
	if !ok {
		t.Fatalf("expected env block, got: %+v", tma1)
	}
	if env["TMA1_GREPTIMEDB_HTTP_PORT"] != "14555" {
		t.Errorf("env port wrong: %v", env)
	}
	// TMA1_MCP_CALLER ships unconditionally so get_peer_sessions can
	// exclude the caller's own sessions on empty agent_source.
	if env["TMA1_MCP_CALLER"] != "claude_code" {
		t.Errorf("env TMA1_MCP_CALLER = %v, want claude_code", env["TMA1_MCP_CALLER"])
	}

	// Idempotent: re-running with the same port must NOT report a change.
	_, changed2, err := inst.installMCPServer()
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if changed2 {
		t.Error("expected no-op on second install with identical port")
	}

	// Default port → env still carries TMA1_MCP_CALLER but drops the
	// GreptimeDB port override (don't clutter the file with the default).
	inst.GreptimeDBHTTPPort = 14000
	_, _, _ = inst.installMCPServer()
	got = readClaudeConfig(t, path)
	tma1 = got["mcpServers"].(map[string]any)["tma1"].(map[string]any)
	env2, ok := tma1["env"].(map[string]any)
	if !ok {
		t.Fatalf("env block must persist for TMA1_MCP_CALLER, got: %+v", tma1)
	}
	if env2["TMA1_MCP_CALLER"] != "claude_code" {
		t.Errorf("default-port env missing TMA1_MCP_CALLER: %+v", env2)
	}
	if _, hasPort := env2["TMA1_GREPTIMEDB_HTTP_PORT"]; hasPort {
		t.Errorf("default port should not emit TMA1_GREPTIMEDB_HTTP_PORT: %+v", env2)
	}
}

func TestInstallMCPServerCreatesFileWhenAbsent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	inst := &ClaudeCodeInstaller{
		DataDir: filepath.Join(home, ".tma1"),
		Port:    14318,
		Logger:  slog.Default(),
	}
	path, changed, err := inst.installMCPServer()
	if err != nil {
		t.Fatalf("installMCPServer: %v", err)
	}
	if !changed {
		t.Error("expected changed=true when creating from scratch")
	}
	got := readClaudeConfig(t, path)
	servers, _ := got["mcpServers"].(map[string]any)
	if _, ok := servers["tma1"]; !ok {
		t.Errorf("tma1 entry not written: %+v", got)
	}
}

func TestInstallCommandsCopiesEmbeddedTree(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	inst := &ClaudeCodeInstaller{
		DataDir: filepath.Join(home, ".tma1"),
		Port:    14318,
		Logger:  slog.Default(),
	}
	paths, err := inst.installCommands()
	if err != nil {
		t.Fatalf("installCommands: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("expected at least one command file written")
	}
	wantPath := filepath.Join(home, ".claude", "commands", "tma1-peer.md")
	found := false
	for _, p := range paths {
		if p == wantPath {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("tma1-peer.md not in installed paths: %v", paths)
	}
	// File should exist on disk now.
	if _, err := os.Stat(wantPath); err != nil {
		t.Errorf("stat %s: %v", wantPath, err)
	}
	// Second call must be a no-op (file content unchanged).
	paths2, err := inst.installCommands()
	if err != nil {
		t.Fatalf("second installCommands: %v", err)
	}
	if len(paths2) != 0 {
		t.Errorf("expected no changes on second install, got %v", paths2)
	}
}

func TestInstallCommandsRemovesStaleFiles(t *testing.T) {
	// A leftover file under ~/.claude/commands/ that the embed.FS no
	// longer carries must be swept away on the next install. Without
	// this a renamed-or-removed command would linger forever on every
	// user's machine.
	home := t.TempDir()
	t.Setenv("HOME", home)

	cmdsDir := filepath.Join(home, ".claude", "commands")
	if err := os.MkdirAll(cmdsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stalePath := filepath.Join(cmdsDir, "tma1-removed.md")
	if err := os.WriteFile(stalePath, []byte("old command, no longer in embed"), 0o644); err != nil {
		t.Fatal(err)
	}

	inst := &ClaudeCodeInstaller{
		DataDir: filepath.Join(home, ".tma1"),
		Port:    14318,
		Logger:  slog.Default(),
	}
	if _, err := inst.installCommands(); err != nil {
		t.Fatalf("installCommands: %v", err)
	}

	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Errorf("stale file should be removed, stat err = %v", err)
	}
	// And the embedded file is still present.
	wantPath := filepath.Join(cmdsDir, "tma1-peer.md")
	if _, err := os.Stat(wantPath); err != nil {
		t.Errorf("embedded file missing after sync: %v", err)
	}
}

func TestInstallDryRunWritesNothing(t *testing.T) {
	// DryRun must report what *would* happen but leave the filesystem
	// untouched — critical because the installer writes to ~/.claude.json
	// (OAuth tokens live there) and users need a safe preview path.
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(home, "proj")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}

	inst := &ClaudeCodeInstaller{
		DataDir:    filepath.Join(home, ".tma1"),
		Port:       14318,
		ProjectDir: project,
		Logger:     slog.Default(),
		DryRun:     true,
	}
	rep, err := inst.Install()
	if err != nil {
		t.Fatalf("Install dry-run: %v", err)
	}
	if len(rep.Changed) == 0 {
		t.Error("dry-run should still report what would change, got empty Changed list")
	}

	// Nothing under ~/.claude/ should have been created.
	claudeDir := filepath.Join(home, ".claude")
	if entries, _ := os.ReadDir(claudeDir); len(entries) > 0 {
		t.Errorf("dry-run leaked files into %s: %v", claudeDir, entries)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); err == nil {
		t.Error("dry-run wrote ~/.claude.json")
	}
	if _, err := os.Stat(filepath.Join(project, ".gitignore")); err == nil {
		t.Error("dry-run wrote project .gitignore")
	}
	if _, err := os.Stat(filepath.Join(project, "CLAUDE.md")); err == nil {
		t.Error("dry-run wrote project CLAUDE.md")
	}
	// The hook script lives under ~/.tma1/hooks/. Earlier code routed it
	// through EnsureHookScript which bypassed the DryRun sink and wrote
	// the file anyway. Lock that regression: ~/.tma1/ must stay absent.
	if _, err := os.Stat(filepath.Join(home, ".tma1")); err == nil {
		t.Error("dry-run created ~/.tma1/ (hook script write leaked past DryRun)")
	}
	// The report should still name the would-be path so the user sees
	// what would happen.
	if rep.HookScript == "" {
		t.Error("HookScript path missing from dry-run report")
	}
	if !strings.HasSuffix(rep.HookScript, "tma1-hook.sh") &&
		!strings.HasSuffix(rep.HookScript, "tma1-hook.ps1") {
		t.Errorf("HookScript path doesn't look like a hook script: %q", rep.HookScript)
	}
}

func TestRegisterTMA1HooksCoversAllNativeEvents(t *testing.T) {
	// Codex review caught that fresh installs only registered five hook
	// events; PreToolUse / SessionEnd / SubagentStop / Notification were
	// missing, which silently broke perception rules that depend on
	// those events for new users. Lock the full set.
	settings := map[string]any{}
	registerTMA1HookEntries(settings, claudeCodeHookEvents, "/path/to/tma1-hook.sh")

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		t.Fatal("hooks section not created")
		return
	}
	expected := []string{
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
	for _, event := range expected {
		entries, _ := hooks[event].([]any)
		if len(entries) == 0 {
			t.Errorf("hook event %q not registered", event)
		}
	}
	if got, want := len(hooks), len(expected); got != want {
		t.Errorf("registered hook event count = %d, want %d", got, want)
	}
}

func readClaudeConfig(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return m
}

func TestRegisterTMA1HooksAdoptsEquivalentLegacyEntry(t *testing.T) {
	// Legacy entry: no id field, command uses ~/  expansion. The new install
	// should recognize it as TMA1's and rewrite it in place rather than
	// adding a second entry that runs the same script twice (the bug Dennis
	// hit on 2026-05-20).
	home := t.TempDir()
	t.Setenv("HOME", home)
	resolved := home + "/.tma1/hooks/tma1-hook.sh"

	settings := map[string]any{
		"hooks": map[string]any{
			"UserPromptSubmit": []any{
				map[string]any{
					"matcher": "",
					"hooks": []any{
						map[string]any{"type": "command", "command": "~/.tma1/hooks/tma1-hook.sh"},
					},
				},
			},
		},
	}
	if !registerTMA1HookEntries(settings, claudeCodeHookEvents, resolved) {
		t.Fatal("expected mutation when adopting legacy entry")
	}
	list := settings["hooks"].(map[string]any)["UserPromptSubmit"].([]any)
	if len(list) != 1 {
		t.Fatalf("expected exactly 1 UserPromptSubmit entry after adoption, got %d", len(list))
	}
	got := list[0].(map[string]any)
	if id, _ := got["id"].(string); id != "tma1" {
		t.Errorf("entry not tagged with id=tma1: %+v", got)
	}
	if entryCommand(got) != resolved {
		t.Errorf("command not canonicalized to absolute path: %s", entryCommand(got))
	}
}

// TestInstallInstructionsIgnoresInProseMarker is the regression guard
// for the 2026-05-22 AGENTS.md damage. installInstructions previously
// used strings.Index (first occurrence) to find <!-- tma1:start -->,
// so prose mentioning the marker — e.g. a `tma1-server uninstall`
// comment that referred to "the <!-- tma1:start --> block" — became
// a false start that ate ~170 lines of legitimate doc before the real
// end marker. Match only standalone-line markers from now on; this
// test feeds the exact failure shape.
func TestInstallInstructionsIgnoresInProseMarker(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	claudePath := filepath.Join(project, "CLAUDE.md")
	original := "# Project\n\n" +
		"## Commands\n\n" +
		"```bash\n" +
		"tma1-server uninstall --adapter claude-code\n" +
		"                                  # Removes hook registrations, MCP entry, skills, commands, hook script,\n" +
		"                                  # and the <!-- tma1:start --> block. --purge-data also wipes data.\n" +
		"```\n\n" +
		"## Go conventions\n\nSection content that must survive.\n\n" +
		"## Where to look\n\nMore content the install must not touch.\n\n" +
		"<!-- tma1:start -->\n" +
		"## TMA1 Context Layer\nold block body\n" +
		"<!-- tma1:end -->\n"
	if err := os.WriteFile(claudePath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	inst := &ClaudeCodeInstaller{
		DataDir:    filepath.Join(home, ".tma1"),
		Port:       14318,
		ProjectDir: project,
		Logger:     slog.Default(),
	}
	if _, _, err := inst.installInstructions(project); err != nil {
		t.Fatalf("installInstructions: %v", err)
	}

	got, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatalf("read claude.md: %v", err)
	}
	gotStr := string(got)
	// The in-prose marker must survive untouched.
	if !strings.Contains(gotStr, "and the <!-- tma1:start --> block. --purge-data") {
		t.Errorf("in-prose marker text was clobbered by install:\n%s", gotStr)
	}
	// The Go conventions and Where to look sections must survive.
	if !strings.Contains(gotStr, "Go conventions") {
		t.Errorf("install destroyed the Go conventions section:\n%s", gotStr)
	}
	if !strings.Contains(gotStr, "Where to look") {
		t.Errorf("install destroyed the Where to look section:\n%s", gotStr)
	}
	// The real block must have been replaced (its old body gone, new
	// body present).
	if strings.Contains(gotStr, "old block body") {
		t.Errorf("install failed to replace the real marker block:\n%s", gotStr)
	}
	if !strings.Contains(gotStr, "## TMA1 Context Layer") {
		t.Errorf("install did not write the new block body:\n%s", gotStr)
	}
}
