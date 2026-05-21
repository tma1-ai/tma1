package handler

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
)

// hookTelemetry tracks per-event-type hook invocations and how many of them
// returned non-empty injection content. The goal is to compare Phase 0.1
// (UserPromptSubmit + Stop only) against later phases (PostToolUse anomaly
// injections) without instrumenting the agent side.
//
// Counters are flushed to slog every flushInterval. They're not exposed as
// Prometheus metrics yet; that can come once we have more than a handful.
type hookTelemetry struct {
	mu       sync.Mutex
	calls    map[string]uint64 // event_type → total hook invocations
	injected map[string]uint64 // event_type → hook invocations with non-empty stdout
	logger   *slog.Logger
}

const telemetryFlushInterval = 60 * time.Second

func newHookTelemetry(logger *slog.Logger) *hookTelemetry {
	if logger == nil {
		logger = slog.Default()
	}
	return &hookTelemetry{
		calls:    make(map[string]uint64),
		injected: make(map[string]uint64),
		logger:   logger,
	}
}

// record bumps the per-event counters. injected is true when the response
// body was non-empty.
func (t *hookTelemetry) record(eventType string, injected bool) {
	if eventType == "" {
		eventType = "(unknown)"
	}
	t.mu.Lock()
	t.calls[eventType]++
	if injected {
		t.injected[eventType]++
	}
	t.mu.Unlock()
}

// run flushes counters every telemetryFlushInterval until ctx is canceled.
// snapshot+reset is atomic relative to record so concurrent hook invocations
// are not lost.
func (t *hookTelemetry) run(ctx context.Context) {
	ticker := time.NewTicker(telemetryFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.flush()
			return
		case <-ticker.C:
			t.flush()
		}
	}
}

func (t *hookTelemetry) flush() {
	t.mu.Lock()
	if len(t.calls) == 0 {
		t.mu.Unlock()
		return
	}
	calls := t.calls
	injected := t.injected
	t.calls = make(map[string]uint64)
	t.injected = make(map[string]uint64)
	t.mu.Unlock()

	// Build a stable, compact summary line:
	//   "hook telemetry: PostToolUse=42(inj=0) UserPromptSubmit=3(inj=3) ..."
	events := make([]string, 0, len(calls))
	for ev := range calls {
		events = append(events, ev)
	}
	sort.Strings(events)

	var sb strings.Builder
	for i, ev := range events {
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(ev)
		sb.WriteByte('=')
		writeUint(&sb, calls[ev])
		if inj := injected[ev]; inj > 0 {
			sb.WriteString("(inj=")
			writeUint(&sb, inj)
			sb.WriteByte(')')
		}
	}
	t.logger.Info("hook telemetry", "window_s", int(telemetryFlushInterval.Seconds()), "counts", sb.String())
}

func writeUint(sb *strings.Builder, v uint64) {
	// strconv.AppendUint would require importing strconv just for this tiny
	// helper. Build the decimal manually.
	if v == 0 {
		sb.WriteByte('0')
		return
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	sb.Write(buf[i:])
}
