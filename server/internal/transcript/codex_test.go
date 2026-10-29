package transcript

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexSessionGroup(t *testing.T) {
	tests := []struct {
		name     string
		baseName string
		want     string
	}{
		{
			name:     "standard rollout filename",
			baseName: "rollout-2026-03-27T18-10-59-019d2ec6-958f-7cde-b25c-acde48001122",
			want:     "rollout-2026-03-27T18-10-59",
		},
		{
			name:     "unexpected filename falls back to full name",
			baseName: "session-without-timestamp",
			want:     "session-without-timestamp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := codexSessionGroup(tt.baseName); got != tt.want {
				t.Fatalf("codexSessionGroup(%q) = %q, want %q", tt.baseName, got, tt.want)
			}
		})
	}
}

func TestCodexSubagentID(t *testing.T) {
	if got := codexSubagentID("codex:rollout-2026-03-27T18-10-59-a", "review"); got != "codex:rollout-2026-03-27T18-10-59-a" {
		t.Fatalf("codexSubagentID should prefer per-file id, got %q", got)
	}
	if got := codexSubagentID("", "review"); got != "review" {
		t.Fatalf("codexSubagentID should fall back to agent type, got %q", got)
	}
}

func TestProcessCodexLineCarriesConversationIDIntoSubagentLifecycle(t *testing.T) {
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
	fctx := &codexFileContext{fileID: "codex:rollout-2026-03-27T18-10-59-sub"}

	w.processCodexLine("rollout-2026-03-27T18-10-59",
		`{"timestamp":"2026-03-27T18:10:59Z","type":"session_meta","payload":{"id":"conv-123","source":{"subagent":"review"},"cwd":"/tmp/project"}}`,
		seen, fctx)
	w.processCodexLine("rollout-2026-03-27T18-10-59",
		`{"timestamp":"2026-03-27T18:11:00Z","type":"event_msg","payload":{"type":"task_complete"}}`,
		seen, fctx)

	// 3 inserts: SubagentStart, TaskCompleted, SubagentStop. All must
	// carry the conversation UUID.
	sqls := []string{waitForSQL(t, sqlCh), waitForSQL(t, sqlCh), waitForSQL(t, sqlCh)}
	var sawStart, sawStop bool
	for _, sql := range sqls {
		if !strings.Contains(sql, "conv-123") {
			t.Fatalf("expected insert to include conversation_id, got %s", sql)
		}
		if strings.Contains(sql, "'SubagentStart'") {
			sawStart = true
		}
		if strings.Contains(sql, "'SubagentStop'") {
			sawStop = true
		}
	}
	if !sawStart || !sawStop {
		t.Fatalf("expected both SubagentStart and SubagentStop inserts, got %q", sqls)
	}
}

// TestCodexMainSessionUsesConversationUUID guards the fix for the
// "same Codex run shows up as two sessions" bug: the hook handler
// writes rows under the Codex hook payload's `session_id` (which is
// the conversation UUID), but the JSONL parser used to use the
// filename timestamp prefix instead. After session_meta is parsed
// the JSONL parser must switch to the conversation UUID so both
// writers land in the same session_id bucket.
func TestCodexMainSessionUsesConversationUUID(t *testing.T) {
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
	fctx := &codexFileContext{fileID: "codex:rollout-2026-05-22T10-47-23-abc"}

	const filenameSID = "rollout-2026-05-22T10-47-23"
	const conversationUUID = "019e50cc-8d28-7920-9e9a-86dede8dd77f"

	// session_meta WITHOUT a subagent source — this is the main session.
	w.processCodexLine(filenameSID,
		`{"timestamp":"2026-05-22T17:47:29Z","type":"session_meta","payload":{"id":"`+conversationUUID+`","source":{"cli":"codex"},"cwd":"/tmp/proj"}}`,
		seen, fctx)
	// A subsequent user_message that must NOT be inserted under the
	// filename-based id.
	w.processCodexLine(filenameSID,
		`{"timestamp":"2026-05-22T17:47:30Z","type":"event_msg","payload":{"type":"user_message","message":"hi"}}`,
		seen, fctx)

	// Both inserts should reference the conversation UUID, not the
	// filename prefix.
	for i := 0; i < 2; i++ {
		sql := waitForSQL(t, sqlCh)
		if !strings.Contains(sql, conversationUUID) {
			t.Errorf("expected insert to reference conversation UUID %q, got: %s",
				conversationUUID, sql)
		}
		if strings.Contains(sql, filenameSID) {
			t.Errorf("insert still references filename-based session_id %q: %s",
				filenameSID, sql)
		}
	}
}

// TestCodexSubagentAttachesToParentUUID is the happy-path counterpart
// of the main-session test: when the parent's session_meta has
// already been processed and the parent UUID is published in the
// Watcher's codexParentSession map, the subagent file's
// SubagentStart row uses that parent UUID — keeping the dashboard's
// per-session SUM(SubagentStart) attached to the parent.
func TestCodexSubagentAttachesToParentUUID(t *testing.T) {
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
	const prefix = "rollout-2026-05-22T05-00-00"
	const parentUUID = "11111111-aaaa-bbbb-cccc-222222222222"
	w.recordCodexParentSession(prefix, parentUUID)

	seen := make(map[string]struct{})
	fctx := &codexFileContext{fileID: "codex:rollout-2026-05-22T05-00-00-sub"}

	w.processCodexLine(prefix,
		`{"timestamp":"2026-05-22T05:00:00Z","type":"session_meta","payload":{"id":"conv-subagent-uuid","source":{"subagent":"review"}}}`,
		seen, fctx)

	sql := waitForSQL(t, sqlCh)
	if !strings.Contains(sql, parentUUID) {
		t.Errorf("subagent SubagentStart should use parent UUID %q: %s", parentUUID, sql)
	}
	if strings.Contains(sql, "'"+prefix+"'") {
		t.Errorf("subagent SubagentStart should NOT use the filename prefix %q: %s", prefix, sql)
	}
}

// TestPeekCodexMainUUID is the pre-scan helper that closes the
// parent-vs-subagent goroutine race: scanner reads the first line of
// each rollout file synchronously and publishes parent UUIDs before
// any goroutines start, so subagent goroutines see the parent UUID
// the moment they reach their own session_meta.
func TestPeekCodexMainUUID(t *testing.T) {
	dir := t.TempDir()

	write := func(name, line string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(line+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	mainPath := write("rollout-2026-05-22T05-00-00-aaaa.jsonl",
		`{"timestamp":"2026-05-22T05:00:00Z","type":"session_meta","payload":{"id":"parent-uuid","source":{"cli":"codex"},"cwd":"/tmp"}}`)
	subPath := write("rollout-2026-05-22T05-00-00-bbbb.jsonl",
		`{"timestamp":"2026-05-22T05:00:01Z","type":"session_meta","payload":{"id":"sub-uuid","source":{"subagent":"review"}}}`)
	garbagePath := write("rollout-broken.jsonl", `not json at all`)
	emptyPath := write("rollout-empty.jsonl", ``)

	uuid, isMain := peekCodexMainUUID(mainPath)
	if !isMain || uuid != "parent-uuid" {
		t.Errorf("main file: got (%q, %v), want (parent-uuid, true)", uuid, isMain)
	}

	uuid, isMain = peekCodexMainUUID(subPath)
	if isMain || uuid != "" {
		t.Errorf("subagent file: got (%q, %v), want (\"\", false)", uuid, isMain)
	}

	uuid, isMain = peekCodexMainUUID(garbagePath)
	if isMain || uuid != "" {
		t.Errorf("garbage file: got (%q, %v), want (\"\", false)", uuid, isMain)
	}

	uuid, isMain = peekCodexMainUUID(emptyPath)
	if isMain || uuid != "" {
		t.Errorf("empty file: got (%q, %v), want (\"\", false)", uuid, isMain)
	}

	uuid, isMain = peekCodexMainUUID(filepath.Join(dir, "nonexistent.jsonl"))
	if isMain || uuid != "" {
		t.Errorf("missing file: got (%q, %v), want (\"\", false)", uuid, isMain)
	}
}

// TestCodexOrphanSubagentUsesOwnUUID pins down the fallback that
// Codex 0.131.0's `code review` mode actually hits: a subagent
// rollout whose session_meta has no parent reference AND whose
// filename timestamp prefix doesn't match any main session falls
// back to its OWN conversation UUID, not the filename prefix. A
// filename-prefix fallback would write a "rollout-..." pseudo
// session_id that nothing else uses — the UI then shows the row as
// an orphaned "rollout-" card next to real UUID sessions.
func TestCodexOrphanSubagentUsesOwnUUID(t *testing.T) {
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
	// No parent recorded — review-style subagent has no main session
	// it can attach to.
	seen := make(map[string]struct{})
	fctx := &codexFileContext{fileID: "codex:rollout-test-sub"}

	const subagentUUID = "019e5110-c0d9-7653-8bb0-ced77e673716"
	w.processCodexLine("rollout-test",
		`{"timestamp":"2026-05-22T05:00:00Z","type":"session_meta","payload":{"id":"`+subagentUUID+`","source":{"subagent":"review"}}}`,
		seen, fctx)

	sql := waitForSQL(t, sqlCh)
	if !strings.Contains(sql, subagentUUID) {
		t.Errorf("orphan subagent should fall back to its own conversation UUID %q: %s",
			subagentUUID, sql)
	}
	if strings.Contains(sql, "'rollout-test'") {
		t.Errorf("orphan subagent must NOT fall back to the filename prefix: %s", sql)
	}
	if fctx.sessionID != subagentUUID {
		t.Errorf("fctx.sessionID should be the subagent's own UUID; got %q", fctx.sessionID)
	}
}

// TestCodexSubagentWritesCwd pins down the fix for the dashboard's
// empty WORKING DIR column on orphan Codex subagent sessions. The
// subagent's session_meta payload carries `cwd` just like a main
// session, but insertCodexSubagentEvent used to drop it. Since orphan
// subagent sessions never emit a SessionStart row (only SubagentStart),
// the cwd column would never be populated and the dashboard's
// MAX(cwd) GROUP BY session_id returned empty.
func TestCodexSubagentWritesCwd(t *testing.T) {
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
	fctx := &codexFileContext{fileID: "codex:rollout-2026-05-22T13-47-44-sub"}

	const subagentUUID = "019e5171-ab3a-75f3-9057-be5a28cc8038"
	const cwd = "/Users/dennis/programming/go/tma1"
	w.processCodexLine("rollout-2026-05-22T13-47-44",
		`{"timestamp":"2026-05-22T13:47:44Z","type":"session_meta","payload":{"id":"`+subagentUUID+`","source":{"subagent":"review"},"cwd":"`+cwd+`"}}`,
		seen, fctx)

	sql := waitForSQL(t, sqlCh)
	if !strings.Contains(sql, cwd) {
		t.Errorf("SubagentStart insert missing cwd %q: %s", cwd, sql)
	}
}

// TestCodexLiveGateSkipsSubagentEvents pins down the rule that the
// live-hook gate must NOT suppress SubagentStart / SubagentStop —
// Codex never POSTs those via hooks, so the JSONL parser is the only
// writer. Gating them silently dropped hierarchy data for any session
// the hook adapter was active for.
func TestCodexLiveGateSkipsSubagentEvents(t *testing.T) {
	sqlCh := make(chan string, 4)
	ts := httptest.NewServer(httpTestHandler(sqlCh))
	defer ts.Close()

	oldClient := httpClient
	httpClient = ts.Client()
	defer func() { httpClient = oldClient }()

	w := &Watcher{
		sqlURL:        ts.URL,
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		IsLiveSession: func(string) bool { return true }, // every session is "live"
	}
	seen := make(map[string]struct{})
	fctx := &codexFileContext{fileID: "codex:rollout-test-sub"}

	// session_meta with subagent → SubagentStart (must NOT be gated).
	w.processCodexLine("rollout-test",
		`{"timestamp":"2026-05-22T05:00:00Z","type":"session_meta","payload":{"id":"conv-x","source":{"subagent":"review"}}}`,
		seen, fctx)
	// task_complete on a subagent file → TaskCompleted + SubagentStop.
	// TaskCompleted is JSONL-only (no Codex hook for it), so it joins
	// the lifecycle events that survive the gate.
	w.processCodexLine("rollout-test",
		`{"timestamp":"2026-05-22T05:00:01Z","type":"event_msg","payload":{"type":"task_complete"}}`,
		seen, fctx)

	// 3 inserts total: SubagentStart, TaskCompleted, SubagentStop. The
	// async insert goroutines arrive in undefined order so collect all.
	sqls := []string{waitForSQL(t, sqlCh), waitForSQL(t, sqlCh), waitForSQL(t, sqlCh)}
	var sawStart, sawStop bool
	for _, sql := range sqls {
		if strings.Contains(sql, "'SubagentStart'") {
			sawStart = true
		}
		if strings.Contains(sql, "'SubagentStop'") {
			sawStop = true
		}
	}
	if !sawStart || !sawStop {
		t.Fatalf("subagent lifecycle rows must survive live-hook gate, got SQLs %q", sqls)
	}
}

// TestCodexLiveGateSuppressesHookCoveredEvents confirms the gate still
// fires for tma1_hook_events writes for events the hook adapter
// actually posts (PreToolUse / PostToolUse), so we don't double-write
// hook rows. tma1_messages writes are intentionally NOT gated — the
// hook handler never writes messages, so the parser must keep doing it
// (see codexLiveGate doc comment).
func TestCodexLiveGateSuppressesHookCoveredEvents(t *testing.T) {
	sqlCh := make(chan string, 4)
	ts := httptest.NewServer(httpTestHandler(sqlCh))
	defer ts.Close()

	oldClient := httpClient
	httpClient = ts.Client()
	defer func() { httpClient = oldClient }()

	w := &Watcher{
		sqlURL:        ts.URL,
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		IsLiveSession: func(string) bool { return true },
	}
	seen := make(map[string]struct{})
	fctx := &codexFileContext{fileID: "codex:rollout-test", live: true}

	// function_call: hook-covered PreToolUse MUST be gated, but the
	// tool_use tma1_messages row survives so transcript replay stays
	// intact.
	w.processCodexLine("rollout-test",
		`{"timestamp":"2026-05-22T05:00:00Z","type":"response_item","payload":{"type":"function_call","name":"bash","call_id":"c1"}}`,
		seen, fctx)

	deadline := time.After(150 * time.Millisecond)
	for {
		select {
		case sql := <-sqlCh:
			if strings.Contains(sql, "tma1_hook_events") {
				t.Fatalf("expected gate to suppress tma1_hook_events insert, got %s", sql)
			}
			// tma1_messages tool_use is expected; keep draining.
		case <-deadline:
			return
		}
	}
}

// TestParseCodexSessionMetaNestedSubagent locks in the parse path for
// the Codex 0.131.0 auto-review subagent shape
// ({"subagent":{"other":"guardian"}}). Without the guardian rewrite,
// the dashboard's per-subagent rollups split auto-review under a
// brand-new agent_type instead of the codex-auto-review bucket the
// frontend filters expect.
func TestParseCodexSessionMetaNestedSubagent(t *testing.T) {
	meta := parseCodexSessionMeta([]byte(`{"id":"conv-1","source":{"subagent":{"other":"guardian"}},"cwd":"/tmp/project"}`))
	if meta.id != "conv-1" || meta.cwd != "/tmp/project" || meta.subagent != "codex-auto-review" {
		t.Fatalf("unexpected meta: %#v", meta)
	}
}

// TestProcessCodexResponseItemEmitsToolMessages locks in the dual
// emission introduced by this change: function_call /
// function_call_output write both a tma1_hook_events row (used by the
// waterfall) AND a tma1_messages row (used by transcript / replay).
// Pre-change, only the hook row landed, so Codex transcripts couldn't
// surface tool args/results outside the waterfall.
func TestProcessCodexResponseItemEmitsToolMessages(t *testing.T) {
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
	fctx := &codexFileContext{model: "gpt-5.5", conversationID: "conv-tool"}

	w.processCodexLine("rollout-2026-03-27T18-10-59",
		`{"timestamp":"2026-03-27T18:11:01Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"call-1","arguments":"{\"cmd\":\"go test ./...\"}"}}`,
		seen, fctx)
	w.processCodexLine("rollout-2026-03-27T18-10-59",
		`{"timestamp":"2026-03-27T18:11:02Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-1","output":"ok"}}`,
		seen, fctx)

	sqls := []string{
		waitForSQL(t, sqlCh),
		waitForSQL(t, sqlCh),
		waitForSQL(t, sqlCh),
		waitForSQL(t, sqlCh),
	}
	assertAnySQLContains(t, sqls, "tma1_hook_events", "PreToolUse", "exec_command", "call-1")
	assertAnySQLContains(t, sqls, "tma1_hook_events", "PostToolUse", "call-1", "ok")
	assertAnySQLContains(t, sqls, "tma1_messages", "tool_use", "exec_command", "call-1", "gpt-5.5")
	assertAnySQLContains(t, sqls, "tma1_messages", "tool_result", "call-1", "ok", "gpt-5.5")
}

// TestProcessCodexWebSearchEndEmitsToolMessages pins the projection of
// a single Codex web_search_end event: PreToolUse + PostToolUse hook
// pair (so the waterfall draws the tool span) plus ONE tool_use
// message for the input. We deliberately do NOT write a tool_result
// message — Codex's web_search_end carries the query/action but no
// results payload, and emitting `tool_input` as `tool_result` would
// inflate context-length heuristics keyed off result length.
func TestProcessCodexWebSearchEndEmitsToolMessages(t *testing.T) {
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
	fctx := &codexFileContext{model: "gpt-5.5", conversationID: "conv-search"}

	w.processCodexLine("rollout-2026-03-27T18-10-59",
		`{"timestamp":"2026-03-27T18:11:03Z","type":"event_msg","payload":{"type":"web_search_end","call_id":"ws-1","query":"xdisp cursor","action":{"type":"search","query":"xdisp cursor"}}}`,
		seen, fctx)

	sqls := []string{
		waitForSQL(t, sqlCh),
		waitForSQL(t, sqlCh),
		waitForSQL(t, sqlCh),
	}
	assertAnySQLContains(t, sqls, "tma1_hook_events", "PreToolUse", "web_search", "ws-1", "xdisp cursor")
	assertAnySQLContains(t, sqls, "tma1_hook_events", "PostToolUse", "web_search", "ws-1")
	assertAnySQLContains(t, sqls, "tma1_messages", "tool_use", "web_search", "ws-1", "gpt-5.5")

	// No tool_result row should land for web_search.
	for _, sql := range sqls {
		if strings.Contains(sql, "tma1_messages") && strings.Contains(sql, "tool_result") {
			t.Fatalf("expected no tool_result message for web_search_end, got %s", sql)
		}
	}

	// No 4th insert (only 3 total).
	select {
	case sql := <-sqlCh:
		t.Fatalf("expected exactly 3 inserts for web_search_end, got extra: %s", sql)
	case <-time.After(150 * time.Millisecond):
	}
}

// TestProcessCodexResponseItemEmitsReasoningSummary locks in the
// reasoning → thinking projection. Codex 0.131.0 emits reasoning items
// with text under `summary` blocks; this test pins the older summary
// path so a Codex version downgrade doesn't silently drop thinking
// rows from the transcript.
func TestProcessCodexResponseItemEmitsReasoningSummary(t *testing.T) {
	sqlCh := make(chan string, 1)
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
	fctx := &codexFileContext{model: "gpt-5.5"}

	w.processCodexLine("rollout-2026-03-27T18-10-59",
		`{"timestamp":"2026-03-27T18:11:03Z","type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"Need inspect parser."}]}}`,
		seen, fctx)

	sql := waitForSQL(t, sqlCh)
	for _, want := range []string{"tma1_messages", "thinking", "Need inspect parser.", "gpt-5.5"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("expected SQL to contain %q, got %s", want, sql)
		}
	}
}

// TestProcessCodexTokenCountWritesUsageRow locks in the Codex
// `event_msg.token_count` → tma1_messages projection that lets the
// sessions.js Codex fallback derive apiCalls from messages when
// OTel data isn't available. Without this row, reasoning_tokens
// has no writer on the JSONL path and the dashboard's reasoning
// column would always show zero for Codex sessions that aren't
// reporting OTel.
func TestProcessCodexTokenCountWritesUsageRow(t *testing.T) {
	sqlCh := make(chan string, 1)
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
	fctx := &codexFileContext{model: "gpt-5.5", conversationID: "conv-usage"}

	w.processCodexLine("rollout-2026-05-20T15-51-00",
		`{"timestamp":"2026-05-20T22:51:41.859Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":37347,"cached_input_tokens":34688,"cache_write_input_tokens":128,"output_tokens":1549,"reasoning_output_tokens":516,"total_tokens":39412}}}}`,
		seen, fctx)

	sql := waitForSQL(t, sqlCh)
	for _, want := range []string{
		"tma1_messages",
		// 'usage' message_type is dedicated for synthetic per-call
		// usage rows so the timeline can drop them by type instead
		// of an empty-content heuristic.
		"'usage', 'assistant'",
		"'gpt-5.5'",
		"37347", "1549", "34688", "128", "516", "cache_creation_tokens",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("expected SQL to contain %q, got %s", want, sql)
		}
	}
}

// TestProcessCodexTokenCountSkipsEmptyUsage pins down the zero-skip
// behaviour: Codex emits a token_count immediately after session
// handshake where every counter is 0; persisting those would add
// noise to the apiCalls fallback (zero-cost zero-token rows).
func TestProcessCodexTokenCountSkipsEmptyUsage(t *testing.T) {
	sqlCh := make(chan string, 1)
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
	fctx := &codexFileContext{model: "gpt-5.5"}

	w.processCodexLine("rollout-2026-05-20T15-51-00",
		`{"timestamp":"2026-05-20T22:51:00.000Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":0,"cached_input_tokens":0,"output_tokens":0,"reasoning_output_tokens":0,"total_tokens":0}}}}`,
		seen, fctx)

	select {
	case sql := <-sqlCh:
		t.Fatalf("expected no insert for zero usage, got %s", sql)
	case <-time.After(150 * time.Millisecond):
	}
}

func assertAnySQLContains(t *testing.T, sqls []string, wants ...string) {
	t.Helper()
	for _, sql := range sqls {
		ok := true
		for _, want := range wants {
			if !strings.Contains(sql, want) {
				ok = false
				break
			}
		}
		if ok {
			return
		}
	}
	t.Fatalf("expected one SQL to contain %q, got %q", wants, sqls)
}

func httpTestHandler(sqlCh chan<- string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(500)
			return
		}
		sqlCh <- r.Form.Get("sql")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"output":[]}`))
	}
}

func waitForSQL(t *testing.T, sqlCh <-chan string) string {
	t.Helper()
	select {
	case sql := <-sqlCh:
		return sql
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for SQL insert")
		return ""
	}
}
