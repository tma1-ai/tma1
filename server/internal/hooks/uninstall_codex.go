// Package hooks (uninstall_codex.go) reverses install_codex.go for
// OpenAI Codex CLI: removes our hook registrations from
// `~/.codex/hooks.json`, our `[mcp_servers.tma1]` table from
// `~/.codex/config.toml`, and the embedded `tma1-peer` skill from
// `~/.agents/skills/`. User-owned content (other hooks, other MCP
// servers, other skills, content outside our marker block) is left
// intact.
package hooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// CodexUninstaller mirrors CodexInstaller. Same shape as
// ClaudeCodeUninstaller — symmetric across adapters.
type CodexUninstaller struct {
	DataDir    string
	ProjectDir string
	Logger     *slog.Logger
	DryRun     bool
	PurgeData  bool
}

func (u *CodexUninstaller) writeFile(path string, data []byte, perm os.FileMode) error {
	if u.DryRun {
		if u.Logger != nil {
			u.Logger.Info("[dry-run] would write", "path", path, "bytes", len(data))
		}
		return nil
	}
	return writeFileAtomic(path, data, perm)
}

func (u *CodexUninstaller) mkdirAll(path string, perm os.FileMode) error {
	if u.DryRun {
		if u.Logger != nil {
			u.Logger.Info("[dry-run] would mkdir -p", "path", path)
		}
		return nil
	}
	return os.MkdirAll(path, perm)
}

func (u *CodexUninstaller) dryRun() bool            { return u.DryRun }
func (u *CodexUninstaller) getLogger() *slog.Logger { return u.Logger }

// Uninstall executes the reverse of Install for Codex. Step order
// matches the CC variant — config entries first, hook script last.
func (u *CodexUninstaller) Uninstall() (UninstallReport, error) {
	var rep UninstallReport
	var errs []error

	home, err := os.UserHomeDir()
	if err != nil {
		return rep, fmt.Errorf("resolve home: %w", err)
	}

	// 1. hooks.json — drop tma1 hook entries from every event.
	if path, removed, err := u.uninstallHooks(home); err != nil {
		errs = append(errs, fmt.Errorf("hooks.json: %w", err))
		rep.SettingsPath = path
	} else {
		rep.SettingsPath = path
		switch {
		case path == "":
		case removed > 0:
			rep.Removed = append(rep.Removed, reportPath(path, fmt.Sprintf("%d tma1 hook entries", removed)))
		default:
			rep.Skipped = append(rep.Skipped, reportPath(path, "no tma1 hook entries found"))
		}
	}

	// 2. config.toml — drop [mcp_servers.tma1].
	if path, removed, err := u.uninstallMCP(home); err != nil {
		errs = append(errs, fmt.Errorf("config.toml: %w", err))
		rep.MCPConfigPath = path
	} else {
		rep.MCPConfigPath = path
		switch {
		case path == "":
		case removed:
			rep.Removed = append(rep.Removed, reportPath(path, "mcp_servers.tma1"))
		default:
			rep.Skipped = append(rep.Skipped, reportPath(path, "no tma1 MCP entry"))
		}
	}

	// 3. Project instructions — only touch THIS adapter's file. Codex
	// always writes to AGENTS.md (no fallback at install time), so
	// uninstall mirrors that: AGENTS.md only, never CLAUDE.md. This
	// keeps a co-installed CC's CLAUDE.md block intact.
	if u.ProjectDir != "" {
		target := chooseInstructionsFile(u.ProjectDir, "AGENTS.md")
		removed, err := u.uninstallInstructionsFile(target)
		switch {
		case errors.Is(err, ErrInstructionsHalfState):
			rep.Errors = append(rep.Errors, reportPath(target, err.Error()))
		case err != nil:
			errs = append(errs, fmt.Errorf("%s: %w", filepath.Base(target), err))
		case removed:
			rep.InstructionsPaths = append(rep.InstructionsPaths, target)
			rep.Removed = append(rep.Removed, reportPath(target, "tma1 block"))
		}
	}

	// 4. .gitignore — left in place; documented in CC variant.
	if u.ProjectDir != "" {
		gi := filepath.Join(u.ProjectDir, ".gitignore")
		if _, err := os.Stat(gi); err == nil {
			rep.GitignorePath = gi
			rep.Skipped = append(rep.Skipped, reportPath(gi, "left in place — uninstall does not delete; manually remove '.tma1-context.md' if desired"))
		}
	}

	// 5. Skills tree — wipe every tma1-* subdir under ~/.agents/skills/.
	skillRoot := filepath.Join(home, ".agents", "skills")
	if removed, err := u.removeOwnedDir(skillRoot); err != nil {
		errs = append(errs, fmt.Errorf("skills: %w", err))
	} else {
		rep.SkillPaths = removed
		for _, p := range removed {
			rep.Removed = append(rep.Removed, reportPath(p, "skill"))
		}
	}

	// 6. (No commands tree — Codex doesn't have a ~/.codex/commands/
	// directory equivalent to CC. Skip.)

	// 7. Hook script LAST.
	rep.HookScript = HookScriptPathFor(AdapterCodex, u.DataDir)
	if removed, err := u.removeHookScript(rep.HookScript); err != nil {
		errs = append(errs, fmt.Errorf("hook script: %w", err))
	} else if removed {
		rep.Removed = append(rep.Removed, reportPath(rep.HookScript, "hook script"))
	} else {
		rep.Skipped = append(rep.Skipped, reportPath(rep.HookScript, "not present"))
	}

	// 8. --purge-data.
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

func (u *CodexUninstaller) uninstallHooks(home string) (string, int, error) {
	path := filepath.Join(home, ".codex", "hooks.json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path, 0, nil
	}
	existing, err := readJSONFileStrict(path)
	if err != nil {
		return path, 0, fmt.Errorf("refusing to overwrite %s: %w", path, err)
	}
	hookCmd := wrapHookCommand(HookScriptPathFor(AdapterCodex, u.DataDir))
	removed := unregisterTMA1Hooks(existing, codexHookEvents, hookCmd)
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

func (u *CodexUninstaller) uninstallMCP(home string) (string, bool, error) {
	path := filepath.Join(home, ".codex", "config.toml")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path, false, nil
	}
	existing, err := readTOMLFileStrict(path)
	if err != nil {
		return path, false, fmt.Errorf("refusing to overwrite %s: %w", path, err)
	}
	servers, _ := existing["mcp_servers"].(map[string]any)
	if !removeMCPServerEntry(servers, hookOwnerID) {
		return path, false, nil
	}
	existing["mcp_servers"] = servers
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(existing); err != nil {
		return path, false, err
	}
	if err := u.writeFile(path, buf.Bytes(), 0o644); err != nil {
		return path, false, err
	}
	return path, true, nil
}

func (u *CodexUninstaller) uninstallInstructionsFile(path string) (bool, error) {
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

func (u *CodexUninstaller) removeOwnedDir(root string) ([]string, error) {
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil, nil
	}
	removed, err := removeStaleUnder(u, root, map[string]struct{}{}, hookOwnerPrefix)
	if err != nil {
		return removed, err
	}
	// Mirror the CC removeOwnedDir's legacy-name handling. Codex's
	// embed currently doesn't ship a hyphen-less `tma1` skill, but
	// keeping the pass symmetric makes a future rename safe.
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

func (u *CodexUninstaller) removeHookScript(path string) (bool, error) {
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
	removeEmptyDir(filepath.Dir(path))
	return true, nil
}
