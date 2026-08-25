package mcp

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestRunContextCancellationUnblocksIdleInput(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()

	srv := NewServer(slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv.SetIO(reader, io.Discard)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run cancellation error = %v, want clean shutdown", err)
		}
	case <-time.After(time.Second):
		// Close the writer so the pre-fix scanner can reach EOF and the test
		// process does not retain a blocked goroutine after reporting failure.
		_ = writer.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
		}
		t.Fatal("Run stayed blocked in scanner.Scan after context cancellation")
	}
}
