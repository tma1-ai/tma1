package build

import (
	"context"
	"os"
	"testing"
	"time"
)

type discardEventWriter struct{}

func (discardEventWriter) Write(context.Context, Event) error { return nil }

func TestBuildContextHelperProcess(t *testing.T) {
	if os.Getenv("TMA1_BUILD_CONTEXT_HELPER") != "1" {
		return
	}
	time.Sleep(2 * time.Second)
}

func helperCommandArgs() []string {
	return []string{
		"TMA1_BUILD_CONTEXT_HELPER=1",
		os.Args[0],
		"-test.run=^TestBuildContextHelperProcess$",
	}
}

func requireCanceledRunReturnsPromptly(t *testing.T, run func(context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx) }()

	// Give the helper process time to start, then cancel the API context.
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case <-done:
		return
	case <-time.After(500 * time.Millisecond):
		// Let the pre-fix helper finish naturally so the test does not leave a
		// child process or goroutine behind before reporting the failure.
		<-done
		t.Fatal("build runner ignored context cancellation and waited for the child process to exit")
	}
}

func TestRunnerContextCancellationStopsChild(t *testing.T) {
	runner := NewRunner(discardEventWriter{}, Config{Command: "test-helper"})
	requireCanceledRunReturnsPromptly(t, func(ctx context.Context) error {
		_, err := runner.Run(ctx, helperCommandArgs())
		return err
	})
}

func TestLongRunnerContextCancellationStopsChild(t *testing.T) {
	runner := NewLongRunner(discardEventWriter{}, Config{Command: "test-helper"}, 50*time.Millisecond)
	requireCanceledRunReturnsPromptly(t, func(ctx context.Context) error {
		_, err := runner.Run(ctx, helperCommandArgs())
		return err
	})
}
