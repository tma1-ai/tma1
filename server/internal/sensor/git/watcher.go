package git

import (
	"context"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

const (
	// gitPollInterval — how often we run `git log -1` to catch commits/
	// branch switches that fsnotify can't observe at the .git/HEAD level
	// reliably across editors and platforms.
	gitPollInterval = 30 * time.Second

	// fsEventDebounce — drop duplicate file events for the same path that
	// fire within this window (editor "atomic save" sequences typically
	// emit Create + Remove + Write within a few ms).
	fsEventDebounce = 500 * time.Millisecond

	maxWatchDirs = 2048

	// maxWatchFiles / maxWatchDirEntries only apply on kqueue (macOS), where
	// fsnotify opens one fd per file in every watched dir — there the fd budget
	// that matters is files, not dirs. On inotify (Linux) and Windows a watch
	// is per-directory, so file counts don't track resource use; both limits
	// are disabled there (see platformWatchLimits) to avoid dropping changes in
	// large repos.
	maxWatchFiles = 4096

	// A dir with this many files is almost certainly an asset/content folder,
	// not hand-edited source; skip it rather than spend an fd per file. Well
	// above a typical source dir (usually <100, occasionally a few hundred for
	// large packages / codegen), so real code is never dropped — only the
	// genuinely huge asset/generated folders (which run to thousands) get cut.
	maxWatchDirEntries = 512
)

// watchLimits bounds a single project walk. dirs caps registered directories
// (meaningful on every backend); files and dirEntries cap file-descriptor cost
// and only bite on kqueue (see platformWatchLimits).
type watchLimits struct {
	dirs       int
	files      int
	dirEntries int
}

// platformWatchLimits returns the walk caps for the current OS. Only kqueue
// charges a descriptor per watched file, so the file-count caps are disabled
// off macOS — there a watch is per-directory and file counts would needlessly
// truncate coverage of large repos.
func platformWatchLimits() watchLimits {
	l := watchLimits{dirs: maxWatchDirs, files: maxWatchFiles, dirEntries: maxWatchDirEntries}
	if runtime.GOOS != "darwin" {
		l.files = math.MaxInt
		l.dirEntries = math.MaxInt
	}
	return l
}

// projectWatcher watches one project root. It blends fsnotify (file
// modifications) with a periodic git poll (commit / branch movement).
type projectWatcher struct {
	cfg    projectWatcherConfig
	logger *slog.Logger
	cancel context.CancelFunc
	ctx    context.Context
	wg     sync.WaitGroup

	// recentEvents tracks the last fsnotify time per path for debouncing.
	mu           sync.Mutex
	recentEvents map[string]time.Time

	// gitignore captures patterns from <root>/.gitignore so build
	// artifacts a project already declared "not interesting" don't
	// drown the external_changes signal. Nil when no .gitignore.
	gitignore *gitignoreMatcher

	// lastGitSHA / lastGitBranch let the poller emit one event per change.
	lastGitSHA    string
	lastGitBranch string
}

type projectWatcherConfig struct {
	Root       string
	Project    string
	Host       string
	Writer     EventWriter
	Attributor Attributor
	Logger     *slog.Logger
}

func newProjectWatcher(parent context.Context, cfg projectWatcherConfig) *projectWatcher {
	ctx, cancel := context.WithCancel(parent)
	return &projectWatcher{
		cfg:          cfg,
		logger:       cfg.Logger,
		ctx:          ctx,
		cancel:       cancel,
		recentEvents: make(map[string]time.Time),
		gitignore:    loadGitignore(cfg.Root),
	}
}

func (w *projectWatcher) start() error {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	// Best-effort recursive watch by walking once at start. Subdirectories
	// created later don't auto-attach — for Phase 1.2 this is acceptable;
	// most projects have stable directory layouts.
	limits := platformWatchLimits()
	added, stopped, err := addRecursive(fsw, w.cfg.Root, limits, w.dirShouldIgnore)
	if err != nil {
		_ = fsw.Close()
		return err
	}
	if stopped {
		w.logger.Warn("git watcher: stopped registering watches early (dir/file cap or fd limit); deeper subdirs unwatched",
			"root", w.cfg.Root, "dir_cap", limits.dirs, "file_cap", limits.files, "watched_dirs", added)
	}

	w.wg.Add(2)
	go w.runFsLoop(fsw)
	go w.runGitPoll()
	return nil
}

func (w *projectWatcher) stop() {
	w.cancel()
	w.wg.Wait()
}

func (w *projectWatcher) runFsLoop(fsw *fsnotify.Watcher) {
	defer w.wg.Done()
	defer fsw.Close()

	for {
		select {
		case <-w.ctx.Done():
			return
		case ev, ok := <-fsw.Events:
			if !ok {
				return
			}
			w.handleFsEvent(ev)
		case err, ok := <-fsw.Errors:
			if !ok {
				return
			}
			w.logger.Debug("fsnotify error", "err", err)
		}
	}
}

func (w *projectWatcher) handleFsEvent(ev fsnotify.Event) {
	if !shouldRecordFsEvent(ev) {
		return
	}
	if w.shouldIgnorePath(ev.Name) {
		return
	}
	if !w.acceptDebounced(ev.Name) {
		return
	}

	change := Change{
		Timestamp:  time.Now().UTC(),
		Project:    w.cfg.Project,
		ChangeType: classifyFsOp(ev.Op),
		FilePath:   ev.Name,
		Host:       w.cfg.Host,
	}
	change.Attribution = AttributionUnknown
	if w.cfg.Attributor != nil {
		change.Attribution = w.cfg.Attributor.Classify(w.ctx, ev.Name, change.Timestamp)
	}

	if err := w.cfg.Writer.Write(w.ctx, change); err != nil {
		w.logger.Debug("external change write", "err", err, "path", ev.Name)
	}
}

func (w *projectWatcher) acceptDebounced(path string) bool {
	now := time.Now()
	w.mu.Lock()
	defer w.mu.Unlock()
	if last, ok := w.recentEvents[path]; ok && now.Sub(last) < fsEventDebounce {
		return false
	}
	w.recentEvents[path] = now
	// Opportunistic GC: drop entries older than 1 min.
	if len(w.recentEvents) > 256 {
		cutoff := now.Add(-time.Minute)
		for k, v := range w.recentEvents {
			if v.Before(cutoff) {
				delete(w.recentEvents, k)
			}
		}
	}
	return true
}

func (w *projectWatcher) runGitPoll() {
	defer w.wg.Done()

	// Capture baseline so we don't emit a "git_commit" on startup for the
	// existing HEAD.
	w.lastGitSHA = readGitHead(w.ctx, w.cfg.Root)
	w.lastGitBranch = readGitBranch(w.ctx, w.cfg.Root)

	t := time.NewTicker(gitPollInterval)
	defer t.Stop()
	for {
		select {
		case <-w.ctx.Done():
			return
		case <-t.C:
			w.pollGit()
		}
	}
}

func (w *projectWatcher) pollGit() {
	sha := readGitHead(w.ctx, w.cfg.Root)
	branch := readGitBranch(w.ctx, w.cfg.Root)
	if sha == "" {
		return
	}

	// Branch switch (different branch, ignoring blank baseline).
	if w.lastGitBranch != "" && branch != "" && branch != w.lastGitBranch {
		w.emit(Change{
			Timestamp:   time.Now().UTC(),
			Project:     w.cfg.Project,
			ChangeType:  ChangeTypeGitBranchSwitch,
			GitSHA:      sha,
			GitMessage:  "branch: " + w.lastGitBranch + " → " + branch,
			Attribution: AttributionHuman, // git commands ≈ human by default
			Host:        w.cfg.Host,
		})
		w.lastGitBranch = branch
	}

	// New commit on current branch.
	if sha != w.lastGitSHA && w.lastGitSHA != "" {
		msg := readGitSubject(w.ctx, w.cfg.Root, sha)
		w.emit(Change{
			Timestamp:   time.Now().UTC(),
			Project:     w.cfg.Project,
			ChangeType:  ChangeTypeGitCommit,
			GitSHA:      sha,
			GitMessage:  msg,
			Attribution: AttributionHuman,
			Host:        w.cfg.Host,
		})
	}
	w.lastGitSHA = sha
}

func (w *projectWatcher) emit(c Change) {
	if err := w.cfg.Writer.Write(w.ctx, c); err != nil {
		w.logger.Debug("external change write", "err", err, "type", c.ChangeType)
	}
}

// shouldRecordFsEvent returns true for Write/Create/Remove/Rename. Chmod is
// dropped — it generates a lot of noise on macOS (editors touch perms).
func shouldRecordFsEvent(ev fsnotify.Event) bool {
	return ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) != 0
}

func classifyFsOp(op fsnotify.Op) string {
	switch {
	case op&fsnotify.Create != 0:
		return ChangeTypeFileAdded
	case op&fsnotify.Remove != 0:
		return ChangeTypeFileDeleted
	case op&fsnotify.Rename != 0:
		return ChangeTypeFileDeleted // rename's old name is reported here
	default:
		return ChangeTypeFileModified
	}
}

// staticIgnoreFragments are paths the sensor never wants to see across
// every project: VCS internals, dependency installs, build outputs, IDE
// state, and tma1's own bookkeeping. Per-project nuance comes from the
// gitignore matcher (see (*projectWatcher).shouldIgnorePath).
var staticIgnoreFragments = []string{
	"/.git/",
	"/node_modules/",
	"/.next/",
	"/.cache/",
	"/dist/",
	"/build/",
	"/target/",
	"/bin/",              // Go build outputs (server/bin, tooling). Dropped here
	"/out/",              // Java / generic build output dir
	"/vendor/",           // Go / PHP / Ruby vendored deps
	"/.venv/",            // Python venv
	"/venv/",             // Python venv (alt)
	"/.gocache/",         // Go build cache (projects that set GOCACHE in-tree)
	"/.gradle/",          // Gradle build state
	"/.turbo/",           // Turborepo cache
	"/.svelte-kit/",      // SvelteKit generated
	"/.nuxt/",            // Nuxt generated
	"/.parcel-cache/",    // Parcel cache
	"/.angular/",         // Angular cache
	"/.vitepress/cache/", // VitePress dev cache
	"/.vitepress/dist/",  // VitePress build output
	"/.mypy_cache/",      // mypy cache
	"/.ruff_cache/",      // ruff cache
	"/.terraform/",       // Terraform provider/plugin cache
	"/.tma1/",            // tma1's own data dir (avoids self-noise loop)
	"/.idea/",
	"/.vscode/",
	"/__pycache__/",
	"/.pytest_cache/",
	"/coverage/",
	"/.claude/",
}

var staticIgnoreSuffixes = []string{
	".pyc", ".swp", ".swo", ".log", ".DS_Store",
}

// systemRootPrefixes are absolute, filesystem-root OS trees we never walk.
// Matched by PREFIX (not substring like staticIgnoreFragments) so a project
// that merely contains — or is named — "Library" / "System" / "Applications"
// is still watched; only the real OS trees mounted at "/" are blocked.
//
// Defence in depth behind resolveProjectRoot, which already refuses any root
// without a .git / project marker. It exists because a single misresolved
// "/" root walking /Applications was enough to exhaust file descriptors.
// User-level trees (~/Library) are deliberately absent: they carry no marker
// so P0 already declines them, and the fd weight lives in the root-level
// /Applications anyway. /Volumes is absent too — projects legitimately live
// on external disks.
var systemRootPrefixes = []string{
	"/Applications/",
	"/Library/",
	"/System/",
}

// staticShouldIgnorePath is the project-agnostic ignore check. Tests
// call this directly; runtime callers go through
// (*projectWatcher).shouldIgnorePath which also consults the loaded
// .gitignore.
//
// Windows note: fsnotify hands us OS-native paths (backslashes on
// Windows), but staticIgnoreFragments are written POSIX-style so the
// list reads naturally and matches the .gitignore convention. We
// normalize backslashes to forward slashes once at the top so
// substring checks work regardless of platform. We can't use
// filepath.ToSlash here because its behaviour is OS-dependent (no-op
// on Unix) — but we want a Windows path passed to us on a Unix host
// (e.g. cross-platform tests) to still match the fragments.
func staticShouldIgnorePath(p string) bool {
	normalized := strings.ReplaceAll(p, "\\", "/")
	for _, frag := range staticIgnoreFragments {
		if strings.Contains(normalized, frag) {
			return true
		}
	}
	for _, prefix := range systemRootPrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	base := filepath.Base(p)
	for _, suffix := range staticIgnoreSuffixes {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	if base == ".tma1-context.md" {
		return true
	}
	// Atomic-write tempfile pattern that many editors / git itself produce:
	// "<name>.tmp.<pid>.<random>". Drops the noisy intermediates without
	// hiding the resulting committed file modification.
	if strings.Contains(base, ".tmp.") {
		return true
	}
	return false
}

// shouldIgnorePath combines the static, cross-project ignore list with
// per-project rules picked up from <root>/.gitignore. Any project that
// added "bin/" or "logs/" or "build/output.json" to its .gitignore was
// explicitly saying "this isn't interesting" -- the fs sensor honours
// that just like rg / VSCode / ag do, instead of trying to maintain a
// universal ignore list ourselves.
func (w *projectWatcher) shouldIgnorePath(p string) bool {
	if staticShouldIgnorePath(p) {
		return true
	}
	if w.gitignore != nil && w.gitignore.matches(p, w.cfg.Root) {
		return true
	}
	return false
}

// dirShouldIgnore prunes a directory at walk time. Unlike shouldIgnorePath,
// which only drops a change record after the fact, pruning here stops the dir's
// files from ever costing a descriptor — the whole point of honouring
// .gitignore against build/cache dirs. The trailing slash is what the static
// fragment list matches directories on.
func (w *projectWatcher) dirShouldIgnore(dir string) bool {
	if staticShouldIgnorePath(dir + "/") {
		return true
	}
	if w.gitignore != nil && w.gitignore.matches(dir, w.cfg.Root) {
		return true
	}
	return false
}

// addRecursive walks root and registers directories with the watcher. It
// returns how many dirs were added and whether the walk stopped early — on the
// dir cap, the file cap, or the first fsnotify.Add failure. Walk errors are
// non-fatal (partial coverage beats none).
//
// The root directory itself is never subject to the ignore predicate: it is
// the thing we were asked to watch, and a project whose .gitignore lists an
// artifact sharing the repo's own name (e.g. `/codora` in a repo named codora)
// would otherwise prune the root and start with zero watches, silently missing
// every change.
//
// Stopping on the first Add failure matters: once Add returns EMFILE/ENOSPC the
// fd table is full, so every further Add fails too — continuing just burns IO
// on a doomed walk.
func addRecursive(fsw *fsnotify.Watcher, root string, limits watchLimits, ignore func(dir string) bool) (int, bool, error) {
	dirs := 0
	files := 0
	stopped := false
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			return nil
		}
		if path != root && ignore(path) {
			return filepath.SkipDir
		}
		if dirs >= limits.dirs || files >= limits.files {
			stopped = true
			return filepath.SkipAll
		}

		fileCount := countFiles(path)
		if fileCount > limits.dirEntries {
			return nil // skip the dir, but keep descending into its subdirs
		}

		if fsw.Add(path) != nil {
			stopped = true // fd/watch table full — every further Add fails too
			return filepath.SkipAll
		}
		dirs++
		files += fileCount
		return nil
	})
	return dirs, stopped, err
}

func countFiles(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			n++
		}
	}
	return n
}

// readGitHead returns the SHA of HEAD or "" on failure. The context lets a
// shutdown actually unblock a hung `git rev-parse` (e.g. against an NFS root
// or behind a stale index.lock) — without it, w.wg.Wait() in stop() would
// deadlock until git eventually returned.
func readGitHead(ctx context.Context, root string) string {
	out, err := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// readGitBranch returns the current branch name or "" on detached HEAD/failure.
func readGitBranch(ctx context.Context, root string) string {
	out, err := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	b := strings.TrimSpace(string(out))
	if b == "HEAD" {
		return "" // detached
	}
	return b
}

// readGitSubject returns the commit subject (first line of message) for sha.
func readGitSubject(ctx context.Context, root, sha string) string {
	if sha == "" {
		return ""
	}
	out, err := exec.CommandContext(ctx, "git", "-C", root, "log", "-1", "--pretty=%s", sha).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
