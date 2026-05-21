package build

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Scanner buffers — ported from devtap. 64 KB initial, 1 MB max.
const (
	scannerInitBuf = 64 * 1024
	scannerMaxBuf  = 1024 * 1024

	// runnerBatchSize: Runner flushes when this many lines accumulate.
	runnerBatchSize = 50
)

var errNoCommand = errors.New("build: no command specified")

// cachedHostname is resolved once per process. Used as Event.Host so a future
// remote-build setup can tell hosts apart without changing the schema.
var cachedHostname string

func init() {
	cachedHostname, _ = os.Hostname()
}

// LineFilter decides whether a captured line should be written. Returning
// true keeps the line; false drops it. A nil filter keeps everything.
type LineFilter func(line string) bool

// RegexFilter returns a LineFilter that matches lines against pattern.
// If invert is true, non-matching lines are kept instead. An empty pattern
// returns nil (no filter).
func RegexFilter(pattern string, invert bool) (LineFilter, error) {
	if pattern == "" {
		return nil, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("build: regex filter: %w", err)
	}
	return func(line string) bool {
		matched := re.MatchString(line)
		if invert {
			return !matched
		}
		return matched
	}, nil
}

// RunResult holds the outcome of a captured subprocess.
type RunResult struct {
	ExitCode   int
	DurationMs int64
}

// Config carries the knobs shared between Runner and LongRunner.
type Config struct {
	Project string
	Command string // human-readable command line (used as Event.Command)
	Tag     string // short label (default: first word of Command)
	Filter  LineFilter
}

func (c Config) tag() string {
	if c.Tag != "" {
		return c.Tag
	}
	if i := strings.IndexAny(c.Command, " \t"); i > 0 {
		return c.Command[:i]
	}
	return c.Command
}

// Runner captures a one-shot subprocess: stdout/stderr are passed through to
// the user's terminal AND written to the EventWriter in batches.
type Runner struct {
	writer EventWriter
	cfg    Config
}

// NewRunner returns a Runner. writer must be non-nil.
func NewRunner(writer EventWriter, cfg Config) *Runner {
	return &Runner{writer: writer, cfg: cfg}
}

// Run executes args. The first leading KEY=VALUE pairs are extracted as env
// vars (devtap parity). Stdout/stderr are tee'd to the terminal; lines that
// pass the filter accumulate into a batch and flush every runnerBatchSize
// lines. A "started" event is emitted on entry; "completed" on exit.
func (r *Runner) Run(ctx context.Context, args []string) (*RunResult, error) {
	cmd, err := buildCommand(args)
	if err != nil {
		return nil, err
	}

	start := time.Now()
	r.emit(ctx, Event{
		Timestamp: start.UTC(),
		EventType: EventTypeStarted,
		Message:   "started: " + r.cfg.Command,
	})

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdin = os.Stdin

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		r.captureStream(ctx, stdoutPipe, os.Stdout, "stdout")
	}()
	go func() {
		defer wg.Done()
		r.captureStream(ctx, stderrPipe, os.Stderr, "stderr")
	}()

	wg.Wait()

	exitCode := waitForExit(cmd)
	dur := time.Since(start).Milliseconds()

	r.emit(ctx, Event{
		Timestamp:  time.Now().UTC(),
		EventType:  EventTypeCompleted,
		Stream:     "exit",
		Message:    fmt.Sprintf("exit %d", exitCode),
		ExitCode:   &exitCode,
		DurationMs: dur,
		Severity:   completionSeverity(exitCode),
	})

	return &RunResult{ExitCode: exitCode, DurationMs: dur}, nil
}

func (r *Runner) captureStream(ctx context.Context, pipe io.ReadCloser, passthrough *os.File, stream string) {
	scanner := bufio.NewScanner(pipe)
	scanner.Buffer(make([]byte, 0, scannerInitBuf), scannerMaxBuf)

	var batch []string
	for scanner.Scan() {
		line := scanner.Text()
		_, _ = passthrough.WriteString(line + "\n")

		if r.cfg.Filter != nil && !r.cfg.Filter(line) {
			continue
		}
		batch = append(batch, line)

		if len(batch) >= runnerBatchSize {
			r.flushBatch(ctx, batch, stream)
			batch = batch[:0]
		}
	}
	if len(batch) > 0 {
		r.flushBatch(ctx, batch, stream)
	}
	// Drain the pipe if scanner errored (line > scannerMaxBuf) so the child
	// doesn't deadlock writing into a full pipe buffer.
	if scanner.Err() != nil {
		_, _ = io.Copy(io.Discard, pipe)
	}
}

func (r *Runner) flushBatch(ctx context.Context, lines []string, stream string) {
	if len(lines) == 0 {
		return
	}
	r.emit(ctx, Event{
		Timestamp: time.Now().UTC(),
		EventType: EventTypeOutput,
		Stream:    stream,
		Message:   strings.Join(lines, "\n"),
		Severity:  streamSeverity(stream),
	})
}

func (r *Runner) emit(ctx context.Context, evt Event) {
	evt = r.fillDefaults(evt)
	if err := r.writer.Write(ctx, evt); err != nil {
		fmt.Fprintf(os.Stderr, "tma1 build: store write: %v\n", err)
	}
}

func (r *Runner) fillDefaults(evt Event) Event {
	if evt.Project == "" {
		evt.Project = r.cfg.Project
	}
	if evt.Command == "" {
		evt.Command = r.cfg.Command
	}
	if evt.Tag == "" {
		evt.Tag = r.cfg.tag()
	}
	if evt.Host == "" {
		evt.Host = cachedHostname
	}
	if evt.Timestamp.IsZero() {
		evt.Timestamp = time.Now().UTC()
	}
	return evt
}

// LongRunner is the long-running counterpart of Runner — flushes on a
// debounce interval rather than after N lines. Suitable for `cargo watch`,
// `npm run dev`, etc.
type LongRunner struct {
	writer   EventWriter
	cfg      Config
	debounce time.Duration
}

// NewLongRunner returns a LongRunner. debounce must be > 0.
func NewLongRunner(writer EventWriter, cfg Config, debounce time.Duration) *LongRunner {
	if debounce <= 0 {
		debounce = 2 * time.Second
	}
	return &LongRunner{writer: writer, cfg: cfg, debounce: debounce}
}

// Run executes args, forwards SIGINT/SIGTERM to the child, and flushes
// buffered output every debounce interval.
func (r *LongRunner) Run(ctx context.Context, args []string) (*RunResult, error) {
	cmd, err := buildCommand(args)
	if err != nil {
		return nil, err
	}

	start := time.Now()
	r.emit(ctx, Event{
		Timestamp: start.UTC(),
		EventType: EventTypeStarted,
		Message:   "watching: " + r.cfg.Command,
	})

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdin = os.Stdin

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	// Forward parent SIGINT/SIGTERM to the child so Ctrl-C cleans up.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sigDone := make(chan struct{})
	go func() {
		for {
			select {
			case sig := <-sigCh:
				if cmd.Process != nil {
					_ = cmd.Process.Signal(sig)
				}
			case <-sigDone:
				return
			}
		}
	}()
	defer func() {
		signal.Stop(sigCh)
		close(sigDone)
	}()

	var mu sync.Mutex
	stdoutBuf := make([]string, 0, 64)
	stderrBuf := make([]string, 0, 64)

	ticker := time.NewTicker(r.debounce)
	done := make(chan struct{})
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				mu.Lock()
				r.flushBuf(ctx, &stdoutBuf, "stdout")
				r.flushBuf(ctx, &stderrBuf, "stderr")
				mu.Unlock()
			case <-done:
				return
			}
		}
	}()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		r.captureStreamDebounced(stdoutPipe, os.Stdout, &mu, &stdoutBuf)
	}()
	go func() {
		defer wg.Done()
		r.captureStreamDebounced(stderrPipe, os.Stderr, &mu, &stderrBuf)
	}()

	wg.Wait()
	close(done)

	// Final flush before recording exit.
	mu.Lock()
	r.flushBuf(ctx, &stdoutBuf, "stdout")
	r.flushBuf(ctx, &stderrBuf, "stderr")
	mu.Unlock()

	exitCode := waitForExit(cmd)
	dur := time.Since(start).Milliseconds()
	r.emit(ctx, Event{
		Timestamp:  time.Now().UTC(),
		EventType:  EventTypeCompleted,
		Stream:     "exit",
		Message:    fmt.Sprintf("exit %d", exitCode),
		ExitCode:   &exitCode,
		DurationMs: dur,
		Severity:   completionSeverity(exitCode),
	})
	return &RunResult{ExitCode: exitCode, DurationMs: dur}, nil
}

func (r *LongRunner) captureStreamDebounced(pipe io.ReadCloser, passthrough *os.File, mu *sync.Mutex, buf *[]string) {
	scanner := bufio.NewScanner(pipe)
	scanner.Buffer(make([]byte, 0, scannerInitBuf), scannerMaxBuf)

	for scanner.Scan() {
		line := scanner.Text()
		_, _ = passthrough.WriteString(line + "\n")
		if r.cfg.Filter != nil && !r.cfg.Filter(line) {
			continue
		}
		mu.Lock()
		*buf = append(*buf, line)
		mu.Unlock()
	}
	if scanner.Err() != nil {
		_, _ = io.Copy(io.Discard, pipe)
	}
}

func (r *LongRunner) flushBuf(ctx context.Context, buf *[]string, stream string) {
	if len(*buf) == 0 {
		return
	}
	lines := make([]string, len(*buf))
	copy(lines, *buf)
	*buf = (*buf)[:0]

	r.emit(ctx, Event{
		Timestamp: time.Now().UTC(),
		EventType: EventTypeOutput,
		Stream:    stream,
		Message:   strings.Join(lines, "\n"),
		Severity:  streamSeverity(stream),
	})
}

func (r *LongRunner) emit(ctx context.Context, evt Event) {
	evt = (&Runner{cfg: r.cfg}).fillDefaults(evt) // share defaults with Runner
	if err := r.writer.Write(ctx, evt); err != nil {
		fmt.Fprintf(os.Stderr, "tma1 build: store write: %v\n", err)
	}
}

// buildCommand creates an exec.Cmd from args, extracting leading KEY=VALUE
// pairs as env vars (matches devtap's `--  KEY=VAL cmd ...` ergonomics).
func buildCommand(args []string) (*exec.Cmd, error) {
	if len(args) == 0 {
		return nil, errNoCommand
	}
	var envVars []string
	i := 0
	for i < len(args) {
		if k, _, ok := strings.Cut(args[i], "="); ok && k != "" && !strings.ContainsAny(k, " \t/\\") {
			envVars = append(envVars, args[i])
			i++
		} else {
			break
		}
	}
	if i >= len(args) {
		return nil, errNoCommand
	}
	cmd := exec.Command(args[i], args[i+1:]...) //nolint:gosec
	if len(envVars) > 0 {
		cmd.Env = append(os.Environ(), envVars...)
	}
	return cmd, nil
}

// waitForExit returns the child's exit code (0 on clean exit, signal/abnormal
// errors logged as non-zero).
func waitForExit(cmd *exec.Cmd) int {
	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		return 1
	}
	return 0
}

func completionSeverity(code int) string {
	if code == 0 {
		return SeverityInfo
	}
	return SeverityError
}

func streamSeverity(stream string) string {
	if stream == "stderr" {
		// Heuristic: stderr lines are flagged as warnings until we add
		// content-based severity parsing. Better-than-info, not as loud
		// as a non-zero exit.
		return SeverityWarning
	}
	return SeverityInfo
}
