package git

import (
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"github.com/fsnotify/fsnotify"
)

func TestPlatformWatchLimits(t *testing.T) {
	l := platformWatchLimits()
	if l.dirs != maxWatchDirs {
		t.Errorf("dirs = %d, want %d on every platform", l.dirs, maxWatchDirs)
	}
	if runtime.GOOS == "darwin" {
		// kqueue charges a descriptor per file — file caps stay active.
		if l.files != maxWatchFiles || l.dirEntries != maxWatchDirEntries {
			t.Errorf("darwin file caps = (%d,%d), want (%d,%d)",
				l.files, l.dirEntries, maxWatchFiles, maxWatchDirEntries)
		}
	} else {
		// inotify / Windows watch per directory — file caps must be disabled.
		if l.files != math.MaxInt || l.dirEntries != math.MaxInt {
			t.Errorf("non-darwin file caps = (%d,%d), want disabled (MaxInt)", l.files, l.dirEntries)
		}
	}
}

func TestStaticShouldIgnorePath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/repo/.git/HEAD", true},
		{"/repo/node_modules/foo/index.js", true},
		{"/repo/dist/bundle.js", true},
		{"/repo/build/x.o", true},
		{"/repo/bin/tma1-server", true}, // dogfood report: bin/ was leaking
		{"/repo/out/foo.class", true},
		{"/repo/vendor/x.go", true},
		{"/repo/.venv/bin/python", true},
		{"/repo/.tma1/state.json", true}, // tma1's own data dir
		{"/repo/__pycache__/x.cpython-311.pyc", true},
		{"/repo/x.pyc", true},
		{"/repo/.DS_Store", true},
		{"/repo/.claude/settings.local.json", true},
		{"/repo/.tma1-context.md", true},
		// Atomic-write tempfiles must be skipped — capturing them produces
		// a noisy "file_added" event per editor save with no signal value.
		{"/repo/main.go.tmp.27019.4ef42db74560", true},
		{"/repo/src/main.go", false},
		{"/repo/README.md", false},
		// Root-level macOS system trees — matched by prefix.
		{"/Applications/Foo.app/Contents/MacOS/foo", true},
		{"/Library/Caches/x", true},
		{"/System/Library/Frameworks/x", true},
		// A project named or containing "Library"/"System"/"Applications" must
		// still be watched — prefix matching only blocks the real OS trees at
		// "/", not same-named subdirs (Unity Library/, ECS System/, ...).
		{"/Users/dennis/code/Library/src/main.go", false},
		{"/Users/dennis/code/game/System/world.go", false},
		{"/Users/dennis/Library/Caches/y", false}, // ~/Library: P0's job, not this guard
		// External disks are NOT blocked — projects legitimately live there.
		{"/Volumes/Disk/proj/src/main.go", false},
		// Windows-style paths: fsnotify hands us backslashes on
		// Windows. Without ToSlash normalization these never matched
		// any POSIX fragment, so the recursive watcher descended into
		// .git/ and node_modules/.
		{`C:\repo\.git\HEAD`, true},
		{`C:\repo\node_modules\foo\index.js`, true},
		{`C:\repo\.tma1\state.json`, true},
		{`C:\repo\src\main.go`, false},
	}
	for _, tc := range cases {
		if got := staticShouldIgnorePath(tc.path); got != tc.want {
			t.Errorf("staticShouldIgnorePath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestAddRecursiveRespectsCap(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 10; i++ {
		if err := os.MkdirAll(filepath.Join(root, "d"+strconv.Itoa(i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("cap truncates the walk", func(t *testing.T) {
		fsw, err := fsnotify.NewWatcher()
		if err != nil {
			t.Fatal(err)
		}
		defer fsw.Close()

		added, stopped, err := addRecursive(fsw, root, dirCap(3), noIgnore)
		if err != nil {
			t.Fatal(err)
		}
		if !stopped {
			t.Fatal("stopped = false, want true")
		}
		if added != 3 {
			t.Errorf("added = %d, want 3 (cap reached, 11 dirs available)", added)
		}
	})

	t.Run("ample cap watches root + all subdirs", func(t *testing.T) {
		fsw, err := fsnotify.NewWatcher()
		if err != nil {
			t.Fatal(err)
		}
		defer fsw.Close()

		added, stopped, err := addRecursive(fsw, root, dirCap(100), noIgnore)
		if err != nil {
			t.Fatal(err)
		}
		if stopped {
			t.Fatal("stopped = true, want false")
		}
		if added != 11 { // root + 10 subdirs
			t.Errorf("added = %d, want 11 (root + 10 subdirs)", added)
		}
	})

	t.Run("cap still stops when add fails", func(t *testing.T) {
		fsw, err := fsnotify.NewWatcher()
		if err != nil {
			t.Fatal(err)
		}
		if err := fsw.Close(); err != nil {
			t.Fatal(err)
		}

		added, stopped, err := addRecursive(fsw, root, dirCap(3), noIgnore)
		if err != nil {
			t.Fatal(err)
		}
		if !stopped {
			t.Fatal("stopped = false, want true")
		}
		if added != 0 {
			t.Errorf("added = %d, want 0 after watcher is closed", added)
		}
	})
}

func noIgnore(string) bool { return false }

func fullLimits() watchLimits {
	return watchLimits{dirs: maxWatchDirs, files: maxWatchFiles, dirEntries: maxWatchDirEntries}
}

func dirCap(n int) watchLimits {
	l := fullLimits()
	l.dirs = n
	return l
}

func TestAddRecursivePrunesAndBudgets(t *testing.T) {
	root := t.TempDir()
	writeFiles := func(dir string, n int) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < n; i++ {
			if err := os.WriteFile(filepath.Join(dir, "f"+strconv.Itoa(i)), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	t.Run("ignore predicate prunes a subtree", func(t *testing.T) {
		src := filepath.Join(root, "src")
		skip := filepath.Join(root, "skipme")
		writeFiles(src, 1)
		writeFiles(skip, 1)

		fsw, err := fsnotify.NewWatcher()
		if err != nil {
			t.Fatal(err)
		}
		defer fsw.Close()

		ignore := func(dir string) bool { return filepath.Base(dir) == "skipme" }
		added, _, err := addRecursive(fsw, root, fullLimits(), ignore)
		if err != nil {
			t.Fatal(err)
		}
		if added != 2 { // root + src; skipme pruned
			t.Errorf("added = %d, want 2", added)
		}
	})

	t.Run("asset-heavy dir is skipped but subdirs still watched", func(t *testing.T) {
		assets := filepath.Join(root, "assets")
		writeFiles(assets, maxWatchDirEntries+1)
		nested := filepath.Join(assets, "nested")
		writeFiles(nested, 1)

		fsw, err := fsnotify.NewWatcher()
		if err != nil {
			t.Fatal(err)
		}
		defer fsw.Close()

		added, _, err := addRecursive(fsw, filepath.Join(root, "assets"), fullLimits(), noIgnore)
		if err != nil {
			t.Fatal(err)
		}
		if added != 1 { // assets skipped (too many files), nested watched
			t.Errorf("added = %d, want 1", added)
		}
	})

	t.Run("file budget stops the walk", func(t *testing.T) {
		froot := t.TempDir()
		for i := 0; i < 3; i++ {
			writeFiles(filepath.Join(froot, "d"+strconv.Itoa(i)), 3)
		}

		fsw, err := fsnotify.NewWatcher()
		if err != nil {
			t.Fatal(err)
		}
		defer fsw.Close()

		// fileLimit 5: root(0) + d0(3) + d1(3=6) trips the budget before d2.
		limits := fullLimits()
		limits.files = 5
		added, stopped, err := addRecursive(fsw, froot, limits, noIgnore)
		if err != nil {
			t.Fatal(err)
		}
		if !stopped {
			t.Fatal("stopped = false, want true (file budget hit)")
		}
		if added != 3 { // root + d0 + d1; d2 cut by the file budget
			t.Errorf("added = %d, want 3 (root + d0 + d1 before budget)", added)
		}
	})

	t.Run("root is never pruned by the ignore predicate", func(t *testing.T) {
		proot := t.TempDir()
		writeFiles(filepath.Join(proot, "src"), 1)

		fsw, err := fsnotify.NewWatcher()
		if err != nil {
			t.Fatal(err)
		}
		defer fsw.Close()

		// Mirrors a repo whose .gitignore lists an artifact sharing its own
		// name, making the matcher hit the root. The root must still be
		// watched — otherwise the walk registers zero watches and every
		// change is silently missed.
		ignoreAll := func(string) bool { return true }
		added, _, err := addRecursive(fsw, proot, fullLimits(), ignoreAll)
		if err != nil {
			t.Fatal(err)
		}
		if added < 1 {
			t.Errorf("added = %d, want >=1 (root watched despite ignore match)", added)
		}
	})
}

func TestClassifyFsOp(t *testing.T) {
	cases := []struct {
		op   fsnotify.Op
		want string
	}{
		{fsnotify.Create, ChangeTypeFileAdded},
		{fsnotify.Remove, ChangeTypeFileDeleted},
		{fsnotify.Rename, ChangeTypeFileDeleted},
		{fsnotify.Write, ChangeTypeFileModified},
		{fsnotify.Chmod, ChangeTypeFileModified}, // shouldn't be reached in practice
	}
	for _, tc := range cases {
		if got := classifyFsOp(tc.op); got != tc.want {
			t.Errorf("classifyFsOp(%v) = %q, want %q", tc.op, got, tc.want)
		}
	}
}

func TestShouldRecordFsEvent(t *testing.T) {
	keep := []fsnotify.Op{fsnotify.Write, fsnotify.Create, fsnotify.Remove, fsnotify.Rename}
	for _, op := range keep {
		if !shouldRecordFsEvent(fsnotify.Event{Op: op}) {
			t.Errorf("op %v should be recorded", op)
		}
	}
	if shouldRecordFsEvent(fsnotify.Event{Op: fsnotify.Chmod}) {
		t.Error("Chmod should be dropped (too noisy on macOS)")
	}
}

func TestProjectLabelStripsPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/Users/x/code/tma1", "tma1"},
		{"/Users/x/code/tma1/", "tma1"},
		{"tma1", "tma1"},
	}
	for _, tc := range cases {
		if got := projectLabel(tc.in); got != tc.want {
			t.Errorf("projectLabel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
