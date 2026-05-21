package build

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// memWriter is an in-memory EventWriter for tests.
type memWriter struct {
	mu     sync.Mutex
	events []Event
}

func (w *memWriter) Write(_ context.Context, evt Event) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.events = append(w.events, evt)
	return nil
}

func (w *memWriter) snapshot() []Event {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]Event, len(w.events))
	copy(out, w.events)
	return out
}

func TestBuildCommandSeparatesEnvVars(t *testing.T) {
	cmd, err := buildCommand([]string{"FOO=bar", "BAZ=qux", "echo", "hi"})
	if err != nil {
		t.Fatalf("buildCommand: %v", err)
	}
	if got := cmd.Path; !strings.HasSuffix(got, "echo") {
		t.Errorf("cmd.Path = %q, want endswith 'echo'", got)
	}
	if got := cmd.Args; len(got) != 2 || got[1] != "hi" {
		t.Errorf("cmd.Args = %v, want [echo hi]", got)
	}
	var hasFoo bool
	for _, e := range cmd.Env {
		if e == "FOO=bar" {
			hasFoo = true
		}
	}
	if !hasFoo {
		t.Errorf("env var FOO=bar not propagated; env=%v", cmd.Env)
	}
}

func TestBuildCommandRejectsEmpty(t *testing.T) {
	if _, err := buildCommand(nil); err == nil {
		t.Error("expected error on empty args")
	}
	// Just KEY=VAL with no command — should also error.
	if _, err := buildCommand([]string{"FOO=bar"}); err == nil {
		t.Error("expected error when only env vars given")
	}
}

func TestRegexFilterMatchesAndInverts(t *testing.T) {
	f, err := RegexFilter("error|warning", false)
	if err != nil {
		t.Fatal(err)
	}
	if !f("compile error: x") {
		t.Error("expected match")
	}
	if f("clean compile") {
		t.Error("expected non-match")
	}

	inv, _ := RegexFilter("error|warning", true)
	if inv("compile error") {
		t.Error("invert: expected drop")
	}
	if !inv("clean compile") {
		t.Error("invert: expected keep")
	}

	none, err := RegexFilter("", false)
	if err != nil {
		t.Fatal(err)
	}
	if none != nil {
		t.Errorf("empty pattern should return nil filter, got %T", none)
	}
}

func TestRunnerCapturesOutputAndExitCode(t *testing.T) {
	w := &memWriter{}
	r := NewRunner(w, Config{
		Project: "test",
		Command: "echo hello",
		Tag:     "echo",
	})

	result, err := r.Run(context.Background(), []string{"sh", "-c", "echo hello; echo world; exit 0"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}

	events := w.snapshot()
	if len(events) < 2 {
		t.Fatalf("expected at least started + completed, got %d events", len(events))
	}

	// First event must be "started"; last must be "completed" with exit code.
	if events[0].EventType != EventTypeStarted {
		t.Errorf("first event type = %q, want started", events[0].EventType)
	}
	last := events[len(events)-1]
	if last.EventType != EventTypeCompleted {
		t.Errorf("last event type = %q, want completed", last.EventType)
	}
	if last.ExitCode == nil || *last.ExitCode != 0 {
		t.Errorf("completed.ExitCode = %v, want 0", last.ExitCode)
	}

	// Project/Tag default-filling.
	for _, e := range events {
		if e.Project != "test" {
			t.Errorf("event.Project = %q, want test", e.Project)
		}
		if e.Tag != "echo" {
			t.Errorf("event.Tag = %q, want echo", e.Tag)
		}
	}
}

func TestRunnerCapturesNonZeroExit(t *testing.T) {
	w := &memWriter{}
	r := NewRunner(w, Config{Project: "p", Command: "false", Tag: "fail"})
	result, err := r.Run(context.Background(), []string{"sh", "-c", "echo oops 1>&2; exit 7"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.ExitCode != 7 {
		t.Errorf("ExitCode = %d, want 7", result.ExitCode)
	}

	events := w.snapshot()
	var completed *Event
	for i := range events {
		if events[i].EventType == EventTypeCompleted {
			completed = &events[i]
		}
	}
	if completed == nil || *completed.ExitCode != 7 || completed.Severity != SeverityError {
		t.Errorf("completed = %+v, want ExitCode=7 severity=error", completed)
	}
}

func TestRunnerFilterDropsLines(t *testing.T) {
	filter, _ := RegexFilter("KEEP", false)
	w := &memWriter{}
	r := NewRunner(w, Config{Project: "p", Command: "x", Filter: filter})
	if _, err := r.Run(context.Background(), []string{"sh", "-c",
		"echo KEEP-1; echo drop-2; echo KEEP-3"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, e := range w.snapshot() {
		if e.EventType != EventTypeOutput {
			continue
		}
		if strings.Contains(e.Message, "drop") {
			t.Errorf("filter let through %q", e.Message)
		}
	}
}

func TestLongRunnerFlushesOnDebounce(t *testing.T) {
	w := &memWriter{}
	r := NewLongRunner(w, Config{Project: "p", Command: "x", Tag: "x"},
		100*time.Millisecond)

	// Subprocess emits 3 lines, sleeps 250ms (so the debounce flush is
	// likely to fire mid-run), then exits.
	if _, err := r.Run(context.Background(), []string{"sh", "-c",
		"echo one; echo two; echo three; sleep 0.25; echo done"}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Should see at least: started + (>=1 output flush) + completed.
	events := w.snapshot()
	var outputs int
	for _, e := range events {
		if e.EventType == EventTypeOutput {
			outputs++
		}
	}
	if outputs == 0 {
		t.Errorf("expected at least one output event, events=%+v", events)
	}
}
