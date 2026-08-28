// Package hooks (status.go) reports whether the skill / command files
// an adapter installed on disk still match the ones embedded in the
// running binary.
//
// Upgrading the binary does not rewrite them: `tma1-server install`
// does. So a user who upgrades keeps yesterday's SKILL.md while the
// server ships today's MCP tools. This file finds that drift so the
// server can say so at startup — it never writes, because rewriting
// files under ~/.claude without being asked is not ours to do.
package hooks

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
)

// AssetDrift reports files that differ from the embedded copy, or are
// missing, for one adapter.
type AssetDrift struct {
	Adapter    Adapter
	StalePaths []string
	FixCommand string
}

// CheckInstalledAssets returns drift for every adapter that is
// currently installed. Adapters the user never installed are skipped —
// there is nothing to be stale about.
func CheckInstalledAssets() ([]AssetDrift, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	type assetTree struct {
		fs   embed.FS
		root string
		dest string
	}
	var (
		drifts []AssetDrift
		firstE error
	)
	add := func(adapter Adapter, fix string, trees []assetTree) {
		var stale []string
		for _, tree := range trees {
			found, err := diffEmbeddedTree(tree.fs, tree.root, tree.dest)
			if err != nil && firstE == nil {
				firstE = err
			}
			stale = append(stale, found...)
		}
		if len(stale) > 0 {
			drifts = append(drifts, AssetDrift{Adapter: adapter, StalePaths: stale, FixCommand: fix})
		}
	}

	if claudeCodeInstalled(home) {
		add(AdapterClaudeCode, "tma1 install --adapter claude-code", []assetTree{
			{embeddedSkills, "skills", filepath.Join(home, ".claude", "skills")},
			{embeddedCommands, "commands", filepath.Join(home, ".claude", "commands")},
		})
	}
	if codexInstalled(home) {
		add(AdapterCodex, "tma1 install --adapter codex", []assetTree{
			{embeddedCodexSkills, "codex-skills", filepath.Join(home, ".agents", "skills")},
		})
	}
	return drifts, firstE
}

// claudeCodeInstalled keys off the hook registration in settings.json
// rather than the presence of ~/.claude/skills/tma1. A deleted skill
// tree is exactly the case that most needs reporting, and a directory
// check would read it as "never installed".
func claudeCodeInstalled(home string) bool {
	settings, err := readJSONFileStrict(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		return false
	}
	hooks, _ := settings["hooks"].(map[string]any)
	for _, v := range hooks {
		list, _ := v.([]any)
		for _, entry := range list {
			if m, ok := entry.(map[string]any); ok {
				if id, _ := m["id"].(string); id == hookOwnerID {
					return true
				}
			}
		}
	}
	return false
}

// codexInstalled keys off the hook config install writes and uninstall
// removes.
func codexInstalled(home string) bool {
	cfg, err := readJSONFileStrict(filepath.Join(home, ".codex", "hooks.json"))
	if err != nil || len(cfg) == 0 {
		return false
	}
	hooks, _ := cfg["hooks"].(map[string]any)
	for _, v := range hooks {
		list, _ := v.([]any)
		for _, entry := range list {
			if m, ok := entry.(map[string]any); ok {
				if id, _ := m["id"].(string); id == hookOwnerID {
					return true
				}
			}
		}
	}
	return false
}

// diffEmbeddedTree is the read-only counterpart to syncEmbeddedTree.
func diffEmbeddedTree(src embed.FS, embedRoot, destRoot string) ([]string, error) {
	var stale []string
	err := fs.WalkDir(src, embedRoot, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(embedRoot, p)
		if err != nil {
			return err
		}
		want, err := src.ReadFile(p)
		if err != nil {
			return err
		}
		target := filepath.Join(destRoot, rel)
		got, err := os.ReadFile(target)
		if err != nil || string(got) != string(want) {
			stale = append(stale, target)
		}
		return nil
	})
	return stale, err
}
