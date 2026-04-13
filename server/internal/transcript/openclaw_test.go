package transcript

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenClawSessionIDFromFilename(t *testing.T) {
	tests := []struct {
		name string
		file string
		want string
	}{
		{
			name: "standard timestamp_uuid format",
			file: "2026-04-13T10-30-45-123Z_a1b2c3d4-e5f6-7890-abcd-ef1234567890.jsonl",
			want: "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
		},
		{
			name: "topic session",
			file: "2026-04-13T10-30-45-123Z_a1b2c3d4-topic-12345.jsonl",
			want: "a1b2c3d4-topic-12345",
		},
		{
			name: "bare uuid without timestamp",
			file: "a1b2c3d4-e5f6.jsonl",
			want: "a1b2c3d4-e5f6",
		},
		{
			name: "multiple underscores takes first split",
			file: "2026-04-13T10-30-45_abc_def.jsonl",
			want: "abc_def",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := openclawSessionIDFromFilename(tt.file)
			if got != tt.want {
				t.Fatalf("openclawSessionIDFromFilename(%q) = %q, want %q", tt.file, got, tt.want)
			}
		})
	}
}

func TestIsOpenClawPrimaryTranscript(t *testing.T) {
	tests := []struct {
		name string
		file string
		want bool
	}{
		{"normal jsonl", "2026-04-13_abc.jsonl", true},
		{"sessions.json index", "sessions.json", false},
		{"non-jsonl", "readme.txt", false},
		{"reset archive", "abc.jsonl.reset.2026-04-13T10-30-45Z", false},
		{"deleted archive", "abc.jsonl.deleted.2026-04-13T10-30-45Z", false},
		{"bak archive", "abc.jsonl.bak.2026-04-13T10-30-45Z", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isOpenClawPrimaryTranscript(tt.file)
			if got != tt.want {
				t.Fatalf("isOpenClawPrimaryTranscript(%q) = %v, want %v", tt.file, got, tt.want)
			}
		})
	}
}

func TestOcExtractText(t *testing.T) {
	tests := []struct {
		name          string
		content       string
		skipThinking  bool
		want          string
	}{
		{
			name:    "plain string",
			content: `"hello world"`,
			want:    "hello world",
		},
		{
			name:    "text blocks",
			content: `[{"type":"text","text":"line 1"},{"type":"text","text":"line 2"}]`,
			want:    "line 1\nline 2",
		},
		{
			name:         "skip thinking",
			content:      `[{"type":"text","text":"answer"},{"type":"thinking","thinking":"hmm"}]`,
			skipThinking: true,
			want:         "answer",
		},
		{
			name:         "include thinking",
			content:      `[{"type":"text","text":"answer"},{"type":"thinking","thinking":"hmm"}]`,
			skipThinking: false,
			want:         "answer\nhmm",
		},
		{
			name:    "skip toolCall and image",
			content: `[{"type":"text","text":"hi"},{"type":"toolCall","name":"Read"},{"type":"image","data":"..."}]`,
			want:    "hi",
		},
		{
			name:    "empty content",
			content: `""`,
			want:    "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ocExtractText([]byte(tt.content), tt.skipThinking)
			if got != tt.want {
				t.Fatalf("ocExtractText(%s, %v) = %q, want %q", tt.content, tt.skipThinking, got, tt.want)
			}
		})
	}
}

func TestProcessOpenClawLineSessionHeader(t *testing.T) {
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
	fctx := &openclawFileContext{agentID: "main"}

	w.processOpenClawLine("oc:main:abc123",
		`{"type":"session","version":3,"id":"abc123","timestamp":"2026-04-13T10:30:00Z","cwd":"/home/user/project"}`,
		seen, fctx)

	sql := waitForSQL(t, sqlCh)
	if !strings.Contains(sql, "SessionStart") {
		t.Fatalf("expected SessionStart event, got: %s", sql)
	}
	if !strings.Contains(sql, "openclaw") {
		t.Fatalf("expected agent_source=openclaw, got: %s", sql)
	}
	if !strings.Contains(sql, "/home/user/project") {
		t.Fatalf("expected cwd in insert, got: %s", sql)
	}
}

func TestProcessOpenClawLineUserMessage(t *testing.T) {
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
	fctx := &openclawFileContext{agentID: "main"}

	w.processOpenClawLine("oc:main:abc",
		`{"type":"message","id":"e1","parentId":null,"timestamp":"2026-04-13T10:30:01Z","message":{"role":"user","content":"fix the bug","timestamp":1713006601000}}`,
		seen, fctx)

	sql := waitForSQL(t, sqlCh)
	if !strings.Contains(sql, "tma1_messages") {
		t.Fatalf("expected insert into tma1_messages, got: %s", sql)
	}
	if !strings.Contains(sql, "fix the bug") {
		t.Fatalf("expected user content in insert, got: %s", sql)
	}
	if !strings.Contains(sql, "'user'") {
		t.Fatalf("expected role=user, got: %s", sql)
	}
}

func TestProcessOpenClawLineAssistantWithUsage(t *testing.T) {
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
	fctx := &openclawFileContext{agentID: "main"}

	line := `{"type":"message","id":"e2","parentId":"e1","timestamp":"2026-04-13T10:30:02Z","message":{` +
		`"role":"assistant",` +
		`"content":[{"type":"text","text":"I will fix it"},{"type":"toolCall","id":"tc1","name":"Read","arguments":{"path":"/tmp/foo.go"}}],` +
		`"provider":"anthropic","model":"claude-sonnet-4-20250514",` +
		`"usage":{"input":1000,"output":200,"cacheRead":50,"cacheWrite":10,"totalTokens":1260,"cost":{"total":0.005}},` +
		`"stopReason":"toolUse","timestamp":1713006602000}}`

	w.processOpenClawLine("oc:main:abc", line, seen, fctx)

	// Expect: 1 assistant message + 1 PreToolUse hook event + 1 thinking(none here)
	var sqls []string
	for i := 0; i < 2; i++ {
		sqls = append(sqls, waitForSQL(t, sqlCh))
	}

	var sawMessage, sawPreTool bool
	for _, sql := range sqls {
		if strings.Contains(sql, "tma1_messages") && strings.Contains(sql, "I will fix it") {
			sawMessage = true
			if !strings.Contains(sql, "claude-sonnet") {
				t.Fatalf("expected model in message insert, got: %s", sql)
			}
			if !strings.Contains(sql, "1000") {
				t.Fatalf("expected input_tokens=1000 in insert, got: %s", sql)
			}
		}
		if strings.Contains(sql, "PreToolUse") && strings.Contains(sql, "Read") {
			sawPreTool = true
		}
	}
	if !sawMessage {
		t.Fatalf("expected assistant message insert, got: %v", sqls)
	}
	if !sawPreTool {
		t.Fatalf("expected PreToolUse insert, got: %v", sqls)
	}
}

func TestProcessOpenClawLineAssistantToolUseFormat(t *testing.T) {
	// Verify Anthropic API format (tool_use + input) is handled alongside pi format (toolCall + arguments).
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
	fctx := &openclawFileContext{agentID: "main"}

	line := `{"type":"message","id":"e5","parentId":"e1","timestamp":"2026-04-13T10:30:05Z","message":{` +
		`"role":"assistant",` +
		`"content":[{"type":"text","text":"reading file"},{"type":"tool_use","id":"tu1","name":"Read","input":{"path":"/tmp/test.go"}}],` +
		`"provider":"anthropic","model":"claude-sonnet-4-20250514",` +
		`"usage":{"input":500,"output":100,"cacheRead":0,"cacheWrite":0,"totalTokens":600},` +
		`"stopReason":"tool_use","timestamp":1713006605000}}`

	w.processOpenClawLine("oc:main:abc", line, seen, fctx)

	var sqls []string
	for i := 0; i < 2; i++ {
		sqls = append(sqls, waitForSQL(t, sqlCh))
	}

	var sawMessage, sawPreTool bool
	for _, sql := range sqls {
		if strings.Contains(sql, "tma1_messages") && strings.Contains(sql, "reading file") {
			sawMessage = true
		}
		if strings.Contains(sql, "PreToolUse") && strings.Contains(sql, "Read") {
			sawPreTool = true
			if !strings.Contains(sql, "/tmp/test.go") {
				t.Fatalf("expected tool input in PreToolUse, got: %s", sql)
			}
		}
	}
	if !sawMessage {
		t.Fatalf("expected assistant message insert, got: %v", sqls)
	}
	if !sawPreTool {
		t.Fatalf("expected PreToolUse for tool_use format, got: %v", sqls)
	}
}

func TestProcessOpenClawLineToolResult(t *testing.T) {
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
	fctx := &openclawFileContext{agentID: "main"}

	w.processOpenClawLine("oc:main:abc",
		`{"type":"message","id":"e3","parentId":"e2","timestamp":"2026-04-13T10:30:03Z","message":{`+
			`"role":"toolResult","toolCallId":"tc1","toolName":"Read",`+
			`"content":[{"type":"text","text":"file contents here"}],"isError":false,"timestamp":1713006603000}}`,
		seen, fctx)

	sql := waitForSQL(t, sqlCh)
	if !strings.Contains(sql, "PostToolUse") {
		t.Fatalf("expected PostToolUse event, got: %s", sql)
	}
	if !strings.Contains(sql, "Read") {
		t.Fatalf("expected tool_name=Read, got: %s", sql)
	}
}

func TestProcessOpenClawLineBashExecution(t *testing.T) {
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
	fctx := &openclawFileContext{agentID: "main"}

	w.processOpenClawLine("oc:main:abc",
		`{"type":"message","id":"e4","parentId":"e3","timestamp":"2026-04-13T10:30:04Z","message":{`+
			`"role":"bashExecution","command":"ls -la","output":"total 42\ndrwx...","timestamp":1713006604000}}`,
		seen, fctx)

	var sqls []string
	for i := 0; i < 2; i++ {
		sqls = append(sqls, waitForSQL(t, sqlCh))
	}

	var sawPre, sawPost bool
	for _, sql := range sqls {
		if strings.Contains(sql, "PreToolUse") && strings.Contains(sql, "bash") {
			sawPre = true
		}
		if strings.Contains(sql, "PostToolUse") && strings.Contains(sql, "bash") {
			sawPost = true
		}
	}
	if !sawPre || !sawPost {
		t.Fatalf("expected PreToolUse + PostToolUse for bash, got: %v", sqls)
	}
}

func TestProcessOpenClawLineCompaction(t *testing.T) {
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
	fctx := &openclawFileContext{agentID: "main"}

	w.processOpenClawLine("oc:main:abc",
		`{"type":"compaction","id":"c1","parentId":"e3","timestamp":"2026-04-13T10:35:00Z","summary":"discussion about bug fix","firstKeptEntryId":"e2","tokensBefore":50000}`,
		seen, fctx)

	sql := waitForSQL(t, sqlCh)
	if !strings.Contains(sql, "ContextCompaction") {
		t.Fatalf("expected ContextCompaction event, got: %s", sql)
	}
}

func TestProcessOpenClawLineDedup(t *testing.T) {
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
	fctx := &openclawFileContext{agentID: "main"}

	line := `{"type":"message","id":"e1","parentId":null,"timestamp":"2026-04-13T10:30:01Z","message":{"role":"user","content":"hello","timestamp":1713006601000}}`

	// First call should produce an insert.
	w.processOpenClawLine("oc:main:abc", line, seen, fctx)
	sql := waitForSQL(t, sqlCh)
	if !strings.Contains(sql, "hello") {
		t.Fatalf("expected first insert, got: %s", sql)
	}

	// Second call with same id should be deduplicated — no insert.
	w.processOpenClawLine("oc:main:abc", line, seen, fctx)

	select {
	case sql := <-sqlCh:
		t.Fatalf("expected no second insert, but got: %s", sql)
	default:
		// Good — no insert was made.
	}
}

func TestProcessOpenClawLineModelChange(t *testing.T) {
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
	fctx := &openclawFileContext{agentID: "main"}

	w.processOpenClawLine("oc:main:abc",
		`{"type":"model_change","id":"mc1","parentId":"e1","timestamp":"2026-04-13T10:31:00Z","provider":"openai","modelId":"gpt-4o"}`,
		seen, fctx)

	sql := waitForSQL(t, sqlCh)
	if !strings.Contains(sql, "tma1_messages") {
		t.Fatalf("expected message insert for model change, got: %s", sql)
	}
	if !strings.Contains(sql, "gpt-4o") {
		t.Fatalf("expected model name in insert, got: %s", sql)
	}
}
