package git

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/tma1-ai/tma1/server/internal/pathutil"
)

// Sensor is the long-lived owner of per-project watchers. Call Observe(cwd)
// from anywhere (e.g. the hook handler) and the sensor lazily spins up a
// watcher for that project's root the first time it sees it. Idempotent.
//
// The sensor is closed by canceling the context passed to Start.
type Sensor struct {
	writer EventWriter
	attr   Attributor
	logger *slog.Logger
	host   string

	mu       sync.Mutex
	watching map[string]*projectWatcher // project_root → watcher
	ctx      context.Context            // captured from Start; child watchers derive from it
}

// NewSensor wires a Sensor. host is reported on every Change (helps if a
// remote build server starts feeding us in the future).
func NewSensor(writer EventWriter, attr Attributor, logger *slog.Logger) *Sensor {
	if logger == nil {
		logger = slog.Default()
	}
	host, _ := os.Hostname()
	return &Sensor{
		writer:   writer,
		attr:     attr,
		logger:   logger,
		host:     host,
		watching: map[string]*projectWatcher{},
	}
}

// Start arms the sensor. It does NOT start any watchers itself — projects
// are picked up lazily via Observe. Watchers stop when ctx is canceled.
func (s *Sensor) Start(ctx context.Context) {
	s.mu.Lock()
	s.ctx = ctx
	s.mu.Unlock()
}

// Observe asks the sensor to watch the project containing cwd. Idempotent:
// repeated calls with the same project root are no-ops. Safe to call from
// any goroutine. cwd values that don't resolve to a project root are
// silently ignored.
//
// Observe is non-blocking even when first encountering a large project.
// w.start() walks the entire tree to register fsnotify watches, which can
// take 100ms+ on monorepos — too slow for the hook hot path. We reserve
// the slot synchronously (so concurrent Observe calls don't double-attach)
// and run the actual watcher initialisation in a goroutine.
func (s *Sensor) Observe(cwd string) {
	root := resolveProjectRoot(cwd)
	if root == "" {
		return
	}
	project := projectLabel(root)

	s.mu.Lock()
	if s.ctx == nil {
		// Sensor not started; do nothing rather than leak a watcher.
		s.mu.Unlock()
		return
	}
	if _, ok := s.watching[root]; ok {
		s.mu.Unlock()
		return
	}
	w := newProjectWatcher(s.ctx, projectWatcherConfig{
		Root:       root,
		Project:    project,
		Host:       s.host,
		Writer:     s.writer,
		Attributor: s.attr,
		Logger:     s.logger.With("project", project),
	})
	// Reserve the slot synchronously so the next Observe for the same root
	// returns immediately. If start() ultimately fails we'll clear the slot.
	s.watching[root] = w
	s.mu.Unlock()

	go func() {
		if err := w.start(); err != nil {
			s.logger.Warn("git sensor: failed to start watcher", "project", project, "err", err)
			s.mu.Lock()
			// Only drop our entry — guard against the rare case where Stop
			// already removed it.
			if cur, ok := s.watching[root]; ok && cur == w {
				delete(s.watching, root)
			}
			s.mu.Unlock()
			return
		}
		s.logger.Info("git sensor: watching project", "project", project, "root", root)
	}()
}

// Stop closes the watcher for the given project root, if any. Mostly useful
// for tests; production callers usually just cancel the Start context.
func (s *Sensor) Stop(root string) {
	s.mu.Lock()
	w, ok := s.watching[root]
	if ok {
		delete(s.watching, root)
	}
	s.mu.Unlock()
	if ok {
		w.stop()
	}
}

// resolveProjectRoot returns the project root for cwd, or "" when cwd
// belongs to no recognisable project. A root is recognised only when cwd or
// an ancestor holds a .git dir or a language project marker (go.mod,
// package.json, ...).
//
// Returning "" for unmarked directories is deliberate. The previous
// fallback returned cwd itself, so an agent whose hook CWD was "/" — or any
// markerless directory like ~/Downloads — made the watcher recursively
// register fsnotify over that whole tree. On macOS every watched path costs
// a file descriptor (kqueue opens the dir and its entries), so a single "/"
// root walked /Applications, /Library and /System and exhausted
// kern.maxfilesperproc, wedging the HTTP listener with EMFILE. No marker,
// no watch.
//
// It deliberately duplicates the perception package's helper rather than
// importing it to keep the sensor package's dependency graph tight:
// perception importing git would be fine, but git importing perception
// would create a cycle later when perception starts reading
// external_changes back.
func resolveProjectRoot(cwd string) string {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return ""
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		abs = cwd
	}

	if r := findAncestorWith(abs, ".git"); r != "" {
		return r
	}
	markers := []string{"go.mod", "package.json", "Cargo.toml", "pyproject.toml", "pom.xml"}
	dir := abs
	for {
		for _, m := range markers {
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
	return "" // no project marker found — refuse to watch a bare directory
}

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

func projectLabel(root string) string {
	return pathutil.Basename(strings.TrimRight(root, `/\`))
}
