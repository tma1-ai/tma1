package transcript

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tma1-ai/tma1/server/internal/derive"
	"github.com/tma1-ai/tma1/server/internal/sqlutil"
)

// nullableBool renders *bool as TRUE/FALSE/NULL SQL literal.
// Local to transcript package; sqlutil.Quote handles string columns.
func nullableBool(b *bool) string {
	if b == nil {
		return "NULL"
	}
	if *b {
		return "TRUE"
	}
	return "FALSE"
}

const (
	codexScanInterval = 5 * time.Second
	codexActiveAge    = 10 * time.Minute // only watch files modified within this window
)

// StartCodexScanner periodically scans ~/.codex/sessions/ for active JSONL files
// and starts watching any new ones. This remains a fallback/backfill path for
// Codex sessions that are not surfaced through hooks.
func (w *Watcher) StartCodexScanner(ctx context.Context) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		w.logger.Warn("codex scanner: cannot determine home directory", "error", err)
		return
	}
	codexDir := filepath.Join(homeDir, ".codex", "sessions")
	w.logger.Info("codex session scanner started", "path", codexDir)

	ticker := time.NewTicker(codexScanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Directory may not exist yet on fresh installs; keep polling.
			if _, err := os.Stat(codexDir); err == nil {
				w.scanCodexSessions(codexDir)
			}
		}
	}
}

func (w *Watcher) scanCodexSessions(baseDir string) {
	now := time.Now()

	// Prune stopped codex watcher entries to prevent unbounded memory growth.
	// Keep recent stopped entries (their seen maps prevent re-insertion on restart).
	// Only prune when count exceeds threshold — old sessions from prior days.
	w.mu.Lock()
	var stoppedCount int
	for key, sw := range w.sessions {
		if sw.stopped && strings.HasPrefix(key, "codex:") {
			stoppedCount++
		}
	}
	if stoppedCount > 50 {
		for key, sw := range w.sessions {
			if sw.stopped && strings.HasPrefix(key, "codex:") {
				delete(w.sessions, key)
			}
		}
	}
	w.mu.Unlock()

	// Walk today's and yesterday's date dirs to find active JSONL files.
	//
	// Two passes per directory: first peek every new file's session_meta
	// (synchronous, one line per file) to publish each main session's
	// conversation UUID, then start the tail goroutines. Without this
	// pre-pass the subagent goroutine can race ahead of the parent
	// goroutine on a restart — `lookupCodexParentSession` returns ""
	// and the subagent's lifecycle rows fall back to the filename
	// prefix instead of attaching to the parent's UUID.
	for _, offset := range []int{0, -1} {
		d := now.AddDate(0, 0, offset)
		dir := filepath.Join(baseDir, d.Format("2006"), d.Format("01"), d.Format("02"))
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}

		type pending struct {
			watcherKey, sessionID, filePath string
		}
		var queue []pending
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
				continue
			}
			info, err := entry.Info()
			if err != nil || now.Sub(info.ModTime()) > codexActiveAge {
				continue
			}
			// Group files from the same Codex run by extracting the timestamp prefix
			// from the filename. Format: rollout-YYYY-MM-DDTHH-MM-SS-<uuid>.jsonl
			// Files from the same run share the timestamp prefix but have different UUIDs
			// (main session vs subagent).
			baseName := strings.TrimSuffix(entry.Name(), ".jsonl")
			sessionID := codexSessionGroup(baseName)
			watcherKey := "codex:" + baseName
			filePath := filepath.Join(dir, entry.Name())

			// Skip the peek for files we're already watching — their
			// goroutine will (or already did) publish the UUID via
			// processCodexLine.
			w.mu.Lock()
			_, watched := w.sessions[watcherKey]
			w.mu.Unlock()
			if !watched {
				if uuid, isMain := peekCodexMainUUID(filePath); isMain && uuid != "" {
					w.recordCodexParentSession(sessionID, uuid)
				}
			}
			queue = append(queue, pending{watcherKey, sessionID, filePath})
		}

		// Pass 2: start goroutines now that every main session's UUID
		// is in the parent-session map.
		for _, p := range queue {
			w.watchCodex(p.watcherKey, p.sessionID, p.filePath)
		}
	}
}

// WatchCodex starts tailing a specific Codex JSONL transcript path reported by
// a Codex hook. Unlike Watch, this routes lines through the Codex rollout parser
// instead of the Claude Code transcript parser.
func (w *Watcher) WatchCodex(sessionID, transcriptPath string) {
	watcherKey, parserSessionID := codexWatcherIdentity(transcriptPath)
	if watcherKey == "" {
		return
	}
	if parserSessionID == "" {
		parserSessionID = sessionID
	}
	if uuid, isMain := peekCodexMainUUID(transcriptPath); isMain && uuid != "" {
		w.recordCodexParentSession(parserSessionID, uuid)
	}
	w.watchCodex(watcherKey, parserSessionID, transcriptPath)
}

// StopCodex stops a Codex transcript watcher previously started for a concrete
// transcript path. Codex watchers are keyed by rollout filename so they dedupe
// with the ~/.codex scanner, not by hook session_id.
func (w *Watcher) StopCodex(transcriptPath string) {
	watcherKey, _ := codexWatcherIdentity(transcriptPath)
	if watcherKey == "" {
		return
	}
	w.Stop(watcherKey)
}

func codexWatcherIdentity(transcriptPath string) (watcherKey, parserSessionID string) {
	baseName := strings.TrimSuffix(filepath.Base(transcriptPath), ".jsonl")
	if baseName == "" || baseName == "." || baseName == string(filepath.Separator) {
		return "", ""
	}
	return "codex:" + baseName, codexSessionGroup(baseName)
}

// peekCodexMainUUID opens a Codex rollout file, reads only the first
// JSON line, and returns (uuid, true) when the line is a session_meta
// event for a MAIN session (no source.subagent). Used by the scanner
// to pre-publish parent UUIDs before any subagent goroutine starts,
// closing the lookupCodexParentSession race.
//
// Best-effort: any IO or parse failure returns ("", false), and the
// caller falls back to the in-line publish path inside processCodexLine.
func peekCodexMainUUID(filePath string) (string, bool) {
	f, err := os.Open(filePath) //nolint:gosec
	if err != nil {
		return "", false
	}
	defer f.Close()
	reader := bufio.NewReader(f)
	// session_meta line is small (~200 bytes). 8KB cap is generous.
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return "", false
	}
	var ev codexEvent
	if json.Unmarshal([]byte(strings.TrimSpace(line)), &ev) != nil || ev.Type != "session_meta" {
		return "", false
	}
	var meta struct {
		ID     string          `json:"id"`
		Source json.RawMessage `json:"source"`
	}
	if json.Unmarshal(ev.Payload, &meta) != nil || meta.ID == "" {
		return "", false
	}
	var subSource struct {
		Subagent string `json:"subagent"`
	}
	if json.Unmarshal(meta.Source, &subSource) == nil && subSource.Subagent != "" {
		return "", false // subagent file, skip
	}
	return meta.ID, true
}

// codexSessionGroup extracts the timestamp prefix from a Codex JSONL filename.
// "rollout-2026-03-27T18-10-59-019d2ec6-958f-..." → "rollout-2026-03-27T18-10-59"
// This groups main session + subagent files into one session.
func codexSessionGroup(baseName string) string {
	// Extract timestamp prefix by finding the 3rd hyphen after 'T'.
	// "rollout-2026-03-27T18-10-59-<uuid>" → "rollout-2026-03-27T18-10-59"
	tIdx := strings.IndexByte(baseName, 'T')
	if tIdx == -1 {
		return baseName
	}
	dashCount := 0
	for i := tIdx + 1; i < len(baseName); i++ {
		if baseName[i] == '-' {
			dashCount++
			if dashCount == 3 {
				return baseName[:i]
			}
		}
	}
	return baseName
}

func (w *Watcher) watchCodex(watcherKey, sessionID, filePath string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	existing, ok := w.sessions[watcherKey]
	if ok && !existing.stopped {
		return // already watching this file
	}

	// Reuse existing seen map to avoid re-inserting previously processed lines.
	var seen map[string]struct{}
	if ok && existing.seen != nil {
		seen = existing.seen
	} else {
		seen = make(map[string]struct{})
	}

	ctx, cancel := context.WithCancel(context.Background())
	sw := &sessionWatch{cancel: cancel, seen: seen}
	w.sessions[watcherKey] = sw

	go w.tailCodexFile(ctx, watcherKey, sessionID, filePath, seen)
	w.logger.Info("watching codex session", "session", sessionID, "file", filePath)
}

// tailCodexFile reads a Codex JSONL session file and inserts events into GreptimeDB.
func (w *Watcher) tailCodexFile(ctx context.Context, watcherKey, sessionID, filePath string, seen map[string]struct{}) {
	// Mark as stopped on exit so scanner can restart with preserved seen map.
	defer func() {
		w.mu.Lock()
		if sw, ok := w.sessions[watcherKey]; ok {
			sw.stopped = true
		}
		w.mu.Unlock()
	}()
	var f *os.File
	for i := 0; i < 5; i++ {
		var err error
		f, err = os.Open(filePath) //nolint:gosec
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(pollInterval):
		}
	}
	if f == nil {
		return
	}
	defer f.Close()

	reader := bufio.NewReader(f)
	var buf strings.Builder
	fctx := &codexFileContext{fileID: watcherKey} // populated by session_meta event
	idleCount := 0
	const maxIdlePolls = 600 // 5 minutes at 500ms interval
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			idleCount = 0 // reset on activity
			buf.WriteString(line)
			if strings.HasSuffix(line, "\n") {
				trimmed := strings.TrimSpace(buf.String())
				buf.Reset()
				if trimmed != "" {
					w.processCodexLine(sessionID, trimmed, seen, fctx)
				}
			}
			continue
		}
		if err == io.EOF {
			// First EOF marks end of backfill — subsequent lines are live.
			if !fctx.live {
				fctx.live = true
			}
			idleCount++
			if idleCount > maxIdlePolls {
				w.logger.Info("codex session idle, stopping watcher", "session", sessionID)
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(pollInterval):
			}
			continue
		}
		if err != nil {
			w.logger.Debug("codex file read error", "session", sessionID, "error", err)
			return
		}
	}
}

// codexEvent represents a single line in a Codex JSONL session file.
type codexEvent struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexResponseItem struct {
	Type    string          `json:"type"`
	Role    string          `json:"role"`
	Name    string          `json:"name"`
	CallID  string          `json:"call_id"`
	Content json.RawMessage `json:"content"`
	Summary json.RawMessage `json:"summary"`
	Output  string          `json:"output"`
	Input   string          `json:"input"`
	// function_call fields
	Arguments string `json:"arguments"`
}

// codexFileContext tracks per-file agent identity (main vs subagent).
type codexFileContext struct {
	fileID         string
	agentID        string
	agentType      string
	conversationID string // from session_meta.payload.id (= OTel conversation.id)
	// sessionID overrides the filename-based default ONLY for main
	// sessions, where it is set to the conversation UUID so JSONL-derived
	// rows share session_id with hook-derived rows (the hook handler
	// keys on the same UUID — see handler/hooks.go where it pulls
	// session_id from the Codex hook payload). Empty for subagent files
	// so the filename-prefix grouping that links parent + subagent
	// rollout files is preserved.
	sessionID string
	// model is the most recently observed model name from a turn_context
	// event in this rollout. Stored on the file context so subsequent
	// response_item / event_msg rows (tool_use, tool_result, reasoning)
	// can stamp model into tma1_messages alongside the assistant payload
	// — without it, the dashboard's per-call cost lookup can't price
	// rollout-derived inference rows.
	model string
	live  bool // true after initial backfill completes (first EOF)
}

// effectiveSessionID returns the conversation UUID once session_meta
// has been parsed for a main-session rollout file, otherwise the
// filename-based fallback. This is what aligns the JSONL parser's
// session_id with the hook handler's session_id (= the same Codex
// conversation UUID), so the live-gate dedup and the dashboard's
// per-session grouping both see ONE session per Codex run instead
// of two.
func (c *codexFileContext) effectiveSessionID(fallback string) string {
	if c != nil && c.sessionID != "" {
		return c.sessionID
	}
	return fallback
}

func (w *Watcher) processCodexLine(sessionID, line string, seen map[string]struct{}, fctx *codexFileContext) {
	var ev codexEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return
	}

	// Parse timestamp from event.
	ts, _ := time.Parse(time.RFC3339Nano, ev.Timestamp)
	if ts.IsZero() {
		ts = time.Now()
	}

	switch ev.Type {
	case "session_meta":
		// Detect subagent from source field. Codex emits either a string
		// ("cli") or an object — {"subagent":"review"} for the classic
		// review subagent, and {"subagent":{"other":"guardian"}} for the
		// auto-review (`codex review`) variant Codex 0.131.0 introduced;
		// parseCodexSubagentSource normalises both forms and rewrites
		// guardian → codex-auto-review so the dashboard groups the new
		// flow together.
		meta := parseCodexSessionMeta(ev.Payload)
		if meta.id != "" {
			fctx.conversationID = meta.id
		}
		isSubagent := meta.subagent != ""
		if isSubagent {
			fctx.agentID = codexSubagentID(fctx.fileID, meta.subagent)
			fctx.agentType = meta.subagent
			// Subagent files: try to attribute to the PARENT's
			// conversation UUID via the Watcher map (keyed on
			// shared timestamp prefix). If found, the dashboard's
			// per-session SUM(SubagentStart) attaches under the
			// parent. If NOT found (e.g. Codex 0.131.0's `code
			// review` mode spawns a subagent rollout whose
			// session_meta carries no parent reference AND its
			// timestamp prefix doesn't match any main session),
			// fall back to the subagent's OWN conversation UUID —
			// NOT the filename prefix, which would create a
			// "rollout-..." pseudo-session_id that mismatches
			// every hook-derived row and confuses the UI.
			if parentUUID := w.lookupCodexParentSession(sessionID); parentUUID != "" {
				fctx.sessionID = parentUUID
			} else if meta.id != "" {
				fctx.sessionID = meta.id
			}
		} else if meta.id != "" {
			// Main session: promote conversation UUID to be the
			// canonical session_id so every JSONL-derived row matches
			// what the hook handler writes for the same run. Publish
			// to the parent-session map so subagent goroutines in
			// the same Codex run can attribute their lifecycle
			// events to this UUID.
			fctx.sessionID = meta.id
			w.recordCodexParentSession(sessionID, meta.id)
		}
		sid := fctx.effectiveSessionID(sessionID)
		if isSubagent {
			w.insertCodexSubagentEvent(sid, ts, fctx.agentID, fctx.agentType, fctx.conversationID, meta.cwd)
			if fctx.live {
				w.broadcastHookEvent(sid, "SubagentStart", "", "", "", "", fctx.agentID, fctx.agentType)
			}
			break
		}
		w.insertCodexSessionStart(sid, ts, meta.cwd, fctx.conversationID)
		if fctx.live {
			w.broadcastHookEvent(sid, "SessionStart", "", "", "", "", "", "")
		}

	case "turn_context":
		// Stash the model on the file context so subsequent
		// response_item / event_msg rows can stamp model into
		// tma1_messages alongside the assistant payload. Continue
		// to insert the synthetic assistant-row so the dashboard's
		// session KPI / cost lookup still surfaces the model even
		// when no tool/reasoning rows follow.
		var turnCtx struct {
			Model string `json:"model"`
		}
		if json.Unmarshal(ev.Payload, &turnCtx) == nil && turnCtx.Model != "" {
			fctx.model = turnCtx.Model
			w.insertCodexModelMessage(fctx.effectiveSessionID(sessionID), ts, turnCtx.Model, seen)
		}

	case "event_msg":
		var eventMsg struct {
			Type    string          `json:"type"`
			Message string          `json:"message"`
			Phase   string          `json:"phase"`
			TurnID  string          `json:"turn_id"`
			CallID  string          `json:"call_id"`
			Query   string          `json:"query"`
			Action  json.RawMessage `json:"action"`
		}
		if err := json.Unmarshal(ev.Payload, &eventMsg); err != nil {
			return
		}
		sid := fctx.effectiveSessionID(sessionID)
		switch eventMsg.Type {
		case "task_started":
			// TaskCreated marks the start of a turn. The waterfall
			// pairs it with TaskCompleted (below) to render a turn
			// span — without TaskCreated, agentic subagents and LLM
			// calls have nothing to reparent under and the timeline
			// renders as a flat list.
			w.insertCodexHookEvent(sid, ts, "TaskCreated", "", "", eventMsg.TurnID, "", fctx)
		case "task_complete":
			w.insertCodexHookEvent(sid, ts, "TaskCompleted", "", "", eventMsg.TurnID, "", fctx)
			// Emit SubagentStop for subagent files.
			if fctx.agentID != "" {
				w.insertCodexHookEvent(sid, ts, "SubagentStop", "", "", "", "", fctx)
			}
		case "web_search_end":
			// Codex's web_search_end payload carries the `query` and
			// `action` but no results — the actual search output
			// arrives folded into the next assistant message. Emit
			// only the Pre/Post hook pair (so the waterfall draws
			// the tool span) and a tool_use message for the input;
			// don't write a tool_result with toolInput stuffed into
			// the result slot, which would duplicate input as output
			// and inflate context-length heuristics keyed off
			// tool_result.length.
			toolInput := codexWebSearchInput(eventMsg.Query, eventMsg.Action)
			w.insertCodexHookEvent(sid, ts, "PreToolUse", "web_search", toolInput, eventMsg.CallID, "", fctx)
			w.insertCodexTypedMessage(sid, ts, "tool_use", "assistant", toolInput, fctx.model, "web_search", eventMsg.CallID, seen)
			w.insertCodexHookEvent(sid, ts, "PostToolUse", "web_search", "", eventMsg.CallID, "", fctx)
		case "token_count":
			// Codex emits token_count after each response with the
			// per-call usage in info.last_token_usage. This is the
			// only place reasoning_output_tokens surfaces from
			// JSONL, so we persist it as a synthetic assistant row
			// whose token columns light up the message-derived
			// apiCalls fallback in sessions.js (used when Codex
			// OTel logs aren't present).
			var tc struct {
				Info struct {
					LastTokenUsage struct {
						InputTokens           int64 `json:"input_tokens"`
						CachedInputTokens     int64 `json:"cached_input_tokens"`
						OutputTokens          int64 `json:"output_tokens"`
						ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
					} `json:"last_token_usage"`
				} `json:"info"`
			}
			if json.Unmarshal(ev.Payload, &tc) == nil {
				usage := tc.Info.LastTokenUsage
				// Skip zero-usage events (handshake / no-op turns).
				// Output OR reasoning > 0 is the marker that the
				// API actually answered.
				if usage.OutputTokens > 0 || usage.ReasoningOutputTokens > 0 {
					w.insertCodexUsageMessage(sid, ts, fctx.model,
						usage.InputTokens, usage.OutputTokens,
						usage.CachedInputTokens, usage.ReasoningOutputTokens, seen)
				}
			}
		case "user_message":
			msg := strings.TrimSpace(eventMsg.Message)
			if msg != "" {
				w.insertCodexMessage(sid, ts, "user", msg, seen)
			}
		case "agent_message":
			msg := strings.TrimSpace(eventMsg.Message)
			if msg != "" {
				w.insertCodexMessage(sid, ts, "assistant", msg, seen)
			}
		}

	case "response_item":
		var item codexResponseItem
		if err := json.Unmarshal(ev.Payload, &item); err != nil {
			return
		}
		w.processCodexResponseItem(fctx.effectiveSessionID(sessionID), ts, item, seen, fctx)
	}
}

type codexSessionMeta struct {
	id       string
	cwd      string
	subagent string
}

// parseCodexSessionMeta normalises the {id, cwd, source} shape Codex
// writes at the head of every rollout. The source field carries the
// subagent identity for review / auto-review subagents; everything
// else (string "cli", missing, etc.) maps to an empty subagent.
func parseCodexSessionMeta(raw json.RawMessage) codexSessionMeta {
	var meta struct {
		ID     string          `json:"id"`
		Source json.RawMessage `json:"source"`
		CWD    string          `json:"cwd"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return codexSessionMeta{}
	}
	return codexSessionMeta{
		id:       meta.ID,
		cwd:      meta.CWD,
		subagent: parseCodexSubagentSource(meta.Source),
	}
}

// parseCodexSubagentSource extracts the subagent identity from
// session_meta.source. Codex emits one of:
//   - "cli"                            (main session, not a subagent)
//   - {"subagent":"review"}            (classic review subagent)
//   - {"subagent":{"other":"guardian"}} (auto-review: `codex review`)
//
// Returns "" for the main-session case. The guardian → codex-auto-review
// rewrite groups the new auto-review flow under a stable agent_type so
// the dashboard's per-subagent rollups don't fragment.
func parseCodexSubagentSource(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	// "cli" / any plain string → main session, not a subagent.
	var sourceString string
	if json.Unmarshal(raw, &sourceString) == nil {
		return ""
	}
	var source struct {
		Subagent json.RawMessage `json:"subagent"`
	}
	if json.Unmarshal(raw, &source) != nil || len(source.Subagent) == 0 || string(source.Subagent) == "null" {
		return ""
	}
	// {"subagent":"review"}
	var subagent string
	if json.Unmarshal(source.Subagent, &subagent) == nil {
		return strings.TrimSpace(subagent)
	}
	// {"subagent":{"other":"guardian"}} — Codex 0.131.0 auto-review.
	var subagentObj map[string]string
	if json.Unmarshal(source.Subagent, &subagentObj) == nil {
		if other := strings.TrimSpace(subagentObj["other"]); other != "" {
			if other == "guardian" {
				return "codex-auto-review"
			}
			return other
		}
	}
	return ""
}

// extractCodexReasoning pulls the text from a `reasoning` response_item.
// Codex has shipped two layouts: newer rollouts put text under
// `content` blocks (`reasoning_text` / `text` / `output_text`), older
// ones under `summary` blocks (`summary_text` / `text`). Try both so
// reasoning rows show up regardless of Codex version.
func extractCodexReasoning(item codexResponseItem) string {
	if text := extractCodexText(item.Content, "reasoning_text", "text", "output_text"); text != "" {
		return text
	}
	return extractCodexText(item.Summary, "summary_text", "text")
}

func extractCodexText(raw json.RawMessage, allowedTypes ...string) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	allowed := make(map[string]struct{}, len(allowedTypes))
	for _, typ := range allowedTypes {
		allowed[typ] = struct{}{}
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var sb strings.Builder
	for _, b := range blocks {
		if _, ok := allowed[b.Type]; !ok {
			continue
		}
		text := strings.TrimSpace(b.Text)
		if text == "" {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(text)
	}
	return sb.String()
}

// codexWebSearchInput composes the tool_input column for a web_search
// hook row. Codex emits the query plus an opaque `action` payload; we
// keep both so downstream can show full intent without parsing JSON
// twice (the existing tool-pair renderer already escapes string
// content).
func codexWebSearchInput(query string, action json.RawMessage) string {
	query = strings.TrimSpace(query)
	actionText := strings.TrimSpace(string(action))
	if query == "" {
		if actionText == "null" {
			return ""
		}
		return actionText
	}
	if actionText == "" || actionText == "null" {
		return query
	}
	queryJSON, err := json.Marshal(query)
	if err != nil {
		return actionText
	}
	return `{"query":` + string(queryJSON) + `,"action":` + actionText + `}`
}

func (w *Watcher) processCodexResponseItem(sessionID string, ts time.Time, item codexResponseItem, seen map[string]struct{}, fctx *codexFileContext) {
	switch item.Type {
	case "message":
		role := item.Role
		if role == "developer" {
			return // system/developer messages not relevant
		}
		// Extract text content.
		var contentBlocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(item.Content, &contentBlocks); err != nil {
			// Try as single string.
			var s string
			if err := json.Unmarshal(item.Content, &s); err == nil && s != "" {
				w.insertCodexMessage(sessionID, ts, role, s, seen)
			}
			return
		}
		for _, b := range contentBlocks {
			if (b.Type == "input_text" || b.Type == "output_text" || b.Type == "text") && b.Text != "" {
				w.insertCodexMessage(sessionID, ts, role, b.Text, seen)
			}
		}

	case "reasoning":
		// Reasoning items become "thinking" rows so the timeline/waterfall
		// can render them alongside the assistant payload they precede.
		// The text may live under either `content` blocks (newer Codex)
		// or `summary` blocks (older), so try both.
		if text := extractCodexReasoning(item); text != "" {
			w.insertCodexTypedMessage(sessionID, ts, "thinking", "assistant", text, fctx.model, "", "", seen)
		}

	case "function_call":
		w.insertCodexHookEvent(sessionID, ts, "PreToolUse", item.Name, item.Arguments, item.CallID, "", fctx)
		w.insertCodexTypedMessage(sessionID, ts, "tool_use", "assistant", item.Arguments, fctx.model, item.Name, item.CallID, seen)

	case "function_call_output":
		w.insertCodexHookEvent(sessionID, ts, "PostToolUse", "", "", item.CallID, item.Output, fctx)
		w.insertCodexTypedMessage(sessionID, ts, "tool_result", "user", item.Output, fctx.model, "", item.CallID, seen)

	case "custom_tool_call":
		w.insertCodexHookEvent(sessionID, ts, "PreToolUse", item.Name, item.Input, item.CallID, "", fctx)
		w.insertCodexTypedMessage(sessionID, ts, "tool_use", "assistant", item.Input, fctx.model, item.Name, item.CallID, seen)

	case "custom_tool_call_output":
		w.insertCodexHookEvent(sessionID, ts, "PostToolUse", "", "", item.CallID, item.Output, fctx)
		w.insertCodexTypedMessage(sessionID, ts, "tool_result", "user", item.Output, fctx.model, "", item.CallID, seen)
	}
}

// insertCodexModelMessage stores a synthetic message with the model field set.
// This makes the model visible in session detail KPI and cost calculation.
//
// The row carries message_type='usage' so the timeline renderer
// (sessions.js push step) can drop it explicitly — without that gate
// it'd surface as a blank assistant entry. message_type='usage' is
// also the shape insertCodexUsageMessage writes; both are synthetic
// metadata rows, never displayed.
func (w *Watcher) insertCodexModelMessage(sessionID string, ts time.Time, model string, seen map[string]struct{}) {
	// NOT gated by codexLiveGate: this writes a row into tma1_messages,
	// which the hook handler never duplicates (hooks only write
	// tma1_hook_events). Gating here would silently kill conversation
	// replay + prompt analysis + peer-session content for active Codex
	// sessions. The gate only belongs on tma1_hook_events writers.
	key := "model:" + model
	if _, ok := seen[key]; ok {
		return
	}
	seen[key] = struct{}{}

	msTs := ts.UnixMilli()
	for {
		prev := lastInsertTS.Load()
		next := msTs
		if next <= prev {
			next = prev + 1
		}
		if lastInsertTS.CompareAndSwap(prev, next) {
			msTs = next
			break
		}
	}

	sql := fmt.Sprintf(
		"INSERT INTO tma1_messages (ts, session_id, message_type, \"role\", content, model, tool_name, tool_use_id) "+
			"VALUES (%d, '%s', 'usage', 'assistant', '', '%s', '', '')",
		msTs,
		escapeSQLString(sessionID),
		escapeSQLString(model),
	)
	go func() {
		insertSem <- struct{}{}
		defer func() { <-insertSem }()
		w.execSQL(sql)
	}()

	// Do NOT broadcast model messages — they are synthetic metadata, not hook events.
}

func (w *Watcher) insertCodexMessage(sessionID string, ts time.Time, role, content string, seen map[string]struct{}) {
	// NOT gated by codexLiveGate -- same reasoning as
	// insertCodexModelMessage above. Writes tma1_messages, never
	// duplicated by the hook handler.
	// Dedup by content prefix hash.
	prefix := content
	if len(prefix) > 200 {
		prefix = prefix[:200]
	}
	key := role + ":" + prefix
	if _, ok := seen[key]; ok {
		return
	}
	seen[key] = struct{}{}

	msgType := "user"
	if role == "assistant" {
		msgType = "assistant"
	}

	msTs := ts.UnixMilli()
	for {
		prev := lastInsertTS.Load()
		next := msTs
		if next <= prev {
			next = prev + 1
		}
		if lastInsertTS.CompareAndSwap(prev, next) {
			msTs = next
			break
		}
	}

	sql := fmt.Sprintf(
		"INSERT INTO tma1_messages (ts, session_id, message_type, \"role\", content, model, tool_name, tool_use_id) "+
			"VALUES (%d, '%s', '%s', '%s', '%s', '', '', '')",
		msTs,
		escapeSQLString(sessionID),
		escapeSQLString(msgType),
		escapeSQLString(role),
		escapeSQLString(truncate(content, maxContentLen)),
	)
	go func() {
		insertSem <- struct{}{}
		defer func() { <-insertSem }()
		w.execSQL(sql)
	}()
}

// insertCodexTypedMessage writes a tma1_messages row with an explicit
// message_type / role / tool_use_id triple — the path used by tool_use,
// tool_result, and thinking rows derived from response_item events.
//
// NOT gated by codexLiveGate -- same reason as insertCodexMessage /
// insertCodexModelMessage: writes tma1_messages, never duplicated by
// the hook handler.
//
// Dedup keys on the tool_use_id when present, otherwise on a
// content-prefix bucket. Codex context replays re-emit identical
// response_item lines on every prompt, so without the in-process seen
// map a single 50-turn run accumulates duplicate transcript rows in
// the hundreds.
func (w *Watcher) insertCodexTypedMessage(sessionID string, ts time.Time, messageType, role, content, model, toolName, toolUseID string, seen map[string]struct{}) {
	key := codexMessageSeenKey(messageType, role, content, toolUseID)
	if seen != nil {
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
	}

	msTs := ts.UnixMilli()
	for {
		prev := lastInsertTS.Load()
		next := msTs
		if next <= prev {
			next = prev + 1
		}
		if lastInsertTS.CompareAndSwap(prev, next) {
			msTs = next
			break
		}
	}

	sql := fmt.Sprintf(
		"INSERT INTO tma1_messages (ts, session_id, message_type, \"role\", content, model, tool_name, tool_use_id) "+
			"VALUES (%d, '%s', '%s', '%s', '%s', '%s', '%s', '%s')",
		msTs,
		escapeSQLString(sessionID),
		escapeSQLString(messageType),
		escapeSQLString(role),
		escapeSQLString(truncate(content, maxContentLen)),
		escapeSQLString(model),
		escapeSQLString(toolName),
		escapeSQLString(toolUseID),
	)
	go func() {
		insertSem <- struct{}{}
		defer func() { <-insertSem }()
		w.execSQL(sql)
	}()
}

// insertCodexUsageMessage writes a per-call usage row sourced from
// Codex JSONL's `event_msg.token_count` payload. The row uses a
// dedicated `message_type='usage'` so it never collides with real
// assistant transcript messages and the sessions.js timeline can
// drop it via an explicit type check instead of an empty-content
// heuristic. The sessions.js Codex fallback path scans usage rows
// where at least one token column is > 0 to build apiCalls; the
// model-only marker row insertCodexModelMessage writes (also
// 'usage', but with NULL tokens) is skipped by the same > 0 guard.
//
// Dedup key includes the timestamp + every token field so a JSONL
// replay on restart resolves to the same key and is skipped.
//
// NOT gated by codexLiveGate — tma1_messages is never written by
// the live hook handler, so there's no double-write to suppress.
func (w *Watcher) insertCodexUsageMessage(sessionID string, ts time.Time, model string, input, output, cachedInput, reasoning int64, seen map[string]struct{}) {
	key := fmt.Sprintf("usage:%d:%d:%d:%d:%d", ts.UnixMilli(), input, output, cachedInput, reasoning)
	if seen != nil {
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
	}

	msTs := ts.UnixMilli()
	for {
		prev := lastInsertTS.Load()
		next := msTs
		if next <= prev {
			next = prev + 1
		}
		if lastInsertTS.CompareAndSwap(prev, next) {
			msTs = next
			break
		}
	}

	sql := fmt.Sprintf(
		"INSERT INTO tma1_messages (ts, session_id, message_type, \"role\", content, model, tool_name, tool_use_id, "+
			"input_tokens, output_tokens, cache_read_tokens, reasoning_tokens) "+
			"VALUES (%d, '%s', 'usage', 'assistant', '', '%s', '', '', %d, %d, %d, %d)",
		msTs,
		escapeSQLString(sessionID),
		escapeSQLString(model),
		input,
		output,
		cachedInput,
		reasoning,
	)
	go func() {
		insertSem <- struct{}{}
		defer func() { <-insertSem }()
		w.execSQL(sql)
	}()
}

func codexMessageSeenKey(messageType, role, content, toolUseID string) string {
	if toolUseID != "" && (messageType == "tool_use" || messageType == "tool_result") {
		return messageType + ":" + toolUseID
	}
	prefix := content
	if len(prefix) > 200 {
		prefix = prefix[:200]
	}
	return messageType + ":" + role + ":" + prefix
}

// codexHookCoveredEvents lists the tma1_hook_events.event_type values
// that the Codex hook handler writes itself. Must stay in sync with
// install_codex.go's codexHookEvents — that's the list registered in
// ~/.codex/hooks.json, so any event NOT in this set is JSONL-only and
// the parser is the only writer.
//
// Notably absent: SubagentStart / SubagentStop. Codex never POSTs those
// (its hook catalogue has no subagent lifecycle event), so the JSONL
// parser must keep writing them even when the live gate is active.
var codexHookCoveredEvents = map[string]struct{}{
	"SessionStart":     {},
	"PreToolUse":       {},
	"PostToolUse":      {},
	"UserPromptSubmit": {},
	"Stop":             {},
}

// codexLiveGate returns true when the Codex hook adapter is actively
// posting events for this session AND the given event_type is one the
// hook handler actually writes. nil gate => always false (parser stays
// the sole writer, original behaviour).
//
// IMPORTANT: only call this from insertion paths that write to
// tma1_hook_events. The hook handler never writes to tma1_messages,
// so gating message-inserts would kill conversation replay for any
// active Codex session. See `insertCodexMessage` /
// `insertCodexModelMessage` for the deliberate exclusion.
func (w *Watcher) codexLiveGate(sessionID, eventType string) bool {
	if w.IsLiveSession == nil {
		return false
	}
	if _, covered := codexHookCoveredEvents[eventType]; !covered {
		return false
	}
	return w.IsLiveSession(sessionID)
}

func (w *Watcher) insertCodexSessionStart(sessionID string, ts time.Time, cwd, conversationID string) {
	if w.codexLiveGate(sessionID, "SessionStart") {
		return
	}
	msTs := ts.UnixMilli()
	for {
		prev := lastInsertTS.Load()
		next := msTs
		if next <= prev {
			next = prev + 1
		}
		if lastInsertTS.CompareAndSwap(prev, next) {
			msTs = next
			break
		}
	}

	sql := fmt.Sprintf(
		"INSERT INTO tma1_hook_events "+
			"(ts, session_id, event_type, agent_source, tool_name, tool_input, tool_result, "+
			"tool_use_id, agent_id, agent_type, notification_type, \"message\", cwd, transcript_path, conversation_id) "+
			"VALUES (%d, '%s', 'SessionStart', 'codex', '', '', '', '', '', '', '', '', '%s', '', '%s')",
		msTs,
		escapeSQLString(sessionID),
		escapeSQLString(truncate(cwd, 512)),
		escapeSQLString(conversationID),
	)
	go func() {
		insertSem <- struct{}{}
		defer func() { <-insertSem }()
		w.execSQL(sql)
	}()
}

func codexSubagentID(fileID, agentType string) string {
	if fileID != "" {
		return fileID
	}
	return agentType
}

func (w *Watcher) insertCodexSubagentEvent(sessionID string, ts time.Time, agentID, agentType, conversationID, cwd string) {
	if w.codexLiveGate(sessionID, "SubagentStart") {
		return
	}
	msTs := ts.UnixMilli()
	for {
		prev := lastInsertTS.Load()
		next := msTs
		if next <= prev {
			next = prev + 1
		}
		if lastInsertTS.CompareAndSwap(prev, next) {
			msTs = next
			break
		}
	}

	// Write cwd from the subagent's own session_meta so orphan
	// subagent rollouts (Codex 0.131.0 `code review`, no parent)
	// still surface a working dir on the dashboard. The dashboard
	// groups by session_id and reduces with MAX(cwd) — without this
	// row, an orphan subagent's WORKING DIR column stays blank.
	sql := fmt.Sprintf(
		"INSERT INTO tma1_hook_events "+
			"(ts, session_id, event_type, agent_source, tool_name, tool_input, tool_result, "+
			"tool_use_id, agent_id, agent_type, notification_type, \"message\", cwd, transcript_path, conversation_id) "+
			"VALUES (%d, '%s', 'SubagentStart', 'codex', '', '', '', '', '%s', '%s', '', '', '%s', '', '%s')",
		msTs,
		escapeSQLString(sessionID),
		escapeSQLString(agentID),
		escapeSQLString(agentType),
		escapeSQLString(truncate(cwd, 512)),
		escapeSQLString(conversationID),
	)
	go func() {
		insertSem <- struct{}{}
		defer func() { <-insertSem }()
		w.execSQL(sql)
	}()
}

func (w *Watcher) insertCodexHookEvent(sessionID string, ts time.Time, eventType, toolName, toolInput, toolUseID, toolResult string, fctx *codexFileContext) {
	if w.codexLiveGate(sessionID, eventType) {
		return
	}
	msTs := ts.UnixMilli()
	for {
		prev := lastInsertTS.Load()
		next := msTs
		if next <= prev {
			next = prev + 1
		}
		if lastInsertTS.CompareAndSwap(prev, next) {
			msTs = next
			break
		}
	}

	agentID := ""
	agentType := ""
	if fctx != nil {
		agentID = fctx.agentID
		agentType = fctx.agentType
	}

	conversationID := ""
	if fctx != nil {
		conversationID = fctx.conversationID
	}

	// Derive the ingest-time columns the way the CC handler does
	// (handler/hooks.go calls derive.Fields too). Without this,
	// downstream queries that COALESCE(tool_file_path, regexp_match(...))
	// would fall back to regex on every Codex row — measurable cost
	// in the anomaly + peer paths.
	filePath, cmdPrefix, success, errSummary := derive.Fields(
		eventType, toolName, toolInput, toolResult, "",
	)

	sql := fmt.Sprintf(
		"INSERT INTO tma1_hook_events "+
			"(ts, session_id, event_type, agent_source, tool_name, tool_input, tool_result, "+
			"tool_use_id, agent_id, agent_type, notification_type, \"message\", cwd, transcript_path, conversation_id, "+
			"tool_file_path, tool_command_prefix, tool_success, tool_error_summary) "+
			"VALUES (%d, '%s', '%s', 'codex', '%s', '%s', '%s', '%s', '%s', '%s', '', '', '', '', '%s', %s, %s, %s, %s)",
		msTs,
		escapeSQLString(sessionID),
		escapeSQLString(eventType),
		escapeSQLString(truncate(toolName, 256)),
		escapeSQLString(truncate(toolInput, maxToolInput)),
		escapeSQLString(truncate(toolResult, maxToolContent)),
		escapeSQLString(toolUseID),
		escapeSQLString(agentID),
		escapeSQLString(agentType),
		escapeSQLString(conversationID),
		sqlutil.Quote(filePath, 512),
		sqlutil.Quote(cmdPrefix, 200),
		nullableBool(success),
		sqlutil.Quote(errSummary, 400),
	)
	go func() {
		insertSem <- struct{}{}
		defer func() { <-insertSem }()
		w.execSQL(sql)
	}()

	if fctx != nil && fctx.live {
		w.broadcastHookEvent(sessionID, eventType, toolName, toolInput, toolUseID, toolResult, agentID, agentType)
	}
}
