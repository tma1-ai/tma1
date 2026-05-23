// Package hooks (uninstall_shared.go) carries the reverse of install:
// helpers that surgically remove what install wrote, without touching
// user-owned content. Mirrors install_shared.go in shape — both use the
// installSink interface so DryRun support comes for free.
package hooks

import (
	"errors"
	"fmt"
	"os"
)

// UninstallReport mirrors InstallReport. Each field captures the path
// involved (so the operator can verify) plus three buckets:
//
//   - Removed: human-readable list of artifacts we deleted/edited.
//   - Skipped: artifacts that were absent or deliberately left in place
//     (e.g. .gitignore — see below).
//   - Errors:  edge cases that need operator attention (e.g. half-state
//     instructions block). Non-empty Errors means the command exits
//     non-zero.
type UninstallReport struct {
	HookScript        string
	SettingsPath      string
	MCPConfigPath     string
	InstructionsPaths []string
	GitignorePath     string
	SkillPaths        []string
	CommandPaths      []string

	Removed []string
	Skipped []string
	Errors  []string
}

// HasErrors reports whether the run accumulated any non-fatal
// inconsistencies (currently: half-state instructions block). Useful
// for the CLI to translate into a non-zero exit code while still
// printing the full report.
func (r UninstallReport) HasErrors() bool { return len(r.Errors) > 0 }

// unregisterTMA1Hooks walks every event in eventNames and drops entries
// whose matchesTMA1HookEntry returns true. Returns true if the settings
// map mutated. Empty event arrays are left in place — the surrounding
// JSON shape stays comparable to what install would write next time.
//
// Predicate is shared with install (matchesTMA1HookEntry in install_shared.go)
// so legacy entries that pre-date the `id` field are recognised by their
// command path. Without that, an uninstall on an old install would leave
// dangling hook registrations pointing to a deleted hook script.
func unregisterTMA1Hooks(settings map[string]any, eventNames []string, hookCommand string) (removed int) {
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		return 0
	}
	for _, event := range eventNames {
		list, ok := hooks[event].([]any)
		if !ok || len(list) == 0 {
			continue
		}
		filtered := make([]any, 0, len(list))
		for _, item := range list {
			if matchesTMA1HookEntry(item, hookCommand, hookOwnerID) {
				removed++
				continue
			}
			filtered = append(filtered, item)
		}
		if len(filtered) != len(list) {
			hooks[event] = filtered
		}
	}
	if removed > 0 {
		settings["hooks"] = hooks
	}
	return removed
}

// removeMCPServerEntry deletes the named server from a CC-style
// `mcpServers` or Codex-style `mcp_servers` map. Returns true if
// anything was removed.
func removeMCPServerEntry(servers map[string]any, name string) bool {
	if servers == nil {
		return false
	}
	if _, ok := servers[name]; !ok {
		return false
	}
	delete(servers, name)
	return true
}

// ErrInstructionsHalfState signals an instructions file with exactly
// one of the two markers present. Uninstall refuses to guess where
// our content ends — the user has to fix the file by hand.
var ErrInstructionsHalfState = errors.New("instructions file has only one tma1 marker (start or end without its pair); refusing to edit")

// removeInstructionsBlock cuts out the block bounded by the start /
// end markers (inclusive), plus a trailing newline if present. Returns
// the new content, whether anything was removed, and a half-state
// error when exactly one marker is present.
//
// Contract:
//   - both markers absent   → (existing, false, nil)
//   - both markers present  → (newContent, true, nil)
//   - exactly one present   → (existing, false, ErrInstructionsHalfState)
//
// Why refuse half-state: install always writes start+end as a pair.
// A file with only `<!-- tma1:start -->` and no end could mean the user
// deleted the end marker and kept legitimate content after it; cutting
// from start to EOF would destroy that content. Conservative is correct.
func removeInstructionsBlock(existing []byte) ([]byte, bool, error) {
	const (
		startMarker = "<!-- tma1:start -->"
		endMarker   = "<!-- tma1:end -->"
	)
	// Standalone-line matching mirrors installInstructions: a marker
	// referenced in prose ("uninstall removes the <!-- tma1:start -->
	// block") must not shadow the real one. See install_shared.go
	// indexOfStandaloneLine for the reasoning.
	startIdx := indexOfStandaloneLine(existing, startMarker)
	endIdx := indexOfStandaloneLine(existing, endMarker)

	if startIdx < 0 && endIdx < 0 {
		return existing, false, nil
	}
	if startIdx < 0 || endIdx < 0 {
		return existing, false, ErrInstructionsHalfState
	}
	if endIdx < startIdx {
		// Pathological: end appears before start. Treat as half-state
		// (we can't safely guess intent).
		return existing, false, ErrInstructionsHalfState
	}

	cutEnd := endIdx + len(endMarker)
	// Eat the single trailing newline install inserts after endMarker
	// so we don't leave a blank-line scar.
	if cutEnd < len(existing) && existing[cutEnd] == '\n' {
		cutEnd++
	}

	// Eat a blank-line scar BEFORE the start marker if both:
	//  - the prefix is non-empty
	//  - the byte before the block is "\n\n" (install inserts an extra
	//    "\n" before the block when prepending to a non-empty file).
	cutStart := startIdx
	if cutStart >= 2 && existing[cutStart-1] == '\n' && existing[cutStart-2] == '\n' {
		cutStart--
	}

	newContent := make([]byte, 0, len(existing)-(cutEnd-cutStart))
	newContent = append(newContent, existing[:cutStart]...)
	newContent = append(newContent, existing[cutEnd:]...)
	return newContent, true, nil
}

// removeEmptyDir best-effort os.Remove on a directory. Returns true if
// the directory was removed. A non-empty directory returns false
// without raising an error — that's the signal "user content lives
// here, leave it alone".
func removeEmptyDir(path string) bool {
	err := os.Remove(path)
	return err == nil
}

// reportPath joins a path with a parenthetical detail for the Removed
// / Skipped / Errors slices. Keeps the report consistently formatted.
func reportPath(path, detail string) string {
	if detail == "" {
		return path
	}
	return fmt.Sprintf("%s (%s)", path, detail)
}
