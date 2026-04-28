package transcript

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func TestFetchCopilotCLIMaxTsParsesTimestampString(t *testing.T) {
	const maxTS = "2026-04-16T01:00:00Z"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(500)
			return
		}
		sql := r.Form.Get("sql")
		if !strings.Contains(sql, "SELECT MAX(ts)") || !strings.Contains(sql, "cp:abc-123") {
			t.Fatalf("unexpected SQL: %s", sql)
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"output":[{"records":{"rows":[["` + maxTS + `"]]}}]}`))
	}))
	defer ts.Close()

	oldClient := httpClient
	httpClient = ts.Client()
	defer func() { httpClient = oldClient }()

	w := &Watcher{
		sqlURL: ts.URL,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	want := time.Date(2026, 4, 16, 1, 0, 0, 0, time.UTC).UnixMilli()
	if got := w.fetchCopilotCLIMaxTs("abc-123"); got != want {
		t.Fatalf("fetchCopilotCLIMaxTs() = %d, want %d", got, want)
	}
}

func TestProcessCopilotCLILineSkipsPersistedEventsFromDBThreshold(t *testing.T) {
	sqlCh := make(chan string, 4)
	const maxTS = "2026-04-16T01:00:00Z"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(500)
			return
		}
		sql := r.Form.Get("sql")
		if strings.Contains(sql, "SELECT MAX(ts)") {
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"output":[{"records":{"rows":[["` + maxTS + `"]]}}]}`))
			return
		}
		sqlCh <- sql
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"output":[]}`))
	}))
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
	fctx.skipUntilTsMs = w.fetchCopilotCLIMaxTs("abc-123")
	fctx.lastTsMs = fctx.skipUntilTsMs

	w.processCopilotCLILine("abc-123", `{"type":"user.message","data":{"content":"old"},"id":"evt-old","timestamp":"2026-04-16T00:59:59Z","parentId":null}`, seen, fctx)
	w.processCopilotCLILine("abc-123", `{"type":"user.message","data":{"content":"equal"},"id":"evt-equal","timestamp":"2026-04-16T01:00:00Z","parentId":null}`, seen, fctx)
	select {
	case sql := <-sqlCh:
		t.Fatalf("expected persisted events to be skipped, but got SQL: %s", sql)
	default:
		// OK — no insert for events at/before the DB threshold.
	}

	w.processCopilotCLILine("abc-123", `{"type":"user.message","data":{"content":"new"},"id":"evt-new","timestamp":"2026-04-16T01:00:01Z","parentId":null}`, seen, fctx)
	sql := waitForSQL(t, sqlCh)
	if !strings.Contains(sql, "new") {
		t.Fatalf("expected post-threshold event to insert, got: %s", sql)
	}
}

// TestProcessCopilotCLILineRefreshesSkipThresholdOnSessionRollover verifies
// that when a JSONL file contains multiple logical sessions (Copilot CLI
// appends across CLI restarts), session.start rollover refetches MAX(ts) for
// the new session so already-persisted events of the rolled-over session are
// not re-inserted on watcher restart.
func TestProcessCopilotCLILineRefreshesSkipThresholdOnSessionRollover(t *testing.T) {
	sqlCh := make(chan string, 4)
	maxTSBySession := map[string]string{
		"cp:dir-A": "2026-04-16T01:00:00Z",
		"cp:sess-B": "2026-04-16T02:00:00Z",
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(500)
			return
		}
		sql := r.Form.Get("sql")
		if strings.Contains(sql, "SELECT MAX(ts)") {
			w.WriteHeader(200)
			for sid, mt := range maxTSBySession {
				if strings.Contains(sql, sid) {
					_, _ = w.Write([]byte(`{"output":[{"records":{"rows":[["` + mt + `"]]}}]}`))
					return
				}
			}
			_, _ = w.Write([]byte(`{"output":[{"records":{"rows":[[null]]}}]}`))
			return
		}
		sqlCh <- sql
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"output":[]}`))
	}))
	defer ts.Close()

	oldClient := httpClient
	httpClient = ts.Client()
	defer func() { httpClient = oldClient }()

	w := &Watcher{
		sqlURL: ts.URL,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	seen := make(map[string]struct{})
	fctx := &copilotCLIContext{sessionID: "dir-A"}
	fctx.skipUntilTsMs = w.fetchCopilotCLIMaxTs("dir-A")
	fctx.lastTsMs = fctx.skipUntilTsMs

	// session.start for sess-B at 01:30:00 — past dir-A's MAX(ts) (01:00:00) but
	// before sess-B's own MAX(ts) (02:00:00). Without the rollover refresh, this
	// event would be re-inserted as a duplicate.
	w.processCopilotCLILine("dir-A",
		`{"type":"session.start","data":{"sessionId":"sess-B","copilotVersion":"1.0","context":{"cwd":"/tmp"}},"id":"evt-start-B","timestamp":"2026-04-16T01:30:00Z","parentId":null}`,
		seen, fctx)
	if fctx.sessionID != "sess-B" {
		t.Fatalf("expected sessionID rollover to sess-B, got %s", fctx.sessionID)
	}
	if fctx.skipUntilTsMs != time.Date(2026, 4, 16, 2, 0, 0, 0, time.UTC).UnixMilli() {
		t.Fatalf("expected skipUntilTsMs to refresh to sess-B's MAX(ts), got %d", fctx.skipUntilTsMs)
	}

	// User message inside sess-B at 01:45:00 — before sess-B's MAX(ts), so already persisted.
	w.processCopilotCLILine("dir-A",
		`{"type":"user.message","data":{"content":"already-ingested"},"id":"evt-mid-B","timestamp":"2026-04-16T01:45:00Z","parentId":null}`,
		seen, fctx)
	select {
	case sql := <-sqlCh:
		t.Fatalf("expected events of rolled-over session before its MAX(ts) to be skipped, got SQL: %s", sql)
	default:
	}

	// User message after sess-B's MAX(ts) — genuinely new, should insert.
	w.processCopilotCLILine("dir-A",
		`{"type":"user.message","data":{"content":"genuinely-new"},"id":"evt-after-B","timestamp":"2026-04-16T02:00:01Z","parentId":null}`,
		seen, fctx)
	sql := waitForSQL(t, sqlCh)
	if !strings.Contains(sql, "genuinely-new") {
		t.Fatalf("expected post-threshold event in rolled-over session to insert, got: %s", sql)
	}
}

func TestCopilotCLIContextDBSessionID(t *testing.T) {
	fctx := &copilotCLIContext{sessionID: "abc-123-def"}
	want := "cp:abc-123-def"
	if got := fctx.dbSessionID(); got != want {
		t.Fatalf("dbSessionID() = %q, want %q", got, want)
	}
}
