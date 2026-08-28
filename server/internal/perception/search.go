package perception

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tma1-ai/tma1/server/internal/strutil"
)

// matches_term is word-boundary based: "Cwd" does not match
// "peerCwdFilter", a substring LIKE does. When the term pass finds
// nothing we widen, and report which mode produced the hits.
const (
	MatchModeTerm      = "term"
	MatchModeSubstring = "substring"
)

// searchScopeCap bounds the session_id set fed into the message search.
// Reported via SearchResults.ScopeTruncated rather than silently
// searching a subset.
const searchScopeCap = 500

// syntheticMessageFilter drops rows that carry no readable content.
// Codex writes one `usage` row per model call with an empty content
// field; without this filter they eat the message budget and push the
// actual conversation out of the window.
const syntheticMessageFilter = `content IS NOT NULL AND content != '' AND message_type != 'usage'`

// SearchOptions parameterises SearchSessions; zero values take defaults.
type SearchOptions struct {
	Query        string
	AgentSource  string // canonical agent_source; "" = every agent
	Project      string // "" = every project
	SinceMin     int
	Limit        int
	SnippetLimit int
}

// SearchResults is the payload SearchSessions returns.
type SearchResults struct {
	Query          string       `json:"query"`
	MatchMode      string       `json:"match_mode"`
	Project        string       `json:"project,omitempty"`
	AgentFilter    string       `json:"agent_filter,omitempty"`
	SinceMin       int          `json:"since_min"`
	Count          int          `json:"count"`
	Sessions       []SessionHit `json:"sessions"`
	ScopeTruncated bool         `json:"scope_truncated,omitempty"`
	Note           string       `json:"note,omitempty"`
}

// SessionHit is one matching session. Header fields mirror PeerSession
// so an agent that can read one list can read the other.
type SessionHit struct {
	SessionID       string    `json:"session_id"`
	AgentSource     string    `json:"agent_source,omitempty"`
	CWD             string    `json:"cwd,omitempty"`
	LastActivityAt  time.Time `json:"last_activity_at"`
	LastActivityAgo string    `json:"last_activity_ago,omitempty"`
	HitCount        int       `json:"hit_count"`
	Snippets        []Snippet `json:"snippets,omitempty"`
}

// Snippet is one matching message, cut to a window around the match so a
// 20 KB tool result doesn't land in the agent's context.
type Snippet struct {
	Timestamp   time.Time `json:"ts"`
	Role        string    `json:"role,omitempty"`
	MessageType string    `json:"message_type,omitempty"`
	Text        string    `json:"text"`
}

const snippetRadius = 120

// SearchSessions finds sessions whose conversation content matches
// `query`, scoped to a project / agent / time window.
//
// Order matters: the project + agent scope is resolved FIRST, then the
// keyword search runs inside that scope. Searching first and filtering
// afterwards loses results — a busy unrelated project can fill the
// candidate limit and blank out the current one — and it also makes the
// term→substring decision depend on other projects' data.
func (b *Bundler) SearchSessions(ctx context.Context, opts SearchOptions) (*SearchResults, error) {
	query := strings.TrimSpace(opts.Query)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	agent, err := b.resolveAgentAlias(opts.AgentSource)
	if err != nil {
		return nil, err
	}
	sinceMin := opts.SinceMin
	if sinceMin <= 0 {
		sinceMin = 7 * 24 * 60
	}
	limit := clampInt(opts.Limit, 1, 10, 5)
	snippetLimit := clampInt(opts.SnippetLimit, 1, 10, 3)

	res := &SearchResults{
		Query:       query,
		MatchMode:   MatchModeTerm,
		Project:     opts.Project,
		AgentFilter: agent,
		SinceMin:    sinceMin,
	}

	// Step 1 — scope. Skipped entirely when neither filter is set, in
	// which case the search runs over every session in the window.
	var sids []string
	if agent != "" || strings.TrimSpace(opts.Project) != "" {
		sids, err = b.searchScope(ctx, agent, opts.Project, sinceMin)
		if err != nil {
			return nil, err
		}
		if len(sids) == 0 {
			res.Note = "no sessions for this project/agent in the time window"
			return res, nil
		}
		res.ScopeTruncated = len(sids) == searchScopeCap
	}

	// Step 2 — search inside the scope, widening term→substring only
	// when the scoped term pass comes back empty.
	rows, err := b.searchCandidates(ctx, query, MatchModeTerm, sids, sinceMin, limit)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		res.MatchMode = MatchModeSubstring
		rows, err = b.searchCandidates(ctx, query, MatchModeSubstring, sids, sinceMin, limit)
		if err != nil {
			return nil, err
		}
	}
	if len(rows) == 0 {
		res.MatchMode = MatchModeTerm
		res.Note = "no sessions matched this query in the time window"
		return res, nil
	}

	hits := make([]SessionHit, 0, len(rows))
	for _, r := range rows {
		sid := stringAt(r, 0)
		if sid == "" {
			continue
		}
		last := time.UnixMilli(int64At(r, 1))
		hits = append(hits, SessionHit{
			SessionID:       sid,
			LastActivityAt:  last,
			LastActivityAgo: relativeAge(last),
			HitCount:        intAt(r, 2),
		})
	}

	// Step 3 — attribution (one query for all hits) + per-session
	// snippets (one query each, concurrently).
	b.attributeHits(ctx, hits)
	var wg sync.WaitGroup
	for i := range hits {
		wg.Add(1)
		go func(h *SessionHit) {
			defer wg.Done()
			h.Snippets = b.fetchSnippets(ctx, h.SessionID, query, res.MatchMode, snippetLimit)
		}(&hits[i])
	}
	wg.Wait()

	res.Sessions = hits
	res.Count = len(hits)
	return res, nil
}

// searchScope resolves the session_id set for project + agent + window.
// tma1_hook_events is the only table carrying cwd and agent_source.
func (b *Bundler) searchScope(ctx context.Context, agent, project string, sinceMin int) ([]string, error) {
	_, rows, err := b.client.Query(ctx, buildSearchScopeSQL(agent, project, sinceMin))
	if err != nil {
		return nil, fmt.Errorf("resolve search scope: %w", err)
	}
	sids := make([]string, 0, len(rows))
	for _, r := range rows {
		if s := stringAt(r, 0); s != "" {
			sids = append(sids, "'"+escapeSQL(s)+"'")
		}
	}
	return sids, nil
}

func (b *Bundler) searchCandidates(ctx context.Context, query, mode string, sids []string, sinceMin, limit int) ([][]any, error) {
	_, rows, err := b.client.Query(ctx, buildSearchCandidatesSQL(query, mode, sids, sinceMin, limit))
	if err != nil {
		return nil, fmt.Errorf("search sessions (%s): %w", mode, err)
	}
	return rows, nil
}

// sessionAttribution is who ran a session and where.
type sessionAttribution struct {
	AgentSource string
	CWD         string
}

// attributionFor resolves agent + working directory for each session id
// (already SQL-quoted).
//
// CWD is the directory carrying the most events, not `MAX(cwd)`. A
// session that moves between repositories has several cwd values, and
// the lexicographic maximum can be a directory it touched twice out of
// four hundred events — observed labelling an 11-hour session on this
// repo with a path it visited exactly twice.
func (b *Bundler) attributionFor(ctx context.Context, quotedSIDs []string) map[string]sessionAttribution {
	if len(quotedSIDs) == 0 {
		return nil
	}
	sql := fmt.Sprintf(
		`SELECT session_id, cwd, MAX(agent_source) AS agent_source, COUNT(*) AS events
		 FROM tma1_hook_events
		 WHERE session_id IN (%s)
		 GROUP BY session_id, cwd
		 ORDER BY events DESC, cwd`,
		strings.Join(quotedSIDs, ","),
	)
	_, rows, err := b.client.Query(ctx, sql)
	if err != nil {
		b.logger.Debug("attribution lookup failed", "err", err)
		return nil
	}
	out := make(map[string]sessionAttribution, len(rows))
	for _, r := range rows {
		sid := stringAt(r, 0)
		if sid == "" {
			continue
		}
		// Rows arrive most-frequent first, so the first non-empty cwd
		// per session wins. agent_source is constant per session, so any
		// row supplies it — including sessions whose only rows have no
		// cwd at all (Codex sets it on SessionStart only).
		attr := out[sid]
		if attr.AgentSource == "" {
			attr.AgentSource = stringAt(r, 2)
		}
		if attr.CWD == "" {
			attr.CWD = stringAt(r, 1)
		}
		out[sid] = attr
	}
	return out
}

// attributeHits fills AgentSource / CWD from tma1_hook_events. Sessions
// with no hook rows (JSONL-only ingest) keep empty attribution rather
// than being dropped — their content still matched.
func (b *Bundler) attributeHits(ctx context.Context, hits []SessionHit) {
	quoted := make([]string, 0, len(hits))
	for _, h := range hits {
		quoted = append(quoted, "'"+escapeSQL(h.SessionID)+"'")
	}
	byID := b.attributionFor(ctx, quoted)
	for i := range hits {
		if attr, ok := byID[hits[i].SessionID]; ok {
			hits[i].AgentSource = attr.AgentSource
			hits[i].CWD = attr.CWD
		}
	}
}

func (b *Bundler) fetchSnippets(ctx context.Context, sessionID, query, mode string, limit int) []Snippet {
	sql := fmt.Sprintf(
		`SELECT CAST(ts AS BIGINT) AS ts_ms, "role", message_type, content
		 FROM tma1_messages
		 WHERE session_id = '%s' AND %s AND %s
		 ORDER BY ts DESC LIMIT %d`,
		escapeSQL(sessionID), syntheticMessageFilter, matchClause(query, mode), limit,
	)
	_, rows, err := b.client.Query(ctx, sql)
	if err != nil {
		b.logger.Debug("search: snippet fetch failed", "session", sessionID, "err", err)
		return nil
	}
	out := make([]Snippet, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		r := rows[i]
		out = append(out, Snippet{
			Timestamp:   time.UnixMilli(int64At(r, 0)),
			Role:        stringAt(r, 1),
			MessageType: stringAt(r, 2),
			Text:        snippetAround(stringAt(r, 3), query),
		})
	}
	return out
}

// buildSearchScopeSQL lists candidate session_ids for a project/agent.
// Mirrors getPeerSessionsOneAgent's pass 1: cwd is matched over ALL
// events because some agents (Codex) only set it on SessionStart.
func buildSearchScopeSQL(agent, project string, sinceMin int) string {
	agentFilter := ""
	if agent != "" {
		agentFilter = fmt.Sprintf("AND agent_source = '%s' ", escapeSQL(agent))
	}
	return fmt.Sprintf(
		`SELECT DISTINCT session_id FROM tma1_hook_events
		 WHERE ts > now() - INTERVAL '%d minutes'
		   AND session_id IS NOT NULL AND session_id != ''
		   %s%s
		 LIMIT %d`,
		sinceMin, agentFilter, peerCwdFilter(project), searchScopeCap,
	)
}

// buildSearchCandidatesSQL ranks matching sessions by their most recent
// matching message. `sids` are already SQL-quoted; nil means "no scope
// restriction".
func buildSearchCandidatesSQL(query, mode string, sids []string, sinceMin, limit int) string {
	scope := ""
	if len(sids) > 0 {
		scope = fmt.Sprintf("AND session_id IN (%s) ", strings.Join(sids, ","))
	}
	return fmt.Sprintf(
		`SELECT session_id,
		        CAST(MAX(ts) AS BIGINT) AS last_ms,
		        COUNT(*) AS hits
		 FROM tma1_messages
		 WHERE ts > now() - INTERVAL '%d minutes'
		   AND %s
		   AND %s
		   %s
		 GROUP BY session_id
		 ORDER BY last_ms DESC
		 LIMIT %d`,
		sinceMin, syntheticMessageFilter, matchClause(query, mode), scope, limit,
	)
}

func matchClause(query, mode string) string {
	if mode == MatchModeSubstring {
		return fmt.Sprintf("content LIKE '%%%s%%'", escapeSQLLike(query))
	}
	return fmt.Sprintf("matches_term(content, '%s')", escapeSQL(query))
}

// snippetAround centres a window on the first occurrence of query.
// Term matching can hit on a tokenised form that isn't a literal
// substring, in which case we fall back to the head of the message.
func snippetAround(content, query string) string {
	width := snippetRadius*2 + len(query)
	if len(content) <= width {
		return content
	}
	idx := strings.Index(strings.ToLower(content), strings.ToLower(query))
	if idx < 0 {
		return strutil.SafeTruncate(content, width) + "…"
	}
	start := idx - snippetRadius
	if start < 0 {
		start = 0
	}
	// Walk both cuts back to rune boundaries; a window landing mid-rune
	// would emit invalid UTF-8.
	for start > 0 && !isRuneStart(content[start]) {
		start--
	}
	end := idx + len(query) + snippetRadius
	if end > len(content) {
		end = len(content)
	}
	for end < len(content) && !isRuneStart(content[end]) {
		end++
	}
	out := content[start:end]
	if start > 0 {
		out = "…" + out
	}
	if end < len(content) {
		out += "…"
	}
	return out
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// TranscriptOptions parameterises GetSessionTranscript.
type TranscriptOptions struct {
	SessionID    string
	MessageLimit int
	ContentChars int
	MessageType  string
	// Offset pages backwards through history: 0 is the newest page,
	// MessageLimit is the page before it, and so on.
	Offset int
}

// SessionTranscript is one page of a session's conversation plus the
// header needed to make sense of it.
type SessionTranscript struct {
	SessionID       string        `json:"session_id"`
	AgentSource     string        `json:"agent_source,omitempty"`
	CWD             string        `json:"cwd,omitempty"`
	StartedAt       time.Time     `json:"started_at,omitempty"`
	LastActivityAt  time.Time     `json:"last_activity_at,omitempty"`
	LastActivityAgo string        `json:"last_activity_ago,omitempty"`
	MessageCount    int           `json:"message_count"`
	Offset          int           `json:"offset"`
	HasMore         bool          `json:"has_more"`
	NextOffset      int           `json:"next_offset,omitempty"`
	Messages        []PeerMessage `json:"messages,omitempty"`
	Candidates      []string      `json:"candidates,omitempty"`
	Note            string        `json:"note,omitempty"`
}

// The <tma1-context> block abbreviates session ids to 8 characters, so
// 6 leaves room without inviting near-random matches.
const (
	minSessionIDPrefix     = 6
	maxSessionIDLen        = 128
	transcriptCandidateCap = 6
)

// GetSessionTranscript returns the conversation for one session.
// `SessionID` may be a full id or a prefix; ambiguous prefixes come
// back as a candidate list rather than a guess.
func (b *Bundler) GetSessionTranscript(ctx context.Context, opts TranscriptOptions) (*SessionTranscript, error) {
	raw := strings.TrimSpace(opts.SessionID)
	if raw == "" {
		return nil, fmt.Errorf("session_id is required")
	}
	if len(raw) < minSessionIDPrefix {
		return nil, fmt.Errorf("session_id %q is too short; give at least %d characters", raw, minSessionIDPrefix)
	}
	if len(raw) > maxSessionIDLen {
		return nil, fmt.Errorf("session_id is too long (max %d characters)", maxSessionIDLen)
	}
	msgType := strings.TrimSpace(opts.MessageType)
	limit := clampInt(opts.MessageLimit, 1, 200, 40)
	contentChars := clampInt(opts.ContentChars, 100, 8000, 1200)

	sid, candidates, err := b.resolveSessionID(ctx, raw)
	if err != nil {
		return nil, err
	}
	if sid == "" {
		out := &SessionTranscript{SessionID: raw, Candidates: candidates}
		switch {
		case len(candidates) > 1:
			out.Note = "prefix matches multiple sessions — call again with one of `candidates`"
			if len(candidates) == transcriptCandidateCap {
				out.Note += " (list truncated)"
			}
		default:
			out.Note = "no session found with this id or prefix"
		}
		return out, nil
	}

	offset := opts.Offset
	if offset < 0 {
		offset = 0
	}
	out := &SessionTranscript{SessionID: sid, Offset: offset}
	b.fillTranscriptHeader(ctx, out)

	// Fetch one extra row: its presence is what tells the caller there
	// is an older page, without a second COUNT query. The signal comes
	// from the raw row count, not the deduped slice — replayed JSONL
	// leaves duplicates, and losing the probe row to dedup would report
	// the end of history early.
	msgs, rawRows, err := b.fetchSessionMessagesPage(ctx, sid, limit+1, offset, contentChars, msgType)
	if err != nil {
		return nil, err
	}
	if rawRows > limit {
		out.HasMore = true
		out.NextOffset = offset + limit
	}
	if len(msgs) > limit {
		msgs = msgs[len(msgs)-limit:] // chronological order; drop the oldest
	}
	out.Messages = msgs
	out.MessageCount = len(msgs)
	if len(msgs) == 0 {
		out.Note = "no conversation content at this offset"
	}
	return out, nil
}

// resolveSessionID maps a full id or prefix to exactly one session.
// Exact match is tried first: a complete id that happens to be another
// id's prefix must not be reported as ambiguous.
func (b *Bundler) resolveSessionID(ctx context.Context, input string) (string, []string, error) {
	exact, err := b.distinctSessionIDs(ctx, fmt.Sprintf("session_id = '%s'", escapeSQL(input)), 1)
	if err != nil {
		return "", nil, err
	}
	if len(exact) > 0 {
		return input, nil, nil
	}
	prefixed, err := b.distinctSessionIDs(ctx,
		fmt.Sprintf("session_id LIKE '%s%%'", escapeSQLLike(input)), transcriptCandidateCap)
	if err != nil {
		return "", nil, err
	}
	if len(prefixed) == 1 {
		return prefixed[0], nil, nil
	}
	return "", prefixed, nil
}

// distinctSessionIDs runs `where` against both session tables. A
// session can exist in either one alone: hook events without messages
// (a tool-only session) or messages without hook events (JSONL-only
// ingest).
func (b *Bundler) distinctSessionIDs(ctx context.Context, where string, limit int) ([]string, error) {
	tables := []string{"tma1_hook_events", "tma1_messages"}
	var (
		mu   sync.Mutex
		seen = map[string]struct{}{}
		errs []error
		wg   sync.WaitGroup
	)
	for _, table := range tables {
		wg.Add(1)
		go func(table string) {
			defer wg.Done()
			sql := fmt.Sprintf(
				`SELECT DISTINCT session_id FROM %s WHERE %s LIMIT %d`,
				table, where, limit,
			)
			_, rows, err := b.client.Query(ctx, sql)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", table, err))
				return
			}
			for _, r := range rows {
				if s := stringAt(r, 0); s != "" {
					seen[s] = struct{}{}
				}
			}
		}(table)
	}
	wg.Wait()
	// Only fail when BOTH lookups failed; one healthy table is enough
	// to answer, and a partial answer beats an error here.
	if len(errs) == len(tables) {
		return nil, fmt.Errorf("resolve session id: %w", errs[0])
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// fillTranscriptHeader adds agent / cwd / timing, best-effort. A
// JSONL-only session has no hook rows, so timing falls back to the
// message table.
func (b *Bundler) fillTranscriptHeader(ctx context.Context, t *SessionTranscript) {
	hookSQL := fmt.Sprintf(
		`SELECT CAST(MIN(ts) AS BIGINT) AS started_ms,
		        CAST(MAX(ts) AS BIGINT) AS last_ms
		 FROM tma1_hook_events WHERE session_id = '%s'`,
		escapeSQL(t.SessionID),
	)
	if _, rows, err := b.client.Query(ctx, hookSQL); err == nil && len(rows) > 0 {
		t.StartedAt = time.UnixMilli(int64At(rows[0], 0))
		t.LastActivityAt = time.UnixMilli(int64At(rows[0], 1))
	}
	if attr, ok := b.attributionFor(ctx, []string{"'" + escapeSQL(t.SessionID) + "'"})[t.SessionID]; ok {
		t.AgentSource = attr.AgentSource
		t.CWD = attr.CWD
	}
	if t.LastActivityAt.IsZero() {
		msgSQL := fmt.Sprintf(
			`SELECT CAST(MIN(ts) AS BIGINT), CAST(MAX(ts) AS BIGINT)
			 FROM tma1_messages WHERE session_id = '%s'`,
			escapeSQL(t.SessionID),
		)
		if _, rows, err := b.client.Query(ctx, msgSQL); err == nil && len(rows) > 0 {
			t.StartedAt = time.UnixMilli(int64At(rows[0], 0))
			t.LastActivityAt = time.UnixMilli(int64At(rows[0], 1))
		}
	}
	t.LastActivityAgo = relativeAge(t.LastActivityAt)
}

// fetchSessionMessages returns the most recent `limit` messages of a
// session in chronological order, each capped at contentChars bytes.
// Shared by GetSessionTranscript and enrichPeerSession so the two can't
// disagree on what counts as a message.
func (b *Bundler) fetchSessionMessages(ctx context.Context, sessionID string, limit, contentChars int, messageType string) ([]PeerMessage, error) {
	msgs, _, err := b.fetchSessionMessagesPage(ctx, sessionID, limit, 0, contentChars, messageType)
	return msgs, err
}

// fetchSessionMessagesPage also returns the number of rows the database
// returned, before dedup — the caller needs that to detect another page.
func (b *Bundler) fetchSessionMessagesPage(ctx context.Context, sessionID string, limit, offset, contentChars int, messageType string) ([]PeerMessage, int, error) {
	typeFilter := ""
	if messageType != "" {
		typeFilter = fmt.Sprintf("AND message_type = '%s' ", escapeSQL(messageType))
	}
	offsetClause := ""
	if offset > 0 {
		offsetClause = fmt.Sprintf(" OFFSET %d", offset)
	}
	sql := fmt.Sprintf(
		`SELECT CAST(ts AS BIGINT) AS ts_ms,
		        message_type, "role", content, model, tool_name,
		        input_tokens, output_tokens
		 FROM tma1_messages
		 WHERE session_id = '%s'
		   AND %s
		   %s
		 ORDER BY ts DESC LIMIT %d%s`,
		escapeSQL(sessionID), syntheticMessageFilter, typeFilter, limit, offsetClause,
	)
	_, rows, err := b.client.Query(ctx, sql)
	if err != nil {
		return nil, 0, fmt.Errorf("fetch session messages: %w", err)
	}
	// Fetched DESC; flip to chronological for natural reading.
	msgs := make([]PeerMessage, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		r := rows[i]
		content := stringAt(r, 3)
		truncated := false
		if contentChars > 0 && len(content) > contentChars {
			content = strutil.SafeTruncate(content, contentChars)
			truncated = true
		}
		msgs = append(msgs, PeerMessage{
			Timestamp:    time.UnixMilli(int64At(r, 0)),
			MessageType:  stringAt(r, 1),
			Role:         stringAt(r, 2),
			Content:      content,
			Truncated:    truncated,
			Model:        stringAt(r, 4),
			ToolName:     stringAt(r, 5),
			InputTokens:  int64At(r, 6),
			OutputTokens: int64At(r, 7),
		})
	}
	return dedupPeerMessages(msgs), len(rows), nil
}

func clampInt(n, min, max, fallback int) int {
	if n <= 0 {
		return fallback
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}
