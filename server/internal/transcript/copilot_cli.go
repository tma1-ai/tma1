package transcript

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	copilotCLIScanInterval  = 10 * time.Second
	copilotCLIActiveAge     = 10 * time.Minute
	copilotCLIAgentSource   = "copilot_cli"
	copilotCLISessionPrefix = "cp:"
)

// copilotCLIIngestedDirs is the set of on-disk session directory names whose
// events have already been ingested into GreptimeDB. Used to skip re-backfill
// on server restart, since the hook/message tables are append_mode=true and
// have no unique constraint. Populated once at scanner startup.
var (
	copilotCLIIngestedDirs   = make(map[string]struct{})
	copilotCLIIngestedDirsMu sync.RWMutex
)

// markCopilotCLIDirIngested records that a directory has been (or is being)
// ingested, so subsequent scan cycles won't re-process it.
func markCopilotCLIDirIngested(dirName string) {
	copilotCLIIngestedDirsMu.Lock()
	copilotCLIIngestedDirs[dirName] = struct{}{}
	copilotCLIIngestedDirsMu.Unlock()
}

func copilotCLIDirIngested(dirName string) bool {
	copilotCLIIngestedDirsMu.RLock()
	_, ok := copilotCLIIngestedDirs[dirName]
	copilotCLIIngestedDirsMu.RUnlock()
	return ok
}

// StartCopilotCLIScanner periodically scans ~/.copilot/session-state/ for active
// events.jsonl files and starts watching any new ones. GitHub Copilot CLI stores
// session events as JSONL files in per-session directories.
func (w *Watcher) StartCopilotCLIScanner(ctx context.Context) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		w.logger.Warn("copilot-cli scanner: cannot determine home directory", "error", err)
		return
	}
	baseDir := filepath.Join(homeDir, ".copilot", "session-state")
	w.logger.Info("copilot-cli session scanner started", "path", baseDir)

	// Load the set of already-ingested session directories so we don't re-backfill
	// them on restart. The tables are append-only with no unique constraints, so
	// duplicate ingestion would compound metrics on every server restart.
	w.loadCopilotCLIIngestedDirs()

	// First scan has no age limit to pick up ALL historical sessions.
	firstScan := true
	ticker := time.NewTicker(copilotCLIScanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := os.Stat(baseDir); err == nil {
				if firstScan {
					w.scanCopilotCLISessionsWithAge(baseDir, 0) // 0 = no limit
					firstScan = false
				} else {
					w.scanCopilotCLISessionsWithAge(baseDir, copilotCLIActiveAge)
				}
			}
		}
	}
}

func (w *Watcher) scanCopilotCLISessionsWithAge(baseDir string, activeAge time.Duration) {
	now := time.Now()

	// Prune stopped watchers to prevent unbounded memory growth.
	w.mu.Lock()
	var stoppedCount, activeCount int
	for key, sw := range w.sessions {
		if strings.HasPrefix(key, copilotCLISessionPrefix) {
			if sw.stopped {
				stoppedCount++
			} else {
				activeCount++
			}
		}
	}
	if stoppedCount > 50 {
		for key, sw := range w.sessions {
			if sw.stopped && strings.HasPrefix(key, copilotCLISessionPrefix) {
				delete(w.sessions, key)
			}
		}
	}
	w.mu.Unlock()

	// Limit concurrent watchers to avoid overwhelming GreptimeDB.
	const maxConcurrentWatchers = 10
	newWatchers := 0

	// Sort entries by modification time (newest first) so recent sessions are prioritized.
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return
	}

	type sessionFile struct {
		id   string
		path string
		mod  time.Time
	}
	var candidates []sessionFile
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		eventsFile := filepath.Join(baseDir, entry.Name(), "events.jsonl")
		info, err := os.Stat(eventsFile)
		if err != nil {
			continue
		}
		if activeAge > 0 && now.Sub(info.ModTime()) > activeAge {
			continue
		}
		candidates = append(candidates, sessionFile{entry.Name(), eventsFile, info.ModTime()})
	}

	// Sort newest first.
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].mod.After(candidates[j].mod)
	})

	for _, c := range candidates {
		watcherKey := copilotCLISessionPrefix + c.id
		w.mu.Lock()
		existing, ok := w.sessions[watcherKey]
		alreadyWatching := ok && !existing.stopped
		w.mu.Unlock()

		if alreadyWatching {
			continue
		}
		isActive := now.Sub(c.mod) < copilotCLIActiveAge
		// Skip sessions already fully ingested into the DB from a previous run
		// UNLESS the file has been recently modified — in which case the CLI
		// may still be writing events we haven't captured yet (watcher timed
		// out on idle but user resumed activity). The preserved per-session
		// `seen` map in w.sessions ensures we don't double-insert.
		if !isActive && copilotCLIDirIngested(c.id) {
			continue
		}
		if activeCount+newWatchers >= maxConcurrentWatchers {
			break // defer remaining to next scan cycle
		}
		markCopilotCLIDirIngested(c.id)
		w.watchCopilotCLIWithActive(watcherKey, c.id, c.path, isActive)
		newWatchers++
	}
}

// loadCopilotCLIIngestedDirs queries GreptimeDB for all session_ids already
// ingested from Copilot CLI and populates copilotCLIIngestedDirs. Session IDs
// in the DB are namespaced as "cp:<sessionId>" (with optional "#N" suffix for
// split multi-session files); the directory name is just "<sessionId>" before
// the first split, so we strip both the prefix and any split suffix.
func (w *Watcher) loadCopilotCLIIngestedDirs() {
	form := url.Values{}
	form.Set("sql", "SELECT DISTINCT session_id FROM tma1_hook_events WHERE agent_source = 'copilot_cli'")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := newPostRequest(ctx, w.sqlURL, form)
	if err != nil {
		w.logger.Warn("copilot-cli: failed to build ingested-sessions query", "error", err)
		return
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		w.logger.Warn("copilot-cli: failed to query ingested sessions", "error", err)
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != 200 {
		w.logger.Warn("copilot-cli: non-200 querying ingested sessions", "status", resp.StatusCode)
		return
	}
	var output struct {
		Output []struct {
			Records struct {
				Rows [][]json.RawMessage `json:"rows"`
			} `json:"records"`
		} `json:"output"`
	}
	if err := json.Unmarshal(body, &output); err != nil {
		w.logger.Warn("copilot-cli: failed to parse ingested-sessions response", "error", err)
		return
	}
	if len(output.Output) == 0 {
		return
	}
	copilotCLIIngestedDirsMu.Lock()
	defer copilotCLIIngestedDirsMu.Unlock()
	for _, row := range output.Output[0].Records.Rows {
		if len(row) == 0 {
			continue
		}
		var sid string
		if err := json.Unmarshal(row[0], &sid); err != nil {
			continue
		}
		sid = strings.TrimPrefix(sid, copilotCLISessionPrefix)
		// Strip "#N" split suffix to recover the on-disk directory name.
		if idx := strings.Index(sid, "#"); idx > 0 {
			sid = sid[:idx]
		}
		if sid != "" {
			copilotCLIIngestedDirs[sid] = struct{}{}
		}
	}
	w.logger.Info("copilot-cli: loaded previously-ingested sessions", "count", len(copilotCLIIngestedDirs))
}

// fetchCopilotCLIMaxTs returns the max ts (ms) of events already persisted
// for the given session, or 0 if none. Used to seed re-watch dedup so a
// session that was partially ingested before server restart can resume
// without double-inserting historical events.
func (w *Watcher) fetchCopilotCLIMaxTs(sessionID string) int64 {
	form := url.Values{}
	dbSid := copilotCLISessionPrefix + sessionID
	form.Set("sql", "SELECT MAX(ts) FROM tma1_hook_events WHERE session_id = '"+escapeSQLString(dbSid)+"'")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := newPostRequest(ctx, w.sqlURL, form)
	if err != nil {
		return 0
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != 200 {
		return 0
	}
	var output struct {
		Output []struct {
			Records struct {
				Rows [][]json.RawMessage `json:"rows"`
			} `json:"records"`
		} `json:"output"`
	}
	if json.Unmarshal(body, &output) != nil {
		return 0
	}
	if len(output.Output) == 0 || len(output.Output[0].Records.Rows) == 0 || len(output.Output[0].Records.Rows[0]) == 0 {
		return 0
	}
	raw := output.Output[0].Records.Rows[0][0]
	if string(raw) == "null" {
		return 0
	}
	if ms, ok := parseSQLTimestampMs(raw); ok {
		return ms
	}
	w.logger.Debug("copilot-cli: failed to parse max ts", "session", dbSid, "raw", string(raw))
	return 0
}

func parseSQLTimestampMs(raw json.RawMessage) (int64, bool) {
	var n float64
	if json.Unmarshal(raw, &n) == nil {
		return normalizeUnixTimestampMs(n), true
	}

	var s string
	if json.Unmarshal(raw, &s) != nil {
		return 0, false
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		return normalizeUnixTimestampMs(n), true
	}

	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05.999999",
		"2006-01-02 15:04:05.999",
		"2006-01-02 15:04:05",
	}
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t.UnixMilli(), true
		}
	}
	return 0, false
}

func normalizeUnixTimestampMs(v float64) int64 {
	switch {
	case v > 1e18:
		return int64(v / 1e6) // nanoseconds
	case v > 1e15:
		return int64(v / 1e3) // microseconds
	case v > 1e12:
		return int64(v) // milliseconds
	default:
		return int64(v * 1000) // seconds
	}
}

func (w *Watcher) watchCopilotCLIWithActive(watcherKey, sessionID, filePath string, isActive bool) {
	w.mu.Lock()
	defer w.mu.Unlock()

	existing, ok := w.sessions[watcherKey]
	if ok && !existing.stopped {
		return // already watching
	}

	var seen map[string]struct{}
	if ok && existing.seen != nil {
		seen = existing.seen
	} else {
		seen = make(map[string]struct{})
	}

	ctx, cancel := context.WithCancel(context.Background())
	sw := &sessionWatch{cancel: cancel, seen: seen}
	w.sessions[watcherKey] = sw

	go w.tailCopilotCLIFile(ctx, watcherKey, sessionID, filePath, seen, isActive)
	w.logger.Info("watching copilot-cli session", "session", sessionID, "file", filePath)
}

func (w *Watcher) tailCopilotCLIFile(ctx context.Context, watcherKey, sessionID, filePath string, seen map[string]struct{}, isActive bool) {
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

	fctx := &copilotCLIContext{sessionID: sessionID}
	// Seed re-watch dedup threshold from DB: skip events we've already persisted.
	fctx.skipUntilTsMs = w.fetchCopilotCLIMaxTs(sessionID)
	fctx.lastTsMs = fctx.skipUntilTsMs

	reader := bufio.NewReader(f)
	var buf strings.Builder
	idleCount := 0
	// Active sessions wait 5 minutes for new data; completed sessions stop immediately.
	maxIdlePolls := 600 // 5 minutes at 500ms
	if !isActive {
		maxIdlePolls = 2 // 1 second — just drain any buffered writes
	}

	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			idleCount = 0
			buf.WriteString(line)
			if strings.HasSuffix(line, "\n") {
				trimmed := strings.TrimSpace(buf.String())
				buf.Reset()
				if trimmed != "" {
					w.processCopilotCLILine(sessionID, trimmed, seen, fctx)
				}
			}
			continue
		}
		if err == io.EOF {
			if !fctx.live {
				fctx.live = true
			}
			idleCount++
			if idleCount > maxIdlePolls {
				w.logger.Info("copilot-cli session idle, stopping watcher", "session", sessionID)
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
			w.logger.Debug("copilot-cli file read error", "session", sessionID, "error", err)
			return
		}
	}
}

// copilotCLIEvent is the common envelope for all Copilot CLI JSONL events.
type copilotCLIEvent struct {
	Type      string          `json:"type"`
	Data      json.RawMessage `json:"data"`
	ID        string          `json:"id"`
	Timestamp string          `json:"timestamp"`
	ParentID  *string         `json:"parentId"`
	// AgentID is populated on events emitted inside a sub-agent's context
	// (e.g. tool.execution_start/complete, assistant.message). Its value
	// equals the toolCallId of the enclosing subagent.started event.
	// Empty/absent for events emitted by the main agent.
	AgentID string `json:"agentId"`
}

// copilotCLIContext tracks per-file state during parsing.
type copilotCLIContext struct {
	sessionID     string
	model         string // current model (updated by session.start and session.model_change)
	cwd           string
	live          bool  // true after initial backfill completes
	lastTsMs      int64 // per-session monotonic timestamp (avoids global lastInsertTS collision)
	skipUntilTsMs int64 // events with raw ts <= this are skipped (re-watch dedup after server restart)
}

// parseCopilotCLITimestamp handles both RFC3339 and Copilot CLI's MM/DD/YYYY HH:mm:ss format.
// Copilot CLI uses UTC timestamps without timezone suffix.
func parseCopilotCLITimestamp(s string) time.Time {
	// Try RFC3339 first (e.g. "2026-04-16T01:37:27.693Z").
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	// Copilot CLI format: "MM/DD/YYYY HH:mm:ss" (UTC, no timezone suffix).
	if t, err := time.ParseInLocation("01/02/2006 15:04:05", s, time.UTC); err == nil {
		return t
	}
	return time.Time{}
}

// dbSessionID returns the namespaced session ID for database storage.
func (c *copilotCLIContext) dbSessionID() string {
	return copilotCLISessionPrefix + c.sessionID
}

// nextTsMs returns a monotonically increasing millisecond timestamp for this session.
// Uses the event's actual timestamp, only incrementing if collisions occur within the same session.
func (c *copilotCLIContext) nextTsMs(eventTs time.Time) int64 {
	msTs := eventTs.UnixMilli()
	if msTs <= c.lastTsMs {
		msTs = c.lastTsMs + 1
	}
	c.lastTsMs = msTs
	return msTs
}

func (w *Watcher) processCopilotCLILine(sessionID, line string, seen map[string]struct{}, fctx *copilotCLIContext) {
	var ev copilotCLIEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return
	}

	// Dedup by event ID.
	if ev.ID != "" {
		if _, ok := seen[ev.ID]; ok {
			return
		}
		seen[ev.ID] = struct{}{}
	}

	// Split on session.start: if the JSONL contains multiple logical sessions
	// (appended across restarts), update fctx.sessionID to create separate DB sessions.
	if ev.Type == "session.start" {
		var startData struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(ev.Data, &startData) == nil && startData.SessionID != "" && startData.SessionID != fctx.sessionID {
			fctx.sessionID = startData.SessionID
			fctx.model = ""
			fctx.cwd = ""
			// Re-seed dedup threshold for the new logical session: skipUntilTsMs
			// was fetched at watcher start for the directory's primary session,
			// so without this refresh we'd re-insert any events of the rolled-over
			// session that were already persisted in a previous run.
			fctx.skipUntilTsMs = w.fetchCopilotCLIMaxTs(fctx.sessionID)
			fctx.lastTsMs = fctx.skipUntilTsMs
		}
	}

	ts := parseCopilotCLITimestamp(ev.Timestamp)
	if ts.IsZero() {
		ts = time.Now()
	}

	// Re-watch dedup: skip events already persisted in a previous run.
	// skipUntilTsMs is seeded from DB at watcher start; events with raw
	// timestamp <= that are drops (their IDs are recorded in `seen` above
	// so subsequent lookups still work for cross-event references).
	if fctx.skipUntilTsMs > 0 && ts.UnixMilli() <= fctx.skipUntilTsMs {
		return
	}

	// Intentionally unhandled event types: hook.start, hook.end, session.warning,
	// system.notification, session.mode_changed, session.context_changed,
	// assistant.turn_start, assistant.turn_end.
	switch ev.Type {
	case "session.start":
		w.handleCopilotCLISessionStart(ts, ev, fctx)
	case "session.shutdown":
		w.insertCopilotCLIHookEvent(ts, fctx, "SessionEnd", "", "", "", "", nil)
	case "session.model_change":
		w.handleCopilotCLIModelChange(ts, ev, seen, fctx)
	case "session.task_complete":
		w.insertCopilotCLIHookEvent(ts, fctx, "TaskCompleted", "", "", "", "", nil)
	case "user.message":
		w.handleCopilotCLIUserMessage(ts, ev, seen, fctx)
	case "assistant.message":
		w.handleCopilotCLIAssistantMessage(ts, ev, seen, fctx)
	case "tool.execution_start":
		w.handleCopilotCLIToolStart(ts, ev, fctx)
	case "tool.execution_complete":
		w.handleCopilotCLIToolComplete(ts, ev, fctx)
	case "subagent.started":
		w.handleCopilotCLISubagentStart(ts, ev, fctx)
	case "subagent.completed":
		w.handleCopilotCLISubagentComplete(ts, ev, fctx)
	case "skill.invoked":
		w.handleCopilotCLISkillInvoked(ts, ev, fctx)
	}
}

func (w *Watcher) handleCopilotCLISessionStart(ts time.Time, ev copilotCLIEvent, fctx *copilotCLIContext) {
	var data struct {
		SessionID      string `json:"sessionId"`
		CopilotVersion string `json:"copilotVersion"`
		Context        struct {
			CWD        string `json:"cwd"`
			GitRoot    string `json:"gitRoot"`
			Branch     string `json:"branch"`
			HeadCommit string `json:"headCommit"`
			Repository string `json:"repository"`
		} `json:"context"`
	}
	if err := json.Unmarshal(ev.Data, &data); err != nil {
		return
	}

	fctx.cwd = data.Context.CWD

	// Store branch and repo in metadata (skip git_root to avoid backslash SQL issues).
	metadata := map[string]string{
		"copilot_version": data.CopilotVersion,
		"branch":          data.Context.Branch,
		"repository":      data.Context.Repository,
	}

	w.insertCopilotCLIHookEventWithCWD(ts, fctx, "SessionStart", "", "", "", "", metadata, data.Context.CWD)
	if fctx.live {
		w.broadcastHookEvent(fctx.dbSessionID(), "SessionStart", "", "", "", "", "", "")
	}
}

func (w *Watcher) handleCopilotCLIModelChange(ts time.Time, ev copilotCLIEvent, seen map[string]struct{}, fctx *copilotCLIContext) {
	var data struct {
		NewModel string `json:"newModel"`
	}
	if err := json.Unmarshal(ev.Data, &data); err != nil || data.NewModel == "" {
		return
	}
	fctx.model = data.NewModel

	// Don't insert a visible message for model changes — just update fctx.model.
	// The model is stamped on subsequent real messages via fctx.model.
}

func (w *Watcher) handleCopilotCLIUserMessage(ts time.Time, ev copilotCLIEvent, seen map[string]struct{}, fctx *copilotCLIContext) {
	var data struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(ev.Data, &data); err != nil {
		return
	}
	content := strings.TrimSpace(data.Content)
	if content == "" {
		return
	}
	w.insertCopilotCLIMessage(fctx, ts, "user", "user", content, fctx.model, "", "", nil)
	if fctx.live {
		w.broadcastHookEvent(fctx.dbSessionID(), "UserPromptSubmit", "", "", "", "", "", "")
	}
}

func (w *Watcher) handleCopilotCLIAssistantMessage(ts time.Time, ev copilotCLIEvent, seen map[string]struct{}, fctx *copilotCLIContext) {
	var data struct {
		Content       string `json:"content"`
		ReasoningText string `json:"reasoningText"`
		OutputTokens  int64  `json:"outputTokens"`
		RequestID     string `json:"requestId"`
	}
	if err := json.Unmarshal(ev.Data, &data); err != nil {
		return
	}

	// Update model from tool.execution_complete if available (handled elsewhere).
	// outputTokens is per-response; no inputTokens available in this event.
	var usage *msgUsage
	if data.OutputTokens > 0 {
		usage = &msgUsage{OutputTokens: data.OutputTokens}
	}

	// Emit reasoning text as thinking message.
	reasoning := strings.TrimSpace(data.ReasoningText)
	if reasoning != "" {
		w.insertCopilotCLIMessage(fctx, ts, "thinking", "assistant", reasoning, fctx.model, "", "", nil)
	}

	// Emit content as assistant message (skip empty-content messages).
	content := strings.TrimSpace(data.Content)
	if content != "" {
		w.insertCopilotCLIMessage(fctx, ts, "assistant", "assistant", content, fctx.model, "", "", usage)
	}
	// Usage with empty content (tool-only turns) is dropped — cost tracking
	// relies on output_tokens from messages with actual content.
}

func (w *Watcher) handleCopilotCLIToolStart(ts time.Time, ev copilotCLIEvent, fctx *copilotCLIContext) {
	var data struct {
		ToolCallID string          `json:"toolCallId"`
		ToolName   string          `json:"toolName"`
		Arguments  json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(ev.Data, &data); err != nil {
		return
	}

	argsStr := truncate(string(data.Arguments), maxToolInput)
	w.insertCopilotCLIHookEventFull(ts, fctx, "PreToolUse", data.ToolName, argsStr, data.ToolCallID, "", ev.AgentID, "", nil, "")
	if fctx.live {
		w.broadcastHookEvent(fctx.dbSessionID(), "PreToolUse", data.ToolName, argsStr, data.ToolCallID, "", ev.AgentID, "")
	}
}

func (w *Watcher) handleCopilotCLIToolComplete(ts time.Time, ev copilotCLIEvent, fctx *copilotCLIContext) {
	var data struct {
		ToolCallID string `json:"toolCallId"`
		Model      string `json:"model"`
		Success    bool   `json:"success"`
		Result     struct {
			Content string `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(ev.Data, &data); err != nil {
		return
	}

	// Update model from tool completion context.
	if data.Model != "" {
		fctx.model = data.Model
	}

	eventType := "PostToolUse"
	if !data.Success {
		eventType = "PostToolUseFailure"
	}

	resultStr := truncate(data.Result.Content, maxToolContent)
	w.insertCopilotCLIHookEventFull(ts, fctx, eventType, "", "", data.ToolCallID, resultStr, ev.AgentID, "", nil, "")
	if fctx.live {
		w.broadcastHookEvent(fctx.dbSessionID(), eventType, "", "", data.ToolCallID, resultStr, ev.AgentID, "")
	}
}

func (w *Watcher) handleCopilotCLISubagentStart(ts time.Time, ev copilotCLIEvent, fctx *copilotCLIContext) {
	var data struct {
		ToolCallID  string `json:"toolCallId"`
		AgentName   string `json:"agentName"`
		Description string `json:"agentDescription"`
	}
	if err := json.Unmarshal(ev.Data, &data); err != nil {
		return
	}

	agentID := data.ToolCallID
	agentType := data.AgentName

	metadata := map[string]string{"description": truncate(data.Description, 512)}
	w.insertCopilotCLIHookEventWithAgent(ts, fctx, "SubagentStart", agentID, agentType, metadata)
	if fctx.live {
		w.broadcastHookEvent(fctx.dbSessionID(), "SubagentStart", "", "", "", "", agentID, agentType)
	}
}

func (w *Watcher) handleCopilotCLISubagentComplete(ts time.Time, ev copilotCLIEvent, fctx *copilotCLIContext) {
	var data struct {
		ToolCallID     string `json:"toolCallId"`
		AgentName      string `json:"agentName"`
		Model          string `json:"model"`
		TotalToolCalls int    `json:"totalToolCalls"`
		TotalTokens    int64  `json:"totalTokens"`
		DurationMs     int64  `json:"durationMs"`
	}
	if err := json.Unmarshal(ev.Data, &data); err != nil {
		return
	}

	agentID := data.ToolCallID
	agentType := data.AgentName

	metadata := map[string]string{
		"model":            data.Model,
		"total_tool_calls": fmt.Sprintf("%d", data.TotalToolCalls),
		"total_tokens":     fmt.Sprintf("%d", data.TotalTokens),
		"duration_ms":      fmt.Sprintf("%d", data.DurationMs),
	}
	w.insertCopilotCLIHookEventWithAgent(ts, fctx, "SubagentStop", agentID, agentType, metadata)
	if fctx.live {
		w.broadcastHookEvent(fctx.dbSessionID(), "SubagentStop", "", "", "", "", agentID, agentType)
	}
}

func (w *Watcher) handleCopilotCLISkillInvoked(ts time.Time, ev copilotCLIEvent, fctx *copilotCLIContext) {
	var data struct {
		Skill string `json:"skill"`
	}
	if err := json.Unmarshal(ev.Data, &data); err != nil || data.Skill == "" {
		return
	}
	metadata := map[string]string{"skill": data.Skill}
	w.insertCopilotCLIHookEvent(ts, fctx, "SkillInvoked", "", "", "", "", metadata)
}

// execSQLDebug wraps execSQL with additional logging for debugging insert failures.
func (w *Watcher) execSQLDebug(sql, eventType, sessionID string) {
	form := url.Values{}
	form.Set("sql", sql)

	ctx, cancel := context.WithTimeout(context.Background(), insertTimeout)
	defer cancel()

	req, err := newPostRequest(ctx, w.sqlURL, form)
	if err != nil {
		w.logger.Warn("copilot-cli insert: create request failed", "event", eventType, "session", sessionID, "error", err)
		return
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		w.logger.Warn("copilot-cli insert failed", "event", eventType, "session", sessionID, "error", err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		w.logger.Warn("copilot-cli insert non-200", "event", eventType, "session", sessionID, "status", resp.StatusCode, "body", string(body))
	}
}

// insertCopilotCLIMessage inserts a conversation message into tma1_messages.
func (w *Watcher) insertCopilotCLIMessage(fctx *copilotCLIContext, ts time.Time, messageType, role, content, model, toolName, toolUseID string, usage *msgUsage) {
	msTs := fctx.nextTsMs(ts)

	var sql string
	if usage != nil {
		sql = fmt.Sprintf(
			"INSERT INTO tma1_messages (ts, session_id, message_type, \"role\", content, model, tool_name, tool_use_id, "+
				"input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens) "+
				"VALUES (%d, '%s', '%s', '%s', '%s', '%s', '%s', '%s', %d, %d, %d, %d)",
			msTs,
			escapeSQLString(fctx.dbSessionID()),
			escapeSQLString(messageType),
			escapeSQLString(role),
			escapeSQLString(truncate(content, maxContentLen)),
			escapeSQLString(model),
			escapeSQLString(toolName),
			escapeSQLString(toolUseID),
			usage.InputTokens,
			usage.OutputTokens,
			usage.CacheReadTokens,
			usage.CacheCreationTokens,
		)
	} else {
		sql = fmt.Sprintf(
			"INSERT INTO tma1_messages (ts, session_id, message_type, \"role\", content, model, tool_name, tool_use_id) "+
				"VALUES (%d, '%s', '%s', '%s', '%s', '%s', '%s', '%s')",
			msTs,
			escapeSQLString(fctx.dbSessionID()),
			escapeSQLString(messageType),
			escapeSQLString(role),
			escapeSQLString(truncate(content, maxContentLen)),
			escapeSQLString(model),
			escapeSQLString(toolName),
			escapeSQLString(toolUseID),
		)
	}

	go func() {
		insertSem <- struct{}{}
		defer func() { <-insertSem }()
		w.execSQL(sql)
	}()
}

// insertCopilotCLIHookEvent inserts a hook event into tma1_hook_events.
func (w *Watcher) insertCopilotCLIHookEvent(ts time.Time, fctx *copilotCLIContext, eventType, toolName, toolInput, toolUseID, toolResult string, metadata map[string]string) {
	w.insertCopilotCLIHookEventFull(ts, fctx, eventType, toolName, toolInput, toolUseID, toolResult, "", "", metadata, "")
}

func (w *Watcher) insertCopilotCLIHookEventWithCWD(ts time.Time, fctx *copilotCLIContext, eventType, toolName, toolInput, toolUseID, toolResult string, metadata map[string]string, cwd string) {
	w.insertCopilotCLIHookEventFull(ts, fctx, eventType, toolName, toolInput, toolUseID, toolResult, "", "", metadata, cwd)
}

func (w *Watcher) insertCopilotCLIHookEventWithAgent(ts time.Time, fctx *copilotCLIContext, eventType, agentID, agentType string, metadata map[string]string) {
	w.insertCopilotCLIHookEventFull(ts, fctx, eventType, "", "", "", "", agentID, agentType, metadata, "")
}

func (w *Watcher) insertCopilotCLIHookEventFull(ts time.Time, fctx *copilotCLIContext, eventType, toolName, toolInput, toolUseID, toolResult, agentID, agentType string, metadata map[string]string, cwd string) {
	msTs := fctx.nextTsMs(ts)

	metadataJSON := ""
	if len(metadata) > 0 {
		if b, err := json.Marshal(metadata); err == nil {
			metadataJSON = string(b)
		}
	}

	if cwd == "" {
		cwd = fctx.cwd
	}

	sql := fmt.Sprintf(
		"INSERT INTO tma1_hook_events "+
			"(ts, session_id, event_type, agent_source, tool_name, tool_input, tool_result, "+
			"tool_use_id, agent_id, agent_type, notification_type, \"message\", cwd, transcript_path, conversation_id, metadata) "+
			"VALUES (%d, '%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s', '', '', '%s', '', '%s', '%s')",
		msTs,
		escapeSQLString(fctx.dbSessionID()),
		escapeSQLString(eventType),
		copilotCLIAgentSource,
		escapeSQLString(truncate(toolName, 256)),
		escapeSQLString(truncate(toolInput, maxToolInput)),
		escapeSQLString(truncate(toolResult, maxToolContent)),
		escapeSQLString(toolUseID),
		escapeSQLString(agentID),
		escapeSQLString(agentType),
		escapeSQLString(truncate(cwd, 512)),
		escapeSQLString(fctx.dbSessionID()),
		escapeSQLString(metadataJSON), // json.Marshal already escapes backslashes
	)
	go func() {
		insertSem <- struct{}{}
		defer func() { <-insertSem }()
		w.execSQLDebug(sql, eventType, fctx.sessionID)
	}()
}
