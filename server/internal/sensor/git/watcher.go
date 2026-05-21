package git

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
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
)

// projectWatcher watches one project root. It blends fsnotify (file
// modifications) with a periodic git poll (commit / branch movement).
type projectWatcher struct {
	cfg     projectWatcherConfig
	logger  *slog.Logger
	cancel  context.CancelFunc
	ctx     context.Context
	wg      sync.WaitGroup

	// recentEvents tracks the last fsnotify time per path for debouncing.
	mu           sync.Mutex
	recentEvents map[string]time.Time

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
	if err := addRecursive(fsw, w.cfg.Root); err != nil {
		_ = fsw.Close()
		return err
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
	if !w.acceptDebounced(ev.Name) {
		return
	}
	if shouldIgnorePath(ev.Name) {
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
	w.lastGitSHA = readGitHead(w.cfg.Root)
	w.lastGitBranch = readGitBranch(w.cfg.Root)

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
	sha := readGitHead(w.cfg.Root)
	branch := readGitBranch(w.cfg.Root)
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
		msg := readGitSubject(w.cfg.Root, sha)
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
	if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
		return false
	}
	return true
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

// shouldIgnorePath excludes high-noise directories + files: .git/,
// node_modules/, build outputs, IDE state, AND tma1's own bookkeeping
// (.tma1-context.md, .claude/settings.local.json) so the sensor doesn't
// observe its own file_writer or CC's local-state file.
func shouldIgnorePath(p string) bool {
	for _, frag := range []string{
		"/.git/",
		"/node_modules/",
		"/.next/",
		"/.cache/",
		"/dist/",
		"/build/",
		"/target/",
		"/.idea/",
		"/.vscode/",
		"/__pycache__/",
		"/.pytest_cache/",
		"/coverage/",
		"/.claude/",
	} {
		if strings.Contains(p, frag) {
			return true
		}
	}
	// File-extension / name noise.
	base := filepath.Base(p)
	for _, suffix := range []string{".pyc", ".swp", ".swo", ".log", ".DS_Store"} {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	switch base {
	case ".tma1-context.md":
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

// addRecursive walks root and registers every directory with the watcher,
// skipping ignored paths. Errors on individual descents are logged but not
// fatal — partial coverage is better than none.
func addRecursive(fsw *fsnotify.Watcher, root string) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			return nil
		}
		if shouldIgnorePath(path + "/") {
			return filepath.SkipDir
		}
		_ = fsw.Add(path)
		return nil
	})
}

// readGitHead returns the SHA of HEAD or "" on failure.
func readGitHead(root string) string {
	out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// readGitBranch returns the current branch name or "" on detached HEAD/failure.
func readGitBranch(root string) string {
	out, err := exec.Command("git", "-C", root, "rev-parse", "--abbrev-ref", "HEAD").Output()
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
func readGitSubject(root, sha string) string {
	if sha == "" {
		return ""
	}
	out, err := exec.Command("git", "-C", root, "log", "-1", "--pretty=%s", sha).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
