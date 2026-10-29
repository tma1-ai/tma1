package transcript

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestCodexThreadSpawnUsesExplicitParent(t *testing.T) {
	ch := make(chan string, 4)
	srv := httptest.NewServer(httpTestHandler(ch))
	defer srv.Close()
	w := &Watcher{sqlURL: srv.URL, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx := &codexFileContext{fileID: "child-file"}
	seen := map[string]struct{}{}
	w.processCodexLine("different-timestamp", `{"timestamp":"2026-10-28T10:00:00Z","type":"session_meta","payload":{"id":"child-id","cwd":"/repo","source":{"subagent":{"thread_spawn":{"parent_thread_id":"parent-id","depth":1,"agent_path":"/root/tests","agent_nickname":"Ada","agent_role":null}}}}}`, seen, ctx)
	sql := waitForSQL(t, ch)
	if !strings.Contains(sql, "'parent-id', 'SubagentStart'") || !strings.Contains(sql, "'child-id'") {
		t.Fatalf("child must attach to explicit parent: %s", sql)
	}
	w.processCodexLine("different-timestamp", `{"timestamp":"2026-10-28T10:01:00Z","type":"compacted","payload":{"message":"summary"}}`, seen, ctx)
	sql = waitForSQL(t, ch)
	if !strings.Contains(sql, "'parent-id', 'ContextCompaction'") {
		t.Fatalf("missing compaction: %s", sql)
	}
}

func TestCodexRepeatedCumulativeUsage(t *testing.T) {
	ch := make(chan string, 4)
	srv := httptest.NewServer(httpTestHandler(ch))
	defer srv.Close()
	w := &Watcher{sqlURL: srv.URL, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx := &codexFileContext{model: "test-model"}
	seen := map[string]struct{}{}
	payload := `"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1000,"output_tokens":100,"total_tokens":1100},"last_token_usage":{"input_tokens":1000,"output_tokens":100,"cached_input_tokens":800,"reasoning_output_tokens":20}}}`
	w.processCodexLine("session", `{"timestamp":"2026-10-28T10:00:00Z",`+payload+`}`, seen, ctx)
	waitForSQL(t, ch)
	w.processCodexLine("session", `{"timestamp":"2026-10-28T10:00:01Z",`+payload+`}`, seen, ctx)
	select {
	case sql := <-ch:
		t.Fatalf("duplicated unchanged cumulative usage: %s", sql)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestClaudeThinkingOnlyKeepsUsage(t *testing.T) {
	ch := make(chan string, 4)
	srv := httptest.NewServer(httpTestHandler(ch))
	defer srv.Close()
	w := &Watcher{sqlURL: srv.URL, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	seen := map[string]struct{}{sessionStartKey: {}}
	w.processLine("session", `{"type":"assistant","uuid":"message-one","message":{"role":"assistant","model":"test-model","content":[{"type":"thinking","thinking":"analysis"}],"usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":80}}}`, seen)
	sqls := []string{waitForSQL(t, ch), waitForSQL(t, ch)}
	assertAnySQLContains(t, sqls, "'usage'", "100, 20, 80, 0")
}

func TestTranscriptTruncationPreservesUTF8(t *testing.T) {
	s := strings.Repeat("界", 1000)
	for _, n := range []int{1, 2, 4, 2048} {
		got := truncate(s, n)
		if !utf8.ValidString(got) || len(got) > n {
			t.Fatalf("invalid truncation at %d", n)
		}
	}
}

func BenchmarkCodexSessionMeta(b *testing.B) {
	raw := json.RawMessage(`{"id":"child-id","cwd":"/repo","source":{"subagent":{"thread_spawn":{"parent_thread_id":"parent-id","depth":1,"agent_path":"/root/tests"}}}}`)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		parseCodexSessionMeta(raw)
	}
}
