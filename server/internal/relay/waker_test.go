package relay

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// fakeWaker is a test double shared across relay tests.
type fakeWaker struct {
	name  string
	can   bool
	err   error
	block chan struct{} // when non-nil, Wake blocks until it's closed

	mu        sync.Mutex
	called    int
	gotTarget Target
	gotPrompt string
}

func (f *fakeWaker) Name() string          { return f.name }
func (f *fakeWaker) CanWake(t Target) bool { return f.can }
func (f *fakeWaker) Wake(_ context.Context, t Target, prompt string) error {
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called++
	f.gotTarget = t
	f.gotPrompt = prompt
	return f.err
}

func TestRegistryPicksFirstCanWake(t *testing.T) {
	a := &fakeWaker{name: "a", can: false}
	b := &fakeWaker{name: "b", can: true}
	c := &fakeWaker{name: "c", can: true}
	r := NewRegistry(a, b, c)

	name, err := r.WakeWith(context.Background(), Target{}, "p")
	if err != nil {
		t.Fatal(err)
	}
	if name != "b" {
		t.Fatalf("want b, got %s", name)
	}
	if c.called != 0 {
		t.Fatal("c should not have been called once b succeeded")
	}
}

func TestRegistryFallThroughOnError(t *testing.T) {
	a := &fakeWaker{name: "a", can: true, err: errNoWorkerBin}
	b := &fakeWaker{name: "b", can: true}
	r := NewRegistry(a, b)

	name, err := r.WakeWith(context.Background(), Target{}, "p")
	if err != nil {
		t.Fatal(err)
	}
	if name != "b" {
		t.Fatalf("want b after a errored, got %s", name)
	}
}

func TestRegistryNoApplicable(t *testing.T) {
	r := NewRegistry(&fakeWaker{name: "a", can: false})
	if _, err := r.WakeWith(context.Background(), Target{}, "p"); err == nil {
		t.Fatal("want error when no waker applies")
	}
}

func TestTmuxWakerArgs(t *testing.T) {
	var calls [][]string
	w := &TmuxWaker{bin: "tmux", run: func(_ context.Context, name string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))
		return nil
	}}

	if w.CanWake(Target{}) {
		t.Fatal("no pane → should not CanWake")
	}
	pane := Target{Terminals: map[string]string{"tmux": "%5"}}
	if !w.CanWake(pane) {
		t.Fatal("pane present → should CanWake")
	}

	if err := w.Wake(context.Background(), pane, "hello world"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 {
		t.Fatalf("want 3 tmux calls, got %d", len(calls))
	}
	// set-buffer uses a per-call buffer name; capture it and assert the
	// paste-buffer reuses the SAME name (else concurrent handoffs clobber).
	if calls[0][0] != "tmux" || calls[0][1] != "set-buffer" || calls[0][2] != "-b" {
		t.Fatalf("call[0] not set-buffer: %v", calls[0])
	}
	buf := calls[0][3]
	if !strings.HasPrefix(buf, "tma1-relay-") {
		t.Fatalf("buffer name = %q, want tma1-relay- prefix", buf)
	}
	want0 := []string{"tmux", "set-buffer", "-b", buf, "--", "hello world"}
	if !reflect.DeepEqual(calls[0], want0) {
		t.Fatalf("call[0]=%v want %v", calls[0], want0)
	}
	want1 := []string{"tmux", "paste-buffer", "-t", "%5", "-b", buf, "-p", "-r", "-d"}
	if !reflect.DeepEqual(calls[1], want1) {
		t.Fatalf("call[1]=%v want %v", calls[1], want1)
	}
	want2 := []string{"tmux", "send-keys", "-t", "%5", "Enter"}
	if !reflect.DeepEqual(calls[2], want2) {
		t.Fatalf("call[2]=%v want %v", calls[2], want2)
	}
}

func TestTmuxWakerUniqueBuffers(t *testing.T) {
	var bufs []string
	w := &TmuxWaker{bin: "tmux", run: func(_ context.Context, _ string, args ...string) error {
		if len(args) >= 3 && args[0] == "set-buffer" {
			bufs = append(bufs, args[2])
		}
		return nil
	}}
	pane := Target{Terminals: map[string]string{"tmux": "%5"}}
	_ = w.Wake(context.Background(), pane, "a")
	_ = w.Wake(context.Background(), pane, "b")
	if len(bufs) != 2 || bufs[0] == bufs[1] {
		t.Fatalf("buffers must be unique per Wake, got %v", bufs)
	}
}

func TestTmuxWakerSanitizesPasteEnd(t *testing.T) {
	var calls [][]string
	w := &TmuxWaker{bin: "tmux", run: func(_ context.Context, name string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))
		return nil
	}}
	pane := Target{Terminals: map[string]string{"tmux": "%5"}}
	// An embedded paste-end marker must be stripped before set-buffer.
	if err := w.Wake(context.Background(), pane, "a"+bracketEnd+"b"); err != nil {
		t.Fatal(err)
	}
	if got := calls[0][len(calls[0])-1]; got != "ab" {
		t.Fatalf("set-buffer payload not sanitised: %q", got)
	}
}

func TestTmuxWakerNoBin(t *testing.T) {
	w := &TmuxWaker{bin: "", run: defaultExecRunner}
	if w.CanWake(Target{Terminals: map[string]string{"tmux": "%5"}}) {
		t.Fatal("no tmux binary → should not CanWake")
	}
}

func TestWorkerWakerArgs(t *testing.T) {
	type call struct {
		dir, name string
		args      []string
	}
	var got call
	w := &WorkerWaker{codexBin: "/bin/codex", claudeBin: "/bin/claude", start: func(dir, name string, args ...string) error {
		got = call{dir, name, args}
		return nil
	}}

	if !w.CanWake(Target{Agent: "codex"}) {
		t.Fatal("codex resolved → CanWake")
	}
	if err := w.Wake(context.Background(), Target{Agent: "codex", CWD: "/repo"}, "do review"); err != nil {
		t.Fatal(err)
	}
	if got.name != "/bin/codex" || got.dir != "/repo" {
		t.Fatalf("codex spawn = %+v", got)
	}
	if !reflect.DeepEqual(got.args, []string{"exec", "do review"}) {
		t.Fatalf("codex args = %v", got.args)
	}

	if err := w.Wake(context.Background(), Target{Agent: "claude_code", CWD: "/repo"}, "fix"); err != nil {
		t.Fatal(err)
	}
	if got.name != "/bin/claude" {
		t.Fatalf("claude spawn = %+v", got)
	}
	if !reflect.DeepEqual(got.args, []string{"-p", "fix"}) {
		t.Fatalf("claude args = %v", got.args)
	}
}

func TestWorkerWakerNoBin(t *testing.T) {
	w := &WorkerWaker{}
	if w.CanWake(Target{Agent: "codex"}) {
		t.Fatal("no binary → should not CanWake")
	}
	if err := w.Wake(context.Background(), Target{Agent: "codex"}, "x"); err == nil {
		t.Fatal("want error when no binary resolved")
	}
}
