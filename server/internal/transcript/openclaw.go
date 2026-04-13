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
	openclawScanInterval = 5 * time.Second
	openclawActiveAge    = 10 * time.Minute
)

// StartOpenClawScanner periodically scans OpenClaw session directories for
// active JSONL transcript files and starts watching new ones.
// OpenClaw stores sessions at ~/.openclaw/agents/<agentId>/sessions/<timestamp>_<sessionId>.jsonl.
// The OPENCLAW_STATE_DIR env var overrides the base directory.
func (w *Watcher) StartOpenClawScanner(ctx context.Context) {
	dirs := openclawBaseDirs()
	if len(dirs) == 0 {
		w.logger.Warn("openclaw scanner: cannot determine home directory")
		return
	}
	w.logger.Info("openclaw session scanner started", "paths", dirs)

	ticker := time.NewTicker(openclawScanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, base := range dirs {
				agentsDir := filepath.Join(base, "agents")
				if _, err := os.Stat(agentsDir); err != nil {
					continue
				}
				w.scanOpenClawAgents(agentsDir)
			}
		}
	}
}

// openclawBaseDirs returns the directories to scan, in priority order.
func openclawBaseDirs() []string {
	var dirs []string

	// OPENCLAW_STATE_DIR takes precedence.
	if stateDir := os.Getenv("OPENCLAW_STATE_DIR"); stateDir != "" {
		dirs = append(dirs, stateDir)
		return dirs
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	dirs = append(dirs, filepath.Join(homeDir, ".openclaw"))
	// Legacy pre-rebrand directory.
	dirs = append(dirs, filepath.Join(homeDir, ".clawdbot"))
	return dirs
}

func (w *Watcher) scanOpenClawAgents(agentsDir string) {
	now := time.Now()

	// Prune stopped openclaw watchers to prevent unbounded memory growth.
	w.mu.Lock()
	var stoppedCount int
	for key, sw := range w.sessions {
		if sw.stopped && strings.HasPrefix(key, "openclaw:") {
			stoppedCount++
		}
	}
	if stoppedCount > 50 {
		for key, sw := range w.sessions {
			if sw.stopped && strings.HasPrefix(key, "openclaw:") {
				delete(w.sessions, key)
			}
		}
	}
	w.mu.Unlock()

	// List agent directories: ~/.openclaw/agents/<agentId>/
	agentEntries, err := os.ReadDir(agentsDir)
	if err != nil {
		return
	}
	for _, agentEntry := range agentEntries {
		if !agentEntry.IsDir() {
			continue
		}
		agentID := agentEntry.Name()
		sessionsDir := filepath.Join(agentsDir, agentID, "sessions")
		entries, err := os.ReadDir(sessionsDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if !isOpenClawPrimaryTranscript(name) {
				continue
			}
			info, err := entry.Info()
			if err != nil || now.Sub(info.ModTime()) > openclawActiveAge {
				continue
			}
			sessionID := openclawSessionIDFromFilename(name)
			dbSessionID := "oc:" + agentID + ":" + sessionID
			watcherKey := "openclaw:" + agentID + ":" + strings.TrimSuffix(name, ".jsonl")
			filePath := filepath.Join(sessionsDir, name)
			w.watchOpenClaw(watcherKey, dbSessionID, agentID, filePath)
		}
	}
}

// isOpenClawPrimaryTranscript returns true for active transcript files,
// excluding archive artifacts (.reset.*, .deleted.*, .bak.*) and the store index.
func isOpenClawPrimaryTranscript(name string) bool {
	if name == "sessions.json" {
		return false
	}
	if !strings.HasSuffix(name, ".jsonl") {
		return false
	}
	// Exclude archive artifacts: *.jsonl.reset.<ts>, *.jsonl.deleted.<ts>, *.jsonl.bak.<ts>
	for _, suffix := range []string{".reset.", ".deleted.", ".bak."} {
		if strings.Contains(name, suffix) {
			return false
		}
	}
	return true
}

// openclawSessionIDFromFilename extracts the session UUID from the transcript filename.
// OpenClaw gateway format: "<sessionId>.jsonl" (primary).
// Pi CLI fallback: "<timestamp>_<sessionId>.jsonl" (splits on first underscore).
// Topic sessions may appear with or without a timestamp prefix:
//   - "<sessionId>-topic-<threadId>.jsonl"
//   - "<timestamp>_<sessionId>-topic-<threadId>.jsonl"
func openclawSessionIDFromFilename(name string) string {
	base := strings.TrimSuffix(name, ".jsonl")
	// Split on first underscore: timestamp part is before, sessionId part is after.
	if idx := strings.IndexByte(base, '_'); idx >= 0 {
		return base[idx+1:]
	}
	return base
}

func (w *Watcher) watchOpenClaw(watcherKey, dbSessionID, agentID, filePath string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	existing, ok := w.sessions[watcherKey]
	if ok && !existing.stopped {
		return
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

	go w.tailOpenClawFile(ctx, watcherKey, dbSessionID, agentID, filePath, seen)
	w.logger.Info("watching openclaw session", "session", dbSessionID, "file", filePath)
}

func (w *Watcher) tailOpenClawFile(ctx context.Context, watcherKey, dbSessionID, agentID, filePath string, seen map[string]struct{}) {
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
	fctx := &openclawFileContext{agentID: agentID}
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
					w.processOpenClawLine(dbSessionID, trimmed, seen, fctx)
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
				w.logger.Info("openclaw session idle, stopping watcher", "session", dbSessionID)
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
			w.logger.Debug("openclaw file read error", "session", dbSessionID, "error", err)
			return
		}
	}
}

// openclawFileContext tracks per-file state during parsing.
type openclawFileContext struct {
	agentID string
	live    bool // true after initial backfill (first EOF)
}

// --- OpenClaw JSONL types (from pi-coding-agent SessionManager) ---

// openclawEntry is the top-level JSON object per JSONL line.
type openclawEntry struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	ParentID  *string         `json:"parentId"`
	Timestamp string          `json:"timestamp"`
	Message   json.RawMessage `json:"message,omitempty"`
	// session header fields
	Version       int    `json:"version,omitempty"`
	CWD           string `json:"cwd,omitempty"`
	ParentSession string `json:"parentSession,omitempty"`
	// compaction fields
	Summary      string `json:"summary,omitempty"`
	TokensBefore int    `json:"tokensBefore,omitempty"`
	// model_change fields
	Provider string `json:"provider,omitempty"`
	ModelID  string `json:"modelId,omitempty"`
}

// ocMessage represents an AgentMessage in the JSONL transcript.
type ocMessage struct {
	Role         string          `json:"role"`
	Content      json.RawMessage `json:"content"`
	Provider     string          `json:"provider"`
	Model        string          `json:"model"`
	Timestamp    int64           `json:"timestamp"` // Unix ms
	StopReason   string          `json:"stopReason"`
	ErrorMessage string          `json:"errorMessage"`
	DurationMs   float64         `json:"durationMs"`
	Usage        *ocUsage        `json:"usage"`
	// toolResult fields
	ToolCallID string `json:"toolCallId"`
	ToolName   string `json:"toolName"`
	IsError    bool   `json:"isError"`
	// bashExecution fields
	Command   string `json:"command"`
	Output    string `json:"output"`
	ExitCode  *int   `json:"exitCode"`
	Cancelled bool   `json:"cancelled"`
	Truncated bool   `json:"truncated"`
}

type ocUsage struct {
	Input       int     `json:"input"`
	Output      int     `json:"output"`
	CacheRead   int     `json:"cacheRead"`
	CacheWrite  int     `json:"cacheWrite"`
	TotalTokens int     `json:"totalTokens"`
	Cost        *ocCost `json:"cost"`
}

type ocCost struct {
	Total float64 `json:"total"`
}

// ocContentBlock represents a content block within a message.
// Pi runtime writes toolCall + arguments; Anthropic API uses tool_use + input.
type ocContentBlock struct {
	Type      string                 `json:"type"` // text, thinking, toolCall/tool_use, image
	Text      string                 `json:"text"`
	Thinking  string                 `json:"thinking"`
	ID        string                 `json:"id"`
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"` // pi-coding-agent format
	Input     map[string]interface{} `json:"input"`     // Anthropic API format
}

// isToolCallBlock returns true for tool call content blocks.
// Pi runtime uses "toolCall"; this helper also accepts "tool_use",
// "tool_call", "toolcall" for read-time compatibility.
func isToolCallBlock(t string) bool {
	switch strings.ToLower(t) {
	case "toolcall", "tool_call", "tool_use":
		return true
	}
	return false
}

// toolCallInput returns the tool arguments, preferring pi format (arguments)
// and falling back to Anthropic format (input).
func (b *ocContentBlock) toolCallInput() map[string]interface{} {
	if len(b.Arguments) > 0 {
		return b.Arguments
	}
	return b.Input
}

func (w *Watcher) processOpenClawLine(sessionID, line string, seen map[string]struct{}, fctx *openclawFileContext) {
	var entry openclawEntry
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		return
	}

	// Dedup by entry ID (OpenClaw v3 entries all have unique 8-char hex IDs).
	// Falls back to type+timestamp+content-prefix hash for v1 format (no id field).
	dedupKey := entry.ID
	if dedupKey == "" {
		prefix := line
		if len(prefix) > 200 {
			prefix = prefix[:200]
		}
		dedupKey = entry.Type + ":" + entry.Timestamp + ":" + prefix
	}
	if _, ok := seen[dedupKey]; ok {
		return
	}
	seen[dedupKey] = struct{}{}

	ts, _ := time.Parse(time.RFC3339Nano, entry.Timestamp)
	if ts.IsZero() {
		ts = time.Now()
	}

	switch entry.Type {
	case "session":
		w.insertOpenClawSessionStart(sessionID, ts, entry.CWD)
		if fctx.live {
			w.broadcastHookEvent(sessionID, "SessionStart", "", "", "", "", "", "")
		}

	case "message":
		w.processOpenClawMessage(sessionID, ts, entry.ID, entry.Message, seen, fctx)

	case "compaction":
		w.insertOpenClawHookEvent(sessionID, ts, "ContextCompaction", "", "", "", "", fctx)

	case "model_change":
		if entry.ModelID != "" {
			w.insertOpenClawModelMessage(sessionID, ts, entry.ModelID, entry.Provider, seen)
		}

	// Skip: custom, custom_message, label, session_info, branch_summary,
	// thinking_level_change — not relevant for observability.
	}
}

func (w *Watcher) processOpenClawMessage(sessionID string, ts time.Time, entryID string, raw json.RawMessage, seen map[string]struct{}, fctx *openclawFileContext) {
	if len(raw) == 0 {
		return
	}
	var msg ocMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return
	}

	switch msg.Role {
	case "user":
		text := ocExtractText(msg.Content, false)
		if text == "" {
			return
		}
		w.insertOpenClawMessage(sessionID, ts, "user", "user", text, "", nil, 0)

	case "assistant":
		w.processOpenClawAssistant(sessionID, ts, &msg, seen, fctx)

	case "toolResult":
		text := ocExtractText(msg.Content, false)
		w.insertOpenClawHookEvent(sessionID, ts, "PostToolUse", msg.ToolName, "", msg.ToolCallID, truncate(text, maxToolContent), fctx)

	case "bashExecution":
		// Generate a synthetic tool_use_id so session detail can pair Pre/PostToolUse.
		bashID := "bash-" + entryID
		if entryID == "" {
			bashID = "bash-" + fmt.Sprintf("%d", ts.UnixMilli())
		}
		w.insertOpenClawHookEvent(sessionID, ts, "PreToolUse", "bash", truncate(msg.Command, maxToolInput), bashID, "", fctx)
		w.insertOpenClawHookEvent(sessionID, ts, "PostToolUse", "bash", "", bashID, truncate(msg.Output, maxToolContent), fctx)

	// Skip: custom, branchSummary, compactionSummary — not conversation content.
	}
}

func (w *Watcher) processOpenClawAssistant(sessionID string, ts time.Time, msg *ocMessage, seen map[string]struct{}, fctx *openclawFileContext) {
	// Convert usage to CC-compatible format for insertMessage.
	var usage *msgUsage
	if msg.Usage != nil {
		usage = &msgUsage{
			InputTokens:         int64(msg.Usage.Input),
			OutputTokens:        int64(msg.Usage.Output),
			CacheReadTokens:     int64(msg.Usage.CacheRead),
			CacheCreationTokens: int64(msg.Usage.CacheWrite),
		}
	}

	durMs := int64(msg.DurationMs)

	// Extract text content (skip thinking blocks for the main message).
	text := ocExtractText(msg.Content, true)
	if text != "" {
		w.insertOpenClawMessage(sessionID, ts, "assistant", "assistant", text, msg.Model, usage, durMs)
		usage = nil // attach to first emitted message only
	}

	// Extract tool calls from content blocks.
	var blocks []ocContentBlock
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		// Content was a plain string — no tool calls or thinking to extract.
		// If we haven't emitted a message yet (empty text), emit a synthetic one for usage tracking.
		if text == "" && usage != nil {
			w.insertOpenClawMessage(sessionID, ts, "assistant", "assistant", "", msg.Model, usage, durMs)
		}
		return
	}

	// If no text was emitted but there are tool calls, emit a synthetic message for usage tracking.
	if text == "" && usage != nil {
		w.insertOpenClawMessage(sessionID, ts, "assistant", "assistant", "", msg.Model, usage, durMs)
	}
	for _, b := range blocks {
		if !isToolCallBlock(b.Type) {
			continue
		}
		toolDedupKey := "toolcall:" + b.ID
		if _, ok := seen[toolDedupKey]; ok {
			continue
		}
		seen[toolDedupKey] = struct{}{}

		var inputStr string
		if args := b.toolCallInput(); len(args) > 0 {
			if data, err := json.Marshal(args); err == nil {
				inputStr = string(data)
			}
		}
		w.insertOpenClawHookEvent(sessionID, ts, "PreToolUse", b.Name, truncate(inputStr, maxToolInput), b.ID, "", fctx)
	}

	// Extract thinking blocks as separate messages.
	for _, b := range blocks {
		if b.Type == "thinking" {
			thinking := strings.TrimSpace(b.Thinking)
			if thinking != "" {
				w.insertOpenClawMessage(sessionID, ts, "thinking", "assistant", truncate(thinking, maxContentLen), msg.Model, nil, 0)
			}
		}
	}
}

// ocExtractText extracts displayable text from a message content field.
// Content can be a plain string or an array of content blocks.
func ocExtractText(content json.RawMessage, skipThinking bool) string {
	if len(content) == 0 {
		return ""
	}

	// Try as plain string first.
	var str string
	if err := json.Unmarshal(content, &str); err == nil {
		return strings.TrimSpace(str)
	}

	// Parse as array of content blocks.
	var blocks []ocContentBlock
	if err := json.Unmarshal(content, &blocks); err != nil {
		return ""
	}

	var sb strings.Builder
	for _, b := range blocks {
		switch b.Type {
		case "text":
			text := strings.TrimSpace(b.Text)
			if text != "" {
				if sb.Len() > 0 {
					sb.WriteByte('\n')
				}
				sb.WriteString(text)
			}
		case "thinking":
			if !skipThinking {
				thinking := strings.TrimSpace(b.Thinking)
				if thinking != "" {
					if sb.Len() > 0 {
						sb.WriteByte('\n')
					}
					sb.WriteString(thinking)
				}
			}
		}
	}
	return sb.String()
}

// --- Insert helpers ---

func (w *Watcher) insertOpenClawSessionStart(sessionID string, ts time.Time, cwd string) {
	msTs := w.nextTS(ts)

	sql := fmt.Sprintf(
		"INSERT INTO tma1_hook_events "+
			"(ts, session_id, event_type, agent_source, tool_name, tool_input, tool_result, "+
			"tool_use_id, agent_id, agent_type, notification_type, \"message\", cwd, transcript_path, conversation_id) "+
			"VALUES (%d, '%s', 'SessionStart', 'openclaw', '', '', '', '', '', '', '', '', '%s', '', '')",
		msTs,
		escapeSQLString(sessionID),
		escapeSQLString(truncate(cwd, 512)),
	)
	go func() {
		insertSem <- struct{}{}
		defer func() { <-insertSem }()
		w.execSQL(sql)
	}()
}

func (w *Watcher) insertOpenClawMessage(sessionID string, ts time.Time, msgType, role, content, model string, usage *msgUsage, durationMs int64) {
	msTs := w.nextTS(ts)

	var sql string
	if usage != nil {
		sql = fmt.Sprintf(
			"INSERT INTO tma1_messages (ts, session_id, message_type, \"role\", content, model, tool_name, tool_use_id, "+
				"input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, duration_ms) "+
				"VALUES (%d, '%s', '%s', '%s', '%s', '%s', '', '', %d, %d, %d, %d, %d)",
			msTs,
			escapeSQLString(sessionID),
			escapeSQLString(msgType),
			escapeSQLString(role),
			escapeSQLString(truncate(content, maxContentLen)),
			escapeSQLString(model),
			usage.InputTokens,
			usage.OutputTokens,
			usage.CacheReadTokens,
			usage.CacheCreationTokens,
			durationMs,
		)
	} else if durationMs > 0 {
		sql = fmt.Sprintf(
			"INSERT INTO tma1_messages (ts, session_id, message_type, \"role\", content, model, tool_name, tool_use_id, duration_ms) "+
				"VALUES (%d, '%s', '%s', '%s', '%s', '%s', '', '', %d)",
			msTs,
			escapeSQLString(sessionID),
			escapeSQLString(msgType),
			escapeSQLString(role),
			escapeSQLString(truncate(content, maxContentLen)),
			escapeSQLString(model),
			durationMs,
		)
	} else {
		sql = fmt.Sprintf(
			"INSERT INTO tma1_messages (ts, session_id, message_type, \"role\", content, model, tool_name, tool_use_id) "+
				"VALUES (%d, '%s', '%s', '%s', '%s', '%s', '', '')",
			msTs,
			escapeSQLString(sessionID),
			escapeSQLString(msgType),
			escapeSQLString(role),
			escapeSQLString(truncate(content, maxContentLen)),
			escapeSQLString(model),
		)
	}

	go func() {
		insertSem <- struct{}{}
		defer func() { <-insertSem }()
		w.execSQL(sql)
	}()
}

func (w *Watcher) insertOpenClawModelMessage(sessionID string, ts time.Time, model, provider string, seen map[string]struct{}) {
	key := "ocmodel:" + model
	if _, ok := seen[key]; ok {
		return
	}
	seen[key] = struct{}{}

	msTs := w.nextTS(ts)
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
}

func (w *Watcher) insertOpenClawHookEvent(sessionID string, ts time.Time, eventType, toolName, toolInput, toolUseID, toolResult string, fctx *openclawFileContext) {
	msTs := w.nextTS(ts)

	sql := fmt.Sprintf(
		"INSERT INTO tma1_hook_events "+
			"(ts, session_id, event_type, agent_source, tool_name, tool_input, tool_result, "+
			"tool_use_id, agent_id, agent_type, notification_type, \"message\", cwd, transcript_path, conversation_id) "+
			"VALUES (%d, '%s', '%s', 'openclaw', '%s', '%s', '%s', '%s', '', '', '', '', '', '', '')",
		msTs,
		escapeSQLString(sessionID),
		escapeSQLString(eventType),
		escapeSQLString(truncate(toolName, 256)),
		escapeSQLString(truncate(toolInput, maxToolInput)),
		escapeSQLString(truncate(toolResult, maxToolContent)),
		escapeSQLString(toolUseID),
	)
	go func() {
		insertSem <- struct{}{}
		defer func() { <-insertSem }()
		w.execSQL(sql)
	}()

	if fctx != nil && fctx.live {
		w.broadcastHookEvent(sessionID, eventType, toolName, toolInput, toolUseID, toolResult, "", "")
	}
}

// nextTS returns a monotonically increasing millisecond timestamp.
func (w *Watcher) nextTS(ts time.Time) int64 {
	msTs := ts.UnixMilli()
	for {
		prev := lastInsertTS.Load()
		next := msTs
		if next <= prev {
			next = prev + 1
		}
		if lastInsertTS.CompareAndSwap(prev, next) {
			return next
		}
	}
}
