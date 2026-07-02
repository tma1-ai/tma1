package hooks

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// TestCodexInstallEndToEndIdempotent runs Install() twice on a fresh
// sandbox and asserts the second pass reports zero changes. This is
// the regression guard for the Codex side mirroring the CC test —
// any drift in mcpEntryEqual, hooks.json marker matching, or env-
// block ordering would surface as a re-write loop.
func TestCodexInstallEndToEndIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(t.TempDir(), "myproj")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}

	inst := &CodexInstaller{
		DataDir:    filepath.Join(home, ".tma1"),
		Port:       14318,
		ProjectDir: project,
		Logger:     slog.Default(),
	}

	rep, err := inst.Install()
	if err != nil {
		t.Fatalf("first install: %v", err)
	}
	if len(rep.Changed) == 0 {
		t.Error("first install should report changes")
	}

	// hooks.json registers the 5 Codex hook events with id=tma1.
	hooksRaw, err := os.ReadFile(filepath.Join(home, ".codex", "hooks.json"))
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}
	var hooksParsed map[string]any
	if err := json.Unmarshal(hooksRaw, &hooksParsed); err != nil {
		t.Fatalf("parse hooks.json: %v", err)
	}
	hookSection, _ := hooksParsed["hooks"].(map[string]any)
	for _, event := range []string{"SessionStart", "PreToolUse", "PostToolUse", "UserPromptSubmit", "Stop"} {
		entries, _ := hookSection[event].([]any)
		if len(entries) == 0 {
			t.Errorf("hooks.json missing %q", event)
			continue
		}
		first, _ := entries[0].(map[string]any)
		if id, _ := first["id"].(string); id != "tma1" {
			t.Errorf("%s entry missing id=tma1, got %v", event, first)
		}
		// Codex ignores matchers on UserPromptSubmit/Stop and drops
		// them from the trust-identity hash, so the key must be absent
		// there — a spurious `"matcher": ""` desyncs tools that
		// re-derive the trust hash from the definition verbatim.
		_, hasMatcher := first["matcher"]
		if codexMatcherlessEvents[event] {
			if hasMatcher {
				t.Errorf("%s entry must not carry a matcher key, got %v", event, first)
			}
		} else if !hasMatcher {
			t.Errorf("%s entry missing matcher key, got %v", event, first)
		}
	}

	// config.toml's [mcp_servers.tma1] must carry the TMA1_MCP_CALLER
	// env var so the mcp-serve child knows who invoked it.
	tomlRaw, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	var tomlParsed map[string]any
	if err := toml.Unmarshal(tomlRaw, &tomlParsed); err != nil {
		t.Fatalf("parse config.toml: %v", err)
	}
	servers, _ := tomlParsed["mcp_servers"].(map[string]any)
	tma1, _ := servers["tma1"].(map[string]any)
	env, _ := tma1["env"].(map[string]any)
	if env["TMA1_MCP_CALLER"] != "codex" {
		t.Errorf("config.toml env TMA1_MCP_CALLER = %v, want codex", env["TMA1_MCP_CALLER"])
	}

	// AGENTS.md (not CLAUDE.md) is the Codex target.
	if _, err := os.Stat(filepath.Join(project, "AGENTS.md")); err != nil {
		t.Errorf("AGENTS.md not written: %v", err)
	}
	if !strings.Contains(string(mustRead(t, filepath.Join(project, "AGENTS.md"))), "<!-- tma1:start -->") {
		t.Error("AGENTS.md missing tma1 marker")
	}

	// tma1-peer skill must land under ~/.agents/skills/ (Codex's skill dir).
	if _, err := os.Stat(filepath.Join(home, ".agents", "skills", "tma1-peer", "SKILL.md")); err != nil {
		t.Errorf("tma1-peer skill not installed: %v", err)
	}

	// Second install must be a no-op.
	rep2, err := inst.Install()
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if len(rep2.Changed) != 0 {
		t.Errorf("second install should be a no-op, got changes: %v", rep2.Changed)
	}
}

// TestCodexInstallMigratesEmptyMatcher seeds a hooks.json in the
// legacy shape — `"matcher": ""` on every event, as written by older
// TMA1 versions — and asserts one install pass strips the key from the
// matcherless events (UserPromptSubmit/Stop), keeps it elsewhere, and
// that a second pass is a no-op. Guards both directions of the
// presence-aware entryEqual: it must fire once for the migration and
// then never again.
func TestCodexInstallMigratesEmptyMatcher(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	inst := &CodexInstaller{
		DataDir: filepath.Join(home, ".tma1"),
		Port:    14318,
		Logger:  slog.Default(),
	}
	scriptPath := filepath.Join(home, ".tma1", "hooks", "tma1-hook-codex.sh")
	command := wrapHookCommand(scriptPath)

	legacy := map[string]any{"hooks": map[string]any{}}
	for _, event := range codexHookEvents {
		legacy["hooks"].(map[string]any)[event] = []any{
			map[string]any{
				"id":      "tma1",
				"matcher": "",
				"hooks": []any{
					map[string]any{"type": "command", "command": command},
				},
			},
		}
	}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(home, ".codex", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	_, changed, err := inst.installHooksConfig(scriptPath)
	if err != nil {
		t.Fatalf("installHooksConfig: %v", err)
	}
	if !changed {
		t.Fatal("migration pass should rewrite legacy empty-matcher entries")
	}

	var parsed map[string]any
	if err := json.Unmarshal(mustRead(t, cfgPath), &parsed); err != nil {
		t.Fatal(err)
	}
	hookSection, _ := parsed["hooks"].(map[string]any)
	for _, event := range codexHookEvents {
		entries, _ := hookSection[event].([]any)
		if len(entries) != 1 {
			t.Fatalf("%s: want 1 entry, got %v", event, entries)
		}
		entry, _ := entries[0].(map[string]any)
		_, hasMatcher := entry["matcher"]
		if codexMatcherlessEvents[event] && hasMatcher {
			t.Errorf("%s: matcher key should be stripped, got %v", event, entry)
		}
		if !codexMatcherlessEvents[event] && !hasMatcher {
			t.Errorf("%s: matcher key should be kept, got %v", event, entry)
		}
	}

	_, changed, err = inst.installHooksConfig(scriptPath)
	if err != nil {
		t.Fatalf("second installHooksConfig: %v", err)
	}
	if changed {
		t.Error("second pass should be a no-op after migration")
	}
}

// TestCodexInstallMCPCallerPersists guards the specific env var the
// peer-sessions caller-aware exclusion depends on.
func TestCodexInstallMCPCallerPersists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	inst := &CodexInstaller{
		DataDir: filepath.Join(home, ".tma1"),
		Port:    14318,
		Logger:  slog.Default(),
	}
	if _, _, err := inst.installMCPServer(); err != nil {
		t.Fatalf("installMCPServer: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	var parsed map[string]any
	if err := toml.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("parse config.toml: %v", err)
	}
	servers := parsed["mcp_servers"].(map[string]any)
	tma1 := servers["tma1"].(map[string]any)
	env, ok := tma1["env"].(map[string]any)
	if !ok {
		t.Fatalf("env block missing on Codex MCP entry: %+v", tma1)
	}
	if env["TMA1_MCP_CALLER"] != "codex" {
		t.Errorf("TMA1_MCP_CALLER = %v, want codex", env["TMA1_MCP_CALLER"])
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}
