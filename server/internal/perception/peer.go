package perception

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// PeerSession is one non-claude_code agent session, returned by
// GetPeerSessions for cross-agent lookup (`/tma1-peer` slash command).
type PeerSession struct {
	SessionID         string        `json:"session_id"`
	AgentSource       string        `json:"agent_source"`
	StartedAt         time.Time     `json:"started_at"`
	LastActivityAt    time.Time     `json:"last_activity_at"`
	LastActivityAgo   string        `json:"last_activity_ago"` // human-friendly: "3m ago" / "2h ago"
	DurationMinutes   int           `json:"duration_minutes"`
	ToolCallCount     int           `json:"tool_call_count"`
	TokensInput       int64         `json:"tokens_input"`
	TokensOutput      int64         `json:"tokens_output"`
	CWD               string        `json:"cwd,omitempty"`
	Messages          []PeerMessage `json:"messages,omitempty"`
	RecentToolNames   []string      `json:"recent_tool_names,omitempty"` // top 5
	FilesTouched      []string      `json:"files_touched,omitempty"`     // unique paths
}

// PeerMessage is one conversation entry. Role is "user" / "assistant" /
// "thinking" / "tool_use" / "tool_result" depending on transcript source.
type PeerMessage struct {
	Timestamp    time.Time `json:"ts"`
	Role         string    `json:"role,omitempty"`
	MessageType  string    `json:"message_type,omitempty"`
	Content      string    `json:"content"`
	Model        string    `json:"model,omitempty"`
	ToolName     string    `json:"tool_name,omitempty"`
	InputTokens  int64     `json:"input_tokens,omitempty"`
	OutputTokens int64     `json:"output_tokens,omitempty"`
}

// validPeerAgents are the allowed agent_source values for cross-agent
// lookup. claude_code is deliberately excluded — the caller is CC.
var validPeerAgents = map[string]bool{
	"codex":       true,
	"openclaw":    true,
	"copilot_cli": true,
}

// GetPeerSessions returns the N most recent sessions for `agentSource`
// within `project`. agentSource="" returns up to `limit` sessions per peer
// agent (not `limit` total) — so a chatty agent doesn't crowd out a quiet
// one.
//
// `project` is interpreted two ways:
//   - Absolute path (starts with "/"): cwd prefix match on the resolved
//     project root. Two unrelated projects with the same basename no
//     longer collide.
//   - Anything else: legacy basename LIKE — kept for callers (e.g. tests)
//     that haven't been updated.
//
// Each returned session carries up to `messageLimit` recent messages.
func (b *Bundler) GetPeerSessions(
	ctx context.Context,
	agentSource, project string,
	limit, messageLimit, sinceMin int,
) ([]PeerSession, error) {
	if limit <= 0 || limit > 5 {
		limit = 1
	}
	if messageLimit <= 0 || messageLimit > 100 {
		messageLimit = 20
	}
	if sinceMin <= 0 {
		sinceMin = 24 * 60
	}
	agentSource = normalizePeerAgent(agentSource)
	if agentSource != "" && !validPeerAgents[agentSource] {
		return nil, fmt.Errorf("invalid agent_source %q (valid: codex, openclaw, copilot_cli, or empty for all)", agentSource)
	}

	if agentSource != "" {
		return b.getPeerSessionsOneAgent(ctx, agentSource, project, limit, messageLimit, sinceMin)
	}

	// All-peers path: top-N per peer agent. Each agent is queried
	// independently so we get a per-agent LIMIT instead of a global one.
	// Iteration is serial — three peers, query latency dominates anyway.
	agents := make([]string, 0, len(validPeerAgents))
	for a := range validPeerAgents {
		agents = append(agents, a)
	}
	sort.Strings(agents) // stable output order for tests

	var all []PeerSession
	for _, agent := range agents {
		sessions, err := b.getPeerSessionsOneAgent(ctx, agent, project, limit, messageLimit, sinceMin)
		if err != nil {
			// best-effort: one agent's query failing must not blank out the others
			continue
		}
		all = append(all, sessions...)
	}
	// Most-recent first across all agents — the caller's mental model is
	// "show me what peers were doing".
	sort.SliceStable(all, func(i, j int) bool {
		return all[i].LastActivityAt.After(all[j].LastActivityAt)
	})
	return all, nil
}

// getPeerSessionsOneAgent runs the two-pass query for a single, non-empty
// agentSource. Caller has validated the agent.
func (b *Bundler) getPeerSessionsOneAgent(
	ctx context.Context,
	agentSource, project string,
	limit, messageLimit, sinceMin int,
) ([]PeerSession, error) {
	agentFilter := fmt.Sprintf("AND agent_source = '%s' ", escapeSQL(agentSource))

	// Step 1: find candidate session_ids whose ANY event matches `project`.
	// Some agents (notably Codex) populate cwd only on SessionStart and
	// leave it empty on tool events — so we can't filter cwd + event_type
	// in the same predicate. Two-pass approach: first find session_ids
	// where any event's cwd matches, then aggregate metadata in pass 2.
	sidSQL := fmt.Sprintf(
		`SELECT DISTINCT session_id FROM tma1_hook_events
		 WHERE ts > now() - INTERVAL '%d minutes'
		   AND session_id IS NOT NULL AND session_id != ''
		   %s %s`,
		sinceMin, agentFilter, peerCwdFilter(project),
	)
	_, sidRows, err := b.client.Query(ctx, sidSQL)
	if err != nil {
		return nil, fmt.Errorf("list peer session ids: %w", err)
	}
	if len(sidRows) == 0 {
		return nil, nil
	}
	sids := make([]string, 0, len(sidRows))
	for _, r := range sidRows {
		if s := stringAt(r, 0); s != "" {
			sids = append(sids, "'"+escapeSQL(s)+"'")
		}
	}
	if len(sids) == 0 {
		return nil, nil
	}

	// Step 2: aggregate metadata over the matched session_ids. event_type
	// filter scopes the tool_call_count to actual tool events but the row
	// is included even if there are 0 (SessionStart-only sessions still
	// surface so the caller learns they exist).
	listSQL := fmt.Sprintf(
		`SELECT session_id,
		        MAX(agent_source)             AS agent_source,
		        MAX(cwd)                      AS cwd,
		        CAST(MIN(ts) AS BIGINT)       AS started_ms,
		        CAST(MAX(ts) AS BIGINT)       AS last_ms,
		        SUM(CASE WHEN event_type IN ('PreToolUse','PostToolUse','PostToolUseFailure') THEN 1 ELSE 0 END) AS tool_call_count
		 FROM tma1_hook_events
		 WHERE session_id IN (%s)
		 GROUP BY session_id
		 ORDER BY last_ms DESC
		 LIMIT %d`,
		strings.Join(sids, ","), limit,
	)
	_, rows, err := b.client.Query(ctx, listSQL)
	if err != nil {
		return nil, fmt.Errorf("list peer sessions: %w", err)
	}

	out := make([]PeerSession, 0, len(rows))
	for _, r := range rows {
		sid := stringAt(r, 0)
		if sid == "" {
			continue
		}
		startMs := int64At(r, 3)
		endMs := int64At(r, 4)
		ps := PeerSession{
			SessionID:      sid,
			AgentSource:    stringAt(r, 1),
			CWD:            stringAt(r, 2),
			StartedAt:      time.UnixMilli(startMs),
			LastActivityAt: time.UnixMilli(endMs),
			ToolCallCount:  intAt(r, 5),
		}
		if startMs > 0 && endMs > 0 {
			ps.DurationMinutes = int((endMs - startMs) / 1000 / 60)
		}
		// Reuse the bundle's relative-age formatter so the user-facing
		// "3m ago" string matches the format the agent already sees in
		// `<tma1-context>`. Empty when no LastActivityAt.
		ps.LastActivityAgo = relativeAge(ps.LastActivityAt)
		// Per-session enrichment.
		b.enrichPeerSession(ctx, &ps, messageLimit)
		out = append(out, ps)
	}
	return out, nil
}

// peerCwdFilter builds the `AND cwd …` clause used to scope sessions to a
// project. Absolute paths get a true prefix match (no basename collision);
// anything else falls back to the legacy basename LIKE.
//
// Empty input means "no project filter — match every session in the time
// window".
func peerCwdFilter(project string) string {
	project = strings.TrimSpace(project)
	if project == "" {
		return ""
	}
	if strings.HasPrefix(project, "/") {
		root := strings.TrimRight(project, "/")
		// Match the root exactly OR any subdirectory under it. Anchoring
		// with the trailing slash prevents `/foo` from matching `/foobar`.
		return fmt.Sprintf("AND (cwd = '%s' OR cwd LIKE '%s/%%') ",
			escapeSQL(root), escapeSQL(root))
	}
	return fmt.Sprintf("AND cwd LIKE '%%/%s%%' ", escapeSQL(project))
}

// enrichPeerSession fills Messages / RecentToolNames / FilesTouched /
// tokens. Errors on individual fills are swallowed (best-effort).
func (b *Bundler) enrichPeerSession(ctx context.Context, ps *PeerSession, messageLimit int) {
	// Messages: pull from tma1_messages.
	msgSQL := fmt.Sprintf(
		`SELECT CAST(ts AS BIGINT) AS ts_ms,
		        message_type, "role", content, model, tool_name,
		        input_tokens, output_tokens
		 FROM tma1_messages
		 WHERE session_id = '%s'
		   AND content IS NOT NULL
		 ORDER BY ts DESC LIMIT %d`,
		escapeSQL(ps.SessionID), messageLimit,
	)
	if _, rows, err := b.client.Query(ctx, msgSQL); err == nil {
		// We fetched DESC; flip to chronological order for natural reading.
		msgs := make([]PeerMessage, 0, len(rows))
		for i := len(rows) - 1; i >= 0; i-- {
			r := rows[i]
			msgs = append(msgs, PeerMessage{
				Timestamp:    time.UnixMilli(int64At(r, 0)),
				MessageType:  stringAt(r, 1),
				Role:         stringAt(r, 2),
				Content:      stringAt(r, 3),
				Model:        stringAt(r, 4),
				ToolName:     stringAt(r, 5),
				InputTokens:  int64At(r, 6),
				OutputTokens: int64At(r, 7),
			})
		}
		ps.Messages = msgs
	}

	// Token totals (use SUM separately — messages above may be capped by limit).
	tokSQL := fmt.Sprintf(
		`SELECT COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0)
		 FROM tma1_messages WHERE session_id = '%s'`,
		escapeSQL(ps.SessionID),
	)
	if _, rows, err := b.client.Query(ctx, tokSQL); err == nil && len(rows) > 0 {
		ps.TokensInput = int64At(rows[0], 0)
		ps.TokensOutput = int64At(rows[0], 1)
	}

	// Top tools by count.
	toolSQL := fmt.Sprintf(
		`SELECT tool_name, COUNT(*) AS n FROM tma1_hook_events
		 WHERE session_id = '%s' AND event_type = 'PreToolUse' AND tool_name != ''
		 GROUP BY tool_name ORDER BY n DESC LIMIT 5`,
		escapeSQL(ps.SessionID),
	)
	if _, rows, err := b.client.Query(ctx, toolSQL); err == nil {
		names := make([]string, 0, len(rows))
		for _, r := range rows {
			if s := stringAt(r, 0); s != "" {
				names = append(names, s)
			}
		}
		ps.RecentToolNames = names
	}

	// Files touched — drop the CC-specific tool_name filter so we also
	// pick up Codex (apply_patch, write_stdin), OpenClaw (custom tools),
	// etc. Anything whose tool_input carries a file_path counts.
	fileSQL := fmt.Sprintf(
		`SELECT DISTINCT COALESCE(tool_file_path,
		                          regexp_match(tool_input, '"file_path":"([^"]+)"')[1]) AS fp
		 FROM tma1_hook_events
		 WHERE session_id = '%s' AND event_type = 'PreToolUse'
		 LIMIT 30`,
		escapeSQL(ps.SessionID),
	)
	if _, rows, err := b.client.Query(ctx, fileSQL); err == nil {
		files := make([]string, 0, len(rows))
		seen := map[string]bool{}
		for _, r := range rows {
			fp := stringAt(r, 0)
			if fp == "" || seen[fp] {
				continue
			}
			seen[fp] = true
			files = append(files, fp)
		}
		ps.FilesTouched = files
	}
}

// normalizePeerAgent maps user-friendly aliases to canonical agent_source
// values stored in the DB. Empty + "all" both yield "" (all peers).
func normalizePeerAgent(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	switch s {
	case "", "all", "*":
		return ""
	case "copilot":
		return "copilot_cli"
	}
	return s
}
