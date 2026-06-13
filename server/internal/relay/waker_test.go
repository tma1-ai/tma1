package relay

import (
	"context"
	"reflect"
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
	if len(calls) != 2 {
		t.Fatalf("want 2 send-keys calls, got %d", len(calls))
	}
	want0 := []string{"tmux", "send-keys", "-t", "%5", "-l", "--", "hello world"}
	if !reflect.DeepEqual(calls[0], want0) {
		t.Fatalf("call[0]=%v want %v", calls[0], want0)
	}
	want1 := []string{"tmux", "send-keys", "-t", "%5", "Enter"}
	if !reflect.DeepEqual(calls[1], want1) {
		t.Fatalf("call[1]=%v want %v", calls[1], want1)
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
