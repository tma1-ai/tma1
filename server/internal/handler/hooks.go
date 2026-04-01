package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
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
// Returns 200 with empty body immediately — writes happen asynchronously.
func (s *Server) handleHooks(w http.ResponseWriter, r *http.Request) {
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

	// Deduplicate FileChanged events: skip if same session+path within 1s.
	if payload.HookEventName == "FileChanged" {
		meta := hookMeta(payload)
		fp, _ := meta["file_path"].(string)
		if fp != "" {
			key := payload.SessionID + "\x00" + fp
			now := time.Now()
			if prev, ok := fileChangedDedup.Load(key); ok {
				if now.Sub(prev.(time.Time)) < time.Second {
					return
				}
			}
			fileChangedDedup.Store(key, now)
		}
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
	sql := fmt.Sprintf(
		"INSERT INTO tma1_hook_events "+
			"(ts, session_id, event_type, agent_source, tool_name, tool_input, tool_result, "+
			"tool_use_id, agent_id, agent_type, notification_type, \"message\", cwd, transcript_path, "+
			"permission_mode, metadata) "+
			"VALUES (%d, '%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s')",
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
