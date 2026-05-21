package git

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// stubWriter / stubAttributor let us test Sensor without a GreptimeDB.
type stubWriter struct {
	mu     sync.Mutex
	events []Change
}

func (s *stubWriter) Write(_ context.Context, c Change) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, c)
	return nil
}

func (s *stubWriter) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

type stubAttributor struct{ verdict string }

func (s stubAttributor) Classify(_ context.Context, _ string, _ time.Time) string {
	return s.verdict
}

func TestSensorObserveIsIdempotent(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	sensor := NewSensor(&stubWriter{}, stubAttributor{AttributionHuman}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sensor.Start(ctx)

	sensor.Observe(root)
	sensor.Observe(root) // second call must be a no-op
	sensor.Observe(root) // third too

	sensor.mu.Lock()
	defer sensor.mu.Unlock()
	if got := len(sensor.watching); got != 1 {
		t.Errorf("expected 1 watcher after 3 Observe calls, got %d", got)
	}
}

func TestSensorObserveSkipsBeforeStart(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, ".git"), 0o755)

	sensor := NewSensor(&stubWriter{}, stubAttributor{AttributionHuman}, nil)
	sensor.Observe(root) // Start not yet called → must no-op

	sensor.mu.Lock()
	defer sensor.mu.Unlock()
	if got := len(sensor.watching); got != 0 {
		t.Errorf("Observe before Start should not attach watcher, got %d", got)
	}
}

func TestSensorObserveDetectsRealFileWrite(t *testing.T) {
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

	// Give fsnotify a moment to install the watch.
	time.Sleep(100 * time.Millisecond)

	target := filepath.Join(root, "hello.txt")
	if err := os.WriteFile(target, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Wait up to 2s for the event to land.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if writer.count() > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if writer.count() == 0 {
		t.Fatal("fsnotify did not deliver any events")
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	saw := false
	for _, c := range writer.events {
		if c.FilePath == target {
			saw = true
			if c.Attribution != AttributionHuman {
				t.Errorf("attribution = %q, want %q", c.Attribution, AttributionHuman)
			}
			break
		}
	}
	if !saw {
		t.Errorf("no event for %s; got events: %+v", target, writer.events)
	}
}

func TestResolveProjectRootPrefersGitOverGoMod(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, ".git"), 0o755)
	sub := filepath.Join(root, "server")
	_ = os.MkdirAll(sub, 0o755)
	_ = os.WriteFile(filepath.Join(sub, "go.mod"), []byte("module x"), 0o644)

	if got := resolveProjectRoot(sub); got != root {
		t.Errorf("resolveProjectRoot(%q) = %q, want %q", sub, got, root)
	}
}
