// Package build is the build sensor: it spawns subprocesses (typically
// build / test / dev commands), captures their stdout/stderr, and feeds the
// output into tma1_build_events so the perception layer can surface build
// status to coding agents.
//
// The capture pipeline is ported from devtap; the storage layer writes
// directly to GreptimeDB (devtap's file/MCP fan-out is replaced by a single
// GreptimeDB-backed EventWriter).
package build

import (
	"context"
	"time"
)

// Event-type constants used in the event_type column of tma1_build_events.
const (
	EventTypeStarted   = "started"
	EventTypeOutput    = "output"
	EventTypeCompleted = "completed"
	EventTypeError     = "error"
	EventTypeWarning   = "warning"
)

// Severity constants for the severity column.
const (
	SeverityInfo    = "info"
	SeverityWarning = "warning"
	SeverityError   = "error"
)

// Event is a single row destined for tma1_build_events. Fields default to
// zero/empty when not applicable to a given event type.
type Event struct {
	Timestamp  time.Time
	Project    string // project root basename (e.g. "tma1")
	Command    string // full original command line ("cargo check")
	EventType  string // started | output | completed | error | warning
	Severity   string // info | warning | error
	Stream     string // stdout | stderr | exit | "" (empty for started/completed)
	Message    string // line(s) of output joined by \n, or status message
	FilePath   string // best-effort file path extracted from an error line
	LineNo     int    // best-effort line number extracted from an error line
	ExitCode   *int   // set on EventTypeCompleted
	DurationMs int64  // wall clock for EventTypeCompleted
	Host       string // cached hostname (helps if you ever route remote builds)
	Tag        string // short identifier (default: command name, override via --tag)
}

// EventWriter persists a single Event. Implementations must be safe for
// concurrent use; both Runner (batch) and LongRunner (debounce) call Write
// from background goroutines.
type EventWriter interface {
	Write(ctx context.Context, evt Event) error
}
