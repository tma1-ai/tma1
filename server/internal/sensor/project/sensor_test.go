package project

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type stubWriter struct {
	mu     sync.Mutex
	events []State
}

func (w *stubWriter) Write(_ context.Context, s State) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.events = append(w.events, s)
	return nil
}
func (w *stubWriter) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.events)
}

func TestSensorIndexIdempotentWithinTTL(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, ".git"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o644)

	w := &stubWriter{}
	s := NewSensor(w, nil)

	s.Index(root)
	s.Index(root) // duplicate within TTL — must be a no-op
	s.Index(root)

	// Allow the single goroutine spawned by the first Index() to finish.
	deadline := time.Now().Add(1 * time.Second)
	for w.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := w.count(); got != 1 {
		t.Errorf("expected exactly 1 write across 3 Index calls within TTL, got %d", got)
	}
}

func TestSensorIndexReIndexesAfterTTL(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, ".git"), 0o755)

	w := &stubWriter{}
	s := NewSensor(w, nil)

	s.Index(root)
	// Force expiry: rewrite the last-indexed time directly.
	s.mu.Lock()
	s.lastAt[root] = time.Now().Add(-2 * IndexTTL)
	s.mu.Unlock()
	s.Index(root)

	deadline := time.Now().Add(1 * time.Second)
	for w.count() < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := w.count(); got != 2 {
		t.Errorf("expected 2 writes after TTL expiry, got %d", got)
	}
}

func TestSensorIndexIgnoresEmptyCwd(t *testing.T) {
	w := &stubWriter{}
	s := NewSensor(w, nil)
	s.Index("")
	time.Sleep(50 * time.Millisecond)
	if w.count() != 0 {
		t.Errorf("expected no writes for empty cwd, got %d", w.count())
	}
}
