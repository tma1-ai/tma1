package perception

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tma1-ai/tma1/server/internal/sqlutil"
)

// SessionState is a snapshot of a single agent session.
type SessionState struct {
	SessionID       string
	AgentSource     string
	StartedAt       time.Time
	LastActivityAt  time.Time
	DurationMinutes int
	ToolCallCount   int
	TokensInput     int64
	TokensOutput    int64
	RecentTools     []ToolCount // sorted desc by count
	RecentFiles     []string    // unique file paths recently touched (most recent first)
	CurrentFocus    string      // single most-edited file in last 10 min
}

// ToolCount records how often a tool was used in a session.
type ToolCount struct {
	Name  string
	Count int
}

// ActionEntry is one raw hook event in a session — the unit the
// verbose=true variant of get_session_state returns. Lightweight by
// design: file_path / command_prefix / success are pulled from the
// derived ingest-side columns so the payload is bounded and quick.
type ActionEntry struct {
	Timestamp     time.Time `json:"ts"`
	EventType     string    `json:"event_type"`
	ToolName      string    `json:"tool_name,omitempty"`
	FilePath      string    `json:"file_path,omitempty"`
	CommandPrefix string    `json:"command_prefix,omitempty"`
	Success       *bool     `json:"success,omitempty"`
}

// LatestSessionForCWD returns the most recent session_id that emitted a hook
// event from the given cwd. Returns "" if no match (no agent active in this
// project yet).
func (b *Bundler) LatestSessionForCWD(ctx context.Context, cwd string) (string, error) {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return "", nil
	}
	// Rank by last *activity* event, not last event: an exited/resumed shell
	// whose newest event is infra (SessionStart/SessionEnd/ConfigChange) must
	// not win over a session that actually did work in the same cwd. The cwd
	// match runs as a subquery over all events so Codex's SessionStart-only
	// cwd still matches. See buildLatestSessionForCWDSQL.
	_, rows, err := b.client.Query(ctx, buildLatestSessionForCWDSQL(cwd))
	if err != nil || len(rows) == 0 {
		return "", err
	}
	if s, ok := rows[0][0].(string); ok {
		return s, nil
	}
	return "", nil
}

// GetSessionState returns the state of the given session, computed from
// tma1_hook_events + tma1_messages. Returns nil (no error) if the session has
// no recorded events.
func (b *Bundler) GetSessionState(ctx context.Context, sessionID string) (*SessionState, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, nil
	}

	state := &SessionState{SessionID: sessionID}

	// Header: agent_source, first/last activity, tool_call count.
	// agent_source is constant per session, but GreptimeDB requires an aggregate
	// when other selected columns are aggregated → wrap in MAX().
	headerSQL := fmt.Sprintf(
		`SELECT MAX(agent_source) AS agent_source,
		        CAST(MIN(ts) AS BIGINT) AS started_at,
		        CAST(MAX(ts) AS BIGINT) AS last_ts,
		        COUNT(*) AS tool_calls
		 FROM tma1_hook_events
		 WHERE session_id = '%s'
		   AND event_type IN ('PreToolUse','PostToolUse','PostToolUseFailure')`,
		escapeSQL(sessionID),
	)
	cols, rows, err := b.client.Query(ctx, headerSQL)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	row := rows[0]
	idx := indexCols(cols)
	state.AgentSource = stringAt(row, idx["agent_source"])
	state.StartedAt = msTimestamp(row, idx["started_at"])
	state.LastActivityAt = msTimestamp(row, idx["last_ts"])
	state.ToolCallCount = intAt(row, idx["tool_calls"])
	if state.ToolCallCount == 0 {
		// The aggregate row exists but the session emitted no tool events
		// (or the session_id has no recorded activity at all). Treat as
		// "no session" so RenderSummary skips us.
		return nil, nil
	}
	if !state.StartedAt.IsZero() && !state.LastActivityAt.IsZero() {
		dur := state.LastActivityAt.Sub(state.StartedAt)
		state.DurationMinutes = int(dur.Minutes())
	}

	// The three remaining queries (per-tool counts, token totals,
	// recent files) are independent — each writes to a distinct
	// field of `state`. Run them concurrently to drop the wall-clock
	// to max(3) instead of sum(3). Same pattern peer.go uses for
	// enrichPeerSession. Each goroutine swallows its own error
	// (best-effort enrichment) so a slow / failing sub-query never
	// blocks the rest.
	var wg sync.WaitGroup
	wg.Add(3)

	// Per-tool counts.
	go func() {
		defer wg.Done()
		toolSQL := fmt.Sprintf(
			`SELECT tool_name, COUNT(*) AS n FROM tma1_hook_events
			 WHERE session_id = '%s' AND event_type = 'PreToolUse' AND tool_name != ''
			 GROUP BY tool_name ORDER BY n DESC LIMIT 12`,
			escapeSQL(sessionID),
		)
		_, toolRows, err := b.client.Query(ctx, toolSQL)
		if err != nil {
			return
		}
		tc := make([]ToolCount, 0, len(toolRows))
		for _, tr := range toolRows {
			tc = append(tc, ToolCount{
				Name:  stringAt(tr, 0),
				Count: intAt(tr, 1),
			})
		}
		state.RecentTools = tc
	}()

	// Tokens — sum from tma1_messages assistant rows.
	go func() {
		defer wg.Done()
		tokenSQL := fmt.Sprintf(
			`SELECT COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0)
			 FROM tma1_messages
			 WHERE session_id = '%s'`,
			escapeSQL(sessionID),
		)
		_, tokRows, err := b.client.Query(ctx, tokenSQL)
		if err != nil || len(tokRows) == 0 {
			return
		}
		state.TokensInput = int64At(tokRows[0], 0)
		state.TokensOutput = int64At(tokRows[0], 1)
	}()

	// Recent file paths — prefer the ingest-side tool_file_path column;
	// fall back to regex extraction on tool_input for legacy rows.
	go func() {
		defer wg.Done()
		pathSQL := fmt.Sprintf(
			`SELECT tool_name,
			        COALESCE(tool_file_path,
			                 regexp_match(tool_input, '"file_path":"([^"]+)"')[1]) AS fp,
			        CAST(ts AS BIGINT) AS ts_ms
			 FROM tma1_hook_events
			 WHERE session_id = '%s' AND event_type = 'PreToolUse'
			   AND tool_name IN ('Edit','Write','Read','MultiEdit')
			 ORDER BY ts DESC LIMIT 60`,
			escapeSQL(sessionID),
		)
		_, pathRows, err := b.client.Query(ctx, pathSQL)
		if err != nil {
			return
		}
		state.RecentFiles, state.CurrentFocus = extractFilesFromRows(pathRows, state.LastActivityAt)
	}()

	wg.Wait()
	return state, nil
}

// extractFilesFromRows returns the most recent unique file paths and the
// single file most actively edited in the last 10 min before lastActivity.
//
// rows shape: [tool_name STRING, file_path STRING, ts TimestampMs]. The
// caller is expected to have already lifted file_path via COALESCE on the
// ingest-side derived column with regex fallback.
func extractFilesFromRows(rows [][]any, lastActivity time.Time) ([]string, string) {
	type touch struct {
		path     string
		ts       time.Time
		toolName string
	}
	var touches []touch
	for _, r := range rows {
		toolName := stringAt(r, 0)
		fp := stringAt(r, 1)
		ts := msTimestamp(r, 2)
		if fp == "" {
			continue
		}
		touches = append(touches, touch{path: fp, ts: ts, toolName: toolName})
	}

	// Recent unique paths in temporal order (newest first).
	seen := map[string]bool{}
	var recent []string
	for _, t := range touches {
		if seen[t.path] {
			continue
		}
		seen[t.path] = true
		recent = append(recent, t.path)
		if len(recent) >= 8 {
			break
		}
	}

	// Current focus: most edited (Edit/Write/MultiEdit only) in last 10 min.
	cutoff := lastActivity.Add(-10 * time.Minute)
	counts := map[string]int{}
	for _, t := range touches {
		if t.toolName == "Read" {
			continue
		}
		if t.ts.Before(cutoff) {
			continue
		}
		counts[t.path]++
	}
	type pc struct {
		path  string
		count int
	}
	var ranked []pc
	for p, c := range counts {
		ranked = append(ranked, pc{p, c})
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].count > ranked[j].count })
	if len(ranked) > 0 {
		return recent, ranked[0].path
	}
	return recent, ""
}

// GetRecentActions returns the most recent PreToolUse / PostToolUse /
// PostToolUseFailure entries for sessionID, newest first. limit is
// clamped to [1, 200] (default 50). Backs the verbose=true variant of
// get_session_state -- the channel the plan originally proposed as a
// separate get_recent_actions tool, folded in here.
func (b *Bundler) GetRecentActions(ctx context.Context, sessionID string, limit int) ([]ActionEntry, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	sql := fmt.Sprintf(
		`SELECT CAST(ts AS BIGINT) AS ts_ms, event_type, tool_name,
		        tool_file_path, tool_command_prefix, tool_success
		 FROM tma1_hook_events
		 WHERE session_id = '%s'
		   AND event_type IN ('PreToolUse','PostToolUse','PostToolUseFailure')
		 ORDER BY ts DESC LIMIT %d`,
		escapeSQL(sessionID), limit,
	)
	_, rows, err := b.client.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	out := make([]ActionEntry, 0, len(rows))
	for _, r := range rows {
		entry := ActionEntry{
			Timestamp:     time.UnixMilli(int64At(r, 0)),
			EventType:     stringAt(r, 1),
			ToolName:      stringAt(r, 2),
			FilePath:      stringAt(r, 3),
			CommandPrefix: stringAt(r, 4),
		}
		// tool_success is BOOLEAN NULL. GreptimeDB returns null/missing
		// as nil so the *bool stays nil for events that don't carry the
		// signal (PreToolUse always; PostToolUse only when the
		// extractor flipped event_type to PostToolUseFailure).
		if len(r) > 5 && r[5] != nil {
			switch v := r[5].(type) {
			case bool:
				entry.Success = &v
			case float64:
				b := v != 0
				entry.Success = &b
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

// escapeSQL / escapeSQLLike are thin aliases over the shared sqlutil
// package -- single source of truth for SQL string + LIKE escaping
// across perception, handler, and sensor packages.
func escapeSQL(s string) string     { return sqlutil.Escape(s) }
func escapeSQLLike(s string) string { return sqlutil.EscapeLike(s) }

func indexCols(cols []string) map[string]int {
	m := make(map[string]int, len(cols))
	for i, c := range cols {
		m[c] = i
	}
	return m
}

func stringAt(row []any, i int) string {
	if i < 0 || i >= len(row) || row[i] == nil {
		return ""
	}
	if s, ok := row[i].(string); ok {
		return s
	}
	return fmt.Sprintf("%v", row[i])
}

func intAt(row []any, i int) int {
	if i < 0 || i >= len(row) || row[i] == nil {
		return 0
	}
	switch v := row[i].(type) {
	case float64:
		return int(v)
	case json.Number:
		n, err := v.Int64()
		if err == nil {
			return int(n)
		}
	case int64:
		return int(v)
	case int:
		return v
	}
	return 0
}

func int64At(row []any, i int) int64 {
	if i < 0 || i >= len(row) || row[i] == nil {
		return 0
	}
	switch v := row[i].(type) {
	case float64:
		return int64(v)
	case json.Number:
		n, err := v.Int64()
		if err == nil {
			return n
		}
	case int64:
		return v
	case int:
		return int64(v)
	}
	return 0
}

// msTimestamp parses a GreptimeDB millisecond Unix timestamp from a row cell.
// GreptimeDB returns timestamps as integers (ms since epoch) in JSON.
func msTimestamp(row []any, i int) time.Time {
	if i < 0 || i >= len(row) || row[i] == nil {
		return time.Time{}
	}
	switch v := row[i].(type) {
	case float64:
		return time.UnixMilli(int64(v))
	case json.Number:
		n, err := v.Int64()
		if err == nil {
			return time.UnixMilli(n)
		}
	case int64:
		return time.UnixMilli(v)
	case int:
		return time.UnixMilli(int64(v))
	}
	return time.Time{}
}
