// Package greptimedb manages the GreptimeDB child process.
package greptimedb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

const (
	// Restart backoff bounds. The first retry is fast because the common case
	// is a one-off kill (OOM killer, a stray `pkill greptime`, a crash), and
	// every query fails until the DB is back.
	restartBackoffMin = 1 * time.Second
	restartBackoffMax = 30 * time.Second

	// A child that stayed up this long counts as a healthy run, so the next
	// unexpected exit starts backing off from scratch. Shorter runs keep the
	// previous backoff, which is what stops a broken binary from being
	// respawned in a tight loop.
	stableRuntime = 60 * time.Second

	healthTimeout = 30 * time.Second
)

// Process wraps a supervised GreptimeDB child process. The supervisor
// goroutine owns Wait on the child: when the child exits without Stop having
// been called (killed externally, OOM, crash), it is respawned with backoff.
type Process struct {
	logger *slog.Logger

	// launch starts a fresh child and returns once it reports healthy. It
	// gives up early when stop is closed, so a shutdown that lands mid-launch
	// doesn't leave an orphan holding the data dir. Replaced in tests.
	launch func(stop <-chan struct{}) (*exec.Cmd, error)

	mu sync.Mutex
	// cmd is the live child, nil between an exit and the next successful
	// launch. Only the supervisor assigns it after Start.
	cmd      *exec.Cmd
	stopping bool

	stopReq  chan struct{} // closed by Stop to wake the supervisor
	stopOnce sync.Once
	exited   chan struct{} // closed when the supervisor returns
}

// Config holds the parameters needed to launch GreptimeDB.
type Config struct {
	BinPath   string
	DataDir   string
	HTTPPort  int
	GRPCPort  int
	MySQLPort int
	Logger    *slog.Logger
}

// Start launches GreptimeDB as a child process and waits until its HTTP API is
// healthy. The process is parented to the tma1-server process; it will be
// killed when Stop is called or when the parent exits. Once healthy, a
// supervisor goroutine keeps it alive until Stop.
func Start(cfg Config) (*Process, error) {
	dataPath := filepath.Join(cfg.DataDir, "data")
	if err := os.MkdirAll(dataPath, 0755); err != nil {
		return nil, fmt.Errorf("greptimedb: create data dir: %w", err)
	}
	excludeFromSpotlight(dataPath, cfg.Logger)

	configPath, err := ensureDefaultConfigFile(cfg.DataDir, cfg.Logger)
	if err != nil {
		return nil, err
	}

	args := startArgs(cfg, dataPath, configPath)
	healthURL := fmt.Sprintf("http://localhost:%d/health", cfg.HTTPPort)

	p := newProcess(cfg.Logger, func(stop <-chan struct{}) (*exec.Cmd, error) {
		cmd := exec.Command(cfg.BinPath, args...) //nolint:gosec
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		setProcAttr(cmd)

		cfg.Logger.Info("starting greptimedb",
			"bin", cfg.BinPath,
			"http_port", cfg.HTTPPort,
			"config_file", configPath,
		)
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("greptimedb: start process: %w", err)
		}

		if err := waitHealthy(healthURL, healthTimeout, stop); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return nil, fmt.Errorf("greptimedb: did not become healthy: %w", err)
		}

		cfg.Logger.Info("greptimedb healthy", "http_port", cfg.HTTPPort)
		return cmd, nil
	})

	cmd, err := p.launch(p.stopReq)
	if err != nil {
		return nil, err
	}
	p.cmd = cmd
	go p.supervise()
	return p, nil
}

func newProcess(logger *slog.Logger, launch func(stop <-chan struct{}) (*exec.Cmd, error)) *Process {
	return &Process{
		logger:  logger,
		launch:  launch,
		stopReq: make(chan struct{}),
		exited:  make(chan struct{}),
	}
}

// supervise owns Wait on the child process and respawns it after an
// unexpected exit. It returns once Stop has been requested, closing exited so
// Stop knows the child has been reaped.
func (p *Process) supervise() {
	defer close(p.exited)

	backoff := restartBackoffMin
	for {
		p.mu.Lock()
		cmd := p.cmd
		p.mu.Unlock()

		if cmd != nil {
			started := time.Now()
			waitErr := cmd.Wait()
			ran := time.Since(started)

			p.mu.Lock()
			p.cmd = nil
			stopping := p.stopping
			p.mu.Unlock()

			if stopping {
				p.logger.Info("greptimedb exited", "err", waitErr)
				return
			}
			if ran >= stableRuntime {
				backoff = restartBackoffMin
			}
			p.logger.Error("greptimedb exited unexpectedly, restarting",
				"err", waitErr, "ran", ran.Round(time.Second).String(), "delay", backoff.String())
		}

		if !p.sleep(backoff) {
			return
		}
		next, err := p.launch(p.stopReq)
		if err != nil {
			p.logger.Error("greptimedb restart failed", "err", err, "retry_in", backoff.String())
			backoff = nextBackoff(backoff)
			continue
		}

		p.mu.Lock()
		if p.stopping {
			// Stop landed while we were launching: it can't see this child,
			// so tear it down here.
			p.mu.Unlock()
			_ = next.Process.Kill()
			_ = next.Wait()
			return
		}
		p.cmd = next
		p.mu.Unlock()

		p.logger.Info("greptimedb restarted")
		backoff = nextBackoff(backoff)
	}
}

// sleep waits for d, reporting false if shutdown was requested meanwhile.
func (p *Process) sleep(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		// select picks randomly when both are ready, so re-check: a backoff
		// that expires as shutdown starts must not spawn a child.
		select {
		case <-p.stopReq:
			return false
		default:
			return true
		}
	case <-p.stopReq:
		return false
	}
}

func nextBackoff(d time.Duration) time.Duration {
	if d*2 > restartBackoffMax {
		return restartBackoffMax
	}
	return d * 2
}

// excludeFromSpotlight drops a `.metadata_never_index` marker in the data dir
// on macOS so Spotlight (mds/mdworker) skips it. GreptimeDB constantly rewrites
// SST/WAL files here; without the marker Spotlight re-indexes them on every
// change, and on a busy or freshly-OS-upgraded machine that mdworker churn can
// pull system load high enough to starve the DB and slow every query.
//
// No-op off macOS. The OS-independent marker logic lives in
// writeSpotlightExcludeMarker so it can be unit-tested on any CI runner, not
// just Darwin.
func excludeFromSpotlight(dataPath string, logger *slog.Logger) {
	if runtime.GOOS != "darwin" {
		return
	}
	writeSpotlightExcludeMarker(dataPath, logger)
}

// writeSpotlightExcludeMarker creates the `.metadata_never_index` marker in
// dataPath if it doesn't already exist, returning true only when it created a
// new marker (false when one was already present or the write failed). The OS
// gate lives in the caller so this stays platform-independent and testable.
// Best-effort: a write failure only forfeits the optimization, it never blocks
// startup.
func writeSpotlightExcludeMarker(dataPath string, logger *slog.Logger) bool {
	marker := filepath.Join(dataPath, ".metadata_never_index")
	if _, err := os.Stat(marker); err == nil {
		return false
	}
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		if logger != nil {
			logger.Warn("greptimedb: could not write Spotlight exclusion marker",
				"path", marker, "err", err)
		}
		return false
	}
	return true
}

// BeginShutdown retires the supervisor without touching the child yet. Call it
// as soon as a shutdown starts: SIGINT from a terminal and `launchctl stop` /
// `systemctl stop` reach the whole process group, so the child usually dies
// before the server has finished draining and calls Stop. Without this the
// supervisor reads that as a crash and respawns a database that is about to be
// torn down. The child keeps running until Stop, so in-flight writes still land.
func (p *Process) BeginShutdown() {
	p.mu.Lock()
	p.stopping = true
	p.mu.Unlock()
	p.stopOnce.Do(func() { close(p.stopReq) })
}

// Stop disables the supervisor, interrupts the GreptimeDB process, and waits
// for it to be reaped. Safe to call more than once.
func (p *Process) Stop(ctx context.Context) error {
	p.BeginShutdown()

	p.mu.Lock()
	cmd := p.cmd
	p.mu.Unlock()

	// cmd is nil when the child already exited and the supervisor is between
	// restarts; signalling then would target a dead (possibly recycled) pid.
	if cmd != nil && cmd.Process != nil {
		p.logger.Info("stopping greptimedb")
		if err := sendInterrupt(cmd.Process); err != nil {
			_ = cmd.Process.Kill()
		}
	}

	select {
	case <-p.exited:
		return nil
	case <-ctx.Done():
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return ctx.Err()
	}
}

// IsRunning returns true if a child process is currently alive.
func (p *Process) IsRunning() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cmd != nil
}

// waitHealthy polls the GreptimeDB /health endpoint until it returns 200,
// timeout expires, or stop is closed.
func waitHealthy(url string, timeout time.Duration, stop <-chan struct{}) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(url) //nolint:gosec
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-time.After(500 * time.Millisecond):
		case <-stop:
			return errors.New("shutdown requested")
		}
	}
	return fmt.Errorf("timeout after %s", timeout)
}
