// Package hooks (uninstall_cc.go) reverses install_cc.go for Claude
// Code: removes our hook script registrations from
// `~/.claude/settings.json`, our MCP server entry from
// `~/.claude.json`, our embedded skills + slash commands, and our
// project-level instruction block. User-owned content (other hooks,
// other MCP servers, other skills, content outside our marker block,
// the .gitignore line) is left intact.
package hooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// ClaudeCodeUninstaller mirrors ClaudeCodeInstaller. Fields are kept
// minimal: we never need the Port or GreptimeDBHTTPPort (uninstall
// doesn't write any value depending on them), and the DryRun flag
// goes through the same sink as install.
type ClaudeCodeUninstaller struct {
	DataDir    string // ~/.tma1
	ProjectDir string // project root (CLAUDE.md / AGENTS.md / .gitignore)
	Logger     *slog.Logger
	DryRun     bool
	// PurgeData removes ~/.tma1/data/ and ~/.tma1/bin/ on top of the
	// config / script artifacts. Off by default — historical traces
	// are user data, not install scaffolding.
	PurgeData bool
}

// writeFile / mkdirAll / dryRun / getLogger satisfy installSink so we
// can reuse removeStaleUnder unchanged.
func (u *ClaudeCodeUninstaller) writeFile(path string, data []byte, perm os.FileMode) error {
	if u.DryRun {
		if u.Logger != nil {
			u.Logger.Info("[dry-run] would write", "path", path, "bytes", len(data))
		}
		return nil
	}
	return writeFileAtomic(path, data, perm)
}

func (u *ClaudeCodeUninstaller) mkdirAll(path string, perm os.FileMode) error {
	if u.DryRun {
		if u.Logger != nil {
			u.Logger.Info("[dry-run] would mkdir -p", "path", path)
		}
		return nil
	}
	return os.MkdirAll(path, perm)
}

func (u *ClaudeCodeUninstaller) dryRun() bool            { return u.DryRun }
func (u *ClaudeCodeUninstaller) getLogger() *slog.Logger { return u.Logger }

// Uninstall executes the reverse of Install. Step order intentionally
// undoes peripheral references first (config entries) so a mid-failure
// can't leave a registered hook pointing at a deleted script.
func (u *ClaudeCodeUninstaller) Uninstall() (UninstallReport, error) {
	var rep UninstallReport
	var errs []error

	home, err := os.UserHomeDir()
	if err != nil {
		return rep, fmt.Errorf("resolve home: %w", err)
	}

	// 1. settings.json — drop tma1 hook entries from every event.
	if path, removed, err := u.uninstallSettings(home); err != nil {
		errs = append(errs, fmt.Errorf("settings.json: %w", err))
		rep.SettingsPath = path
	} else {
		rep.SettingsPath = path
		switch {
		case path == "":
			// nothing to record
		case removed > 0:
			rep.Removed = append(rep.Removed, reportPath(path, fmt.Sprintf("%d tma1 hook entries", removed)))
		default:
			rep.Skipped = append(rep.Skipped, reportPath(path, "no tma1 hook entries found"))
		}
	}

	// 2. claude.json — drop mcpServers.tma1.
	if path, removed, err := u.uninstallMCP(home); err != nil {
		errs = append(errs, fmt.Errorf("claude.json: %w", err))
		rep.MCPConfigPath = path
	} else {
		rep.MCPConfigPath = path
		switch {
		case path == "":
		case removed:
			rep.Removed = append(rep.Removed, reportPath(path, "mcpServers.tma1"))
		default:
			rep.Skipped = append(rep.Skipped, reportPath(path, "no tma1 MCP entry"))
		}
	}

	// 3. Project instructions — only touch THIS adapter's file. The
	// earlier draft scanned both CLAUDE.md and AGENTS.md unconditionally
	// to cover CC's fallback-into-AGENTS.md path, but in a dual-adapter
	// project (CC owns CLAUDE.md, Codex owns AGENTS.md) that loop
	// happily stripped the Codex block too. Mirror install's choice
	// logic: target CLAUDE.md, fall back to AGENTS.md only when
	// CLAUDE.md is absent. A user who manually deletes CLAUDE.md after
	// installing both adapters still trips the fallback into AGENTS.md
	// — out of scope for now; documented in docs/hooks.md.
	if u.ProjectDir != "" {
		target := chooseInstructionsFile(u.ProjectDir, "CLAUDE.md")
		removed, err := u.uninstallInstructionsFile(target)
		switch {
		case errors.Is(err, ErrInstructionsHalfState):
			rep.Errors = append(rep.Errors, reportPath(target, err.Error()))
		case err != nil:
			errs = append(errs, fmt.Errorf("%s: %w", filepath.Base(target), err))
		case removed:
			rep.InstructionsPaths = append(rep.InstructionsPaths, target)
			rep.Removed = append(rep.Removed, reportPath(target, "tma1 block"))
		default:
			// File absent OR file present without markers — silent skip.
		}
	}

	// 4. .gitignore — deliberately NOT deleted. Install never recorded
	// provenance, so we can't tell apart "we added '.tma1-context.md'"
	// from "user had it before install". Report the path so the
	// operator knows where to look if they want to clean up manually.
	if u.ProjectDir != "" {
		gi := filepath.Join(u.ProjectDir, ".gitignore")
		if _, err := os.Stat(gi); err == nil {
			rep.GitignorePath = gi
			rep.Skipped = append(rep.Skipped, reportPath(gi, "left in place — uninstall does not delete; manually remove '.tma1-context.md' if desired"))
		}
	}

	// 5. Skills tree — wipe every tma1-* subdir.
	skillRoot := filepath.Join(home, ".claude", "skills")
	if removed, err := u.removeOwnedDir(skillRoot); err != nil {
		errs = append(errs, fmt.Errorf("skills: %w", err))
	} else {
		rep.SkillPaths = removed
		for _, p := range removed {
			rep.Removed = append(rep.Removed, reportPath(p, "skill"))
		}
	}

	// 6. Commands tree — same primitive on ~/.claude/commands/.
	cmdRoot := filepath.Join(home, ".claude", "commands")
	if removed, err := u.removeOwnedDir(cmdRoot); err != nil {
		errs = append(errs, fmt.Errorf("commands: %w", err))
	} else {
		rep.CommandPaths = removed
		for _, p := range removed {
			rep.Removed = append(rep.Removed, reportPath(p, "command"))
		}
	}

	// 7. Hook script LAST — any reference to it has already been
	// scrubbed by steps 1–6. If ~/.tma1/hooks/ ends up empty, drop it.
	rep.HookScript = HookScriptPathFor(AdapterClaudeCode, u.DataDir)
	if removed, err := u.removeHookScript(rep.HookScript); err != nil {
		errs = append(errs, fmt.Errorf("hook script: %w", err))
	} else if removed {
		rep.Removed = append(rep.Removed, reportPath(rep.HookScript, "hook script"))
	} else {
		rep.Skipped = append(rep.Skipped, reportPath(rep.HookScript, "not present"))
	}

	// 8. --purge-data — remove ~/.tma1/data/ + ~/.tma1/bin/ on demand.
	if u.PurgeData {
		for _, sub := range []string{"data", "bin"} {
			p := filepath.Join(u.DataDir, sub)
			if u.DryRun {
				if u.Logger != nil {
					u.Logger.Info("[dry-run] would purge", "path", p)
				}
				rep.Removed = append(rep.Removed, reportPath(p, "purge-data"))
				continue
			}
			if _, err := os.Stat(p); err == nil {
				if err := os.RemoveAll(p); err != nil {
					errs = append(errs, fmt.Errorf("purge %s: %w", p, err))
				} else {
					rep.Removed = append(rep.Removed, reportPath(p, "purge-data"))
				}
			}
		}
	}

	if len(errs) > 0 {
		return rep, joinErrors(errs)
	}
	return rep, nil
}

// uninstallSettings drops every TMA1-owned hook entry from
// ~/.claude/settings.json. Returns the resolved path, the count of
// entries removed, and any error.
func (u *ClaudeCodeUninstaller) uninstallSettings(home string) (string, int, error) {
	path := filepath.Join(home, ".claude", "settings.json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path, 0, nil
	}
	existing, err := readJSONFileStrict(path)
	if err != nil {
		return path, 0, fmt.Errorf("refusing to overwrite %s: %w", path, err)
	}
	hookCmd := wrapHookCommand(HookScriptPathFor(AdapterClaudeCode, u.DataDir))
	removed := unregisterTMA1Hooks(existing, claudeCodeHookEvents, hookCmd)
	if removed == 0 {
		return path, 0, nil
	}
	out, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return path, 0, err
	}
	if err := u.writeFile(path, append(out, '\n'), 0o644); err != nil {
		return path, 0, err
	}
	return path, removed, nil
}

// uninstallMCP removes the `tma1` entry from ~/.claude.json mcpServers.
func (u *ClaudeCodeUninstaller) uninstallMCP(home string) (string, bool, error) {
	path := filepath.Join(home, ".claude.json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path, false, nil
	}
	existing, err := readJSONFileStrict(path)
	if err != nil {
		return path, false, fmt.Errorf("refusing to overwrite %s: %w", path, err)
	}
	servers, _ := existing["mcpServers"].(map[string]any)
	if !removeMCPServerEntry(servers, hookOwnerID) {
		return path, false, nil
	}
	existing["mcpServers"] = servers
	out, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return path, false, err
	}
	if err := u.writeFile(path, append(out, '\n'), 0o644); err != nil {
		return path, false, err
	}
	return path, true, nil
}

// uninstallInstructionsFile removes the <!-- tma1:start --> ...
// <!-- tma1:end --> block from a single CLAUDE.md / AGENTS.md file.
// Returns whether anything was removed and any error. A half-state
// file returns ErrInstructionsHalfState with removed=false.
func (u *ClaudeCodeUninstaller) uninstallInstructionsFile(path string) (bool, error) {
	existing, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	newContent, removed, err := removeInstructionsBlock(existing)
	if err != nil {
		return false, err
	}
	if !removed {
		return false, nil
	}
	if err := u.writeFile(path, newContent, 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// removeOwnedDir wipes every TMA1-owned subdirectory under root.
//
// Two passes:
//  1. removeStaleUnder with the hookOwnerPrefix prefix catches the
//     standard entries (tma1-peer, tma1-setup, …).
//  2. The legacy `tma1` skill (no hyphen) doesn't match that prefix
//     but is still ours; remove it explicitly when present.
//
// User-owned siblings (humanizer, find-skills, etc.) survive both
// passes — the prefix guard scopes pass 1 and pass 2 hard-codes a
// single known name.
func (u *ClaudeCodeUninstaller) removeOwnedDir(root string) ([]string, error) {
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil, nil
	}
	removed, err := removeStaleUnder(u, root, map[string]struct{}{}, hookOwnerPrefix)
	if err != nil {
		return removed, err
	}
	legacy := filepath.Join(root, hookOwnerID)
	if _, statErr := os.Stat(legacy); statErr == nil {
		if u.DryRun {
			if u.Logger != nil {
				u.Logger.Info("[dry-run] would remove legacy tma1 dir", "path", legacy)
			}
			removed = append(removed, legacy+" (legacy tma1 dir, would remove)")
		} else if err := os.RemoveAll(legacy); err != nil {
			return removed, err
		} else {
			removed = append(removed, legacy+" (legacy tma1 dir, removed)")
		}
	}
	return removed, nil
}

// removeHookScript removes the per-adapter hook script. If the parent
// ~/.tma1/hooks/ directory ends up empty, drops it too.
func (u *ClaudeCodeUninstaller) removeHookScript(path string) (bool, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return false, nil
	}
	if u.DryRun {
		if u.Logger != nil {
			u.Logger.Info("[dry-run] would remove hook script", "path", path)
		}
		return true, nil
	}
	if err := os.Remove(path); err != nil {
		return false, err
	}
	removeEmptyDir(filepath.Dir(path)) // best-effort
	return true, nil
}
