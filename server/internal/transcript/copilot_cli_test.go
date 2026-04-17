package transcript

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProcessCopilotCLILineSessionStart(t *testing.T) {
	sqlCh := make(chan string, 4)
	ts := httptest.NewServer(httpTestHandler(sqlCh))
	defer ts.Close()

	oldClient := httpClient
	httpClient = ts.Client()
	defer func() { httpClient = oldClient }()

	w := &Watcher{
		sqlURL: ts.URL,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	seen := make(map[string]struct{})
	fctx := &copilotCLIContext{sessionID: "abc-123"}

	w.processCopilotCLILine("abc-123",
		`{"type":"session.start","data":{"sessionId":"abc-123","copilotVersion":"1.0.28","context":{"cwd":"/tmp/project","gitRoot":"/tmp/project","branch":"main","headCommit":"deadbeef","repository":"owner/repo"}},"id":"evt-1","timestamp":"2026-04-16T01:00:00Z","parentId":null}`,
		seen, fctx)

	sql := waitForSQL(t, sqlCh)
	if !strings.Contains(sql, "SessionStart") {
		t.Fatalf("expected SessionStart event, got: %s", sql)
	}
	if !strings.Contains(sql, "copilot_cli") {
		t.Fatalf("expected agent_source copilot_cli, got: %s", sql)
	}
	if !strings.Contains(sql, "cp:abc-123") {
		t.Fatalf("expected namespaced session ID cp:abc-123, got: %s", sql)
	}
	if !strings.Contains(sql, "main") {
		t.Fatalf("expected branch info in metadata, got: %s", sql)
	}
}

func TestProcessCopilotCLILineUserMessage(t *testing.T) {
	sqlCh := make(chan string, 4)
	ts := httptest.NewServer(httpTestHandler(sqlCh))
	defer ts.Close()

	oldClient := httpClient
	httpClient = ts.Client()
	defer func() { httpClient = oldClient }()

	w := &Watcher{
		sqlURL: ts.URL,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	seen := make(map[string]struct{})
	fctx := &copilotCLIContext{sessionID: "abc-123", model: "claude-opus-4.6"}

	w.processCopilotCLILine("abc-123",
		`{"type":"user.message","data":{"content":"fix the bug","transformedContent":"fix the bug"},"id":"evt-2","timestamp":"2026-04-16T01:01:00Z","parentId":"evt-1"}`,
		seen, fctx)

	sql := waitForSQL(t, sqlCh)
	if !strings.Contains(sql, "tma1_messages") {
		t.Fatalf("expected insert into tma1_messages, got: %s", sql)
	}
	if !strings.Contains(sql, "fix the bug") {
		t.Fatalf("expected user content, got: %s", sql)
	}
	if !strings.Contains(sql, "user") {
		t.Fatalf("expected role=user, got: %s", sql)
	}
}

func TestProcessCopilotCLILineAssistantWithReasoning(t *testing.T) {
	sqlCh := make(chan string, 4)
	ts := httptest.NewServer(httpTestHandler(sqlCh))
	defer ts.Close()

	oldClient := httpClient
	httpClient = ts.Client()
	defer func() { httpClient = oldClient }()

	w := &Watcher{
		sqlURL: ts.URL,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	seen := make(map[string]struct{})
	fctx := &copilotCLIContext{sessionID: "abc-123", model: "claude-opus-4.6"}

	w.processCopilotCLILine("abc-123",
		`{"type":"assistant.message","data":{"content":"Here is the fix","reasoningText":"Let me analyze the code","outputTokens":150},"id":"evt-3","timestamp":"2026-04-16T01:02:00Z","parentId":"evt-2"}`,
		seen, fctx)

	// Should produce two messages: thinking + assistant.
	sql1 := waitForSQL(t, sqlCh)
	sql2 := waitForSQL(t, sqlCh)
	sqls := []string{sql1, sql2}

	var hasThinking, hasAssistant bool
	for _, sql := range sqls {
		if strings.Contains(sql, "thinking") && strings.Contains(sql, "Let me analyze") {
			hasThinking = true
		}
		if strings.Contains(sql, "'assistant'") && strings.Contains(sql, "Here is the fix") {
			hasAssistant = true
		}
	}
	if !hasThinking {
		t.Fatalf("expected thinking message with reasoning text, got: %v", sqls)
	}
	if !hasAssistant {
		t.Fatalf("expected assistant message with content, got: %v", sqls)
	}
}

func TestProcessCopilotCLILineToolSuccess(t *testing.T) {
	sqlCh := make(chan string, 4)
	ts := httptest.NewServer(httpTestHandler(sqlCh))
	defer ts.Close()

	oldClient := httpClient
	httpClient = ts.Client()
	defer func() { httpClient = oldClient }()

	w := &Watcher{
		sqlURL: ts.URL,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	seen := make(map[string]struct{})
	fctx := &copilotCLIContext{sessionID: "abc-123", model: "claude-opus-4.6"}

	w.processCopilotCLILine("abc-123",
		`{"type":"tool.execution_complete","data":{"toolCallId":"tool-1","model":"claude-opus-4.6","success":true,"result":{"content":"output text"}},"id":"evt-4","timestamp":"2026-04-16T01:03:00Z","parentId":"evt-3"}`,
		seen, fctx)

	sql := waitForSQL(t, sqlCh)
	if !strings.Contains(sql, "PostToolUse") {
		t.Fatalf("expected PostToolUse for success=true, got: %s", sql)
	}
}

func TestProcessCopilotCLILineToolFailure(t *testing.T) {
	sqlCh := make(chan string, 4)
	ts := httptest.NewServer(httpTestHandler(sqlCh))
	defer ts.Close()

	oldClient := httpClient
	httpClient = ts.Client()
	defer func() { httpClient = oldClient }()

	w := &Watcher{
		sqlURL: ts.URL,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	seen := make(map[string]struct{})
	fctx := &copilotCLIContext{sessionID: "abc-123"}

	w.processCopilotCLILine("abc-123",
		`{"type":"tool.execution_complete","data":{"toolCallId":"tool-2","model":"claude-opus-4.6","success":false,"result":{"content":"error: permission denied"}},"id":"evt-5","timestamp":"2026-04-16T01:04:00Z","parentId":"evt-3"}`,
		seen, fctx)

	sql := waitForSQL(t, sqlCh)
	if !strings.Contains(sql, "PostToolUseFailure") {
		t.Fatalf("expected PostToolUseFailure for success=false, got: %s", sql)
	}
}

func TestProcessCopilotCLILineModelPropagation(t *testing.T) {
	sqlCh := make(chan string, 8)
	ts := httptest.NewServer(httpTestHandler(sqlCh))
	defer ts.Close()

	oldClient := httpClient
	httpClient = ts.Client()
	defer func() { httpClient = oldClient }()

	w := &Watcher{
		sqlURL: ts.URL,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	seen := make(map[string]struct{})
	fctx := &copilotCLIContext{sessionID: "abc-123"}

	// Model change updates fctx.model but no longer inserts a synthetic message.
	w.processCopilotCLILine("abc-123",
		`{"type":"session.model_change","data":{"newModel":"gpt-5.4"},"id":"evt-m1","timestamp":"2026-04-16T01:05:00Z","parentId":null}`,
		seen, fctx)

	if fctx.model != "gpt-5.4" {
		t.Fatalf("expected model to be gpt-5.4, got %s", fctx.model)
	}

	// Now an assistant message should carry the model.
	w.processCopilotCLILine("abc-123",
		`{"type":"assistant.message","data":{"content":"done","outputTokens":10},"id":"evt-a1","timestamp":"2026-04-16T01:06:00Z","parentId":null}`,
		seen, fctx)

	sql := waitForSQL(t, sqlCh)
	if !strings.Contains(sql, "gpt-5.4") {
		t.Fatalf("expected model gpt-5.4 on assistant message, got: %s", sql)
	}
}

func TestProcessCopilotCLILineSubagentLifecycle(t *testing.T) {
	sqlCh := make(chan string, 4)
	ts := httptest.NewServer(httpTestHandler(sqlCh))
	defer ts.Close()

	oldClient := httpClient
	httpClient = ts.Client()
	defer func() { httpClient = oldClient }()

	w := &Watcher{
		sqlURL: ts.URL,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	seen := make(map[string]struct{})
	fctx := &copilotCLIContext{sessionID: "abc-123"}

	w.processCopilotCLILine("abc-123",
		`{"type":"subagent.started","data":{"toolCallId":"sa-1","agentName":"rubber-duck","agentDescription":"A critic agent"},"id":"evt-sa1","timestamp":"2026-04-16T01:07:00Z","parentId":null}`,
		seen, fctx)

	sql := waitForSQL(t, sqlCh)
	if !strings.Contains(sql, "SubagentStart") {
		t.Fatalf("expected SubagentStart, got: %s", sql)
	}
	if !strings.Contains(sql, "rubber-duck") {
		t.Fatalf("expected agent_type rubber-duck, got: %s", sql)
	}

	w.processCopilotCLILine("abc-123",
		`{"type":"subagent.completed","data":{"toolCallId":"sa-1","agentName":"rubber-duck","model":"gpt-5.4","totalToolCalls":36,"totalTokens":898928,"durationMs":259183},"id":"evt-sa2","timestamp":"2026-04-16T01:11:00Z","parentId":null}`,
		seen, fctx)

	sql = waitForSQL(t, sqlCh)
	if !strings.Contains(sql, "SubagentStop") {
		t.Fatalf("expected SubagentStop, got: %s", sql)
	}
	if !strings.Contains(sql, "898928") {
		t.Fatalf("expected total_tokens in metadata, got: %s", sql)
	}
}

func TestProcessCopilotCLILineDedup(t *testing.T) {
	sqlCh := make(chan string, 4)
	ts := httptest.NewServer(httpTestHandler(sqlCh))
	defer ts.Close()

	oldClient := httpClient
	httpClient = ts.Client()
	defer func() { httpClient = oldClient }()

	w := &Watcher{
		sqlURL: ts.URL,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	seen := make(map[string]struct{})
	fctx := &copilotCLIContext{sessionID: "abc-123"}

	line := `{"type":"user.message","data":{"content":"hello"},"id":"evt-dup","timestamp":"2026-04-16T01:00:00Z","parentId":null}`

	// First call should insert.
	w.processCopilotCLILine("abc-123", line, seen, fctx)
	waitForSQL(t, sqlCh)

	// Second call with same event ID should be deduped (no SQL).
	w.processCopilotCLILine("abc-123", line, seen, fctx)

	// Verify no second SQL was produced.
	select {
	case sql := <-sqlCh:
		t.Fatalf("expected dedup to prevent second insert, but got: %s", sql)
	default:
		// OK — no second insert.
	}
}

func TestCopilotCLIContextDBSessionID(t *testing.T) {
	fctx := &copilotCLIContext{sessionID: "abc-123-def"}
	want := "cp:abc-123-def"
	if got := fctx.dbSessionID(); got != want {
		t.Fatalf("dbSessionID() = %q, want %q", got, want)
	}
}
