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
	"strings"
	"time"
)

const (
	copilotCLIScanInterval = 10 * time.Second
	copilotCLIActiveAge    = 10 * time.Minute
	copilotCLIAgentSource  = "copilot_cli"
	copilotCLISessionPfx   = "cp:"
)

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

	ticker := time.NewTicker(copilotCLIScanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := os.Stat(baseDir); err == nil {
				w.scanCopilotCLISessions(baseDir)
			}
		}
	}
}

func (w *Watcher) scanCopilotCLISessions(baseDir string) {
	now := time.Now()

	// Prune stopped watchers to prevent unbounded memory growth.
	w.mu.Lock()
	var stoppedCount int
	for key, sw := range w.sessions {
		if sw.stopped && strings.HasPrefix(key, copilotCLISessionPfx) {
			stoppedCount++
		}
	}
	if stoppedCount > 50 {
		for key, sw := range w.sessions {
			if sw.stopped && strings.HasPrefix(key, copilotCLISessionPfx) {
				delete(w.sessions, key)
			}
		}
	}
	w.mu.Unlock()

	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		eventsFile := filepath.Join(baseDir, entry.Name(), "events.jsonl")
		info, err := os.Stat(eventsFile)
		if err != nil || now.Sub(info.ModTime()) > copilotCLIActiveAge {
			continue
		}
		sessionID := entry.Name()
		watcherKey := copilotCLISessionPfx + sessionID
		w.watchCopilotCLI(watcherKey, sessionID, eventsFile)
	}
}

func (w *Watcher) watchCopilotCLI(watcherKey, sessionID, filePath string) {
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

	go w.tailCopilotCLIFile(ctx, watcherKey, sessionID, filePath, seen)
	w.logger.Info("watching copilot-cli session", "session", sessionID, "file", filePath)
}

func (w *Watcher) tailCopilotCLIFile(ctx context.Context, watcherKey, sessionID, filePath string, seen map[string]struct{}) {
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

	// Skip backfill if this session was already ingested (prevents duplicates on restart).
	dbSID := copilotCLISessionPfx + sessionID
	if w.copilotCLISessionExists(dbSID) {
		// Seek to end — only process new lines written after this point.
		if _, err := f.Seek(0, io.SeekEnd); err != nil {
			w.logger.Warn("copilot-cli seek to end failed", "session", sessionID, "error", err)
		}
	}

	reader := bufio.NewReader(f)
	var buf strings.Builder
	fctx := &copilotCLIContext{sessionID: sessionID}
	idleCount := 0
	const maxIdlePolls = 600 // 5 minutes at 500ms

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
}

// copilotCLIContext tracks per-file state during parsing.
type copilotCLIContext struct {
	sessionID string
	model     string // current model (updated by session.start and session.model_change)
	cwd       string
	live      bool // true after initial backfill completes
}

// dbSessionID returns the namespaced session ID for database storage.
func (c *copilotCLIContext) dbSessionID() string {
	return copilotCLISessionPfx + c.sessionID
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

	ts, _ := time.Parse(time.RFC3339Nano, ev.Timestamp)
	if ts.IsZero() {
		ts = time.Now()
	}

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
	// Skip: hook.start, hook.end, session.warning, system.notification,
	// session.mode_changed, session.context_changed, assistant.turn_start, assistant.turn_end
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

	// Build metadata with context info.
	metadata := map[string]string{
		"copilot_version": data.CopilotVersion,
		"git_root":        data.Context.GitRoot,
		"branch":          data.Context.Branch,
		"head_commit":     data.Context.HeadCommit,
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

	// Insert synthetic model message (same pattern as Codex).
	w.insertCopilotCLIMessage(fctx.dbSessionID(), ts, "assistant", "assistant", "", fctx.model, "", "", nil)
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
	w.insertCopilotCLIMessage(fctx.dbSessionID(), ts, "user", "user", content, fctx.model, "", "", nil)
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
		w.insertCopilotCLIMessage(fctx.dbSessionID(), ts, "thinking", "assistant", reasoning, fctx.model, "", "", nil)
	}

	// Emit content as assistant message (may be empty when only tool calls).
	content := strings.TrimSpace(data.Content)
	if content != "" {
		w.insertCopilotCLIMessage(fctx.dbSessionID(), ts, "assistant", "assistant", content, fctx.model, "", "", usage)
	} else if usage != nil {
		// Even with empty content, record usage on a synthetic message so cost tracking works.
		w.insertCopilotCLIMessage(fctx.dbSessionID(), ts, "assistant", "assistant", "", fctx.model, "", "", usage)
	}
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
	w.insertCopilotCLIHookEvent(ts, fctx, "PreToolUse", data.ToolName, argsStr, data.ToolCallID, "", nil)
	if fctx.live {
		w.broadcastHookEvent(fctx.dbSessionID(), "PreToolUse", data.ToolName, argsStr, data.ToolCallID, "", "", "")
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
	w.insertCopilotCLIHookEvent(ts, fctx, eventType, "", "", data.ToolCallID, resultStr, nil)
	if fctx.live {
		w.broadcastHookEvent(fctx.dbSessionID(), eventType, "", "", data.ToolCallID, resultStr, "", "")
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

// copilotCLISessionExists checks if data for this session already exists in GreptimeDB.
// Used to skip backfill on restart and prevent duplicate ingestion.
func (w *Watcher) copilotCLISessionExists(dbSessionID string) bool {
	form := url.Values{}
	form.Set("sql", fmt.Sprintf(
		"SELECT 1 FROM tma1_hook_events WHERE session_id = '%s' AND agent_source = 'copilot_cli' LIMIT 1",
		escapeSQLString(dbSessionID),
	))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := newPostRequest(ctx, w.sqlURL, form)
	if err != nil {
		return false
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	// If the response contains any row data, the session exists.
	return resp.StatusCode == 200 && strings.Contains(string(body), "\"rows\":[")
}

// insertCopilotCLIMessage inserts a conversation message into tma1_messages.
func (w *Watcher) insertCopilotCLIMessage(dbSessionID string, ts time.Time, messageType, role, content, model, toolName, toolUseID string, usage *msgUsage) {
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

	var sql string
	if usage != nil {
		sql = fmt.Sprintf(
			"INSERT INTO tma1_messages (ts, session_id, message_type, \"role\", content, model, tool_name, tool_use_id, "+
				"input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens) "+
				"VALUES (%d, '%s', '%s', '%s', '%s', '%s', '%s', '%s', %d, %d, %d, %d)",
			msTs,
			escapeSQLString(dbSessionID),
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
			escapeSQLString(dbSessionID),
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

	dbSessionID := fctx.dbSessionID()

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
		escapeSQLString(dbSessionID),
		escapeSQLString(eventType),
		copilotCLIAgentSource,
		escapeSQLString(truncate(toolName, 256)),
		escapeSQLString(truncate(toolInput, maxToolInput)),
		escapeSQLString(truncate(toolResult, maxToolContent)),
		escapeSQLString(toolUseID),
		escapeSQLString(agentID),
		escapeSQLString(agentType),
		escapeSQLString(truncate(cwd, 512)),
		escapeSQLString(dbSessionID),
		escapeSQLString(metadataJSON),
	)
	go func() {
		insertSem <- struct{}{}
		defer func() { <-insertSem }()
		w.execSQL(sql)
	}()
}
