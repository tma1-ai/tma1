package transcript

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strconv"
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
	w.processLine("session", `{"type":"assistant","uuid":"message-one","message":{"role":"assistant","model":"test-model","content":[{"type":"thinking","thinking":"analysis"}],"usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":80}}}`, seen, make(map[string]msgUsage))
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

func TestClaudeStreamingUsageDeltas(t *testing.T) {
	ch := make(chan string, 8)
	srv := httptest.NewServer(httpTestHandler(ch))
	defer srv.Close()
	w := &Watcher{sqlURL: srv.URL, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	seen := map[string]struct{}{sessionStartKey: {}}
	usage := map[string]msgUsage{}
	line := `{"timestamp":"2026-10-28T10:00:00Z","type":"assistant","message":{"id":"response-1","role":"assistant","content":[{"type":"text","text":"answer"}],"usage":{"input_tokens":100,"output_tokens":1,"cache_read_input_tokens":80}}}`
	w.processLine("session", line, seen, usage)
	first := waitForSQL(t, ch)
	if !strings.Contains(first, "1793181600000") {
		t.Fatalf("source timestamp lost: %s", first)
	}
	w.processLine("session", line, seen, usage)
	w.processLine("session", strings.Replace(line, `"output_tokens":1`, `"output_tokens":21`, 1), seen, usage)
	sql := waitForSQL(t, ch)
	if !strings.Contains(sql, "'usage'") || !strings.Contains(sql, ", 0, 20, 0, 0, NULL, NULL)") {
		t.Fatalf("expected incremental output only: %s", sql)
	}
	select {
	case extra := <-ch:
		t.Fatalf("duplicate usage: %s", extra)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestCodexPreservesSourceTimeAcrossSessions(t *testing.T) {
	ch := make(chan string, 4)
	srv := httptest.NewServer(httpTestHandler(ch))
	defer srv.Close()
	w := &Watcher{sqlURL: srv.URL, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	newer := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	older := newer.Add(-24 * time.Hour)
	w.insertCodexMessage("newer-session", newer, "assistant", "newer", map[string]struct{}{})
	waitForSQL(t, ch)
	w.insertCodexMessage("older-session", older, "assistant", "older", map[string]struct{}{})
	sql := waitForSQL(t, ch)
	if !strings.Contains(sql, "1793181600000") {
		t.Fatalf("source time shifted by unrelated session: %s", sql)
	}
}

func TestClaudeRepeatedRequestsKeepIdentityAndOriginalStart(t *testing.T) {
	ch := make(chan string, 8)
	srv := httptest.NewServer(httpTestHandler(ch))
	defer srv.Close()
	w := &Watcher{sqlURL: srv.URL, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	seen, usage := map[string]struct{}{}, map[string]msgUsage{}
	line := `{"timestamp":"2026-10-28T10:00:00Z","uuid":"first","type":"user","message":{"role":"user","content":"continue"}}`
	w.processLine("session", line, seen, usage)
	start := waitForSQL(t, ch)
	if !strings.Contains(start, "1793181600000") {
		t.Fatalf("backfill start uses ingestion time: %s", start)
	}
	waitForSQL(t, ch)
	w.processLine("session", strings.Replace(line, `"first"`, `"second"`, 1), seen, usage)
	waitForSQL(t, ch)
	w.processLine("session", line, seen, usage)
	select {
	case sql := <-ch:
		t.Fatalf("replayed prompt inserted: %s", sql)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestStoppedWatcherDoesNotStopReplacement(t *testing.T) {
	old, replacement := &sessionWatch{}, &sessionWatch{}
	w := &Watcher{sessions: map[string]*sessionWatch{"session": replacement}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.tailFile(ctx, "session", t.TempDir()+"/missing.jsonl", old)
	if replacement.stopped {
		t.Fatal("old watcher marked its replacement as stopped")
	}
}

func TestClaudeCacheWriteSplitAndTier(t *testing.T) {
	ch := make(chan string, 8)
	srv := httptest.NewServer(httpTestHandler(ch))
	defer srv.Close()
	w := &Watcher{sqlURL: srv.URL, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	seen := map[string]struct{}{sessionStartKey: {}}
	usage := map[string]msgUsage{}
	line := `{"timestamp":"2026-10-28T10:00:00Z","type":"assistant","message":{"id":"response-1","role":"assistant","content":[{"type":"text","text":"answer"}],"usage":{"input_tokens":10,"output_tokens":1,"cache_read_input_tokens":0,"cache_creation_input_tokens":300,"cache_creation":{"ephemeral_1h_input_tokens":200,"ephemeral_5m_input_tokens":100},"service_tier":"standard"}}}`
	w.processLine("session", line, seen, usage)
	if sql := waitForSQL(t, ch); !strings.Contains(sql, ", 10, 1, 0, 300, 200, 'standard')") {
		t.Fatalf("missing long cache writes or tier: %s", sql)
	}
	grown := strings.NewReplacer(`"output_tokens":1`, `"output_tokens":5`, `"cache_creation_input_tokens":300`, `"cache_creation_input_tokens":350`, `"ephemeral_1h_input_tokens":200`, `"ephemeral_1h_input_tokens":250`).Replace(line)
	w.processLine("session", grown, seen, usage)
	if sql := waitForSQL(t, ch); !strings.Contains(sql, ", 0, 4, 0, 50, 50, 'standard')") {
		t.Fatalf("long cache writes must be recorded as deltas: %s", sql)
	}
}

func TestCodexUsageRecordsServiceTier(t *testing.T) {
	ch := make(chan string, 8)
	srv := httptest.NewServer(httpTestHandler(ch))
	defer srv.Close()
	w := &Watcher{sqlURL: srv.URL, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx := &codexFileContext{model: "test-model"}
	seen := map[string]struct{}{}
	usage := func(ts string, input int) string {
		return `{"timestamp":"` + ts + `","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":` + strconv.Itoa(input) + `,"output_tokens":1}}}}`
	}
	w.processCodexLine("session", usage("2026-10-28T10:00:00Z", 10), seen, ctx)
	if sql := waitForSQL(t, ch); !strings.HasSuffix(sql, ", NULL)") {
		t.Fatalf("tier must stay unknown before thread settings: %s", sql)
	}
	w.processCodexLine("session", `{"timestamp":"2026-10-28T10:00:01Z","type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"test-model","service_tier":"priority"}}}`, seen, ctx)
	w.processCodexLine("session", usage("2026-10-28T10:00:02Z", 11), seen, ctx)
	if sql := waitForSQL(t, ch); !strings.HasSuffix(sql, ", 'priority')") {
		t.Fatalf("missing priority tier: %s", sql)
	}
	w.processCodexLine("session", `{"timestamp":"2026-10-28T10:00:03Z","type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"test-model","service_tier":null}}}`, seen, ctx)
	w.processCodexLine("session", usage("2026-10-28T10:00:04Z", 12), seen, ctx)
	if sql := waitForSQL(t, ch); !strings.HasSuffix(sql, ", 'default')") {
		t.Fatalf("a null tier setting means requests use the default tier: %s", sql)
	}
}

func TestClaudeCacheSplitSurvivesEntryWithoutSplit(t *testing.T) {
	ch := make(chan string, 8)
	srv := httptest.NewServer(httpTestHandler(ch))
	defer srv.Close()
	w := &Watcher{sqlURL: srv.URL, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	seen := map[string]struct{}{sessionStartKey: {}}
	usage := map[string]msgUsage{}
	entry := func(output int, split string) string {
		return `{"timestamp":"2026-10-28T10:00:00Z","type":"assistant","message":{"id":"response-1","role":"assistant","content":[],"usage":{"input_tokens":10,"output_tokens":` +
			strconv.Itoa(output) + `,"cache_creation_input_tokens":200` + split + `,"service_tier":"standard"}}}`
	}
	withSplit := `,"cache_creation":{"ephemeral_1h_input_tokens":200}`
	w.processLine("session", entry(1, withSplit), seen, usage)
	waitForSQL(t, ch)
	w.processLine("session", entry(2, ""), seen, usage)
	if sql := waitForSQL(t, ch); !strings.HasSuffix(sql, ", 0, 1, 0, 0, NULL, 'standard')") {
		t.Fatalf("entry without a split must leave its split unknown: %s", sql)
	}
	w.processLine("session", entry(3, withSplit), seen, usage)
	if sql := waitForSQL(t, ch); !strings.HasSuffix(sql, ", 0, 1, 0, 0, 0, 'standard')") {
		t.Fatalf("1-hour writes counted twice: %s", sql)
	}
}
