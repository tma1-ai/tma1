package git

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseGitignoreIgnoresCommentsAndEmpty(t *testing.T) {
	m := parseGitignore("# comment\n\n  \nbin/\n*.log\n")
	if m == nil {
		t.Fatal("expected non-nil matcher")
		return // unreachable, but appeases staticcheck's SA5011 below
	}
	if len(m.dirs) != 1 || m.dirs[0] != "bin" {
		t.Errorf("dirs = %v, want [bin]", m.dirs)
	}
	if len(m.suffixes) != 1 || m.suffixes[0] != ".log" {
		t.Errorf("suffixes = %v, want [.log]", m.suffixes)
	}
}

func TestParseGitignoreSkipsNegation(t *testing.T) {
	// Negation is intentionally unsupported: simpler to under-ignore than
	// to mis-track include/exclude state.
	m := parseGitignore("bin/\n!bin/keep.bin\n")
	if m == nil || len(m.dirs) != 1 {
		t.Fatalf("dirs = %v, want [bin]", m)
	}
	if len(m.literals) != 0 {
		t.Errorf("negation should be skipped, got literals=%v", m.literals)
	}
}

func TestParseGitignoreEmptyReturnsNil(t *testing.T) {
	if m := parseGitignore(""); m != nil {
		t.Errorf("empty content should yield nil matcher, got %+v", m)
	}
	if m := parseGitignore("# comments only\n"); m != nil {
		t.Errorf("comments-only should yield nil matcher, got %+v", m)
	}
}

func TestMatchesCoversCommonProjects(t *testing.T) {
	// Composite .gitignore patterned after what tma1 itself + a Rust /
	// Node project would emit.
	m := parseGitignore(`
# build outputs
bin/
target/
dist/
node_modules/

# log + tmp
*.log
*.tmp

# tma1-specific
.tma1-context.md

# specific path
docs/secrets.md
`)
	if m == nil {
		t.Fatal("matcher should not be nil")
	}
	root := "/repo"
	cases := []struct {
		path string
		want bool
	}{
		{"/repo/bin/tma1-server", true},
		{"/repo/server/bin/something", true},
		{"/repo/target/release/lib.a", true},
		{"/repo/dist/bundle.js", true},
		{"/repo/node_modules/react/index.js", true},
		{"/repo/var/log/app.log", true},
		{"/repo/scratch.tmp", true},
		{"/repo/.tma1-context.md", true},
		{"/repo/docs/secrets.md", true},
		{"/repo/src/main.rs", false},
		{"/repo/README.md", false},
		{"/repo/Cargo.toml", false},
	}
	for _, c := range cases {
		if got := m.matches(c.path, root); got != c.want {
			t.Errorf("matches(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

// TestMatchesWindowsPathsAgainstPOSIXPatterns pins down the
// cross-platform normalisation: a Windows-shaped path passed in (as
// fsnotify would deliver it on Windows) must still match the same
// .gitignore patterns the project parsed POSIX-style.
func TestMatchesWindowsPathsAgainstPOSIXPatterns(t *testing.T) {
	m := parseGitignore("bin/\nnode_modules/\n*.log\ndocs/secrets.md\n")
	if m == nil {
		t.Fatal("matcher should not be nil")
	}
	root := `C:\repo`
	cases := []struct {
		path string
		want bool
	}{
		{`C:\repo\bin\tma1-server.exe`, true}, // dir pattern
		{`C:\repo\node_modules\react\index.js`, true},
		{`C:\repo\var\app.log`, true}, // suffix pattern
		{`C:\repo\docs\secrets.md`, true},
		{`C:\repo\src\main.go`, false},
		{`C:\repo\README.md`, false},
	}
	for _, c := range cases {
		if got := m.matches(c.path, root); got != c.want {
			t.Errorf("matches(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestLoadGitignoreReadsFile(t *testing.T) {
	dir := t.TempDir()
	content := "bin/\n*.log\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	m := loadGitignore(dir)
	if m == nil {
		t.Fatal("expected non-nil matcher")
	}
	if !m.matches(filepath.Join(dir, "bin/foo"), dir) {
		t.Errorf("should match bin/foo")
	}
	if !m.matches(filepath.Join(dir, "app.log"), dir) {
		t.Errorf("should match *.log")
	}
	if m.matches(filepath.Join(dir, "src/main.go"), dir) {
		t.Errorf("should NOT match src/main.go")
	}
}

func TestLoadGitignoreAbsentReturnsNil(t *testing.T) {
	dir := t.TempDir()
	if m := loadGitignore(dir); m != nil {
		t.Errorf("expected nil for missing .gitignore, got %+v", m)
	}
}

func TestMatcherNilReceiverIsSafe(t *testing.T) {
	var m *gitignoreMatcher
	if m.matches("/anything", "/root") {
		t.Error("nil matcher should never match")
	}
}
