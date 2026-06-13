package relay

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
)

// execRunner runs a command to completion; injectable so tests assert
// the argument vector without spawning tmux.
type execRunner func(ctx context.Context, name string, args ...string) error

func defaultExecRunner(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

// TmuxWaker injects the prompt into a tmux pane via `send-keys`. This is
// the most reliable waker on mac+linux: one implementation covers any
// terminal emulator as long as the agent runs inside tmux.
type TmuxWaker struct {
	bin    string // resolved tmux path; "" when tmux isn't available
	run    execRunner
	logger *slog.Logger
}

func NewTmuxWaker(logger *slog.Logger) *TmuxWaker {
	bin := os.Getenv("TMA1_TMUX_PATH")
	if bin == "" {
		if p, err := exec.LookPath("tmux"); err == nil {
			bin = p
		}
	}
	return &TmuxWaker{bin: bin, run: defaultExecRunner, logger: logger}
}

func (w *TmuxWaker) Name() string { return "tmux" }

func (w *TmuxWaker) CanWake(t Target) bool {
	return w.bin != "" && t.Terminals["tmux"] != ""
}

// Wake sends the prompt as a literal (-l) so tmux doesn't interpret a
// ';' or an embedded key-name (e.g. "Enter", "C-c") inside the text,
// then sends Enter as a separate key to submit it. The prompt is a
// single exec argument (not a shell string), so there's no shell
// injection surface.
func (w *TmuxWaker) Wake(ctx context.Context, t Target, prompt string) error {
	pane := t.Terminals["tmux"]
	if err := w.run(ctx, w.bin, "send-keys", "-t", pane, "-l", "--", prompt); err != nil {
		return err
	}
	return w.run(ctx, w.bin, "send-keys", "-t", pane, "Enter")
}
