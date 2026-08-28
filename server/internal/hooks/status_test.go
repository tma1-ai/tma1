package hooks

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiffEmbeddedTree(t *testing.T) {
	dest := t.TempDir()

	// Nothing on disk: every embedded file is stale.
	missing, err := diffEmbeddedTree(embeddedSkills, "skills", dest)
	if err != nil {
		t.Fatalf("diffEmbeddedTree: %v", err)
	}
	if len(missing) == 0 {
		t.Fatal("expected an empty destination to report every embedded file as stale")
	}

	// Mirror the tree, then it should be clean.
	sink := &ClaudeCodeInstaller{}
	if _, err := syncEmbeddedTree(sink, embeddedSkills, "skills", dest, hookOwnerPrefix); err != nil {
		t.Fatalf("syncEmbeddedTree: %v", err)
	}
	clean, err := diffEmbeddedTree(embeddedSkills, "skills", dest)
	if err != nil {
		t.Fatalf("diffEmbeddedTree: %v", err)
	}
	if len(clean) != 0 {
		t.Errorf("freshly synced tree reported stale files: %v", clean)
	}

	// Edit one file the way an upgrade would leave it behind.
	stalePath := missing[0]
	if err := os.WriteFile(stalePath, []byte("old content"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := diffEmbeddedTree(embeddedSkills, "skills", dest)
	if err != nil {
		t.Fatalf("diffEmbeddedTree: %v", err)
	}
	if len(got) != 1 || got[0] != stalePath {
		t.Errorf("stale files = %v, want exactly [%s]", got, stalePath)
	}

	// Reporting must not repair anything.
	if data, _ := os.ReadFile(stalePath); string(data) != "old content" {
		t.Error("diffEmbeddedTree rewrote a file; it must be read-only")
	}
}

func TestAdapterInstalledDetection(t *testing.T) {
	home := t.TempDir()

	if claudeCodeInstalled(home) {
		t.Error("no settings.json should read as not installed")
	}

	settingsDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(settingsDir, "settings.json")

	if err := os.WriteFile(settings, []byte(`{"hooks":{"Stop":[{"id":"someone-else","hooks":[]}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if claudeCodeInstalled(home) {
		t.Error("another tool's hook must not count as a tma1 install")
	}

	if err := os.WriteFile(settings, []byte(`{"hooks":{"Stop":[{"id":"tma1","hooks":[]}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !claudeCodeInstalled(home) {
		t.Error("a tma1 hook registration must count as installed")
	}

	// The case a directory check would get wrong: hooks registered but
	// the skill tree deleted. That is precisely when we must warn.
	if !claudeCodeInstalled(home) {
		t.Error("install detection must not depend on the skill directory existing")
	}
}
