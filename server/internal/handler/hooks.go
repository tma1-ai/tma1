package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/tma1-ai/tma1/server/internal/perception"
)

// fileChangedDedup tracks last FileChanged timestamp per session+path to suppress duplicates.
// Key: "sessionID\x00filePath", Value: time.Time of last event.
var fileChangedDedup sync.Map

func init() {
	go func() {
		for {
			time.Sleep(5 * time.Minute)
			cutoff := time.Now().Add(-2 * time.Minute)
			fileChangedDedup.Range(func(key, value any) bool {
				if t, ok := value.(time.Time); ok && t.Before(cutoff) {
					fileChangedDedup.Delete(key)
				}
				return true
			})
		}
	}()
}

const (
	maxHookBody    = 1 << 20 // 1 MB
	maxToolInput   = 2048
	maxToolResult  = 4096
	maxHookMessage = 4096
	maxMetadata    = 8192
)

// knownHookFields are the fields extracted into dedicated columns.
// Everything else goes into the metadata JSON column.
var knownHookFields = map[string]bool{
	"session_id":      true,
	"hook_event_name": true,
	"tool_name":       true,
	"tool_input":      true,
	"tool_use_id":     true,
	"tool_response":   true,
	"agent_id":        true,
	"agent_type":      true,
	"notification_type": true,
	"message":         true,
	"title":           true,
	"cwd":             true,
	"transcript_path": true,
	"permission_mode": true,
}

// hookPayload holds the parsed hook event with known fields + extra metadata.
type hookPayload struct {
	SessionID        string
	HookEventName    string
	ToolName         string
	ToolInput        any
	ToolUseID        string
	ToolResponse     any
	AgentID          string
	AgentType        string
	NotificationType string
	Message          string
	Title            string
	CWD              string
	TranscriptPath   string
	PermissionMode   string
	Metadata         string // JSON blob of event-specific fields not in dedicated columns.
}

// parseHookPayload unmarshals the raw JSON into a hookPayload,
// extracting known fields into dedicated struct fields and collecting
// the rest into a metadata JSON string.
func parseHookPayload(body []byte) (hookPayload, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return hookPayload{}, err
	}

	p := hookPayload{
		SessionID:        getString(raw, "session_id"),
		HookEventName:    getString(raw, "hook_event_name"),
		ToolName:         getString(raw, "tool_name"),
		ToolInput:        raw["tool_input"],
		ToolUseID:        getString(raw, "tool_use_id"),
		ToolResponse:     raw["tool_response"],
		AgentID:          getString(raw, "agent_id"),
		AgentType:        getString(raw, "agent_type"),
		NotificationType: getString(raw, "notification_type"),
		Message:          getString(raw, "message"),
		Title:            getString(raw, "title"),
		CWD:              getString(raw, "cwd"),
		TranscriptPath:   getString(raw, "transcript_path"),
		PermissionMode:   getString(raw, "permission_mode"),
	}

	// Collect remaining fields into metadata.
	// Truncate individual string values so the marshalled JSON is always valid.
	extra := make(map[string]any)
	for k, v := range raw {
		if !knownHookFields[k] {
			if s, ok := v.(string); ok {
				extra[k] = truncateStr(s, 2048)
			} else {
				extra[k] = v
			}
		}
	}
	if len(extra) > 0 {
		b, _ := json.Marshal(extra)
		if len(b) <= maxMetadata {
			p.Metadata = string(b)
		} else {
			// Re-marshal with aggressive truncation to stay within limit.
			for ek, ev := range extra {
				if s, ok := ev.(string); ok && len(s) > 256 {
					extra[ek] = truncateStr(s, 256)
				}
			}
			b2, _ := json.Marshal(extra)
			if len(b2) <= maxMetadata {
				p.Metadata = string(b2)
			}
			// else: drop metadata entirely — too many non-string fields
		}
	}

	return p, nil
}

func getString(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

// handleHooks receives Claude Code / Codex hook events and stores them in GreptimeDB.
//
// The response body is the hook script's stdout — Claude Code uses it as
// injection content (UserPromptSubmit prepend, PostToolUse append, Stop
// block JSON). Most events return an empty body. DB writes still happen
// asynchronously so the response stays fast.
func (s *Server) handleHooks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")

	body, err := io.ReadAll(io.LimitReader(r.Body, maxHookBody))
	if err != nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	payload, err := parseHookPayload(body)
	if err != nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	if payload.SessionID == "" || payload.HookEventName == "" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Detect agent source from query param (default: claude_code).
	agentSource := r.URL.Query().Get("source")
	if agentSource == "" {
		agentSource = "claude_code"
	}

	// Normalize tool_response to string.
	toolResult := normalizeToolResponse(payload.ToolResponse)

	// Serialize tool_input to JSON string.
	toolInput := serializeToolInput(payload.ToolInput)

	// CC does not emit PostToolUseFailure natively — derive it from the
	// PostToolUse payload when tool_response carries a failure marker.
	// Anomaly rules (R-build-broken-mine, R-repeated-failed-build) query
	// `event_type = 'PostToolUseFailure'` directly; this is the only place
	// that synthetic kind enters the data path for native CC hooks.
	if payload.HookEventName == "PostToolUse" && isToolFailure(payload.ToolResponse) {
		payload.HookEventName = "PostToolUseFailure"
	}

	// Start transcript watcher on first event for this session.
	if s.transcriptWatcher != nil && payload.TranscriptPath != "" {
		s.transcriptWatcher.Watch(payload.SessionID, payload.TranscriptPath)
	}

	// Stop transcript watcher on session end.
	if s.transcriptWatcher != nil &&
		(payload.HookEventName == "SessionEnd" || payload.HookEventName == "Stop") {
		// Delay slightly to let final JSONL lines flush.
		go func() {
			time.Sleep(2 * time.Second)
			s.transcriptWatcher.Stop(payload.SessionID)
		}()
	}

	// Deduplicate FileChanged events: skip persist + broadcast if same session+path within 1s.
	skipPersist := false
	if payload.HookEventName == "FileChanged" {
		meta := hookMeta(payload)
		fp, _ := meta["file_path"].(string)
		if fp != "" {
			key := payload.SessionID + "\x00" + fp
			now := time.Now()
			if prev, ok := fileChangedDedup.Load(key); ok {
				if now.Sub(prev.(time.Time)) < time.Second {
					skipPersist = true
				}
			}
			if !skipPersist {
				fileChangedDedup.Store(key, now)
			}
		}
	}

	if !skipPersist {
		// Async INSERT into GreptimeDB — never blocks the response. Skipped
		// in test mode (greptimeHTTPPort == 0) to keep test data out of the
		// real database.
		if s.greptimeHTTPPort > 0 {
			go s.insertHookEvent(payload, agentSource, toolInput, toolResult)
		}

		// Broadcast to SSE subscribers for live canvas.
		if s.hookBroadcast != nil {
			s.hookBroadcast.Broadcast(body)
		}

		// Invalidate cached anomalies for this session — the new event may
		// flip a rule's verdict (e.g. a fresh Bash failure escalates an
		// existing file_loop_edit from MEDIUM to HIGH).
		if s.bundler != nil {
			s.bundler.Detector().Invalidate(payload.SessionID)
		}

		// Lazily attach a git/file watcher to this project so subsequent
		// external changes are recorded. Observe is idempotent + cheap if
		// the project is already being watched.
		if s.gitSensor != nil && payload.CWD != "" {
			s.gitSensor.Observe(payload.CWD)
		}
		// Refresh the project-state index (TTL-gated; no-op if recent).
		// SessionStart needs the row committed BEFORE we build the injection
		// bundle — otherwise the bundler's SELECT races the write and the
		// agent sees empty project_state on a cold session. Other events
		// fire-and-forget: the next bundle query will pick up the row.
		if s.projectSensor != nil && payload.CWD != "" {
			if payload.HookEventName == "SessionStart" {
				s.projectSensor.IndexAndWait(payload.CWD, 300*time.Millisecond)
			} else {
				s.projectSensor.Index(payload.CWD)
			}
		}
	}

	// Generate injection content (event-type specific). Bounded to <1KB so
	// the curl-side timeout (0.5s) is plausible even on slow days.
	injection := s.generateInjection(r.Context(), payload, body)
	if s.hookTelemetry != nil {
		s.hookTelemetry.record(payload.HookEventName, injection != "")
	}
	w.WriteHeader(http.StatusOK)
	if injection != "" {
		_, _ = w.Write([]byte(injection))
	}

	// File-callback refresh runs after response so it never delays the hook.
	// Default off — the file is a fallback for non-MCP agents (Aider,
	// Cursor) which haven't landed yet. MCP users get the same data via
	// the hook injection + MCP pull; the file mostly adds IO + git sensor
	// self-noise. Re-enable with TMA1_ENABLE_FILE_CALLBACK=1.
	if s.fileWriter != nil && payload.CWD != "" && os.Getenv("TMA1_ENABLE_FILE_CALLBACK") == "1" {
		go s.refreshContextFile(payload.SessionID, payload.CWD)
	}
}

// generateInjection returns the stdout the hook script should emit for this
// event. Returns empty when:
//   - injection is globally disabled (TMA1_DISABLE_INJECTION=1)
//   - the event type has no injection handler in this phase
//   - the bundle is empty (no observed session)
//   - generation fails (logged, never propagated to the agent)
//
// Phase 0.1 implements UserPromptSubmit (bundle summary) and Stop (no-op
// with stop_hook_active loop guard).
// Phase 0.2 adds the PostToolUse dispatch plumbing — the handler currently
// returns empty (or a debug marker if TMA1_DEBUG_POSTTOOLUSE=1) and will be
// wired to the anomaly engine in Phase 0.3.
func (s *Server) generateInjection(ctx context.Context, payload hookPayload, raw []byte) string {
	if os.Getenv("TMA1_DISABLE_INJECTION") == "1" {
		return ""
	}

	// Bound the bundle query so a slow GreptimeDB cannot exceed the hook
	// script's curl timeout.
	ctx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
	defer cancel()

	switch payload.HookEventName {
	case "UserPromptSubmit":
		if s.bundler == nil {
			return ""
		}
		bundle := s.bundler.BuildBundle(ctx, payload.SessionID, payload.CWD)
		// Suppress identical context turn after turn — the biggest noise
		// source in dogfood. Counters that change every turn (duration,
		// tokens) are excluded from the digest, so a turn that only
		// advances those is correctly treated as "unchanged".
		if s.injectionCache != nil && !s.injectionCache.IfChanged(payload.SessionID, bundle.Digest()) {
			return ""
		}
		return bundle.RenderSummary()

	case "Stop":
		// CC sends stop_hook_active=true when re-entering after a previous
		// block. Skip block logic to avoid infinite loops.
		var rawMap map[string]any
		if err := json.Unmarshal(raw, &rawMap); err == nil {
			if active, _ := rawMap["stop_hook_active"].(bool); active {
				return ""
			}
		}
		return s.generateStopInjection(ctx, payload)

	case "PostToolUse":
		return s.generatePostToolUseInjection(ctx, payload)

	case "SessionStart":
		return s.generateSessionStartInjection(ctx, payload)

	case "PreCompact":
		return s.generatePreCompactInjection(ctx, payload)

	default:
		return ""
	}
}

// generatePreCompactInjection runs immediately before CC compacts old turns
// into a condensed summary. This is the last chance to push perception
// state into the part of context that survives compaction: anything we
// emit here gets folded into CC's "what to remember" summary.
//
// Unlike UserPromptSubmit we deliberately bypass the digest dedup — even
// identical content matters here because the agent is about to forget it.
func (s *Server) generatePreCompactInjection(ctx context.Context, payload hookPayload) string {
	if s.bundler == nil {
		return ""
	}
	bundle := s.bundler.BuildBundle(ctx, payload.SessionID, payload.CWD)
	summary := bundle.RenderSummary()
	if summary == "" {
		return ""
	}
	// Seed the dedup cache so the FIRST UserPromptSubmit after compaction
	// doesn't immediately repeat what we just preserved.
	if s.injectionCache != nil {
		s.injectionCache.IfChanged(payload.SessionID, bundle.Digest())
	}
	// Frame the block so the agent treats it as carry-forward, not a turn-
	// boundary snapshot. CC folds the text below into the compaction
	// summary verbatim.
	return "Preserve through compaction — current session state:\n" + summary
}

// generateSessionStartInjection runs when CC starts a new session. The
// agent has a fresh context window — this is the cheapest moment to give
// it project orientation + any external changes that happened while no
// session was active.
//
// Returns the same `<tma1-context>` markdown the UserPromptSubmit hook
// emits. CC prepends it to the session's first user prompt so the agent
// sees it before its first reasoning step.
func (s *Server) generateSessionStartInjection(ctx context.Context, payload hookPayload) string {
	if s.bundler == nil {
		return ""
	}
	// At SessionStart we don't have a session_id for THIS session yet
	// (payload.SessionID is the new session about to begin). BuildBundle
	// will fall back to "latest session for cwd" — typically the prior
	// session, giving the new one continuity. If there's no prior session
	// we still get project state + recent external changes, which is the
	// main value-add for a cold new session.
	bundle := s.bundler.BuildBundle(ctx, "", payload.CWD)
	// Always inject for SessionStart but seed the cache so the FIRST
	// UserPromptSubmit of this new session doesn't re-emit identical
	// content right after.
	if s.injectionCache != nil {
		s.injectionCache.IfChanged(payload.SessionID, bundle.Digest())
	}
	return bundle.RenderSummary()
}

// generateStopInjection returns a JSON block-decision when there are
// stop-channel anomalies pending; an empty string otherwise (no block).
//
// Phase 1.7: rules tag themselves with Channel="stop_block" when they
// warrant blocking Stop. This replaces the old "block on any HIGH" logic
// — now a rule can be HIGH but route to UserPromptSubmit instead, so we
// don't trap the agent on issues that aren't immediate harm.
func (s *Server) generateStopInjection(ctx context.Context, payload hookPayload) string {
	if s.bundler == nil {
		return ""
	}
	blocking := s.bundler.Detector().DetectByChannel(ctx, payload.SessionID, perception.ChannelStopBlock)
	if len(blocking) == 0 {
		return ""
	}
	reason := summarizeAnomalies(blocking)
	out, err := json.Marshal(map[string]any{
		"decision": "block",
		"reason":   reason,
	})
	if err != nil {
		s.logger.Debug("stop injection marshal failed", "err", err)
		return ""
	}
	return string(out)
}

// generatePostToolUseInjection emits a per-tool-result note ONLY when an
// anomaly rule has explicitly routed to ChannelPostToolUse. Empty
// otherwise so tool results aren't polluted.
//
// Phase 1.7 rationale: R-stale-view (the "file changed externally" signal)
// now lives in the anomaly engine and reaches the agent via next-turn
// UserPromptSubmit; emitting the same warning here too created visible
// duplicates in dogfood. No current rule uses ChannelPostToolUse, so
// PostToolUse is silent unless a rule explicitly opts in.
//
// TMA1_DEBUG_POSTTOOLUSE=1 emits a marker regardless — plumbing aid.
func (s *Server) generatePostToolUseInjection(ctx context.Context, payload hookPayload) string {
	if os.Getenv("TMA1_DEBUG_POSTTOOLUSE") == "1" {
		return fmt.Sprintf("[tma1] PostToolUse observed: tool=%s session=%s",
			payload.ToolName, abbrev(payload.SessionID, 8))
	}
	if s.bundler == nil {
		return ""
	}
	anomalies := s.bundler.Detector().DetectByChannel(ctx, payload.SessionID, perception.ChannelPostToolUse)
	top := topAnomaly(anomalies)
	if top == nil {
		return ""
	}
	return fmt.Sprintf("ℹ️ tma1 [%s] %s — %s",
		strings.ToUpper(top.Severity), top.Kind, top.Suggestion)
}

// topAnomaly returns the most severe anomaly (HIGH > MEDIUM > LOW), or nil.
// Used to keep PostToolUse injection at a single line.
func topAnomaly(anomalies []perception.Anomaly) *perception.Anomaly {
	if len(anomalies) == 0 {
		return nil
	}
	rank := func(s string) int {
		switch s {
		case perception.SeverityHigh:
			return 3
		case perception.SeverityMedium:
			return 2
		case perception.SeverityLow:
			return 1
		}
		return 0
	}
	best := &anomalies[0]
	for i := 1; i < len(anomalies); i++ {
		if rank(anomalies[i].Severity) > rank(best.Severity) {
			best = &anomalies[i]
		}
	}
	return best
}

// summarizeAnomalies turns a list of anomalies into a compact, human-readable
// reason string for the Stop block JSON.
//
// Anomalies are grouped by Kind so the agent gets one bullet per *kind* of
// issue (not per affected file). For each kind we list up to 3 related
// files inline; further hits are summarised as "+N more".
func summarizeAnomalies(anomalies []perception.Anomaly) string {
	if len(anomalies) == 0 {
		return ""
	}

	type group struct {
		kind       string
		suggestion string // first non-empty suggestion in the group
		files      []string
		extra      int // related anomalies beyond what we list inline
	}
	byKind := map[string]*group{}
	order := []string{} // preserve first-seen order for stable output
	for _, a := range anomalies {
		g, ok := byKind[a.Kind]
		if !ok {
			g = &group{kind: a.Kind, suggestion: a.Suggestion}
			byKind[a.Kind] = g
			order = append(order, a.Kind)
		}
		if g.suggestion == "" {
			g.suggestion = a.Suggestion
		}
		g.files = append(g.files, a.RelatedFiles...)
	}

	parts := make([]string, 0, len(order))
	for _, k := range order {
		g := byKind[k]
		segment := g.kind + ": " + g.suggestion
		if len(g.files) > 0 {
			listed := g.files
			if len(listed) > 3 {
				g.extra = len(listed) - 3
				listed = listed[:3]
			}
			segment += " (" + strings.Join(listed, ", ")
			if g.extra > 0 {
				segment += fmt.Sprintf(" +%d more", g.extra)
			}
			segment += ")"
		}
		parts = append(parts, segment)
	}

	return fmt.Sprintf("tma1 detected %d high-severity issue(s) — address before stopping. %s",
		len(anomalies), strings.Join(parts, " | "))
}

// abbrev returns the first n bytes of s, used for compact session IDs in logs.
func abbrev(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// refreshContextFile rewrites .tma1-context.md in the project root. Errors are
// logged but never surfaced — the file is a best-effort fallback.
func (s *Server) refreshContextFile(sessionID, cwd string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := s.fileWriter.Write(ctx, sessionID, cwd); err != nil {
		s.logger.Debug("tma1-context.md refresh failed", "err", err, "cwd", cwd)
	}
}

func (s *Server) insertHookEvent(p hookPayload, agentSource, toolInput, toolResult string) {
	now := time.Now().UnixMilli()

	// Phase 1.4: extract derived structured fields at ingest time so
	// downstream queries don't need to regex/parse_json the raw blob.
	// Extraction is best-effort — failures yield empty strings and we
	// fall back to the raw column.
	filePath, cmdPrefix, success, errSummary := extractDerivedFields(p, toolInput, toolResult)

	sql := fmt.Sprintf(
		"INSERT INTO tma1_hook_events "+
			"(ts, session_id, event_type, agent_source, tool_name, tool_input, tool_result, "+
			"tool_use_id, agent_id, agent_type, notification_type, \"message\", cwd, transcript_path, "+
			"permission_mode, metadata, tool_file_path, tool_command_prefix, tool_success, tool_error_summary) "+
			"VALUES (%d, '%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s', %s, %s, %s, %s)",
		now,
		escapeSQLString(p.SessionID),
		escapeSQLString(p.HookEventName),
		escapeSQLString(agentSource),
		escapeSQLString(truncateStr(p.ToolName, 256)),
		escapeSQLString(truncateStr(toolInput, maxToolInput)),
		escapeSQLString(truncateStr(toolResult, maxToolResult)),
		escapeSQLString(p.ToolUseID),
		escapeSQLString(p.AgentID),
		escapeSQLString(truncateStr(p.AgentType, 256)),
		escapeSQLString(truncateStr(p.NotificationType, 256)),
		escapeSQLString(truncateStr(p.Message, maxHookMessage)),
		escapeSQLString(truncateStr(p.CWD, 512)),
		escapeSQLString(truncateStr(p.TranscriptPath, 512)),
		escapeSQLString(truncateStr(p.PermissionMode, 64)),
		escapeSQLString(p.Metadata),
		nullableString(filePath, 512),
		nullableString(cmdPrefix, 200),
		nullableBool(success),
		nullableString(errSummary, 400),
	)

	sqlURL := fmt.Sprintf("http://localhost:%d/v1/sql", s.greptimeHTTPPort)
	form := url.Values{}
	form.Set("sql", sql)

	resp, err := s.httpClient.Post(sqlURL, "application/x-www-form-urlencoded", strings.NewReader(form.Encode())) //nolint:gosec
	if err != nil {
		s.logger.Debug("hook event insert failed", "error", err, "event", p.HookEventName)
		return
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		s.logger.Debug("hook event insert non-200", "status", resp.StatusCode, "event", p.HookEventName)
	}
}

// fileInputRE / commandInputRE extract the most common tool_input fields.
// We use regex rather than json.Unmarshal because the raw blob is often
// truncated to 2 KB and may not parse — best-effort lift of the leading
// fields is more robust than all-or-nothing JSON parsing.
var (
	fileInputRE    = regexp.MustCompile(`"file_path"\s*:\s*"([^"]+)"`)
	commandInputRE = regexp.MustCompile(`"command"\s*:\s*"((?:[^"\\]|\\.)*)"`)
)

// extractDerivedFields lifts file_path / command prefix / success / error
// summary out of a hook payload so downstream queries can WHERE on them
// without re-parsing JSON.
//
// Returns ("","",nil,"") for events that don't have these signals.
func extractDerivedFields(p hookPayload, toolInput, toolResult string) (filePath, cmdPrefix string, success *bool, errSummary string) {
	// File path: applies to Edit / Write / Read / MultiEdit (anything that
	// takes a file_path arg). Regex lift handles truncated blobs.
	if m := fileInputRE.FindStringSubmatch(toolInput); len(m) >= 2 {
		filePath = m[1]
	}

	// Command prefix: first 200 chars of Bash / exec_command. Quote escapes
	// in the JSON ("\n", '\"') are unescaped to keep the column readable.
	if p.ToolName == "Bash" || p.ToolName == "exec_command" {
		if m := commandInputRE.FindStringSubmatch(toolInput); len(m) >= 2 {
			cmd := unescapeJSONString(m[1])
			if len(cmd) > 200 {
				cmd = cmd[:200]
			}
			cmdPrefix = cmd
		}
	}

	// Success / error summary: PostToolUse / PostToolUseFailure tell us
	// directly; the result body is the error text for failures.
	switch p.HookEventName {
	case "PostToolUse":
		t := true
		success = &t
	case "PostToolUseFailure":
		f := false
		success = &f
		errSummary = firstNonEmpty(toolResult, p.Message)
		if len(errSummary) > 400 {
			errSummary = errSummary[:400]
		}
	}
	return
}

// unescapeJSONString reverses the common JSON string escapes so the
// command stored in tool_command_prefix is human-readable. Best-effort:
// unknown escapes pass through unchanged.
func unescapeJSONString(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			next := s[i+1]
			switch next {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '"', '\\', '/':
				b.WriteByte(next)
			default:
				b.WriteByte(c)
				b.WriteByte(next)
			}
			i++
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// nullableString renders v as a SQL literal, or NULL when empty. Also
// truncates to maxLen to keep individual rows bounded.
func nullableString(v string, maxLen int) string {
	if v == "" {
		return "NULL"
	}
	if len(v) > maxLen {
		v = v[:maxLen]
	}
	return "'" + escapeSQLString(v) + "'"
}

// nullableBool renders *bool as TRUE/FALSE or NULL.
func nullableBool(b *bool) string {
	if b == nil {
		return "NULL"
	}
	if *b {
		return "TRUE"
	}
	return "FALSE"
}

// isToolFailure inspects the structured tool_response and returns true when
// CC has signalled an error. The check is conservative — we only flip a
// PostToolUse event to PostToolUseFailure when there's a clear marker:
//
//   - `isError: true` or `is_error: true` (MCP / generic convention)
//   - `success: false` (Copilot CLI style, defensive — CC also uses it)
//   - non-empty `error` string field
//   - `interrupted: true` (CC's signal for an aborted call)
//   - `code` / `exitCode` is a non-zero number (Bash specifically)
//
// Returning false for ambiguous / missing markers means the event stays
// PostToolUse, which is the safe default: false negatives only miss an
// anomaly trigger, false positives would feed the engine garbage.
func isToolFailure(toolResponse any) bool {
	m, ok := toolResponse.(map[string]any)
	if !ok {
		return false
	}
	if v, ok := m["isError"].(bool); ok && v {
		return true
	}
	if v, ok := m["is_error"].(bool); ok && v {
		return true
	}
	if v, ok := m["success"].(bool); ok && !v {
		return true
	}
	if v, ok := m["interrupted"].(bool); ok && v {
		return true
	}
	if s, ok := m["error"].(string); ok && strings.TrimSpace(s) != "" {
		return true
	}
	// Bash exit-code style. JSON unmarshals numbers as float64.
	if v, ok := m["code"].(float64); ok && v != 0 {
		return true
	}
	if v, ok := m["exitCode"].(float64); ok && v != 0 {
		return true
	}
	return false
}

// normalizeToolResponse converts tool_response (string | {content} | [{text}]) to a plain string.
func normalizeToolResponse(v any) string {
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case map[string]any:
		if c, ok := val["content"]; ok {
			if s, ok := c.(string); ok {
				return s
			}
		}
	case []any:
		var parts []string
		for _, item := range val {
			if m, ok := item.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	// Fallback: marshal to JSON.
	b, _ := json.Marshal(v)
	return string(b)
}

// serializeToolInput converts tool_input to a JSON string.
func serializeToolInput(v any) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

func escapeSQLString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// hookMeta parses the metadata JSON string from a hookPayload.
func hookMeta(p hookPayload) map[string]any {
	if p.Metadata == "" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(p.Metadata), &m); err != nil {
		return nil
	}
	return m
}
