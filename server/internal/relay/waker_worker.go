package relay

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
)

// procStarter launches a detached command in dir and returns once it has
// started (fire-and-forget). Injectable for tests.
type procStarter func(dir, name string, args ...string) error

func defaultProcStarter(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	// Detach: the worker is a real agent run that must outlive the HTTP
	// request that triggered it. setDetach sets a platform-appropriate
	// process-group attribute so it survives the parent.
	setDetach(cmd)
	return cmd.Start()
}

// WorkerWaker is the universal fallback: when no interactive terminal can
// be reached (no tmux pane, etc.), it spawns a fresh non-interactive
// agent run in the target's CWD. Whether that run's hooks fire / MCP
// loads (so the relay chain continues) is verified by a spike before
// being relied on; until then this is best-effort.
type WorkerWaker struct {
	codexBin  string
	claudeBin string
	start     procStarter
	logger    *slog.Logger
}

func NewWorkerWaker(logger *slog.Logger) *WorkerWaker {
	return &WorkerWaker{
		codexBin:  lookCmd("TMA1_CODEX_PATH", "codex"),
		claudeBin: lookCmd("TMA1_CLAUDE_PATH", "claude"),
		start:     defaultProcStarter,
		logger:    logger,
	}
}

func lookCmd(envKey, name string) string {
	if v := os.Getenv(envKey); v != "" {
		return v
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

func (w *WorkerWaker) Name() string { return "worker" }

// CanWake is true only when the binary for the target's agent resolved.
// If neither codex nor claude is on PATH there's nothing to spawn.
func (w *WorkerWaker) CanWake(t Target) bool {
	return w.binFor(t.Agent) != ""
}

func (w *WorkerWaker) binFor(agent string) string {
	switch agent {
	case "codex":
		return w.codexBin
	case "claude_code":
		return w.claudeBin
	default:
		if w.codexBin != "" {
			return w.codexBin
		}
		return w.claudeBin
	}
}

// Wake spawns the agent in non-interactive mode with the prompt. It does
// NOT thread ctx into the process: the HTTP handler returns in
// milliseconds and cancelling ctx would kill the worker.
func (w *WorkerWaker) Wake(_ context.Context, t Target, prompt string) error {
	bin := w.binFor(t.Agent)
	if bin == "" {
		return errNoWorkerBin
	}
	// claude → `-p <prompt>`, codex → `exec <prompt>`.
	args := []string{"exec", prompt}
	if bin == w.claudeBin {
		args = []string{"-p", prompt}
	}
	return w.start(t.CWD, bin, args...)
}
