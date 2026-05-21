package perception

import (
	"context"
	"fmt"
	"time"
)

// BuildStatus is a compact snapshot of recent build activity for a project,
// derived from tma1_build_events. It tells an agent two things at a glance:
//   - is the project building cleanly right now (LastExitCode)
//   - is there a watcher / recent run worth knowing about (LastEventAt, Tag)
//
// More detail (full error output) is available via the dashboard or by
// querying the table directly.
type BuildStatus struct {
	Project           string    `json:"project"`
	Tag               string    `json:"tag,omitempty"`           // last command tag (e.g. "cargo")
	Command           string    `json:"command,omitempty"`       // full command of latest run
	LastEventAt       time.Time `json:"last_event_at,omitempty"` // most recent build event timestamp
	LastExitCode      *int      `json:"last_exit_code,omitempty"`
	LastDurationMs    int64     `json:"last_duration_ms,omitempty"`
	ErrorsInLast30Min int       `json:"errors_in_last_30min"`
	// LastErrorMessage is the most recent stderr / non-zero exit message, or "".
	LastErrorMessage string `json:"last_error_message,omitempty"`
	// LastErrorAt is when that error was captured. Agents should compare it to
	// LastEventAt — when newer non-error events follow, the build has likely
	// recovered.
	LastErrorAt time.Time `json:"last_error_at,omitempty"`
}

// GetBuildStatus returns a coherent snapshot of the most-recently-active
// build for the given project — "most-recently-active" = the tag whose
// latest event is newest.
//
// All fields (Command, LastExitCode, LastDurationMs, LastEventAt,
// LastErrorMessage) come from rows tagged with that same identifier, so
// the agent sees information about ONE logical build, not a Frankenstein
// mash-up of "old completed run × fresh unrelated stderr". ErrorsInLast30Min
// is also scoped to the same tag.
//
// Returns nil if there are no events for this project in the last 24h.
func (b *Bundler) GetBuildStatus(ctx context.Context, project string) (*BuildStatus, error) {
	if project == "" {
		return nil, nil
	}

	// Step 1: identify the most-recently-active tag.
	tagSQL := fmt.Sprintf(
		`SELECT "tag", CAST(MAX(ts) AS BIGINT) AS last_ms
		 FROM tma1_build_events
		 WHERE project = '%s' AND "tag" IS NOT NULL AND "tag" != ''
		   AND ts > now() - INTERVAL '24 hours'
		 GROUP BY "tag" ORDER BY last_ms DESC LIMIT 1`,
		escapeSQL(project),
	)
	_, rows, err := b.client.Query(ctx, tagSQL)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	activeTag := stringAt(rows[0], 0)
	lastMs := int64At(rows[0], 1)
	if activeTag == "" || lastMs == 0 {
		return nil, nil
	}

	status := &BuildStatus{
		Project:     project,
		Tag:         activeTag,
		LastEventAt: time.UnixMilli(lastMs),
	}

	// Step 2: most recent COMPLETED for this tag (defines command + exit
	// code). If the build is still running (watcher mode, no completion
	// yet), these stay zero/null — that's how the agent knows it's live.
	completeSQL := fmt.Sprintf(
		`SELECT command, exit_code, duration_ms
		 FROM tma1_build_events
		 WHERE project = '%s' AND "tag" = '%s' AND event_type = 'completed'
		 ORDER BY ts DESC LIMIT 1`,
		escapeSQL(project), escapeSQL(activeTag),
	)
	if _, cr, err := b.client.Query(ctx, completeSQL); err == nil && len(cr) > 0 {
		status.Command = stringAt(cr[0], 0)
		if cr[0][1] != nil {
			code := intAt(cr[0], 1)
			status.LastExitCode = &code
		}
		status.LastDurationMs = int64At(cr[0], 2)
	}

	// Fallback: if no completion yet, take command from any event for this
	// tag (command is constant per run). Covers both still-running watchers
	// and rows from older binaries that didn't emit a "started" event.
	if status.Command == "" {
		anySQL := fmt.Sprintf(
			`SELECT command FROM tma1_build_events
			 WHERE project = '%s' AND "tag" = '%s'
			   AND command IS NOT NULL AND command != ''
			 ORDER BY ts DESC LIMIT 1`,
			escapeSQL(project), escapeSQL(activeTag),
		)
		if _, ar, err := b.client.Query(ctx, anySQL); err == nil && len(ar) > 0 {
			status.Command = stringAt(ar[0], 0)
		}
	}

	// Step 3: error count for this tag in the last 30 min.
	errCountSQL := fmt.Sprintf(
		`SELECT COUNT(*) FROM tma1_build_events
		 WHERE project = '%s' AND "tag" = '%s'
		   AND ts > now() - INTERVAL '30 minutes'
		   AND (severity = 'error' OR (event_type = 'completed' AND exit_code != 0))`,
		escapeSQL(project), escapeSQL(activeTag),
	)
	if _, er, err := b.client.Query(ctx, errCountSQL); err == nil && len(er) > 0 {
		status.ErrorsInLast30Min = intAt(er[0], 0)
	}

	// Step 4: most recent stderr/error message for THIS tag (truncated).
	// Include ts so the renderer can show "Xm ago" — an agent reading a
	// stale error otherwise has no way to know the build may have recovered.
	lastErrSQL := fmt.Sprintf(
		`SELECT "message", CAST(ts AS BIGINT) AS ts_ms FROM tma1_build_events
		 WHERE project = '%s' AND "tag" = '%s'
		   AND (severity = 'error' OR "stream" = 'stderr')
		   AND ts > now() - INTERVAL '30 minutes'
		 ORDER BY ts DESC LIMIT 1`,
		escapeSQL(project), escapeSQL(activeTag),
	)
	if _, lr, err := b.client.Query(ctx, lastErrSQL); err == nil && len(lr) > 0 {
		msg := stringAt(lr[0], 0)
		if len(msg) > 400 {
			msg = msg[:400] + "…"
		}
		status.LastErrorMessage = msg
		status.LastErrorAt = time.UnixMilli(int64At(lr[0], 1))
	}

	return status, nil
}
