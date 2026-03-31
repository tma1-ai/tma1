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
)

const (
	codexScanInterval = 5 * time.Second
	codexActiveAge    = 10 * time.Minute // only watch files modified within this window
)

// StartCodexScanner periodically scans ~/.codex/sessions/ for active JSONL files
// and starts watching any new ones. Codex doesn't send hooks, so we discover
// session files by polling the filesystem.
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
	for _, offset := range []int{0, -1} {
		d := now.AddDate(0, 0, offset)
		dir := filepath.Join(baseDir, d.Format("2006"), d.Format("01"), d.Format("02"))
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
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
			// watcherKey is unique per file; sessionID groups files from the same run.
			sessionID := codexSessionGroup(baseName)
			watcherKey := "codex:" + baseName
			filePath := filepath.Join(dir, entry.Name())
			w.watchCodex(watcherKey, sessionID, filePath)
		}
	}
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
	live           bool   // true after initial backfill completes (first EOF)
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
		// Detect subagent from source field: {"subagent": "review"} vs "cli"
		var meta struct {
			ID     string          `json:"id"` // conversation ID (= OTel conversation.id)
			Source json.RawMessage `json:"source"`
			CWD    string          `json:"cwd"`
		}
		if err := json.Unmarshal(ev.Payload, &meta); err == nil {
			if meta.ID != "" {
				fctx.conversationID = meta.ID
			}
			var subSource struct {
				Subagent string `json:"subagent"`
			}
			if json.Unmarshal(meta.Source, &subSource) == nil && subSource.Subagent != "" {
				fctx.agentID = codexSubagentID(fctx.fileID, subSource.Subagent)
				fctx.agentType = subSource.Subagent
				w.insertCodexSubagentEvent(sessionID, ts, fctx.agentID, fctx.agentType, fctx.conversationID)
				if fctx.live {
					w.broadcastHookEvent(sessionID, "SubagentStart", "", "", "", "", fctx.agentID, fctx.agentType)
				}
				break
			}
		}
		w.insertCodexSessionStart(sessionID, ts, meta.CWD, fctx.conversationID)
		if fctx.live {
			w.broadcastHookEvent(sessionID, "SessionStart", "", "", "", "", "", "")
		}

	case "turn_context":
		// Extract model name and store as a message with model field set.
		var turnCtx struct {
			Model string `json:"model"`
		}
		if json.Unmarshal(ev.Payload, &turnCtx) == nil && turnCtx.Model != "" {
			w.insertCodexModelMessage(sessionID, ts, turnCtx.Model, seen)
		}

	case "event_msg":
		var eventMsg struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Phase   string `json:"phase"`
		}
		if err := json.Unmarshal(ev.Payload, &eventMsg); err != nil {
			return
		}
		switch eventMsg.Type {
		case "task_complete":
			// Emit SubagentStop for subagent files.
			if fctx.agentID != "" {
				w.insertCodexHookEvent(sessionID, ts, "SubagentStop", "", "", "", "", fctx)
			}
		case "user_message":
			msg := strings.TrimSpace(eventMsg.Message)
			if msg != "" {
				w.insertCodexMessage(sessionID, ts, "user", msg, seen)
			}
		case "agent_message":
			msg := strings.TrimSpace(eventMsg.Message)
			if msg != "" {
				w.insertCodexMessage(sessionID, ts, "assistant", msg, seen)
			}
		}

	case "response_item":
		var item codexResponseItem
		if err := json.Unmarshal(ev.Payload, &item); err != nil {
			return
		}
		w.processCodexResponseItem(sessionID, ts, item, seen, fctx)
	}
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

	case "function_call":
		w.insertCodexHookEvent(sessionID, ts, "PreToolUse", item.Name, item.Arguments, item.CallID, "", fctx)

	case "function_call_output":
		w.insertCodexHookEvent(sessionID, ts, "PostToolUse", "", "", item.CallID, item.Output, fctx)

	case "custom_tool_call":
		w.insertCodexHookEvent(sessionID, ts, "PreToolUse", item.Name, item.Input, item.CallID, "", fctx)

	case "custom_tool_call_output":
		w.insertCodexHookEvent(sessionID, ts, "PostToolUse", "", "", item.CallID, item.Output, fctx)
	}
}

// insertCodexModelMessage stores a synthetic message with the model field set.
// This makes the model visible in session detail KPI and cost calculation.
func (w *Watcher) insertCodexModelMessage(sessionID string, ts time.Time, model string, seen map[string]struct{}) {
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
			"VALUES (%d, '%s', 'assistant', 'assistant', '', '%s', '', '')",
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

func (w *Watcher) insertCodexSessionStart(sessionID string, ts time.Time, cwd, conversationID string) {
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

func (w *Watcher) insertCodexSubagentEvent(sessionID string, ts time.Time, agentID, agentType, conversationID string) {
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
			"VALUES (%d, '%s', 'SubagentStart', 'codex', '', '', '', '', '%s', '%s', '', '', '', '', '%s')",
		msTs,
		escapeSQLString(sessionID),
		escapeSQLString(agentID),
		escapeSQLString(agentType),
		escapeSQLString(conversationID),
	)
	go func() {
		insertSem <- struct{}{}
		defer func() { <-insertSem }()
		w.execSQL(sql)
	}()
}

func (w *Watcher) insertCodexHookEvent(sessionID string, ts time.Time, eventType, toolName, toolInput, toolUseID, toolResult string, fctx *codexFileContext) {
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

	sql := fmt.Sprintf(
		"INSERT INTO tma1_hook_events "+
			"(ts, session_id, event_type, agent_source, tool_name, tool_input, tool_result, "+
			"tool_use_id, agent_id, agent_type, notification_type, \"message\", cwd, transcript_path, conversation_id) "+
			"VALUES (%d, '%s', '%s', 'codex', '%s', '%s', '%s', '%s', '%s', '%s', '', '', '', '', '%s')",
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
