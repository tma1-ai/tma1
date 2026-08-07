package build

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type discardEventWriter struct{}

func (discardEventWriter) Write(context.Context, Event) error { return nil }

type recordingEventWriter struct {
	mu     sync.Mutex
	events []Event
}

func (w *recordingEventWriter) Write(_ context.Context, evt Event) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.events = append(w.events, evt)
	return nil
}

func (w *recordingEventWriter) count(eventType string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, evt := range w.events {
		if evt.EventType == eventType {
			n++
		}
	}
	return n
}

func TestBuildContextHelperProcess(t *testing.T) {
	if os.Getenv("TMA1_BUILD_CONTEXT_HELPER") != "1" {
		return
	}
	if ready := os.Getenv("TMA1_BUILD_CONTEXT_READY"); ready != "" {
		if err := os.WriteFile(ready, []byte("ready"), 0o600); err != nil {
			os.Exit(2)
		}
	}
	time.Sleep(2 * time.Second)
}

func helperCommandArgs(readyPath string) []string {
	return []string{
		"TMA1_BUILD_CONTEXT_HELPER=1",
		"TMA1_BUILD_CONTEXT_READY=" + readyPath,
		os.Args[0],
		"-test.run=^TestBuildContextHelperProcess$",
	}
}

func waitForHelperReady(t *testing.T, readyPath string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(readyPath); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("helper process did not signal readiness at %s", readyPath)
}

func requireCanceledRunReturnsPromptly(t *testing.T, run func(context.Context, []string) error) {
	t.Helper()
	readyPath := filepath.Join(t.TempDir(), "ready")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, helperCommandArgs(readyPath)) }()

	waitForHelperReady(t, readyPath)
	cancel()

	select {
	case <-done:
		return
	case <-time.After(time.Second):
	}

	// Keep cleanup bounded too. If the runner is truly wedged, the regression
	// test must fail rather than hanging the entire package indefinitely.
	select {
	case <-done:
		t.Fatal("build runner ignored context cancellation and waited for the child process to exit")
	case <-time.After(2 * time.Second):
		t.Fatal("build runner remained stuck after context cancellation")
	}
}

func TestRunnerContextCancellationStopsChild(t *testing.T) {
	runner := NewRunner(discardEventWriter{}, Config{Command: "test-helper"})
	requireCanceledRunReturnsPromptly(t, func(ctx context.Context, args []string) error {
		_, err := runner.Run(ctx, args)
		return err
	})
}

func TestLongRunnerContextCancellationStopsChild(t *testing.T) {
	runner := NewLongRunner(discardEventWriter{}, Config{Command: "test-helper"}, 50*time.Millisecond)
	requireCanceledRunReturnsPromptly(t, func(ctx context.Context, args []string) error {
		_, err := runner.Run(ctx, args)
		return err
	})
}

func TestRunnerAlreadyCanceledContextDoesNotEmitStarted(t *testing.T) {
	writer := &recordingEventWriter{}
	runner := NewRunner(writer, Config{Command: "test-helper"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := runner.Run(ctx, []string{os.Args[0], "-test.run=^TestBuildContextHelperProcess$"}); err == nil {
		t.Fatal("Runner.Run with an already-canceled context returned nil error")
	}
	if got := writer.count(EventTypeStarted); got != 0 {
		t.Fatalf("started events = %d, want 0 when the process never started", got)
	}
}
