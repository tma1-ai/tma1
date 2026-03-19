// Package greptimedb manages the GreptimeDB child process.
package greptimedb

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Process wraps a running GreptimeDB child process.
type Process struct {
	cmd     *exec.Cmd
	dataDir string
	logger  *slog.Logger
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

// Start launches GreptimeDB as a child process and waits until its HTTP API is healthy.
// The process is parented to the tma1-server process; it will be killed when Stop is called
// or when the parent exits.
func Start(cfg Config) (*Process, error) {
	dataPath := filepath.Join(cfg.DataDir, "data")
	if err := os.MkdirAll(dataPath, 0755); err != nil {
		return nil, fmt.Errorf("greptimedb: create data dir: %w", err)
	}

	configPath, err := ensureDefaultConfigFile(cfg.DataDir)
	if err != nil {
		return nil, err
	}

	args := startArgs(cfg, dataPath, configPath)

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

	p := &Process{cmd: cmd, dataDir: cfg.DataDir, logger: cfg.Logger}

	healthURL := fmt.Sprintf("http://localhost:%d/health", cfg.HTTPPort)
	if err := p.waitHealthy(healthURL, 30*time.Second); err != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("greptimedb: did not become healthy: %w", err)
	}

	cfg.Logger.Info("greptimedb healthy", "http_port", cfg.HTTPPort)
	return p, nil
}

// Stop sends an interrupt signal to the GreptimeDB process and waits for it to exit.
func (p *Process) Stop(ctx context.Context) error {
	if p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	p.logger.Info("stopping greptimedb")
	if err := sendInterrupt(p.cmd.Process); err != nil {
		_ = p.cmd.Process.Kill()
	}
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			p.logger.Info("greptimedb exited", "err", err)
		}
		return nil
	case <-ctx.Done():
		_ = p.cmd.Process.Kill()
		return ctx.Err()
	}
}

// IsRunning returns true if the child process is still alive.
func (p *Process) IsRunning() bool {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return false
	}
	return p.cmd.ProcessState == nil // nil = not yet exited
}

// waitHealthy polls the GreptimeDB /health endpoint until it returns 200 or timeout.
func (p *Process) waitHealthy(url string, timeout time.Duration) error {
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
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timeout after %s", timeout)
}
