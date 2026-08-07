package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func sawChangePath(writer *stubWriter, path string) bool {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	for _, c := range writer.events {
		if c.FilePath == path {
			return true
		}
	}
	return false
}

func waitForChangePath(writer *stubWriter, path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if sawChangePath(writer, path) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return sawChangePath(writer, path)
}

func TestSensorWatchesDirectoryCreatedAfterStart(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	writer := &stubWriter{}
	sensor := NewSensor(writer, stubAttributor{AttributionHuman}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sensor.Start(ctx)
	sensor.Observe(root)

	// Observe starts the recursive walk asynchronously. Re-write a root-level
	// marker until its event arrives so the test knows the initial watch is
	// active instead of depending on scheduler timing.
	marker := filepath.Join(root, ".watcher-ready")
	readyDeadline := time.Now().Add(2 * time.Second)
	for !sawChangePath(writer, marker) && time.Now().Before(readyDeadline) {
		if err := os.WriteFile(marker, []byte(fmt.Sprint(time.Now().UnixNano())), 0o644); err != nil {
			t.Fatal(err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !sawChangePath(writer, marker) {
		t.Fatal("initial project watch did not become active")
	}

	nested := filepath.Join(root, "later")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	// handleFsEvent attaches the new directory before persisting its change.
	// Seeing the directory event therefore synchronizes with attachCreatedDir.
	if !waitForChangePath(writer, nested, 2*time.Second) {
		t.Fatalf("no fsnotify event for newly-created directory %q", nested)
	}

	target := filepath.Join(nested, "hello.txt")
	if err := os.WriteFile(target, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if waitForChangePath(writer, target, 2*time.Second) {
		return
	}

	writer.mu.Lock()
	defer writer.mu.Unlock()
	t.Fatalf("no fsnotify event for file created inside post-start directory %q; got events: %+v", target, writer.events)
}
