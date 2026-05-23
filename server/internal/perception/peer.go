package perception

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// PeerSession is one peer-agent session, returned by GetPeerSessions
// for cross-agent lookup. "Peer" is relative to the caller: when CC
// invokes the MCP tool, Codex / OpenClaw / Copilot CLI are peers;
// when Codex invokes, CC is also a peer. The Bundler's Caller field
// drives that exclusion when `agent_source` is left empty.
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
// lookup. All four supported agents are accepted as explicit inputs;
// the "exclude the caller from the empty-string fan-out" semantics
// live in GetPeerSessions, driven by the Bundler's Caller field.
//
// An earlier version of this map hard-coded claude_code OUT because
// the only caller was Claude Code. Once Codex started invoking the
// same MCP tool, that asymmetry became a real bug — Codex callers
// got "invalid agent_source 'claude_code'" when asking for CC's
// peer sessions.
var validPeerAgents = map[string]bool{
	"claude_code": true,
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
//
// Return shape:
//   - sessions: peer sessions, most-recent first (across all peers in
//     the empty-agentSource path).
//   - partialFailures: agent → error message map. Populated ONLY in the
//     all-peers path when one or more per-agent queries fail. Callers
//     must check this before treating an empty `sessions` slice as
//     "no peer activity" — a non-empty `partialFailures` means the
//     result is incomplete.
//   - error: returned for input-validation / caller-exclusion failures.
//     Per-agent SQL errors in the all-peers path are NOT bubbled up
//     here; they go into partialFailures so one failing peer doesn't
//     blank out the others.
func (b *Bundler) GetPeerSessions(
	ctx context.Context,
	agentSource, project string,
	limit, messageLimit, sinceMin int,
) ([]PeerSession, map[string]string, error) {
	limit = clampPeerLimit(limit)
	if messageLimit <= 0 || messageLimit > 100 {
		messageLimit = 20
	}
	if sinceMin <= 0 {
		sinceMin = 24 * 60
	}
	agentSource = normalizePeerAgent(agentSource)
	if agentSource != "" && !validPeerAgents[agentSource] {
		return nil, nil, fmt.Errorf("invalid agent_source %q (valid: claude_code, codex, openclaw, copilot_cli, or empty for all peers)", agentSource)
	}

	// Caller-aware self-exclusion on the explicit-agent path. The
	// "all peers" branch already excludes Caller via peerAgentList();
	// without this guard a Codex user asking `/tma1-peer codex`
	// would receive Codex's own sessions — an echo chamber. Skip
	// silently if Caller is empty (e.g. HTTP API path with no
	// TMA1_MCP_CALLER set) so direct callers keep their freedom.
	if agentSource != "" && b.Caller != "" && agentSource == b.Caller {
		return nil, nil, fmt.Errorf("agent_source %q is the calling agent; peer sessions exclude self (use an empty agent_source for all-peers or pick a different one)", agentSource)
	}

	if agentSource != "" {
		sessions, err := b.getPeerSessionsOneAgent(ctx, agentSource, project, limit, messageLimit, sinceMin)
		return sessions, nil, err
	}

	// All-peers path: top-N per peer agent. Each agent is queried
	// independently so we get a per-agent LIMIT instead of a global one.
	// Run the peers' queries in parallel -- each fans out to ~6 HTTP
	// roundtrips against GreptimeDB; serial iteration was costing ~3x
	// the latency for no real reason (we're network-bound, not CPU-bound).
	agents := b.peerAgentList()

	type agentResult struct {
		idx      int
		agent    string
		sessions []PeerSession
		err      error
	}
	resCh := make(chan agentResult, len(agents))
	var wg sync.WaitGroup
	for idx, agent := range agents {
		wg.Add(1)
		go func(idx int, agent string) {
			defer wg.Done()
			sessions, err := b.getPeerSessionsOneAgent(ctx, agent, project, limit, messageLimit, sinceMin)
			// Both success and failure go through the channel so the
			// reassembly step below can surface per-agent failures to
			// the caller. Pre-fix, errors were silently dropped here
			// and partial failures looked identical to "no sessions".
			resCh <- agentResult{idx: idx, agent: agent, sessions: sessions, err: err}
		}(idx, agent)
	}
	wg.Wait()
	close(resCh)

	// Reassemble in agent-name order so test output stays stable.
	gathered := make([][]PeerSession, len(agents))
	var partialFailures map[string]string
	for r := range resCh {
		if r.err != nil {
			if partialFailures == nil {
				partialFailures = make(map[string]string, 1)
			}
			partialFailures[r.agent] = r.err.Error()
			b.logger.Debug("peer sessions: agent query failed",
				"agent", r.agent, "err", r.err)
			continue
		}
		gathered[r.idx] = r.sessions
	}
	var all []PeerSession
	for _, s := range gathered {
		all = append(all, s...)
	}
	// Most-recent first across all agents — the caller's mental model is
	// "show me what peers were doing".
	sort.SliceStable(all, func(i, j int) bool {
		return all[i].LastActivityAt.After(all[j].LastActivityAt)
	})
	return all, partialFailures, nil
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
		b.enrichPeerSession(ctx, &ps, messageLimit)
		out = append(out, ps)
	}
	return out, nil
}

// peerCwdFilter builds the `AND cwd …` clause used to scope sessions
// to a project. Three input shapes are recognised:
//
//   - POSIX absolute ("/Users/.../tma1") — exact + LIKE prefix using
//     forward-slash.
//   - Windows absolute ("C:\Users\...\tma1", "C:/Users/.../tma1", or
//     UNC "\\server\share\...") — exact + LIKE prefix matched against
//     BOTH separator styles. Different Windows shells post cwd with
//     different separators; matching only one would silently miss
//     sessions stored the other way.
//   - Bare name ("tma1") — legacy basename LIKE, matched after either
//     separator. Lower precision; the prefix-match branches above are
//     preferred whenever the caller has an absolute path.
//
// We deliberately avoid stdlib filepath helpers (filepath.IsAbs,
// filepath.ToSlash, etc.). cwd values come from remote agents which
// may run on a different OS than the TMA1 server, so the host's
// native separator is irrelevant — we classify the path purely by
// inspecting the string.
//
// Empty input means "no project filter — every session in the
// time window matches".
func peerCwdFilter(project string) string {
	project = strings.TrimSpace(project)
	if project == "" {
		return ""
	}

	// POSIX absolute.
	if strings.HasPrefix(project, "/") {
		root := strings.TrimRight(project, "/")
		return fmt.Sprintf("AND (cwd = '%s' OR cwd LIKE '%s/%%') ",
			escapeSQL(root), escapeSQLLike(root))
	}

	// Windows absolute. GreptimeDB's LIKE uses `\` as the escape
	// character (sqlutil package comment), so a literal backslash
	// before `%` is rendered as `\\%`. escapeSQLLike has already
	// doubled the backslashes inside the root, so the trailing
	// separator gets its own `\\\\` in the Go format string (→
	// `\\` after fmt.Sprintf → literal `\` in the SQL pattern).
	if isWindowsAbsPath(project) {
		trimmed := strings.TrimRight(project, `/\`)
		fwd := strings.ReplaceAll(trimmed, `\`, `/`)
		bsl := strings.ReplaceAll(trimmed, `/`, `\`)
		return fmt.Sprintf(
			"AND (cwd = '%s' OR cwd LIKE '%s/%%' OR cwd = '%s' OR cwd LIKE '%s\\\\%%') ",
			escapeSQL(fwd), escapeSQLLike(fwd),
			escapeSQL(bsl), escapeSQLLike(bsl),
		)
	}

	// Bare-name fallback. Match the basename preceded by either
	// separator style so a Windows-stored cwd with the same basename
	// still surfaces.
	name := escapeSQLLike(project)
	return fmt.Sprintf(
		"AND (cwd LIKE '%%/%s%%' OR cwd LIKE '%%\\\\%s%%') ",
		name, name,
	)
}

// isWindowsAbsPath returns true for the two Windows absolute-path
// shapes that show up in agent-posted cwd values: drive-letter paths
// ("C:\foo" / "C:/foo") and UNC paths ("\\server\share\..."). Pure
// string inspection — the host OS of the TMA1 server is irrelevant
// when classifying a remote agent's path.
func isWindowsAbsPath(p string) bool {
	if len(p) >= 3 && isASCIILetter(p[0]) && p[1] == ':' && (p[2] == '/' || p[2] == '\\') {
		return true
	}
	if strings.HasPrefix(p, `\\`) {
		return true
	}
	return false
}

func isASCIILetter(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

// enrichPeerSession fills Messages / RecentToolNames / FilesTouched /
// tokens. Errors on individual fills are swallowed (best-effort).
//
// All four sub-queries are independent and read-only -- they run
// concurrently so the per-session enrichment latency is the slowest
// of the four, not their sum. Before this change the four roundtrips
// were serial, making each session ~4x slower than necessary against
// a GreptimeDB under load.
func (b *Bundler) enrichPeerSession(ctx context.Context, ps *PeerSession, messageLimit int) {
	var wg sync.WaitGroup
	wg.Add(4)

	// Messages: pull from tma1_messages.
	go func() {
		defer wg.Done()
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
		_, rows, err := b.client.Query(ctx, msgSQL)
		if err != nil {
			return
		}
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
	}()

	// Token totals (use SUM separately — messages above may be capped by limit).
	go func() {
		defer wg.Done()
		tokSQL := fmt.Sprintf(
			`SELECT COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0)
			 FROM tma1_messages WHERE session_id = '%s'`,
			escapeSQL(ps.SessionID),
		)
		_, rows, err := b.client.Query(ctx, tokSQL)
		if err != nil || len(rows) == 0 {
			return
		}
		ps.TokensInput = int64At(rows[0], 0)
		ps.TokensOutput = int64At(rows[0], 1)
	}()

	// Top tools by count.
	go func() {
		defer wg.Done()
		toolSQL := fmt.Sprintf(
			`SELECT tool_name, COUNT(*) AS n FROM tma1_hook_events
			 WHERE session_id = '%s' AND event_type = 'PreToolUse' AND tool_name != ''
			 GROUP BY tool_name ORDER BY n DESC LIMIT 5`,
			escapeSQL(ps.SessionID),
		)
		_, rows, err := b.client.Query(ctx, toolSQL)
		if err != nil {
			return
		}
		names := make([]string, 0, len(rows))
		for _, r := range rows {
			if s := stringAt(r, 0); s != "" {
				names = append(names, s)
			}
		}
		ps.RecentToolNames = names
	}()

	// Files touched — drop the CC-specific tool_name filter so we also
	// pick up Codex (apply_patch, write_stdin), OpenClaw (custom tools),
	// etc. Anything whose tool_input carries a file_path counts.
	go func() {
		defer wg.Done()
		fileSQL := fmt.Sprintf(
			`SELECT DISTINCT COALESCE(tool_file_path,
			                          regexp_match(tool_input, '"file_path":"([^"]+)"')[1]) AS fp
			 FROM tma1_hook_events
			 WHERE session_id = '%s' AND event_type = 'PreToolUse'
			 LIMIT 30`,
			escapeSQL(ps.SessionID),
		)
		_, rows, err := b.client.Query(ctx, fileSQL)
		if err != nil {
			return
		}
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
	}()

	wg.Wait()
}

// clampPeerLimit constrains the per-agent session limit into [1, 5].
// Out-of-range inputs are clamped to the nearest boundary rather than
// silently defaulting to 1 — `/tma1-peer codex 10` should yield 5
// sessions, not 1, otherwise the cap-vs-default mismatch in the docs
// becomes a UX surprise.
func clampPeerLimit(n int) int {
	if n <= 0 {
		return 1
	}
	if n > 5 {
		return 5
	}
	return n
}

// peerAgentList returns the validPeerAgents set with the Bundler's
// Caller removed and sorted alphabetically for deterministic output.
// When Caller is empty (long-running HTTP API path with no fixed
// caller identity), all four agents are returned.
//
// Extracted to a method so the caller-aware exclusion has a unit-test
// foothold without standing up a fake SQL backend.
func (b *Bundler) peerAgentList() []string {
	agents := make([]string, 0, len(validPeerAgents))
	for a := range validPeerAgents {
		if a == b.Caller {
			continue
		}
		agents = append(agents, a)
	}
	sort.Strings(agents)
	return agents
}

// normalizePeerAgent maps user-friendly aliases to canonical agent_source
// values stored in the DB. Empty + "all" both yield "" (all peers).
//
// Aliases exist because the MCP tool gets called from skills/commands
// that may receive raw user input ("cc", "claude", "copilot"). Canon-
// icalising here keeps the validPeerAgents whitelist tight while still
// letting humans type what they mean. The skill markdown documents
// the same aliases, but skills are LLM-interpreted — server-side
// fallback prevents the obvious typo from looking like a bug.
func normalizePeerAgent(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	switch s {
	case "", "all", "*":
		return ""
	case "cc", "claude", "claude-code", "claudecode":
		return "claude_code"
	case "copilot", "copilot-cli", "github-copilot":
		return "copilot_cli"
	}
	return s
}
