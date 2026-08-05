package relay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
)

// osaRunner runs an AppleScript (read from stdin) with positional argv and
// returns trimmed stdout. Injectable so tests assert the script + argv
// without driving real osascript.
type osaRunner func(ctx context.Context, script string, argv ...string) (string, error)

// itermUUIDRe matches the canonical UUID that iTerm2 exposes as a
// session's AppleScript `id`. ITERM_SESSION_ID is "w<i>t<j>p<k>:UUID";
// only the UUID after the last colon is the AppleScript id.
var itermUUIDRe = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)

// itermUUID extracts and validates the AppleScript session id from an
// ITERM_SESSION_ID value. Returns "" for anything that isn't a UUID, so a
// malformed/forged header can never be substituted into the script argv.
func itermUUID(v string) string {
	if i := strings.LastIndex(v, ":"); i >= 0 {
		v = v[i+1:]
	}
	if !itermUUIDRe.MatchString(v) {
		return ""
	}
	return v
}

// itermInjectScript locates the session by id and injects the text. It
// types the text without a trailing return (newline no) then sends a lone
// return to submit, mirroring the tmux two-step. The text is passed via
// argv (never interpolated into the source), so there's no AppleScript
// injection surface. Return tokens distinguish the outcomes:
//
//	OK       — injected
//	BUSY     — busy gate on and the session is actively processing
//	NOTFOUND — no session with that id (stale/forged/closed)
const itermInjectScript = `on run argv
  set theID to item 1 of argv
  set theText to item 2 of argv
  set gate to item 3 of argv
  tell application "iTerm2"
    repeat with w in windows
      repeat with tb in tabs of w
        repeat with s in sessions of tb
          if id of s is theID then
            if gate is "1" and (is processing of s) then
              return "BUSY"
            end if
            tell s
              write text theText newline no
              write text "" newline yes
            end tell
            return "OK"
          end if
        end repeat
      end repeat
    end repeat
  end tell
  return "NOTFOUND"
end run`

// ItermWaker injects the prompt into an iTerm2 session via osascript. This
// is the visible-wake path for iTerm users who aren't inside tmux.
type ItermWaker struct {
	osascript string // resolved osascript path; "" → unavailable
	enabled   bool   // darwin + osascript present
	busyGate  bool   // skip injection when the session is actively processing
	bracketed bool   // wrap multi-line prompts in bracketed paste (else collapse to one line)
	run       osaRunner
	logger    *slog.Logger
}

func NewItermWaker(logger *slog.Logger) *ItermWaker {
	bin := lookCmd("TMA1_OSASCRIPT_PATH", "osascript")
	w := &ItermWaker{
		osascript: bin,
		enabled:   runtime.GOOS == "darwin" && bin != "",
		busyGate:  os.Getenv("TMA1_RELAY_ITERM_BUSY_GATE") != "0",
		bracketed: os.Getenv("TMA1_RELAY_BRACKETED_PASTE") != "0",
		logger:    logger,
	}
	w.run = func(ctx context.Context, script string, argv ...string) (string, error) {
		args := append([]string{"-"}, argv...) // "-" → read script from stdin
		cmd := exec.CommandContext(ctx, w.osascript, args...)
		cmd.Stdin = strings.NewReader(script)
		out, err := cmd.Output()
		if err != nil {
			// Surface osascript's stderr (e.g. TCC automation denied: -1743)
			// so the SignalResult.Note is diagnosable instead of "exit status 1".
			var ee *exec.ExitError
			if errors.As(err, &ee) && len(ee.Stderr) > 0 {
				err = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
			}
		}
		return strings.TrimSpace(string(out)), err
	}
	return w
}

func (w *ItermWaker) Name() string { return "iterm" }

func (w *ItermWaker) CanWake(t Target) bool {
	return w.enabled && itermUUID(t.Terminals["iterm"]) != ""
}

// Wake injects prompt into the iTerm session. A BUSY result maps to
// errTargetBusy (the Registry does NOT fall through to the worker — the
// real terminal is reachable, just busy), NOTFOUND to errSessionNotFound
// (the Registry falls through). Any osascript failure (e.g. TCC automation
// not authorised → -1743) is returned so the caller can surface it.
func (w *ItermWaker) Wake(ctx context.Context, t Target, prompt string) error {
	uuid := itermUUID(t.Terminals["iterm"])
	if uuid == "" {
		return errSessionNotFound
	}

	text := collapseLines(prompt)
	if w.bracketed {
		text = wrapBracketed(prompt)
	}
	gate := "0"
	if w.busyGate {
		gate = "1"
	}

	out, err := w.run(ctx, itermInjectScript, uuid, text, gate)
	if err != nil {
		return err
	}
	switch out {
	case "OK":
		return nil
	case "BUSY":
		return errTargetBusy
	default: // NOTFOUND or anything unexpected
		return errSessionNotFound
	}
}
