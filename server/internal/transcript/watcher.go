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
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// lastInsertTS ensures each message gets a unique, monotonically increasing timestamp.
// When processing a batch of existing lines, time.Now() would be identical for all.
var lastInsertTS atomic.Int64

const (
	pollInterval   = 500 * time.Millisecond
	maxContentLen  = 32768
	insertTimeout  = 10 * time.Second
	maxToolInput   = 2048
	maxToolContent = 4096
)

// maxConcurrentInserts limits the number of concurrent SQL INSERT goroutines
// to prevent overwhelming GreptimeDB when backfilling large transcripts.
var insertSem = make(chan struct{}, 16)

// Watcher manages per-session JSONL transcript file watchers.
type Watcher struct {
	mu       sync.Mutex
	sessions map[string]*sessionWatch
	sqlURL   string
	logger   *slog.Logger
}

type sessionWatch struct {
	cancel  context.CancelFunc
	seen    map[string]struct{} // content hashes for dedup
	stopped bool               // true after watcher goroutine exits (can be restarted)
}

// NewWatcher creates a transcript watcher that writes to the given GreptimeDB instance.
func NewWatcher(greptimeHTTPPort int, logger *slog.Logger) *Watcher {
	return &Watcher{
		sessions: make(map[string]*sessionWatch),
		sqlURL:   fmt.Sprintf("http://localhost:%d/v1/sql", greptimeHTTPPort),
		logger:   logger,
	}
}

// Watch starts tailing a JSONL transcript file for the given session.
// It reads from the beginning to capture existing content, then polls for new lines.
// Safe to call multiple times for the same session — duplicates are ignored.
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
	sw := &sessionWatch{cancel: cancel, seen: seen}
	w.sessions[sessionID] = sw

	go w.tailFile(ctx, sessionID, transcriptPath, seen)
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

func (w *Watcher) tailFile(ctx context.Context, sessionID, filePath string, seen map[string]struct{}) {
	// Mark as stopped on exit — preserves seen map so Watch() can retry without re-inserting.
	defer func() {
		w.mu.Lock()
		if sw, ok := w.sessions[sessionID]; ok {
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
					w.processLine(sessionID, trimmed, seen)
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
	SessionID string           `json:"sessionId"`
	Type      string           `json:"type"` // "user", "assistant", "progress"
	UUID      string           `json:"uuid"`
	Message   *transcriptMsg   `json:"message"`
	CWD       string           `json:"cwd"`
}

type transcriptMsg struct {
	Role    string          `json:"role"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"` // string or []contentBlock
}

type contentBlock struct {
	Type      string          `json:"type"` // "text", "thinking", "tool_use", "tool_result"
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Name      string          `json:"name"`      // tool_use
	ID        string          `json:"id"`         // tool_use
	Input     json.RawMessage `json:"input"`      // tool_use
	ToolUseID string          `json:"tool_use_id"` // tool_result
	Content   json.RawMessage `json:"content"`     // tool_result (string or array)
}

func (w *Watcher) processLine(sessionID, line string, seen map[string]struct{}) {
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

	// Dedup helper: hash (messageType, role, content_prefix) to detect context compression replays.
	isDup := func(msgType, content string) bool {
		prefix := content
		if len(prefix) > 200 {
			prefix = prefix[:200]
		}
		h := sha256.Sum256([]byte(msgType + "|" + role + "|" + prefix))
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

	// Try as string first.
	var strContent string
	if err := json.Unmarshal(content, &strContent); err == nil {
		strContent = strings.TrimSpace(strContent)
		if strContent != "" && !isDup(emitType, strContent) {
			w.insertMessage(sessionID, emitType, role, truncate(strContent, maxContentLen), model, "", "")
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
				w.insertMessage(sessionID, emitRole, role, truncate(text, maxContentLen), model, "", "")
			}
		case "thinking":
			thinking := strings.TrimSpace(b.Thinking)
			if thinking != "" && !isDup("thinking", thinking) {
				w.insertMessage(sessionID, "thinking", role, truncate(thinking, maxContentLen), model, "", "")
			}
		case "tool_use":
			// tool_use dedup by ID, not content.
			if _, ok := seen["tooluse:"+b.ID]; ok {
				continue
			}
			seen["tooluse:"+b.ID] = struct{}{}
			inputStr := truncate(string(b.Input), maxToolInput)
			w.insertMessage(sessionID, "tool_use", emitRole, inputStr, model, b.Name, b.ID)
		case "tool_result":
			if _, ok := seen["toolresult:"+b.ToolUseID]; ok {
				continue
			}
			seen["toolresult:"+b.ToolUseID] = struct{}{}
			resultStr := extractToolResultContent(b.Content)
			w.insertMessage(sessionID, "tool_result", role, truncate(resultStr, maxToolContent), model, "", b.ToolUseID)
		}
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

func (w *Watcher) insertMessage(sessionID, messageType, role, content, model, toolName, toolUseID string) {
	// Use a monotonically increasing timestamp so batch-processed lines get distinct,
	// ordered timestamps instead of all sharing the same time.Now().
	now := time.Now().UnixMilli()
	for {
		prev := lastInsertTS.Load()
		next := now
		if next <= prev {
			next = prev + 1
		}
		if lastInsertTS.CompareAndSwap(prev, next) {
			now = next
			break
		}
	}
	sql := fmt.Sprintf(
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

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

func escapeSQLString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}
