package greptimedb

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"sync"
	"testing"
	"time"
)

// spawnCounter launches `sleep` stand-ins for GreptimeDB and records how many
// times it was asked to. failures leading launches return an error instead, so
// the retry path can be exercised.
type spawnCounter struct {
	mu       sync.Mutex
	launches int
	failures int
}

func (s *spawnCounter) launch(_ <-chan struct{}) (*exec.Cmd, error) {
	s.mu.Lock()
	if s.failures > 0 {
		s.failures--
		s.mu.Unlock()
		return nil, errors.New("launch failed")
	}
	s.mu.Unlock()

	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.launches++
	s.mu.Unlock()
	return cmd, nil
}

func (s *spawnCounter) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.launches
}

func startSupervised(t *testing.T, launch func(stop <-chan struct{}) (*exec.Cmd, error)) *Process {
	t.Helper()

	p := newProcess(testLogger, launch)
	cmd, err := p.launch(p.stopReq)
	if err != nil {
		t.Fatalf("initial launch: %v", err)
	}
	p.cmd = cmd
	go p.supervise()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := p.Stop(ctx); err != nil {
			t.Errorf("Stop() error = %v", err)
		}
	})
	return p
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// currentCmd reads the live child without racing the supervisor.
func (p *Process) currentCmd() *exec.Cmd {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cmd
}

func TestSuperviseRestartsAfterUnexpectedExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses the unix `sleep` binary as a stand-in child")
	}
	t.Parallel()

	sc := &spawnCounter{}
	p := startSupervised(t, sc.launch)

	first := p.currentCmd()
	if first == nil {
		t.Fatal("no child after Start")
	}
	if err := first.Process.Kill(); err != nil {
		t.Fatalf("kill child: %v", err)
	}

	if !waitFor(t, 10*time.Second, func() bool { return sc.count() >= 2 }) {
		t.Fatal("supervisor did not restart the child after an external kill")
	}
	if !waitFor(t, time.Second, func() bool { return p.IsRunning() }) {
		t.Fatal("IsRunning() = false after restart")
	}
	if got := p.currentCmd(); got == first {
		t.Fatal("supervisor reused the dead child")
	}
}

func TestSuperviseRetriesFailedLaunch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses the unix `sleep` binary as a stand-in child")
	}
	t.Parallel()

	sc := &spawnCounter{}
	p := startSupervised(t, sc.launch)

	// Make the next launch attempt fail, so the restart only succeeds on the
	// retry after it. Set before the kill so the supervisor can't get there first.
	sc.mu.Lock()
	sc.failures = 1
	sc.mu.Unlock()

	if err := p.currentCmd().Process.Kill(); err != nil {
		t.Fatalf("kill child: %v", err)
	}

	if !waitFor(t, 15*time.Second, func() bool { return sc.count() >= 2 && p.IsRunning() }) {
		t.Fatal("supervisor gave up after a failed launch attempt")
	}
}

// A child killed by the shutdown signal itself (terminals and service managers
// signal the whole process group) must not be respawned into a server that is
// on its way out.
func TestBeginShutdownStopsRestarts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses the unix `sleep` binary as a stand-in child")
	}
	t.Parallel()

	sc := &spawnCounter{}
	p := startSupervised(t, sc.launch)

	p.BeginShutdown()
	if err := p.currentCmd().Process.Kill(); err != nil {
		t.Fatalf("kill child: %v", err)
	}

	select {
	case <-p.exited:
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor still running after BeginShutdown + child exit")
	}
	if got := sc.count(); got != 1 {
		t.Errorf("launches = %d, want 1 (no restart after BeginShutdown)", got)
	}
}

// A shutdown that lands while the supervisor is launching must still reap the
// new child; an orphan would keep holding the data dir and break the next start.
func TestStopWhileLaunchingKillsNewChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses the unix `sleep` binary as a stand-in child")
	}
	t.Parallel()

	var (
		mu       sync.Mutex
		n        int
		second   *exec.Cmd
		relaunch = make(chan struct{}, 1)
		release  = make(chan struct{})
	)
	launch := func(_ <-chan struct{}) (*exec.Cmd, error) {
		cmd := exec.Command("sleep", "60")
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		mu.Lock()
		n++
		isSecond := n == 2
		if isSecond {
			second = cmd
		}
		mu.Unlock()
		if isSecond {
			relaunch <- struct{}{}
			<-release // hold the supervisor inside launch until the test says go
		}
		return cmd, nil
	}

	p := startSupervised(t, launch)
	if err := p.currentCmd().Process.Kill(); err != nil {
		t.Fatalf("kill child: %v", err)
	}
	<-relaunch

	go func() {
		time.Sleep(50 * time.Millisecond) // let Stop land first
		close(release)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Stop(ctx); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	mu.Lock()
	child := second
	mu.Unlock()
	if child.ProcessState == nil {
		t.Fatal("child launched during shutdown was left running")
	}
	if p.IsRunning() {
		t.Error("IsRunning() = true after Stop")
	}
}

func TestStopDuringBackoffDoesNotRestart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses the unix `sleep` binary as a stand-in child")
	}
	t.Parallel()

	sc := &spawnCounter{}
	p := startSupervised(t, sc.launch)

	if err := p.currentCmd().Process.Kill(); err != nil {
		t.Fatalf("kill child: %v", err)
	}
	// Stop lands while the supervisor is waiting out restartBackoffMin.
	if !waitFor(t, 5*time.Second, func() bool { return p.currentCmd() == nil }) {
		t.Fatal("supervisor did not observe the child exit")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Stop(ctx); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	if p.IsRunning() {
		t.Error("IsRunning() = true after Stop")
	}
	if got := sc.count(); got != 1 {
		t.Errorf("launches = %d, want 1 (no restart after Stop)", got)
	}
}
