package relay

import (
	"context"
	"strings"
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

func TestBusyDebounce(t *testing.T) {
	fw := &fakeWaker{name: "fake", can: true}
	c := newTestCoord(NewRegistry(fw))
	c.Register(Target{Role: RoleReviewer, SessionID: "r1", CWD: "/repo"})
	c.Register(Target{Role: RoleDriver, SessionID: "d1", CWD: "/repo"})

	// First plan_ready wakes the reviewer and marks it pending.
	if res, _ := c.Signal(context.Background(), StagePlanReady, RoleDriver, "/repo", "s"); !res.Dispatched {
		t.Fatal("first signal should dispatch")
	}
	// Re-sending while the reviewer hasn't handed back must drop, not re-wake.
	res, _ := c.Signal(context.Background(), StagePlanReady, RoleDriver, "/repo", "s")
	if res.Dispatched {
		t.Fatalf("duplicate signal should be dropped: %+v", res)
	}
	if fw.called != 1 {
		t.Fatalf("waker called %d times, want 1 (no re-wake)", fw.called)
	}
	// Reviewer hands back → clears its pending and wakes the driver.
	if res, _ := c.Signal(context.Background(), StagePlanReviewed, RoleReviewer, "/repo", "done"); !res.Dispatched {
		t.Fatal("hand-back should dispatch to driver")
	}
	if fw.called != 2 {
		t.Fatalf("waker called %d times, want 2", fw.called)
	}
}

// TestConcurrentSignalSingleFlight asserts two concurrent identical
// signals wake the target only once — the pending reservation is taken
// under the lock, so the second signal sees "busy" and drops instead of
// double-waking (or spawning two workers).
func TestConcurrentSignalSingleFlight(t *testing.T) {
	block := make(chan struct{})
	fw := &fakeWaker{name: "fake", can: true, block: block}
	c := newTestCoord(NewRegistry(fw))
	c.Register(Target{Role: RoleReviewer, SessionID: "r1", CWD: "/repo"})

	results := make(chan SignalResult, 2)
	for i := 0; i < 2; i++ {
		go func() {
			res, _ := c.Signal(context.Background(), StagePlanReady, RoleDriver, "/repo", "s")
			results <- res
		}()
	}
	// One goroutine reserves + blocks in Wake; the other must drop. Wait
	// for the dropped one to come back, then release the blocked Wake.
	first := <-results
	close(block)
	second := <-results

	dispatched := 0
	for _, r := range []SignalResult{first, second} {
		if r.Dispatched {
			dispatched++
		}
	}
	if dispatched != 1 {
		t.Fatalf("want exactly 1 dispatch, got %d (%+v / %+v)", dispatched, first, second)
	}
	fw.mu.Lock()
	defer fw.mu.Unlock()
	if fw.called != 1 {
		t.Fatalf("waker called %d times, want 1", fw.called)
	}
}

// TestTargetBusyClearsReservation asserts an errTargetBusy outcome drops
// the signal AND clears the reservation, so a later retry isn't permanently
// blocked as "busy".
func TestTargetBusyClearsReservation(t *testing.T) {
	fw := &fakeWaker{name: "iterm", can: true, err: errTargetBusy}
	c := newTestCoord(NewRegistry(fw))
	c.Register(Target{Role: RoleReviewer, SessionID: "r1", CWD: "/repo"})

	res, _ := c.Signal(context.Background(), StagePlanReady, RoleDriver, "/repo", "s")
	if res.Dispatched || !strings.Contains(res.Note, "busy") {
		t.Fatalf("busy wake should drop with a busy note: %+v", res)
	}
	// Reservation must be cleared → a second attempt actually re-tries Wake
	// (called twice) rather than being short-circuited as still-pending.
	c.Signal(context.Background(), StagePlanReady, RoleDriver, "/repo", "s")
	if fw.called != 2 {
		t.Fatalf("waker called %d times, want 2 (reservation not cleared on busy)", fw.called)
	}
}

// TestRegisterClearsStalePending guards the crash-without-SessionEnd case:
// a new session for a woken role must clear the stale pending so handoffs
// aren't blocked forever (relevant when the wake-timeout is disabled).
func TestRegisterClearsStalePending(t *testing.T) {
	fw := &fakeWaker{name: "fake", can: true}
	c := newTestCoord(NewRegistry(fw))
	c.Register(Target{Role: RoleReviewer, SessionID: "r1", CWD: "/repo"})
	c.Register(Target{Role: RoleDriver, SessionID: "d1", CWD: "/repo"})
	c.Signal(context.Background(), StagePlanReady, RoleDriver, "/repo", "s") // pending[reviewer] set

	// Reviewer crashed and restarted as a new session.
	c.Register(Target{Role: RoleReviewer, SessionID: "r2", CWD: "/repo"})

	res, _ := c.Signal(context.Background(), StagePlanReady, RoleDriver, "/repo", "s")
	if !res.Dispatched {
		t.Fatalf("new reviewer session should clear stale pending and allow dispatch: %+v", res)
	}
}

func TestWakeTimeoutNudgesOriginator(t *testing.T) {
	fw := &fakeWaker{name: "fake", can: true}
	c := newTestCoord(NewRegistry(fw))
	c.SetWakeTimeout(30 * time.Millisecond)
	c.Register(Target{Role: RoleReviewer, SessionID: "r1", CWD: "/repo"})
	c.Register(Target{Role: RoleDriver, SessionID: "d1", CWD: "/repo"})

	if res, _ := c.Signal(context.Background(), StagePlanReady, RoleDriver, "/repo", "s"); !res.Dispatched {
		t.Fatal("should dispatch to reviewer")
	}
	// Reviewer never hands back → after the timeout the driver is nudged.
	deadline := time.After(2 * time.Second)
	for {
		fw.mu.Lock()
		n, last := fw.called, fw.gotTarget.SessionID
		fw.mu.Unlock()
		if n >= 2 {
			if last != "d1" {
				t.Fatalf("timeout nudge woke %q, want driver d1", last)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timeout nudge never fired (called=%d)", n)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestStopCancelsTimeout(t *testing.T) {
	fw := &fakeWaker{name: "fake", can: true}
	c := newTestCoord(NewRegistry(fw))
	c.SetWakeTimeout(30 * time.Millisecond)
	c.Register(Target{Role: RoleReviewer, SessionID: "r1", CWD: "/repo"})
	c.Register(Target{Role: RoleDriver, SessionID: "d1", CWD: "/repo"})

	c.Signal(context.Background(), StagePlanReady, RoleDriver, "/repo", "s")
	c.Stop()
	time.Sleep(80 * time.Millisecond)
	fw.mu.Lock()
	defer fw.mu.Unlock()
	if fw.called != 1 {
		t.Fatalf("Stop should cancel the timer; waker called %d times, want 1", fw.called)
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
