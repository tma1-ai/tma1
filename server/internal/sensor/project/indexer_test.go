package project

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestIndexDetectsGoProject(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, root, "go.mod", "module x\n")
	mustWrite(t, root, "README.md", "# x\n")
	mustWrite(t, root, "Makefile", "all:\n\techo hi\n")
	_ = os.Mkdir(filepath.Join(root, "server"), 0o755)
	_ = os.Mkdir(filepath.Join(root, "node_modules"), 0o755) // must be filtered

	s := Index("x", root)

	if s.Language != "go" {
		t.Errorf("Language = %q, want go", s.Language)
	}
	if s.TestFramework != "go test" {
		t.Errorf("TestFramework = %q, want 'go test'", s.TestFramework)
	}
	// Makefile present → build system reflects wrapping.
	if s.BuildSystem != "make (wraps go)" {
		t.Errorf("BuildSystem = %q, want 'make (wraps go)'", s.BuildSystem)
	}
	if !slices.Contains(s.KeyFiles, "README.md") || !slices.Contains(s.KeyFiles, "go.mod") || !slices.Contains(s.KeyFiles, "Makefile") {
		t.Errorf("KeyFiles missing entries: %v", s.KeyFiles)
	}
	if !slices.Contains(s.TopLevelDirs, "server") {
		t.Errorf("TopLevelDirs missing 'server': %v", s.TopLevelDirs)
	}
	if slices.Contains(s.TopLevelDirs, "node_modules") {
		t.Errorf("TopLevelDirs should not include node_modules")
	}
}

func TestIndexDetectsTypeScriptViaTsconfig(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, root, "package.json", `{"name":"x","devDependencies":{"vitest":"^1"}}`+"\n")
	mustWrite(t, root, "tsconfig.json", "{}\n")
	mustWrite(t, root, "pnpm-lock.yaml", "")

	s := Index("x", root)

	if s.Language != "typescript" {
		t.Errorf("Language = %q, want typescript", s.Language)
	}
	if s.BuildSystem != "pnpm" {
		t.Errorf("BuildSystem = %q, want pnpm (overridden by lockfile)", s.BuildSystem)
	}
	if s.TestFramework != "vitest" {
		t.Errorf("TestFramework = %q, want vitest", s.TestFramework)
	}
}

func TestIndexMissingMarkersStaysEmpty(t *testing.T) {
	root := t.TempDir()
	s := Index("x", root)

	if s.Language != "" || s.BuildSystem != "" {
		t.Errorf("expected empty language/build, got lang=%q build=%q", s.Language, s.BuildSystem)
	}
}

func TestIndexNoiseTopLevelDirsFiltered(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{".git", "node_modules", "dist", ".idea", "src"} {
		_ = os.Mkdir(filepath.Join(root, d), 0o755)
	}
	s := Index("x", root)
	if !slices.Contains(s.TopLevelDirs, "src") {
		t.Errorf("expected 'src' in TopLevelDirs, got %v", s.TopLevelDirs)
	}
	for _, bad := range []string{".git", "node_modules", "dist", ".idea"} {
		if slices.Contains(s.TopLevelDirs, bad) {
			t.Errorf("expected %q to be filtered out, got %v", bad, s.TopLevelDirs)
		}
	}
}

func mustWrite(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
