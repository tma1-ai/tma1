package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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

	// Let the initial recursive walk finish before creating a directory that
	// did not exist when the watcher started.
	time.Sleep(100 * time.Millisecond)

	nested := filepath.Join(root, "later")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	// Give the watcher a chance to attach to the newly-created directory.
	time.Sleep(100 * time.Millisecond)

	target := filepath.Join(nested, "hello.txt")
	if err := os.WriteFile(target, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		writer.mu.Lock()
		sawTarget := false
		for _, c := range writer.events {
			if c.FilePath == target {
				sawTarget = true
				break
			}
		}
		writer.mu.Unlock()
		if sawTarget {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}

	writer.mu.Lock()
	defer writer.mu.Unlock()
	t.Fatalf("no fsnotify event for file created inside post-start directory %q; got events: %+v", target, writer.events)
}
