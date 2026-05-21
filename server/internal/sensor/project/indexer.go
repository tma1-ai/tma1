// Package project is the project-state sensor. It scans a project root to
// learn what kind of code base it is (language, build system, test
// framework, key files) so the perception layer can give an agent a
// project-shaped onboarding hint at SessionStart without the agent having
// to ls/cat its way around the repo.
//
// The indexer is intentionally heuristic and shallow: marker files and
// directory listings. Anything that needs parsing source code (full module
// graphs, AST symbols) belongs to a later phase.
package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// State is the indexed snapshot of one project root.
type State struct {
	Project       string    `json:"project"`
	Root          string    `json:"root"`
	IndexedAt     time.Time `json:"indexed_at"`
	Language      string    `json:"language,omitempty"`       // primary language (best guess)
	Frameworks    []string  `json:"frameworks,omitempty"`     // detected frameworks / extra languages
	BuildSystem   string    `json:"build_system,omitempty"`   // make | cargo | npm | go | maven | ...
	TestFramework string    `json:"test_framework,omitempty"` // go test | jest | pytest | ...
	KeyFiles      []string  `json:"key_files,omitempty"`      // README, CLAUDE.md, AGENTS.md, LICENSE, ...
	TopLevelDirs  []string  `json:"top_level_dirs,omitempty"` // first-level directories worth knowing about
}

// Index inspects root and returns a State. It never errors — missing
// signals translate to empty fields. Caller is expected to pass a directory
// that exists; if it doesn't, the returned State will be nearly empty.
func Index(project, root string) State {
	s := State{
		Project:   project,
		Root:      root,
		IndexedAt: time.Now().UTC(),
	}
	if root == "" {
		return s
	}

	// 1) Marker-file driven language/build-system detection. Order matters:
	// the first marker present wins for "primary" language; the rest get
	// added to Frameworks.
	type langSig struct {
		marker, lang, build, test string
	}
	signatures := []langSig{
		{"go.mod", "go", "go", "go test"},
		{"Cargo.toml", "rust", "cargo", "cargo test"},
		{"pyproject.toml", "python", "pip / poetry", "pytest"},
		{"setup.py", "python", "pip", "pytest"},
		{"requirements.txt", "python", "pip", "pytest"},
		{"package.json", "javascript", "npm", ""},
		{"pom.xml", "java", "maven", "junit"},
		{"build.gradle", "java", "gradle", "junit"},
		{"build.gradle.kts", "kotlin", "gradle", "junit"},
		{"composer.json", "php", "composer", "phpunit"},
		{"Gemfile", "ruby", "bundler", "rspec"},
		{"mix.exs", "elixir", "mix", "exunit"},
	}
	languagesSeen := map[string]bool{}
	for _, sig := range signatures {
		if !fileExists(filepath.Join(root, sig.marker)) {
			continue
		}
		if s.Language == "" {
			s.Language = sig.lang
			s.BuildSystem = sig.build
			s.TestFramework = sig.test
		} else if !languagesSeen[sig.lang] && sig.lang != s.Language {
			s.Frameworks = append(s.Frameworks, sig.lang)
		}
		languagesSeen[sig.lang] = true
	}

	// 2) Refinements that need to look at file content.
	if s.Language == "javascript" {
		refineJavaScript(root, &s)
	}

	// 3) Build-system overrides: a Makefile in the root means make is the
	// likely entry point, even when go.mod/Cargo.toml etc. are present.
	if fileExists(filepath.Join(root, "Makefile")) {
		if s.BuildSystem == "" {
			s.BuildSystem = "make"
		} else if !strings.Contains(s.BuildSystem, "make") {
			s.BuildSystem = "make (wraps " + s.BuildSystem + ")"
		}
	}

	// 4) Key files: anything an agent would want to read for orientation.
	s.KeyFiles = scanKeyFiles(root)

	// 5) Top-level directories — first-level only, skipping the same
	// noise set the git sensor ignores so the agent gets the signal
	// directories.
	s.TopLevelDirs = scanTopLevelDirs(root)

	return s
}

// refineJavaScript looks at package.json to upgrade "javascript" to
// "typescript" when tsconfig.json is present, and to detect the test
// framework from devDependencies.
func refineJavaScript(root string, s *State) {
	if fileExists(filepath.Join(root, "tsconfig.json")) {
		s.Language = "typescript"
	}

	raw, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return
	}
	var pkg struct {
		DevDependencies map[string]string `json:"devDependencies"`
		Dependencies    map[string]string `json:"dependencies"`
		Scripts         map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return
	}

	// Test framework detection: check well-known names in dev/dependencies.
	for _, dep := range []string{"vitest", "jest", "mocha", "ava", "playwright"} {
		if _, ok := pkg.DevDependencies[dep]; ok {
			s.TestFramework = dep
			break
		}
		if _, ok := pkg.Dependencies[dep]; ok {
			s.TestFramework = dep
			break
		}
	}

	// Package manager hint: lockfiles narrow down which CLI an agent should use.
	switch {
	case fileExists(filepath.Join(root, "pnpm-lock.yaml")):
		s.BuildSystem = "pnpm"
	case fileExists(filepath.Join(root, "yarn.lock")):
		s.BuildSystem = "yarn"
	case fileExists(filepath.Join(root, "bun.lockb")):
		s.BuildSystem = "bun"
	}
}

// scanKeyFiles returns root-level files that an agent should know about for
// orientation. The list is sorted alphabetically for deterministic output.
func scanKeyFiles(root string) []string {
	candidates := []string{
		"README.md", "README.MD", "README.rst", "README.txt", "README",
		"CLAUDE.md", "AGENTS.md", "GEMINI.md", "CONVENTIONS.md",
		"LICENSE", "LICENSE.md", "LICENSE.txt",
		"CHANGELOG.md", "CONTRIBUTING.md", "SECURITY.md",
		"Makefile", "Dockerfile",
		"go.mod", "Cargo.toml", "package.json", "pyproject.toml", "pom.xml",
	}
	var found []string
	seen := map[string]bool{}
	for _, c := range candidates {
		if seen[strings.ToLower(c)] {
			continue
		}
		if fileExists(filepath.Join(root, c)) {
			found = append(found, c)
			seen[strings.ToLower(c)] = true
		}
	}
	sort.Strings(found)
	return found
}

// scanTopLevelDirs returns first-level subdirectories of root, skipping
// the standard noise. Bounded to 24 entries to keep the bundle compact.
func scanTopLevelDirs(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	skip := map[string]bool{
		".git":            true,
		"node_modules":    true,
		"target":          true,
		"dist":            true,
		"build":           true,
		"out":             true,
		".next":           true,
		".cache":          true,
		".idea":           true,
		".vscode":         true,
		".claude":         true,
		"__pycache__":     true,
		".pytest_cache":   true,
		"coverage":        true,
	}
	var dirs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if skip[name] || strings.HasPrefix(name, ".") {
			continue
		}
		dirs = append(dirs, name)
	}
	sort.Strings(dirs)
	if len(dirs) > 24 {
		dirs = dirs[:24]
	}
	return dirs
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
