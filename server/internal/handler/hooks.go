package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// lastHookTS ensures each hook event gets a unique, monotonically increasing timestamp.
var lastHookTS atomic.Int64

const (
	maxHookBody    = 1 << 20 // 1 MB
	maxToolInput   = 2048
	maxToolResult  = 4096
	maxHookMessage = 4096
)

// hookPayload matches the JSON structure sent by Claude Code hooks via stdin.
type hookPayload struct {
	SessionID        string `json:"session_id"`
	HookEventName    string `json:"hook_event_name"`
	ToolName         string `json:"tool_name"`
	ToolInput        any    `json:"tool_input"`
	ToolUseID        string `json:"tool_use_id"`
	ToolResponse     any    `json:"tool_response"`
	AgentID          string `json:"agent_id"`
	AgentType        string `json:"agent_type"`
	NotificationType string `json:"notification_type"`
	Message          string `json:"message"`
	Title            string `json:"title"`
	CWD              string `json:"cwd"`
	TranscriptPath   string `json:"transcript_path"`
}

// handleHooks receives Claude Code / Codex hook events and stores them in GreptimeDB.
// Returns 200 with empty body immediately — writes happen asynchronously.
func (s *Server) handleHooks(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxHookBody))
	if err != nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	var payload hookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	if payload.SessionID == "" || payload.HookEventName == "" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Return 200 immediately — do not block Claude Code.
	w.WriteHeader(http.StatusOK)

	// Detect agent source from query param (default: claude_code).
	agentSource := r.URL.Query().Get("source")
	if agentSource == "" {
		agentSource = "claude_code"
	}

	// Normalize tool_response to string.
	toolResult := normalizeToolResponse(payload.ToolResponse)

	// Serialize tool_input to JSON string.
	toolInput := serializeToolInput(payload.ToolInput)

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

	// Async INSERT into GreptimeDB.
	go s.insertHookEvent(payload, agentSource, toolInput, toolResult)

	// Broadcast to SSE subscribers for live canvas.
	if s.hookBroadcast != nil {
		s.hookBroadcast.Broadcast(body)
	}
}

func (s *Server) insertHookEvent(p hookPayload, agentSource, toolInput, toolResult string) {
	now := time.Now().UnixMilli()
	for {
		prev := lastHookTS.Load()
		next := now
		if next <= prev {
			next = prev + 1
		}
		if lastHookTS.CompareAndSwap(prev, next) {
			now = next
			break
		}
	}
	sql := fmt.Sprintf(
		"INSERT INTO tma1_hook_events "+
			"(ts, session_id, event_type, agent_source, tool_name, tool_input, tool_result, "+
			"tool_use_id, agent_id, agent_type, notification_type, \"message\", cwd, transcript_path) "+
			"VALUES (%d, '%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s')",
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
