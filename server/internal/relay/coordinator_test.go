package relay

import (
	"context"
	"testing"
	"time"
)

// newTestCoord builds a Coordinator with an identity projectKey (cwd is
// the key) so tests control isolation directly.
func newTestCoord(w *Registry) *Coordinator {
	return NewCoordinator(nil, w, func(cwd string) string { return cwd })
}

func TestSignalWakesCounterpart(t *testing.T) {
	fw := &fakeWaker{name: "fake", can: true}
	c := newTestCoord(NewRegistry(fw))
	c.Register(Target{Role: RoleReviewer, SessionID: "r1", Agent: "codex", CWD: "/repo", Terminals: map[string]string{"tmux": "%2"}})

	res, err := c.Signal(context.Background(), StagePlanReady, RoleDriver, "/repo", "the plan summary")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Dispatched || res.WokeRole != RoleReviewer || res.WakerName != "fake" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if fw.gotTarget.SessionID != "r1" {
		t.Fatalf("woke wrong target: %+v", fw.gotTarget)
	}
}

func TestSignalNoCounterpart(t *testing.T) {
	fw := &fakeWaker{name: "fake", can: true}
	c := newTestCoord(NewRegistry(fw))

	res, _ := c.Signal(context.Background(), StagePlanReady, RoleDriver, "/repo", "")
	if res.Dispatched || res.TargetFound {
		t.Fatalf("should not dispatch without a reviewer: %+v", res)
	}
	if fw.called != 0 {
		t.Fatal("waker must not be called")
	}
}

func TestProjectIsolation(t *testing.T) {
	fw := &fakeWaker{name: "fake", can: true}
	c := newTestCoord(NewRegistry(fw))
	c.Register(Target{Role: RoleReviewer, SessionID: "rA", Agent: "codex", CWD: "/projA"})

	res, _ := c.Signal(context.Background(), StagePlanReady, RoleDriver, "/projB", "")
	if res.TargetFound {
		t.Fatal("projB must not see projA's reviewer")
	}
}

func TestUnregisterBySession(t *testing.T) {
	fw := &fakeWaker{name: "fake", can: true}
	c := newTestCoord(NewRegistry(fw))
	c.Register(Target{Role: RoleReviewer, SessionID: "r1", CWD: "/repo"})

	c.Unregister("/repo", RoleReviewer, "other") // mismatched session → no-op
	if res, _ := c.Signal(context.Background(), StagePlanReady, RoleDriver, "/repo", ""); !res.TargetFound {
		t.Fatal("mismatched-session unregister must not remove")
	}

	c.Unregister("/repo", RoleReviewer, "r1")
	if res, _ := c.Signal(context.Background(), StagePlanReady, RoleDriver, "/repo", ""); res.TargetFound {
		t.Fatal("matched-session unregister must remove")
	}
}

func TestTouchRegistersWhenMissing(t *testing.T) {
	fw := &fakeWaker{name: "fake", can: true}
	c := newTestCoord(NewRegistry(fw))
	c.Touch(Target{Role: RoleReviewer, SessionID: "r1", Agent: "codex", CWD: "/repo"})

	if res, _ := c.Signal(context.Background(), StagePlanReady, RoleDriver, "/repo", ""); !res.TargetFound {
		t.Fatal("Touch should register a missing target")
	}
}

func TestSignalSenderMisconfig(t *testing.T) {
	fw := &fakeWaker{name: "fake", can: true}
	c := newTestCoord(NewRegistry(fw))
	c.Register(Target{Role: RoleReviewer, SessionID: "r1", CWD: "/repo"})
	// plan_ready wakes reviewer; if the *reviewer* sends it, that would
	// wake itself — guard against it.
	res, _ := c.Signal(context.Background(), StagePlanReady, RoleReviewer, "/repo", "")
	if res.Dispatched {
		t.Fatalf("sender==wake role should not dispatch: %+v", res)
	}
}

// TestLockNotHeldDuringWake asserts Signal releases the mutex before
// calling the (possibly blocking) Waker — otherwise a slow Wake would
// stall every other Register/Touch/Signal.
func TestLockNotHeldDuringWake(t *testing.T) {
	block := make(chan struct{})
	fw := &fakeWaker{name: "fake", can: true, block: block}
	c := newTestCoord(NewRegistry(fw))
	c.Register(Target{Role: RoleReviewer, SessionID: "r1", CWD: "/repo"})

	done := make(chan struct{})
	go func() {
		_, _ = c.Signal(context.Background(), StagePlanReady, RoleDriver, "/repo", "s")
		close(done)
	}()

	reg := make(chan struct{})
	go func() {
		c.Register(Target{Role: RoleDriver, SessionID: "d1", CWD: "/repo"})
		close(reg)
	}()

	select {
	case <-reg:
	case <-time.After(2 * time.Second):
		t.Fatal("Register blocked — mutex held across Waker.Wake")
	}
	close(block)
	<-done
}
