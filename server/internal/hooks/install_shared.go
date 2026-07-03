// Package hooks (install_shared.go) hosts the adapter-installer helpers
// that are shared between the Claude Code adapter (install_cc.go) and the
// Codex adapter (install_codex.go). Code that's specific to one agent
// (e.g. Claude Code's settings.json hook registration) stays in the
// adapter-specific file.
//
// The shared functions operate against the installSink interface so they
// stay DryRun-aware and route through whichever installer's
// writeFile / mkdirAll implementation. *ClaudeCodeInstaller and
// *CodexInstaller both satisfy installSink.
package hooks

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// installSink is the surface a shared install helper needs. Both
// adapter installers implement it.
type installSink interface {
	writeFile(path string, data []byte, perm os.FileMode) error
	mkdirAll(path string, perm os.FileMode) error
	dryRun() bool
	getLogger() *slog.Logger
}

const (
	// hookOwnerID is the canonical `id` value TMA1 stamps onto every
	// hook entry and MCP server it registers. The install path uses it
	// to recognise its own entries on re-install; uninstall uses it to
	// scope deletes so user-added neighbour entries survive.
	hookOwnerID = "tma1"
	// hookOwnerPrefix scopes embedded skill/command file deletes during
	// stale-sweep so we only remove things we wrote. Skills not under
	// this prefix are assumed to be user-authored and left alone.
	hookOwnerPrefix = "tma1-"
)

// wrapHookCommand returns the shell invocation used to run the TMA1
// hook script. Windows wraps via PowerShell to dodge ExecutionPolicy;
// POSIX runs the .sh directly. Shared between adapters because both
// CC settings.json and Codex hooks.json store the same string shape.
func wrapHookCommand(scriptPath string) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf(`powershell -ExecutionPolicy Bypass -File "%s"`, scriptPath)
	}
	return scriptPath
}

// entryCommand returns the first hook's command string from an entry,
// or "" if the entry has no hooks or a malformed first hook.
func entryCommand(entry map[string]any) string {
	hs, _ := entry["hooks"].([]any)
	if len(hs) == 0 {
		return ""
	}
	h, _ := hs[0].(map[string]any)
	if h == nil {
		return ""
	}
	cmd, _ := h["command"].(string)
	return cmd
}

// entryEqual reports whether two hook entries match on the fields TMA1
// manages. User-extended fields are preserved by replacing entries
// wholesale only when the command differs.
//
// The matcher comparison is presence-aware: `"matcher": ""` and no
// matcher key at all are NOT equal. Codex hashes hook definitions into
// trust identities, and for matcherless events an empty-string matcher
// and an absent matcher can hash differently in tools that re-derive
// that hash — so when the desired shape drops the key, an existing
// entry that still carries it must be rewritten (one-time migration),
// after which re-installs are no-ops again.
func entryEqual(a, b any) bool {
	am, _ := a.(map[string]any)
	bm, _ := b.(map[string]any)
	if am == nil || bm == nil {
		return false
	}
	if id1, _ := am["id"].(string); id1 != bm["id"] {
		return false
	}
	av, aok := am["matcher"]
	bv, bok := bm["matcher"]
	if aok != bok {
		return false
	}
	if aok {
		a1, a1ok := av.(string)
		b1, b1ok := bv.(string)
		if !a1ok || !b1ok || a1 != b1 {
			return false
		}
	}
	return entryCommand(am) == entryCommand(bm)
}

// findEquivalentEntry returns the index of an existing TMA1-equivalent
// entry, or -1. Per-entry equivalence is delegated to
// matchesTMA1HookEntry so install and uninstall paths can't disagree on
// what "ours" means.
func findEquivalentEntry(list []any, command, tmaID string) int {
	for i, item := range list {
		if matchesTMA1HookEntry(item, command, tmaID) {
			return i
		}
	}
	return -1
}

// matchesTMA1HookEntry reports whether a single hook array entry is
// owned by TMA1. An entry qualifies if EITHER its `id` field equals
// tmaID OR its first hook's command (after ~/ expansion) resolves to
// the same path as the requested command. The command-path check is
// what lets us recognise legacy entries that pre-date the `id` field —
// without it, uninstall on an old install would leave dangling hook
// registrations.
func matchesTMA1HookEntry(entry any, command, tmaID string) bool {
	m, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	if id, _ := m["id"].(string); id == tmaID {
		return true
	}
	first := entryCommand(m)
	if first == "" {
		return false
	}
	return expandHome(first) == expandHome(command)
}

// registerTMA1HookEntries ensures `command` is registered for every
// event in `eventNames`. Returns true if the settings map mutated. The
// hook-entry shape is identical between Claude Code's settings.json and
// Codex's hooks.json, so this single helper backs both adapters; only
// the event list differs.
//
// Events listed in `matcherlessEvents` get their entry written WITHOUT
// a matcher key (nil means "none"). Agents that don't support matchers
// on an event treat "" and absent identically at dispatch time, but the
// raw definition can feed trust-hash computations (Codex), where the
// spurious key changes the hash — see codexMatcherlessEvents.
//
// Two-pass matching avoids duplicate entries:
//  1. Look for an entry whose hooks[].command resolves to the same script
//     (after ~/ expansion). Catches old entries installed before we added
//     the `id` field, or entries the user hand-wrote.
//  2. Fall back to looking for an entry with `id="tma1"`.
//
// The matched entry is rewritten in place (canonical id + absolute
// path). Other entries are left alone — that's important: user-added
// hooks for the same event must keep working.
func registerTMA1HookEntries(settings map[string]any, eventNames []string, command string, matcherlessEvents map[string]bool) bool {
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}

	mutated := false
	for _, event := range eventNames {
		list, _ := hooks[event].([]any)
		idx := findEquivalentEntry(list, command, hookOwnerID)

		entry := map[string]any{
			"id": hookOwnerID,
			"hooks": []any{
				map[string]any{
					"type":    "command",
					"command": command,
				},
			},
		}
		if !matcherlessEvents[event] {
			entry["matcher"] = ""
		}

		switch {
		case idx >= 0:
			if !entryEqual(list[idx], entry) {
				list[idx] = entry
				hooks[event] = list
				mutated = true
			}
		default:
			list = append(list, entry)
			hooks[event] = list
			mutated = true
		}
	}

	if mutated {
		settings["hooks"] = hooks
	}
	return mutated
}

// writeFileAtomic writes data to path via a temp-file + rename so a
// crash or signal between truncate and full write can't leave the
// target half-written. Critical for files that hold user-critical
// state (CC's ~/.claude.json carries OAuth tokens; ~/.claude/settings.json
// carries hook registrations; ~/.codex/config.toml carries MCP entries
// + tool decisions).
//
// The temp file lives in the same directory as the target so the
// rename is guaranteed to be on the same filesystem (atomic).
//
// Symlinks: resolve first via EvalSymlinks so we rename onto the
// underlying file, not onto the symlink itself. POSIX rename(2) over
// a symlink unlinks the symlink and replaces it with the new regular
// file, which silently breaks layouts like CLAUDE.md → AGENTS.md
// (this repo's own layout). Writing through the resolved target keeps
// the symlink intact and both names continue to track the same content.
// EvalSymlinks failures (target absent, broken/circular link) fall
// through to the original path — same behaviour as before.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tma1-write-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// readJSONFileStrict loads a JSON object map. Returns:
//   - (empty map, nil) when the file does not exist
//   - (parsed map, nil) when parse succeeds and the root is an object
//   - (nil, err) when the file exists but does not contain a JSON object
//
// Strict on purpose: callers like installSettings / installMCPServer
// write back to files that hold user-critical state. Silently treating
// a parse error as "empty" would corrupt those files.
func readJSONFileStrict(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if m == nil {
		return nil, fmt.Errorf("parse %s: root is not a JSON object", path)
	}
	return m, nil
}

// tma1BinaryPath returns the absolute path to the tma1-server binary
// CC / Codex should spawn for `mcp-serve`. Prefer os.Executable()
// (the binary we're running right now is what the user just invoked),
// then fall back to the standard install location under DataDir/bin.
func tma1BinaryPath(dataDir string) (string, error) {
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			return resolved, nil
		}
		return exe, nil
	}
	name := "tma1-server"
	if runtime.GOOS == "windows" {
		name = "tma1-server.exe"
	}
	if dataDir == "" {
		return "", fmt.Errorf("cannot determine tma1-server path: no executable, no DataDir")
	}
	return filepath.Join(dataDir, "bin", name), nil
}

// expandHome resolves a leading ~/ to $HOME so two paths pointing at
// the same script are recognised as equal regardless of how they were
// spelled. Stays here so both adapters reuse the same comparison.
func expandHome(p string) string {
	if !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[2:])
}

// chooseInstructionsFile returns the project-level instructions file
// the installer should target. The adapter-specific preference is
// passed in (CC: CLAUDE.md, Codex: AGENTS.md) — each agent reads its
// own primary file, so writing only to the "shared" file would leave
// one of them blind.
//
// Resolution:
//  1. If `preferred` exists in the project, use it.
//  2. CC-only fallback: if CLAUDE.md is missing but AGENTS.md exists,
//     use AGENTS.md. Recent CC versions read AGENTS.md too, so this
//     avoids fragmenting instructions across two files.
//  3. Otherwise create the preferred file.
//
// Codex deliberately has NO fallback: it only scans AGENTS.md, so a
// Claude-only project (CLAUDE.md present, AGENTS.md absent) must get
// a fresh AGENTS.md created — writing to CLAUDE.md would leave the
// installed block invisible to Codex.
//
// A project with both files installed gets the block in each
// adapter's primary independently — that's intentional, the two
// adapters target different agents.
func chooseInstructionsFile(projectDir, preferred string) string {
	agentsMD := filepath.Join(projectDir, "AGENTS.md")

	preferredPath := filepath.Join(projectDir, preferred)
	if fileExists(preferredPath) {
		return preferredPath
	}

	// CC-only convergence: CC reads AGENTS.md in addition to CLAUDE.md,
	// so a CC install on an AGENTS.md-only project should target the
	// existing file rather than create a fragmented second one. The
	// reverse direction (Codex on CLAUDE.md-only) does NOT fall back —
	// Codex only scans AGENTS.md, so we must create it.
	if preferred == "CLAUDE.md" && fileExists(agentsMD) {
		return agentsMD
	}
	return preferredPath
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// instructionsBlock renders the canonical TMA1 instructions block
// inserted between start/end markers in AGENTS.md / CLAUDE.md.
func instructionsBlock(start, end string) []byte {
	return []byte(start + `
## TMA1 Context Layer

TMA1 thickens the Observe step in your reasoning loop. At the start of each
turn it injects a <tma1-context> block summarising the current session
(tool history, tokens, current focus, recent files, build state, anomalies).
Use that block when deciding what to do next.

Example shape (values illustrative):

` + "```" + `
<tma1-context>
project: tma1
session: a1b2c3d4
duration: 12 min
tool_calls: 47
tokens: in=84210 out=312045
current_focus: .../internal/perception/peer.go
tools: Bash×18, Edit×12, Read×9, TaskUpdate×4
recent_files: .../perception/peer.go, .../mcp/tools.go, .../hooks/install_cc.go
build: make (running)
build_last_error (6m ago, may have recovered): exit code 1 ...
external_human_changes: 3
external_files: .../path/to/file.go
anomalies:
  - [MEDIUM] human_modified_during_session — Re-read the listed files before assuming your in-memory copy is current.
</tma1-context>
` + "```" + `

Fields are best-effort — most lines only appear when relevant
(` + "`anomalies`" + ` / ` + "`build_last_error`" + ` / ` + "`external_*`" + ` only render when there's
something worth flagging). ` + "`current_focus`" + ` reflects your most recent
Edit/Write target.

**You should:**
- Read the <tma1-context> block (when present) before reasoning about the next action
- Trust ` + "`external_files`" + ` over your in-memory snapshot — re-read those before editing
- Call the MCP tool ` + "`get_session_state`" + ` if you need a fuller view of your prior tool calls
- Call ` + "`get_context_bundle`" + ` after compaction or when context feels stale
- Wrap persistent processes (dev servers, watchers like ` + "`npm run dev`" + `, ` + "`cargo watch`" + `) with ` + "`tma1 build --watch -- <cmd>`" + ` so output persists past your session; the next agent (or you, after compaction) reads it via ` + "`get_build_status`" + `. One-shot commands don't need wrapping — use Bash directly.
` + end)
}

// containsLine reports whether data has a line equal (post-trim) to
// the given line. Used by installGitignore to dedupe entries.
func containsLine(data []byte, line string) bool {
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) == line {
			return true
		}
	}
	return false
}

// indexOfStandaloneLine returns the byte offset of the first occurrence
// of marker that lives on its own line (i.e. the trimmed contents of
// that line are exactly the marker). Returns -1 if no such line exists.
//
// Why this exists: a plain strings.Index match would also fire on marker
// text that appears inside prose, comments, or code blocks — e.g. an
// AGENTS.md sentence saying "uninstall removes the <!-- tma1:start -->
// block" would shadow the real marker and cause installInstructions to
// replace from the prose match to the real end marker, wiping every
// line in between. That's exactly the failure that ate 170 lines of
// this repo's AGENTS.md on 2026-05-22. Match only standalone markers
// from now on.
func indexOfStandaloneLine(data []byte, marker string) int {
	s := string(data)
	search := 0
	for search < len(s) {
		idx := strings.Index(s[search:], marker)
		if idx < 0 {
			return -1
		}
		idx += search
		lineStart := idx
		for lineStart > 0 && s[lineStart-1] != '\n' {
			lineStart--
		}
		lineEnd := idx + len(marker)
		for lineEnd < len(s) && s[lineEnd] != '\n' {
			lineEnd++
		}
		if strings.TrimSpace(s[lineStart:lineEnd]) == marker {
			return idx
		}
		search = idx + len(marker)
	}
	return -1
}

// joinErrors collapses a slice of errors into one for return from
// Install(). Partial-failure paths still surface; a single error
// is returned verbatim.
func joinErrors(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0]
	}
	parts := make([]string, len(errs))
	for i, e := range errs {
		parts[i] = e.Error()
	}
	return fmt.Errorf("multiple install errors: %s", strings.Join(parts, "; "))
}

// syncEmbeddedTree walks an embed.FS rooted at embedRoot and mirrors
// every file under destRoot. Files whose on-disk content already
// matches are left alone (no mtime churn).
//
// When ownerPrefix is non-empty, the stale-sweep only considers
// entries whose first path component (relative to destRoot) starts
// with that prefix. This is the safe default for multi-tenant
// directories like ~/.claude/{skills,commands}/ and ~/.agents/skills/
// where the user has their own skills + commands sitting alongside
// ours -- we must never delete those. Empty ownerPrefix means "sweep
// everything not in the embed", which is only safe when destRoot is
// a tma1-owned subdirectory.
//
// DryRun-aware via the installSink i.writeFile / i.mkdirAll sinks.
func syncEmbeddedTree(i installSink, src embed.FS, embedRoot, destRoot, ownerPrefix string) ([]string, error) {
	if err := i.mkdirAll(destRoot, 0o755); err != nil {
		return nil, err
	}
	var changed []string
	wanted := map[string]struct{}{}
	walkErr := fs.WalkDir(src, embedRoot, func(p string, d fs.DirEntry, walkErr error) error {
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
		wanted[target] = struct{}{}
		if existing, err := os.ReadFile(target); err == nil && string(existing) == string(want) {
			return nil
		}
		if err := i.mkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := i.writeFile(target, want, 0o644); err != nil {
			return err
		}
		changed = append(changed, target)
		return nil
	})
	if walkErr != nil {
		return changed, walkErr
	}
	staleRemoved, err := removeStaleUnder(i, destRoot, wanted, ownerPrefix)
	if err != nil {
		return changed, fmt.Errorf("remove stale under %s: %w", destRoot, err)
	}
	changed = append(changed, staleRemoved...)
	return changed, nil
}

// removeStaleUnder walks root and removes every file whose path isn't
// in keep. When ownerPrefix is non-empty, only entries whose first
// path component (relative to root) starts with that prefix are
// candidates for removal -- safe default for shared dirs.
//
// Empty directories left behind are pruned bottom-up, but only if
// they themselves are under an ownerPrefix-matching path.
//
// DryRun-aware: in dry-run mode we log + return the would-be paths
// without touching disk.
func removeStaleUnder(i installSink, root string, keep map[string]struct{}, ownerPrefix string) ([]string, error) {
	var removed []string
	var dirs []string
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return removed, nil
	}
	owned := func(path string) bool {
		if ownerPrefix == "" {
			return true
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return false
		}
		first := rel
		if idx := strings.IndexAny(rel, "/\\"); idx >= 0 {
			first = rel[:idx]
		}
		return strings.HasPrefix(first, ownerPrefix)
	}
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		if d.IsDir() {
			if owned(path) {
				dirs = append(dirs, path)
			}
			return nil
		}
		if _, ok := keep[path]; ok {
			return nil
		}
		if !owned(path) {
			return nil // not ours, leave alone
		}
		if i.dryRun() {
			if l := i.getLogger(); l != nil {
				l.Info("[dry-run] would remove stale", "path", path)
			}
			removed = append(removed, path+" (stale, would remove)")
			return nil
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		removed = append(removed, path+" (stale, removed)")
		return nil
	})
	if walkErr != nil {
		return removed, walkErr
	}
	for j := len(dirs) - 1; j >= 0; j-- {
		if i.dryRun() {
			continue
		}
		_ = os.Remove(dirs[j]) // fails noisily only when non-empty; that's the signal
	}
	return removed, nil
}

// installInstructions appends a <!-- tma1:start --> block to the
// agent's primary instructions file (CLAUDE.md for CC, AGENTS.md
// for Codex). Idempotent via marker matching: a re-run replaces the
// existing block in place. When both adapters are installed on the
// same project, each writes to its own primary so each agent sees
// the block — that's intentional, not a duplication bug.
func installInstructions(i installSink, projectDir, preferredFile string) (string, bool, error) {
	target := chooseInstructionsFile(projectDir, preferredFile)
	existing, _ := os.ReadFile(target)

	const startMarker = "<!-- tma1:start -->"
	const endMarker = "<!-- tma1:end -->"

	desired := instructionsBlock(startMarker, endMarker)

	var newContent []byte
	if startIdx := indexOfStandaloneLine(existing, startMarker); startIdx >= 0 {
		endIdx := indexOfStandaloneLine(existing, endMarker)
		if endIdx < 0 {
			endIdx = len(existing)
		} else {
			endIdx += len(endMarker)
		}
		newContent = append([]byte{}, existing[:startIdx]...)
		newContent = append(newContent, desired...)
		newContent = append(newContent, existing[endIdx:]...)
	} else {
		newContent = append([]byte{}, existing...)
		if len(newContent) > 0 && newContent[len(newContent)-1] != '\n' {
			newContent = append(newContent, '\n')
		}
		if len(newContent) > 0 {
			newContent = append(newContent, '\n')
		}
		newContent = append(newContent, desired...)
		newContent = append(newContent, '\n')
	}

	if string(newContent) == string(existing) {
		return target, false, nil
	}
	if err := i.writeFile(target, newContent, 0o644); err != nil {
		return target, false, err
	}
	return target, true, nil
}

// installGitignore appends ".tma1-context.md" to projectDir/.gitignore
// if missing. Tolerates an absent .gitignore (creates it) and an
// absent project (no-op).
func installGitignore(i installSink, projectDir string) (string, bool, error) {
	path := filepath.Join(projectDir, ".gitignore")
	existing, _ := os.ReadFile(path)
	if containsLine(existing, ".tma1-context.md") {
		return path, false, nil
	}
	suffix := ".tma1-context.md\n"
	updated := existing
	if len(updated) > 0 && updated[len(updated)-1] != '\n' {
		updated = append(updated, '\n')
	}
	updated = append(updated, []byte(suffix)...)
	if err := i.writeFile(path, updated, 0o644); err != nil {
		return path, false, err
	}
	return path, true, nil
}
