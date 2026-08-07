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
	// ForceColor injects a small set of "behave as if stdout is a TTY"
	// env vars into the captured subprocess so cargo / pytest / npm /
	// vite / ls etc. keep emitting ANSI escapes through our pipe.
	// Default behaviour at the runBuild flag layer is true; the user
	// opts out with --no-color when a tool misbehaves under forced
	// colour (e.g. emits alt-screen sequences into a logfile).
	ForceColor bool
}

// colorForcingEnvVars are the env vars commonly understood by CLI
// tooling to mean "stay coloured even though stdout isn't a TTY".
// Listed in rough order of how many tools each one covers, so a tool
// reading the first match still gets the answer it expects.
//
// FORCE_COLOR: node / npm / pnpm / jest / vitest / mocha / vite /
//              prettier / eslint / many "supports-color" downstreams
// CLICOLOR_FORCE: BSD / macOS coreutils (ls), grep on darwin
// CARGO_TERM_COLOR: rust cargo and ecosystem
// RUSTC_COLOR: rustc invoked directly
// PY_COLORS: pytest
// MYPY_FORCE_COLOR: mypy
//
// We don't try to set everything ever invented; the long-tail tools
// (rspec etc.) typically take a flag, which the user passes inside
// their command line anyway.
func colorForcingEnvVars() []string {
	return []string{
		"FORCE_COLOR=1",
		"CLICOLOR_FORCE=1",
		"CARGO_TERM_COLOR=always",
		"RUSTC_COLOR=always",
		"PY_COLORS=1",
		"MYPY_FORCE_COLOR=1",
	}
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
// lines. A "started" event is emitted only after the child starts successfully;
// "completed" is emitted after it exits.
func (r *Runner) Run(ctx context.Context, args []string) (*RunResult, error) {
	cmd, err := buildCommandContext(ctx, args)
	if err != nil {
		return nil, err
	}
	applyForceColor(cmd, r.cfg.ForceColor)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdin = os.Stdin

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	r.emit(ctx, Event{
		Timestamp: start.UTC(),
		EventType: EventTypeStarted,
		Message:   "started: " + r.cfg.Command,
	})

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
	cmd, err := buildCommandContext(ctx, args)
	if err != nil {
		return nil, err
	}
	applyForceColor(cmd, r.cfg.ForceColor)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdin = os.Stdin

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	r.emit(ctx, Event{
		Timestamp: start.UTC(),
		EventType: EventTypeStarted,
		Message:   "watching: " + r.cfg.Command,
	})

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
// It uses a background context for callers/tests that only need command
// construction; Runner and LongRunner use buildCommandContext so cancellation
// reaches the child process.
func buildCommand(args []string) (*exec.Cmd, error) {
	return buildCommandContext(context.Background(), args)
}

func buildCommandContext(ctx context.Context, args []string) (*exec.Cmd, error) {
	if len(args) == 0 {
		return nil, errNoCommand
	}
	if ctx == nil {
		ctx = context.Background()
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
	cmd := exec.CommandContext(ctx, args[i], args[i+1:]...) //nolint:gosec
	if len(envVars) > 0 {
		cmd.Env = append(os.Environ(), envVars...)
	}
	return cmd, nil
}

// applyForceColor injects the "behave as if stdout is a TTY" env vars
// when enabled. cmd.Env starts nil (inherits process env from libc);
// once we touch it we must explicitly seed from os.Environ() or the
// child loses PATH / HOME / etc.
//
// Final cmd.Env is deduplicated by key, keeping the LAST occurrence
// for each. This way:
//   - inherited os.Environ values lose to our forced defaults, so
//     FORCE_COLOR=1 (etc.) reach the child even when the host shell
//     had FORCE_COLOR=0 set;
//   - the user's own KEY=VAL prefix args, which buildCommand appended
//     after os.Environ(), still win because they end up appended AFTER
//     our forced defaults (we extract and re-append them).
//
// Before this change behaviour depended on libc's "first vs last
// occurrence wins" semantics for duplicate env entries; that's stable
// on glibc/Darwin but undefined elsewhere.
func applyForceColor(cmd *exec.Cmd, force bool) {
	if !force {
		return
	}
	// Snapshot what buildCommand set up. nil means "inherit os.Environ
	// only, no user KEY=VAL prefix"; non-nil means buildCommand already
	// did `os.Environ() + userKVs` and we need to split the two back.
	osEnv := os.Environ()
	var userKVs []string
	if cmd.Env != nil {
		// User KVs are the entries appended after os.Environ. Match by
		// content rather than index so a divergent osEnv between the
		// two calls (process-level Setenv races) doesn't mis-slice.
		seen := make(map[string]struct{}, len(osEnv))
		for _, kv := range osEnv {
			seen[kv] = struct{}{}
		}
		for _, kv := range cmd.Env {
			if _, ok := seen[kv]; !ok {
				userKVs = append(userKVs, kv)
			}
		}
	}
	// Assemble: inherited → forced defaults → user KVs. The dedup pass
	// below keeps the last occurrence of each key so user > forced >
	// inherited unambiguously.
	merged := make([]string, 0, len(osEnv)+len(colorForcingEnvVars())+len(userKVs))
	merged = append(merged, osEnv...)
	merged = append(merged, colorForcingEnvVars()...)
	merged = append(merged, userKVs...)
	cmd.Env = dedupEnvKeepLast(merged)
}

// dedupEnvKeepLast collapses duplicate KEY=... entries in env, keeping
// the last occurrence for each key. Entries without `=` (degenerate
// input) pass through unchanged. Result order matches the surviving
// occurrence's original position so consumers that walk env in order
// still see the expected layout.
func dedupEnvKeepLast(env []string) []string {
	lastIdx := make(map[string]int, len(env))
	for i, kv := range env {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		lastIdx[k] = i
	}
	out := make([]string, 0, len(env))
	for i, kv := range env {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			out = append(out, kv)
			continue
		}
		if lastIdx[k] == i {
			out = append(out, kv)
		}
	}
	return out
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
