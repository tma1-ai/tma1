package relay

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync/atomic"
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

// tmuxBufferSeq makes each Wake use a distinct paste-buffer name. A shared
// fixed name would let two concurrent handoffs clobber each other's buffer
// between set-buffer and paste-buffer (paste the wrong prompt into the
// wrong pane). os.Getpid()+atomic counter is unique without a clock.
var tmuxBufferSeq atomic.Uint64

// Wake loads the prompt into a per-call buffer and pastes it with
// bracketed paste (-p) keeping LF separators (-r), then submits with a
// separate Enter. Unlike `send-keys -l`, paste-buffer -p makes tmux wrap
// the text in bracketed-paste markers ONLY when the foreground app
// requested that mode — so a multi-line prompt arrives as one block
// instead of line-by-line, and a non-paste-aware program still gets clean
// text. -r keeps embedded newlines as LF (default would rewrite them to CR
// = submit). The prompt is sanitised so an embedded paste-end marker can't
// close the bracket early. Each argument is a single exec arg (no shell).
func (w *TmuxWaker) Wake(ctx context.Context, t Target, prompt string) error {
	pane := t.Terminals["tmux"]
	buf := fmt.Sprintf("tma1-relay-%d-%d", os.Getpid(), tmuxBufferSeq.Add(1))
	if err := w.run(ctx, w.bin, "set-buffer", "-b", buf, "--", sanitizeForPaste(prompt)); err != nil {
		return err
	}
	if err := w.run(ctx, w.bin, "paste-buffer", "-t", pane, "-b", buf, "-p", "-r", "-d"); err != nil {
		return err
	}
	return w.run(ctx, w.bin, "send-keys", "-t", pane, "Enter")
}
