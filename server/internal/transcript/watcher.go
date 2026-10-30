// Package transcript watches Claude Code JSONL transcript files and inserts
// parsed conversation messages into GreptimeDB.
package transcript

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tma1-ai/tma1/server/internal/strutil"
)

const (
	pollInterval    = 500 * time.Millisecond
	maxContentLen   = 32768
	insertTimeout   = 10 * time.Second
	maxToolInput    = 2048
	maxToolContent  = 4096
	sessionStartKey = "__session_started__" // seen-map marker for SessionStart dedup
)

// maxConcurrentInserts limits the number of concurrent SQL INSERT goroutines
// to prevent overwhelming GreptimeDB when backfilling large transcripts.
var insertSem = make(chan struct{}, 16)

// BroadcastFunc is called to fan out hook events to SSE subscribers.
type BroadcastFunc func(data []byte)

// Watcher manages per-session JSONL transcript file watchers.
type Watcher struct {
	mu        sync.Mutex
	sessions  map[string]*sessionWatch
	sqlURL    string
	logger    *slog.Logger
	broadcast BroadcastFunc

	// codexParentSession maps a Codex rollout-file timestamp prefix
	// (the value codexSessionGroup returns) to the parent run's
	// conversation UUID. Populated when a non-subagent session_meta is
	// parsed; consulted by subagent files in the same run so their
	// SubagentStart / SubagentStop rows land in the parent's UUID-keyed
	// session rather than the legacy filename-prefix bucket.
	codexParentSession map[string]string

	// IsLiveSession, when set, tells the Codex JSONL parser to skip
	// inserts for any session that has recently emitted a hook event
	// to /api/hooks (i.e. the Codex adapter is wired and posting in
	// real time). Without this, the JSONL parser and the hook handler
	// would both write rows for the same Codex event. handler.New
	// installs the gate at startup; main.go does the wiring.
	IsLiveSession func(sessionID string) bool
}

// recordCodexParentSession publishes the parent UUID for a Codex
// timestamp prefix. Called once per main-session rollout file; safe
// to call repeatedly with the same value.
func (w *Watcher) recordCodexParentSession(prefix, uuid string) {
	if prefix == "" || uuid == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.codexParentSession == nil {
		w.codexParentSession = map[string]string{}
	}
	w.codexParentSession[prefix] = uuid
}

// lookupCodexParentSession returns the parent UUID for a Codex
// timestamp prefix, or "" if the parent's session_meta hasn't been
// processed yet (subagent goroutine may have raced ahead of parent).
func (w *Watcher) lookupCodexParentSession(prefix string) string {
	if prefix == "" {
		return ""
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.codexParentSession == nil {
		return ""
	}
	return w.codexParentSession[prefix]
}

type sessionWatch struct {
	cancel  context.CancelFunc
	seen    map[string]struct{} // content hashes for dedup
	stopped bool                // true after watcher goroutine exits (can be restarted)
	ccUsage map[string]msgUsage
}

// NewWatcher creates a transcript watcher that writes to the given GreptimeDB instance.
// The optional broadcast callback fans out hook events to SSE subscribers.
func NewWatcher(greptimeHTTPPort int, logger *slog.Logger, broadcast BroadcastFunc) *Watcher {
	return &Watcher{
		sessions:  make(map[string]*sessionWatch),
		sqlURL:    fmt.Sprintf("http://localhost:%d/v1/sql", greptimeHTTPPort),
		logger:    logger,
		broadcast: broadcast,
	}
}

// Watch starts tailing a JSONL transcript file for the given session.
// It reads from the beginning to capture existing content, then polls for new lines.
// Safe to call multiple times for the same session — duplicates are ignored.
//
// The watcher always emits its own SessionStart on the first valid transcript entry.
// When called from the hook handler, this may produce a duplicate SessionStart row,
// but tma1_hook_events is append_mode=true and consumers use SELECT DISTINCT session_id,
// so duplicates are harmless. This is safer than suppressing the fallback, because the
// hook handler's own insertHookEvent is async and may fail silently.
func (w *Watcher) Watch(sessionID, transcriptPath string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	existing, ok := w.sessions[sessionID]
	if ok && !existing.stopped {
		return // already watching
	}

	// Reuse existing seen map to avoid re-inserting previously processed lines.
	var seen map[string]struct{}
	if ok && existing.seen != nil {
		seen = existing.seen
	} else {
		seen = make(map[string]struct{})
	}

	ctx, cancel := context.WithCancel(context.Background())
	usage := make(map[string]msgUsage)
	if ok && existing.ccUsage != nil {
		usage = existing.ccUsage
	}
	sw := &sessionWatch{cancel: cancel, seen: seen, ccUsage: usage}
	w.sessions[sessionID] = sw

	go w.tailFile(ctx, sessionID, transcriptPath, sw)
	w.logger.Info("started watching transcript", "session", sessionID, "path", transcriptPath)
}

// Stop stops watching the transcript for the given session.
func (w *Watcher) Stop(sessionID string) {
	w.mu.Lock()
	sw, ok := w.sessions[sessionID]
	if ok {
		delete(w.sessions, sessionID)
	}
	w.mu.Unlock()

	if ok {
		sw.cancel()
		w.logger.Info("stopped watching transcript", "session", sessionID)
	}
}

// StopAll stops all active watchers. Called on server shutdown.
func (w *Watcher) StopAll() {
	w.mu.Lock()
	sessions := w.sessions
	w.sessions = make(map[string]*sessionWatch)
	w.mu.Unlock()

	for _, sw := range sessions {
		sw.cancel()
	}
}

func (w *Watcher) tailFile(ctx context.Context, sessionID, filePath string, watch *sessionWatch) {
	seen, usage := watch.seen, watch.ccUsage
	// Mark as stopped on exit — preserves seen map so Watch() can retry without re-inserting.
	defer func() {
		w.mu.Lock()
		if sw := w.sessions[sessionID]; sw == watch {
			sw.stopped = true
		}
		w.mu.Unlock()
	}()

	// Wait briefly for the file to appear (hook may arrive before file is created).
	var f *os.File
	for i := 0; i < 10; i++ {
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
		w.logger.Warn("transcript file not found after retries, will retry on next hook", "path", filePath)
		return
	}
	defer f.Close()

	reader := bufio.NewReader(f)
	var buf strings.Builder

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			buf.WriteString(line)
			if strings.HasSuffix(line, "\n") {
				trimmed := strings.TrimSpace(buf.String())
				buf.Reset()
				if trimmed != "" {
					w.processLine(sessionID, trimmed, seen, usage)
				}
			}
			continue
		}

		if err == io.EOF {
			select {
			case <-ctx.Done():
				return
			case <-time.After(pollInterval):
			}
			continue
		}

		if err != nil {
			w.logger.Warn("transcript read error", "session", sessionID, "error", err)
			return
		}
	}
}

// transcriptEntry matches the JSONL format written by Claude Code.
type transcriptEntry struct {
	SessionID string         `json:"sessionId"`
	Timestamp string         `json:"timestamp"`
	Type      string         `json:"type"` // "user", "assistant", "progress"
	UUID      string         `json:"uuid"`
	Message   *transcriptMsg `json:"message"`
	CWD       string         `json:"cwd"`
}

type transcriptMsg struct {
	ID      string          `json:"id"`
	Role    string          `json:"role"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"` // string or []contentBlock
	Usage   *msgUsage       `json:"usage"`
}

// msgUsage captures token counts from the API response.
type msgUsage struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	CacheReadTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_input_tokens"`
	// Splits cache writes by duration, which Anthropic bills at different
	// rates. Older transcripts omit it.
	CacheCreation *cacheCreationSplit `json:"cache_creation"`
	ServiceTier   string              `json:"service_tier"`
}

type cacheCreationSplit struct {
	LongTokens int64 `json:"ephemeral_1h_input_tokens"`
}

func (u msgUsage) countsZero() bool {
	return u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheReadTokens == 0 && u.CacheCreationTokens == 0
}

type contentBlock struct {
	Type      string          `json:"type"` // "text", "thinking", "tool_use", "tool_result"
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Name      string          `json:"name"`        // tool_use
	ID        string          `json:"id"`          // tool_use
	Input     json.RawMessage `json:"input"`       // tool_use
	ToolUseID string          `json:"tool_use_id"` // tool_result
	Content   json.RawMessage `json:"content"`     // tool_result (string or array)
}

func (w *Watcher) processLine(sessionID, line string, seen map[string]struct{}, usageByResponse map[string]msgUsage) {
	var entry transcriptEntry
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		return // skip unparseable
	}

	// Only process user/assistant conversation entries.
	if entry.Type != "user" && entry.Type != "assistant" {
		return
	}
	if entry.Message == nil {
		return
	}

	ts, err := time.Parse(time.RFC3339Nano, entry.Timestamp)
	if err != nil {
		ts = time.Now()
	}

	// Emit SessionStart on the first valid entry processed for this session
	// unless it has already been recorded in seen.
	if _, ok := seen[sessionStartKey]; !ok {
		if err := w.insertCCSessionStart(sessionID, entry.CWD, ts); err != nil {
			w.logger.Warn("failed to insert SessionStart, will retry on next line",
				"session", sessionID, "error", err)
			// Don't set flag — retry on next processLine call.
			// Continue below to process the current message (don't lose it).
		} else {
			seen[sessionStartKey] = struct{}{}
		}
	}

	role := entry.Message.Role
	if role == "human" {
		role = "user"
	}
	model := entry.Message.Model

	// Content can be a string or array of blocks.
	content := entry.Message.Content
	if len(content) == 0 {
		return
	}

	// UUID distinguishes repeated user requests from replayed entries. Hash
	// the full content so two long messages with a common prefix remain distinct.
	isDup := func(msgType, content string) bool {
		h := sha256.Sum256([]byte(entry.UUID + "|" + msgType + "|" + role + "|" + content))
		key := hex.EncodeToString(h[:16])
		if _, ok := seen[key]; ok {
			return true
		}
		seen[key] = struct{}{}
		return false
	}

	// Determine message type from role.
	emitType := role
	if emitType != "user" {
		emitType = "assistant"
	}

	// Streaming entries repeat response-level counters; persist only their increases.
	var usage *msgUsage
	if entry.Type == "assistant" && entry.Message.Usage != nil {
		usage = entry.Message.Usage
		if id := entry.Message.ID; id != "" {
			previous, recorded := usageByResponse[id]
			current := *usage
			current.InputTokens = max(current.InputTokens, previous.InputTokens)
			current.OutputTokens = max(current.OutputTokens, previous.OutputTokens)
			current.CacheReadTokens = max(current.CacheReadTokens, previous.CacheReadTokens)
			current.CacheCreationTokens = max(current.CacheCreationTokens, previous.CacheCreationTokens)
			delta := msgUsage{
				InputTokens:         current.InputTokens - previous.InputTokens,
				OutputTokens:        current.OutputTokens - previous.OutputTokens,
				CacheReadTokens:     current.CacheReadTokens - previous.CacheReadTokens,
				CacheCreationTokens: current.CacheCreationTokens - previous.CacheCreationTokens,
				ServiceTier:         current.ServiceTier,
			}
			if current.CacheCreation != nil {
				var previousLong int64
				if previous.CacheCreation != nil {
					previousLong = previous.CacheCreation.LongTokens
				}
				currentLong := max(current.CacheCreation.LongTokens, previousLong)
				current.CacheCreation = &cacheCreationSplit{LongTokens: currentLong}
				delta.CacheCreation = &cacheCreationSplit{LongTokens: currentLong - previousLong}
			}
			usageByResponse[id] = current
			usage = &delta
			if recorded && delta.countsZero() {
				usage = nil
			}
		}
	}

	// Try as string first.
	var strContent string
	if err := json.Unmarshal(content, &strContent); err == nil {
		strContent = strings.TrimSpace(strContent)
		if strContent != "" && !isDup(emitType, strContent) {
			w.insertMessage(ts, sessionID, emitType, role, truncate(strContent, maxContentLen), model, "", "", usage)
			usage = nil
		}
		if usage != nil {
			w.insertMessage(ts, sessionID, "usage", "assistant", "", model, "", "", usage)
		}
		return
	}

	// Parse as array of blocks.
	var blocks []contentBlock
	if err := json.Unmarshal(content, &blocks); err != nil {
		return
	}

	emitRole := role
	if role != "user" {
		emitRole = "assistant"
	}

	for _, b := range blocks {
		switch b.Type {
		case "text":
			text := strings.TrimSpace(b.Text)
			if text != "" && !isDup(emitRole, text) {
				w.insertMessage(ts, sessionID, emitRole, role, truncate(text, maxContentLen), model, "", "", usage)
				usage = nil // attach usage to first emitted message only
			}
		case "thinking":
			thinking := strings.TrimSpace(b.Thinking)
			if thinking != "" && !isDup("thinking", thinking) {
				w.insertMessage(ts, sessionID, "thinking", role, truncate(thinking, maxContentLen), model, "", "", nil)
			}
		case "tool_use":
			// tool_use dedup by ID, not content.
			if _, ok := seen["tooluse:"+b.ID]; ok {
				continue
			}
			seen["tooluse:"+b.ID] = struct{}{}
			inputStr := truncate(string(b.Input), maxToolInput)
			w.insertMessage(ts, sessionID, "tool_use", emitRole, inputStr, model, b.Name, b.ID, usage)
			usage = nil
		case "tool_result":
			if _, ok := seen["toolresult:"+b.ToolUseID]; ok {
				continue
			}
			seen["toolresult:"+b.ToolUseID] = struct{}{}
			resultStr := extractToolResultContent(b.Content)
			w.insertMessage(ts, sessionID, "tool_result", role, truncate(resultStr, maxToolContent), model, "", b.ToolUseID, nil)
		}
	}
	if usage != nil {
		w.insertMessage(ts, sessionID, "usage", "assistant", "", model, "", "", usage)
	}
}

// extractToolResultContent normalizes tool_result content (string or [{text}] array).
func extractToolResultContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		var sb strings.Builder
		for _, p := range parts {
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(p.Text)
		}
		return sb.String()
	}
	return string(raw)
}

func (w *Watcher) insertMessage(ts time.Time, sessionID, messageType, role, content, model, toolName, toolUseID string, usage *msgUsage) {
	now := ts.UnixMilli()

	var sql string
	if usage != nil {
		sql = fmt.Sprintf(
			"INSERT INTO tma1_messages (ts, session_id, message_type, \"role\", content, model, tool_name, tool_use_id, "+
				"input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, cache_creation_long_tokens, service_tier) "+
				"VALUES (%d, '%s', '%s', '%s', '%s', '%s', '%s', '%s', %d, %d, %d, %d, %s, %s)",
			now,
			escapeSQLString(sessionID),
			escapeSQLString(messageType),
			escapeSQLString(role),
			escapeSQLString(content),
			escapeSQLString(model),
			escapeSQLString(toolName),
			escapeSQLString(toolUseID),
			usage.InputTokens,
			usage.OutputTokens,
			usage.CacheReadTokens,
			usage.CacheCreationTokens,
			usage.longCacheWriteSQL(),
			nullableString(usage.ServiceTier),
		)
	} else {
		sql = fmt.Sprintf(
			"INSERT INTO tma1_messages (ts, session_id, message_type, \"role\", content, model, tool_name, tool_use_id) "+
				"VALUES (%d, '%s', '%s', '%s', '%s', '%s', '%s', '%s')",
			now,
			escapeSQLString(sessionID),
			escapeSQLString(messageType),
			escapeSQLString(role),
			escapeSQLString(content),
			escapeSQLString(model),
			escapeSQLString(toolName),
			escapeSQLString(toolUseID),
		)
	}

	go func() {
		insertSem <- struct{}{}        // acquire
		defer func() { <-insertSem }() // release
		w.execSQL(sql)
	}()
}

func (w *Watcher) execSQL(sql string) {
	form := url.Values{}
	form.Set("sql", sql)

	ctx, cancel := context.WithTimeout(context.Background(), insertTimeout)
	defer cancel()

	req, err := newPostRequest(ctx, w.sqlURL, form)
	if err != nil {
		w.logger.Warn("transcript insert: create request failed", "error", err)
		return
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		w.logger.Debug("transcript insert failed", "error", err)
		return
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body) // drain

	if resp.StatusCode != 200 {
		w.logger.Debug("transcript insert non-200", "status", resp.StatusCode)
	}
}

// longCacheWriteSQL is NULL when the transcript did not split cache writes
// by duration, so the write cost stays unknown.
func (u msgUsage) longCacheWriteSQL() string {
	if u.CacheCreation == nil {
		return "NULL"
	}
	return strconv.FormatInt(u.CacheCreation.LongTokens, 10)
}

func nullableString(s string) string {
	if s == "" {
		return "NULL"
	}
	return "'" + escapeSQLString(s) + "'"
}

func truncate(s string, maxLen int) string {
	return strutil.SafeTruncate(s, maxLen)
}

func escapeSQLString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// broadcastHookEvent sends a hook-compatible JSON payload to SSE subscribers.
// Fields match the Claude Code hook schema so the live canvas can process them uniformly.
func (w *Watcher) broadcastHookEvent(sessionID, eventType, toolName, toolInput, toolUseID, toolResult, agentID, agentType string) {
	if w.broadcast == nil {
		return
	}
	payload := map[string]string{
		"session_id":      sessionID,
		"hook_event_name": eventType,
		"tool_name":       truncate(toolName, 256),
		"tool_input":      truncate(toolInput, maxToolInput),
		"tool_use_id":     toolUseID,
		"tool_response":   truncate(toolResult, maxToolContent),
		"agent_id":        agentID,
		"agent_type":      agentType,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	w.broadcast(data)
}

// insertCCSessionStart writes a SessionStart event to tma1_hook_events for Claude Code sessions.
// This aligns the CC watcher with Codex parsers, ensuring the source filter works
// even if CC HTTP hooks are not configured.
// Runs synchronously so the caller can retry on failure.
// Acquires insertSem to respect the concurrency limit shared with other inserts.
func (w *Watcher) insertCCSessionStart(sessionID, cwd string, ts time.Time) error {
	insertSem <- struct{}{}
	defer func() { <-insertSem }()

	msTs := ts.UnixMilli()

	stmt := fmt.Sprintf(
		"INSERT INTO tma1_hook_events "+
			"(ts, session_id, event_type, agent_source, tool_name, tool_input, tool_result, "+
			"tool_use_id, agent_id, agent_type, notification_type, \"message\", cwd, transcript_path, conversation_id) "+
			"VALUES (%d, '%s', 'SessionStart', 'claude_code', '', '', '', '', '', '', '', '', '%s', '', '')",
		msTs,
		escapeSQLString(sessionID),
		escapeSQLString(truncate(cwd, 512)),
	)

	form := url.Values{}
	form.Set("sql", stmt)

	ctx, cancel := context.WithTimeout(context.Background(), insertTimeout)
	defer cancel()

	req, err := newPostRequest(ctx, w.sqlURL, form)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		return fmt.Errorf("non-200 status: %d", resp.StatusCode)
	}
	return nil
}
