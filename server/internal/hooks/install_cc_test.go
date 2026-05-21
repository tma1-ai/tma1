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
	if !registerTMA1Hooks(settings, "/new/path/tma1-hook.sh") {
		t.Fatal("expected mutation when command changes")
	}
	if registerTMA1Hooks(settings, "/new/path/tma1-hook.sh") {
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
	if !registerTMA1Hooks(settings, resolved) {
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
