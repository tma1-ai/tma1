package handler

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

func TestHookTelemetryAggregatesAndResets(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	tel := newHookTelemetry(logger)

	tel.record("UserPromptSubmit", true)
	tel.record("UserPromptSubmit", true)
	tel.record("PostToolUse", false)
	tel.record("PostToolUse", false)
	tel.record("PostToolUse", true)

	tel.flush()

	out := buf.String()
	for _, want := range []string{
		"hook telemetry",
		"UserPromptSubmit=2(inj=2)",
		"PostToolUse=3(inj=1)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("flush log missing %q\nfull log:\n%s", want, out)
		}
	}

	// Second flush with no new events must be a no-op.
	buf.Reset()
	tel.flush()
	if buf.Len() != 0 {
		t.Errorf("empty flush produced output: %q", buf.String())
	}
}

func TestHookTelemetryEmptyEventTypeBucketed(t *testing.T) {
	tel := newHookTelemetry(slog.Default())
	tel.record("", false)
	tel.mu.Lock()
	defer tel.mu.Unlock()
	if tel.calls["(unknown)"] != 1 {
		t.Errorf("empty event_type should be bucketed as (unknown), got %+v", tel.calls)
	}
}

func TestHookTelemetryConcurrentSafe(t *testing.T) {
	tel := newHookTelemetry(slog.Default())
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tel.record("PostToolUse", false)
		}()
	}
	wg.Wait()
	tel.mu.Lock()
	defer tel.mu.Unlock()
	if tel.calls["PostToolUse"] != 100 {
		t.Errorf("expected 100 PostToolUse calls, got %d", tel.calls["PostToolUse"])
	}
}
