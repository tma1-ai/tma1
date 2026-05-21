package perception

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FileWriter writes .tma1-context.md to a project root so non-MCP agents
// (Aider / Cursor / file-aware tools) can read it via their own Read tool.
type FileWriter struct {
	bundler *Bundler
}

// NewFileWriter returns a FileWriter backed by the given Bundler.
func NewFileWriter(bundler *Bundler) *FileWriter {
	return &FileWriter{bundler: bundler}
}

// Write generates a context bundle for the given session/cwd and writes a
// markdown summary to <project_root>/.tma1-context.md. Returns the written
// path. Errors are returned to the caller (callers may choose to log and
// continue).
//
// project_root is detected by ResolveProjectRoot(cwd).
func (w *FileWriter) Write(ctx context.Context, sessionID, cwd string) (string, error) {
	root := ResolveProjectRoot(cwd)
	if root == "" {
		return "", fmt.Errorf("cannot resolve project root from cwd=%q", cwd)
	}

	bundle := w.bundler.BuildBundle(ctx, sessionID, cwd)
	md := renderMarkdown(bundle)

	target := filepath.Join(root, ".tma1-context.md")
	if err := os.WriteFile(target, []byte(md), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", target, err)
	}
	return target, nil
}

// renderMarkdown produces a longer, more readable view of the bundle
// suitable for human review (and for agents reading via their Read tool).
func renderMarkdown(b *Bundle) string {
	var sb strings.Builder
	sb.WriteString("# TMA1 Context\n\n")
	fmt.Fprintf(&sb, "_Updated: %s_\n\n", b.GeneratedAt.Format("2006-01-02 15:04:05 MST"))
	if b.Project != "" {
		fmt.Fprintf(&sb, "Project: **%s**\n\n", b.Project)
	}

	if ext := b.External; ext != nil && (ext.HumanCount > 0 || ext.GitCount > 0) {
		sb.WriteString("## External changes (last 30 min)\n\n")
		if ext.HumanCount > 0 {
			fmt.Fprintf(&sb, "Human modified %d file(s):\n", ext.HumanCount)
			for i, c := range ext.HumanChanges {
				if i >= 8 {
					break
				}
				fmt.Fprintf(&sb, "- `%s` (%s)\n", c.FilePath, c.ChangeType)
			}
			sb.WriteString("\n")
		}
		if ext.GitCount > 0 {
			sb.WriteString("Git activity:\n")
			for i, c := range ext.GitChanges {
				if i >= 5 {
					break
				}
				if c.GitMessage != "" {
					fmt.Fprintf(&sb, "- %s: %s\n", c.ChangeType, c.GitMessage)
				} else {
					fmt.Fprintf(&sb, "- %s\n", c.ChangeType)
				}
			}
			sb.WriteString("\n")
		}
	}

	// Build status (if any) right at the top — what an agent / developer
	// most wants to know about state of the world.
	if bs := b.Build; bs != nil {
		sb.WriteString("## Build\n\n")
		fmt.Fprintf(&sb, "- Command: `%s`\n", bs.Command)
		if bs.LastExitCode != nil {
			fmt.Fprintf(&sb, "- Last exit: %d\n", *bs.LastExitCode)
		}
		if !bs.LastEventAt.IsZero() {
			fmt.Fprintf(&sb, "- Last event: %s\n", bs.LastEventAt.Format("2006-01-02 15:04:05 MST"))
		}
		if bs.ErrorsInLast30Min > 0 {
			fmt.Fprintf(&sb, "- Errors in last 30 min: %d\n", bs.ErrorsInLast30Min)
		}
		if bs.LastErrorMessage != "" {
			age := relativeAge(bs.LastErrorAt)
			recovered := !bs.LastErrorAt.IsZero() && bs.LastEventAt.After(bs.LastErrorAt.Add(10*time.Second))
			suffix := age
			if recovered {
				suffix += ", may have recovered"
			}
			if suffix == "" {
				fmt.Fprintf(&sb, "- Last error: %s\n", bs.LastErrorMessage)
			} else {
				fmt.Fprintf(&sb, "- Last error (%s): %s\n", suffix, bs.LastErrorMessage)
			}
		}
		sb.WriteString("\n")
	}

	// Project shape (language, build, test, key files). Stable across
	// turns, so it goes near the top as orientation for the reader.
	if ps := b.ProjectState; ps != nil {
		sb.WriteString("## Project\n\n")
		if ps.Language != "" {
			fmt.Fprintf(&sb, "- Language: %s\n", ps.Language)
		}
		if len(ps.Frameworks) > 0 {
			fmt.Fprintf(&sb, "- Also: %s\n", strings.Join(ps.Frameworks, ", "))
		}
		if ps.BuildSystem != "" {
			fmt.Fprintf(&sb, "- Build: %s\n", ps.BuildSystem)
		}
		if ps.TestFramework != "" {
			fmt.Fprintf(&sb, "- Test: %s\n", ps.TestFramework)
		}
		if len(ps.KeyFiles) > 0 {
			fmt.Fprintf(&sb, "- Key files: %s\n", strings.Join(ps.KeyFiles, ", "))
		}
		if len(ps.TopLevelDirs) > 0 {
			fmt.Fprintf(&sb, "- Top dirs: %s\n", strings.Join(ps.TopLevelDirs, ", "))
		}
		sb.WriteString("\n")
	}

	// Anomalies first — these are what the reader most needs to see.
	if len(b.Anomalies) > 0 {
		sb.WriteString("## Anomalies\n\n")
		for _, a := range b.Anomalies {
			fmt.Fprintf(&sb, "- **[%s] %s**: %s\n  _Suggestion:_ %s\n",
				strings.ToUpper(a.Severity), a.Kind, a.Evidence, a.Suggestion)
		}
		sb.WriteString("\n")
	}

	if b.Session == nil {
		if len(b.Anomalies) == 0 {
			sb.WriteString("No active session observed yet.\n")
		}
		return sb.String()
	}
	s := b.Session
	sb.WriteString("## Current Session\n\n")
	fmt.Fprintf(&sb, "- Session: `%s`\n", s.SessionID)
	if s.AgentSource != "" {
		fmt.Fprintf(&sb, "- Agent: %s\n", s.AgentSource)
	}
	if s.DurationMinutes > 0 {
		fmt.Fprintf(&sb, "- Duration: %d minutes\n", s.DurationMinutes)
	}
	if s.ToolCallCount > 0 {
		fmt.Fprintf(&sb, "- Tool calls: %d\n", s.ToolCallCount)
	}
	if s.TokensInput+s.TokensOutput > 0 {
		fmt.Fprintf(&sb, "- Tokens: input=%d, output=%d\n", s.TokensInput, s.TokensOutput)
	}
	if s.CurrentFocus != "" {
		fmt.Fprintf(&sb, "- Current focus: `%s`\n", s.CurrentFocus)
	}
	if len(s.RecentTools) > 0 {
		sb.WriteString("\n### Recent tools\n\n")
		for i, t := range s.RecentTools {
			if i >= 10 {
				break
			}
			fmt.Fprintf(&sb, "- %s × %d\n", t.Name, t.Count)
		}
	}
	if len(s.RecentFiles) > 0 {
		sb.WriteString("\n### Recently touched files\n\n")
		for i, p := range s.RecentFiles {
			if i >= 8 {
				break
			}
			fmt.Fprintf(&sb, "- `%s`\n", p)
		}
	}
	return sb.String()
}

// ResolveProjectRoot returns the project root for a given working directory.
// Resolution order (matches Plan §Phase 0.1):
//  1. nearest parent containing .git (preferred — this is the repository root)
//  2. nearest parent containing a marker file (go.mod, package.json, etc.)
//  3. cwd itself
//
// The two-pass design matters in mono-repo / nested-module layouts: a Go
// project with .git at the repo root and go.mod in a server/ subdirectory
// should yield the repo root, not the module root, so all subdirectories
// share one .tma1-context.md.
//
// Returns "" if cwd is empty.
func ResolveProjectRoot(cwd string) string {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return ""
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		abs = cwd
	}

	if root := findAncestorWith(abs, ".git"); root != "" {
		return root
	}

	otherMarkers := []string{"go.mod", "package.json", "Cargo.toml", "pyproject.toml", "pom.xml"}
	dir := abs
	for {
		for _, m := range otherMarkers {
			if _, err := os.Stat(filepath.Join(dir, m)); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return abs
}

// findAncestorWith walks from start up to /, returning the first directory
// that contains `marker` as a child. Returns "" if none does.
func findAncestorWith(start, marker string) string {
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
