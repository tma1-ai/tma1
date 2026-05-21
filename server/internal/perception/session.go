package perception

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
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

// LatestSessionForCWD returns the most recent session_id that emitted a hook
// event from the given cwd. Returns "" if no match (no agent active in this
// project yet).
func (b *Bundler) LatestSessionForCWD(ctx context.Context, cwd string) (string, error) {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return "", nil
	}
	sql := fmt.Sprintf(
		`SELECT session_id FROM tma1_hook_events
		 WHERE cwd = '%s' AND session_id != ''
		   AND ts > now() - INTERVAL '6 hours'
		 ORDER BY ts DESC LIMIT 1`,
		escapeSQL(cwd),
	)
	_, rows, err := b.client.Query(ctx, sql)
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
		        MIN(ts) AS started_at,
		        MAX(ts) AS last_ts,
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

	// Per-tool counts.
	toolSQL := fmt.Sprintf(
		`SELECT tool_name, COUNT(*) AS n FROM tma1_hook_events
		 WHERE session_id = '%s' AND event_type = 'PreToolUse' AND tool_name != ''
		 GROUP BY tool_name ORDER BY n DESC LIMIT 12`,
		escapeSQL(sessionID),
	)
	_, toolRows, err := b.client.Query(ctx, toolSQL)
	if err == nil {
		for _, tr := range toolRows {
			state.RecentTools = append(state.RecentTools, ToolCount{
				Name:  stringAt(tr, 0),
				Count: intAt(tr, 1),
			})
		}
	}

	// Tokens — sum from tma1_messages assistant rows (per CLAUDE.md tma1_messages
	// has input_tokens / output_tokens columns).
	tokenSQL := fmt.Sprintf(
		`SELECT COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0)
		 FROM tma1_messages
		 WHERE session_id = '%s'`,
		escapeSQL(sessionID),
	)
	_, tokRows, err := b.client.Query(ctx, tokenSQL)
	if err == nil && len(tokRows) > 0 {
		state.TokensInput = int64At(tokRows[0], 0)
		state.TokensOutput = int64At(tokRows[0], 1)
	}

	// Recent file paths — prefer the ingest-side tool_file_path column;
	// fall back to regex extraction on tool_input for legacy rows.
	pathSQL := fmt.Sprintf(
		`SELECT tool_name,
		        COALESCE(tool_file_path,
		                 regexp_match(tool_input, '"file_path":"([^"]+)"')[1]) AS fp,
		        ts
		 FROM tma1_hook_events
		 WHERE session_id = '%s' AND event_type = 'PreToolUse'
		   AND tool_name IN ('Edit','Write','Read','MultiEdit')
		 ORDER BY ts DESC LIMIT 60`,
		escapeSQL(sessionID),
	)
	_, pathRows, err := b.client.Query(ctx, pathSQL)
	if err == nil {
		state.RecentFiles, state.CurrentFocus = extractFilesFromRows(pathRows, state.LastActivityAt)
	}

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

func escapeSQL(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

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
	case int64:
		return time.UnixMilli(v)
	case int:
		return time.UnixMilli(int64(v))
	}
	return time.Time{}
}
