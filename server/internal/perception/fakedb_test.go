package perception

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeDB stands in for GreptimeDB's /v1/sql endpoint. Query behaviour
// that only emerges across several round-trips — scoped fallback, exact
// id beating prefix, pagination — can't be proven by inspecting one SQL
// string, so these tests drive the real Bundler code paths.
type fakeDB struct {
	t *testing.T
	// respond maps a substring of the incoming SQL to the rows returned
	// for it. First match wins, so register specific patterns first.
	respond []fakeRule

	mu   sync.Mutex
	seen []string
}

type fakeRule struct {
	match string
	cols  []string
	rows  [][]any
}

func (f *fakeDB) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	form, _ := url.ParseQuery(string(body))
	sql := form.Get("sql")

	f.mu.Lock()
	f.seen = append(f.seen, sql)
	f.mu.Unlock()

	cols, rows := []string{"c0"}, [][]any{}
	for _, rule := range f.respond {
		if strings.Contains(sql, rule.match) {
			cols, rows = rule.cols, rule.rows
			break
		}
	}
	schema := make([]map[string]string, 0, len(cols))
	for _, c := range cols {
		schema = append(schema, map[string]string{"name": c})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code": 0,
		"output": []map[string]any{{
			"records": map[string]any{
				"schema": map[string]any{"column_schemas": schema},
				"rows":   rows,
			},
		}},
	})
}

func (f *fakeDB) queries() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

func (f *fakeDB) sawMatching(substr string) bool {
	for _, q := range f.queries() {
		if strings.Contains(q, substr) {
			return true
		}
	}
	return false
}

func newFakeBundler(t *testing.T, rules []fakeRule) (*Bundler, *fakeDB) {
	t.Helper()
	fake := &fakeDB{t: t, respond: rules}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parse test server port: %v", err)
	}
	client := NewClient(port)
	return &Bundler{client: client, execClient: client, logger: slog.Default()}, fake
}

var messageCols = []string{"ts_ms", "message_type", "role", "content", "model", "tool_name", "input_tokens", "output_tokens"}

func msgRow(ts int64, msgType, role, content string) []any {
	return []any{float64(ts), msgType, role, content, "", "", float64(0), float64(0)}
}

// The scope must be resolved before the keyword search, and the
// substring fallback must stay inside that same scope. Searching first
// and filtering afterwards loses hits from the current project when a
// busier project fills the candidate limit.
func TestSearchSessionsScopesBeforeSearchingAndFallsBackInScope(t *testing.T) {
	b, fake := newFakeBundler(t, []fakeRule{
		{match: "FROM tma1_hook_events\n\t\t WHERE ts >", cols: []string{"session_id"}, rows: [][]any{{"s-1"}}},
		{match: "GROUP BY session_id, cwd", cols: []string{"session_id", "cwd", "agent_source", "events"},
			rows: [][]any{{"s-1", "/proj", "codex", float64(9)}}},
		{match: "matches_term", rows: [][]any{}},
		{match: "ORDER BY ts DESC", cols: []string{"ts_ms", "role", "message_type", "content"},
			rows: [][]any{{float64(1000), "assistant", "assistant", "a needle here"}}},
		{match: "lower(content) LIKE", cols: []string{"session_id", "last_ms", "hits"},
			rows: [][]any{{"s-1", float64(1000), float64(2)}}},
	})

	res, err := b.SearchSessions(context.Background(), SearchOptions{
		Query:   "needle",
		Project: "/proj",
	})
	if err != nil {
		t.Fatalf("SearchSessions: %v", err)
	}
	if res.MatchMode != MatchModeSubstring {
		t.Errorf("match_mode = %q, want %q after the term pass came back empty", res.MatchMode, MatchModeSubstring)
	}
	if res.Count != 1 || res.Sessions[0].SessionID != "s-1" {
		t.Fatalf("unexpected results: %+v", res.Sessions)
	}
	if res.Sessions[0].AgentSource != "codex" || res.Sessions[0].CWD != "/proj" {
		t.Errorf("attribution missing: %+v", res.Sessions[0])
	}
	if len(res.Sessions[0].Snippets) != 1 || res.Sessions[0].Snippets[0].Text != "a needle here" {
		t.Errorf("snippets = %+v", res.Sessions[0].Snippets)
	}

	queries := fake.queries()
	if !strings.Contains(queries[0], "tma1_hook_events") {
		t.Errorf("scope must be resolved first, got %q", queries[0])
	}
	for _, q := range queries {
		// Only the candidate query ranks across sessions; the snippet
		// query is already pinned to one session_id.
		if strings.Contains(q, "GROUP BY session_id") && !strings.Contains(q, "session_id IN") {
			t.Errorf("candidate search escaped the scope: %s", q)
		}
	}
}

// An empty scope means "nothing to search here" — the message table is
// never touched, and the caller is told why.
func TestSearchSessionsEmptyScopeSkipsSearch(t *testing.T) {
	b, fake := newFakeBundler(t, nil)
	res, err := b.SearchSessions(context.Background(), SearchOptions{Query: "x", Project: "/proj"})
	if err != nil {
		t.Fatalf("SearchSessions: %v", err)
	}
	if res.Count != 0 || res.Note == "" {
		t.Errorf("expected an empty annotated result, got %+v", res)
	}
	if fake.sawMatching("tma1_messages") {
		t.Error("no message query should run when the scope is empty")
	}
}

// A complete id that happens to be another id's prefix must resolve to
// itself, not be reported as ambiguous.
func TestGetSessionTranscriptExactIDBeatsPrefix(t *testing.T) {
	b, fake := newFakeBundler(t, []fakeRule{
		{match: "ORDER BY ts DESC", cols: messageCols,
			rows: [][]any{msgRow(1000, "assistant", "assistant", "hello")}},
		{match: "LIKE 'abc123%'", cols: []string{"session_id"},
			rows: [][]any{{"abc123"}, {"abc1234"}}},
		{match: "DISTINCT session_id", cols: []string{"session_id"}, rows: [][]any{{"abc123"}}},
	})

	out, err := b.GetSessionTranscript(context.Background(), TranscriptOptions{SessionID: "abc123"})
	if err != nil {
		t.Fatalf("GetSessionTranscript: %v", err)
	}
	if out.SessionID != "abc123" || len(out.Candidates) != 0 {
		t.Fatalf("exact id should resolve cleanly, got %+v", out)
	}
	if fake.sawMatching("LIKE 'abc123%'") {
		t.Error("prefix lookup should not run once the exact id matched")
	}
	if out.MessageCount != 1 {
		t.Errorf("message_count = %d, want 1", out.MessageCount)
	}
}

func TestGetSessionTranscriptAmbiguousPrefix(t *testing.T) {
	b, fake := newFakeBundler(t, []fakeRule{
		{match: "LIKE 'abc123%'", cols: []string{"session_id"},
			rows: [][]any{{"abc1230"}, {"abc1231"}}},
		{match: "DISTINCT session_id", rows: [][]any{}},
	})

	out, err := b.GetSessionTranscript(context.Background(), TranscriptOptions{SessionID: "abc123"})
	if err != nil {
		t.Fatalf("GetSessionTranscript: %v", err)
	}
	if len(out.Candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %+v", out.Candidates)
	}
	if out.Note == "" {
		t.Error("ambiguous prefix must carry a note explaining what to do next")
	}
	if fake.sawMatching("ORDER BY ts DESC") {
		t.Error("no transcript should be fetched for an ambiguous prefix")
	}
}

// A session ingested from JSONL has no hook rows. MIN/MAX over zero rows
// still returns one row, of NULLs, and time.UnixMilli(0) is 1970 rather
// than the zero time — so converting before checking would suppress the
// message-table fallback and date the session to the epoch.
func TestGetSessionTranscriptFallsBackToMessageTimestamps(t *testing.T) {
	b, _ := newFakeBundler(t, []fakeRule{
		{match: "FROM tma1_messages WHERE session_id", cols: []string{"started_ms", "last_ms"},
			rows: [][]any{{float64(1_700_000_000_000), float64(1_700_000_060_000)}}},
		{match: "ORDER BY ts DESC", cols: messageCols,
			rows: [][]any{msgRow(1_700_000_060_000, "assistant", "assistant", "hi")}},
		{match: "FROM tma1_hook_events WHERE session_id", cols: []string{"started_ms", "last_ms"},
			rows: [][]any{{nil, nil}}},
		{match: "DISTINCT session_id", cols: []string{"session_id"}, rows: [][]any{{"jsonl-only"}}},
	})

	out, err := b.GetSessionTranscript(context.Background(), TranscriptOptions{SessionID: "jsonl-only"})
	if err != nil {
		t.Fatalf("GetSessionTranscript: %v", err)
	}
	if out.LastActivityAt.Year() != 2023 {
		t.Errorf("last_activity_at = %v, want the message timestamp (2023), not the epoch",
			out.LastActivityAt)
	}
}

// Dedup runs after the probe row is dropped. Doing it the other way
// round lets a row that dedup collapsed reappear as the first entry of
// the next page, because offsets index raw rows.
func TestGetSessionTranscriptPagesWithoutRepeating(t *testing.T) {
	dup := msgRow(1000, "assistant", "assistant", "same text")
	uniq := msgRow(900, "assistant", "assistant", "older, distinct")
	b, _ := newFakeBundler(t, []fakeRule{
		// Newest first: [dup, dup, uniq]. With limit 2 the probe is uniq.
		{match: "ORDER BY ts DESC", cols: messageCols, rows: [][]any{dup, dup, uniq}},
		{match: "DISTINCT session_id", cols: []string{"session_id"}, rows: [][]any{{"sess-1"}}},
	})

	page1, err := b.GetSessionTranscript(context.Background(), TranscriptOptions{
		SessionID:    "sess-1",
		MessageLimit: 2,
	})
	if err != nil {
		t.Fatalf("GetSessionTranscript: %v", err)
	}
	if !page1.HasMore || page1.NextOffset != 2 {
		t.Fatalf("has_more=%v next_offset=%d, want true/2", page1.HasMore, page1.NextOffset)
	}
	for _, m := range page1.Messages {
		if m.Content == "older, distinct" {
			t.Error("the probe row leaked into page 1; offset 2 will return it again")
		}
	}
}

func TestGetSessionTranscriptShortIDRejected(t *testing.T) {
	b, _ := newFakeBundler(t, nil)
	if _, err := b.GetSessionTranscript(context.Background(), TranscriptOptions{SessionID: "abc"}); err == nil {
		t.Error("expected an error for a too-short session_id")
	}
}

// The extra fetched row signals another page without a COUNT query, and
// must not leak into the returned messages.
func TestGetSessionTranscriptPagination(t *testing.T) {
	rows := make([][]any, 0, 4)
	for i := 0; i < 4; i++ {
		rows = append(rows, msgRow(int64(1000+i), "assistant", "assistant", fmt.Sprintf("m%d", i)))
	}
	b, fake := newFakeBundler(t, []fakeRule{
		{match: "ORDER BY ts DESC", cols: messageCols, rows: rows},
		{match: "DISTINCT session_id", cols: []string{"session_id"}, rows: [][]any{{"sess-1"}}},
	})

	out, err := b.GetSessionTranscript(context.Background(), TranscriptOptions{
		SessionID:    "sess-1",
		MessageLimit: 3,
	})
	if err != nil {
		t.Fatalf("GetSessionTranscript: %v", err)
	}
	if !out.HasMore || out.NextOffset != 3 {
		t.Errorf("has_more=%v next_offset=%d, want true/3", out.HasMore, out.NextOffset)
	}
	if out.MessageCount != 3 {
		t.Errorf("message_count = %d, want 3 (the probe row must be dropped)", out.MessageCount)
	}
	if !fake.sawMatching("LIMIT 4") {
		t.Error("expected one extra row to be fetched as the has-more probe")
	}
}

// Replayed JSONL leaves duplicate rows. Dedup may swallow the probe row,
// so has_more must be derived from the raw row count, not the deduped
// slice — otherwise paging stops one page early.
func TestGetSessionTranscriptPaginationSurvivesDedup(t *testing.T) {
	dup := msgRow(1000, "assistant", "assistant", "same text")
	b, _ := newFakeBundler(t, []fakeRule{
		{match: "ORDER BY ts DESC", cols: messageCols, rows: [][]any{dup, dup, dup}},
		{match: "DISTINCT session_id", cols: []string{"session_id"}, rows: [][]any{{"sess-1"}}},
	})

	out, err := b.GetSessionTranscript(context.Background(), TranscriptOptions{
		SessionID:    "sess-1",
		MessageLimit: 2,
	})
	if err != nil {
		t.Fatalf("GetSessionTranscript: %v", err)
	}
	if out.MessageCount != 1 {
		t.Errorf("message_count = %d, want 1 after dedup", out.MessageCount)
	}
	if !out.HasMore || out.NextOffset != 2 {
		t.Errorf("has_more=%v next_offset=%d, want true/2 — 3 rows came back for a limit of 2",
			out.HasMore, out.NextOffset)
	}
}

// Codex writes empty-content `usage` rows per model call; they must
// never consume the message budget.
func TestFetchSessionMessagesFiltersSyntheticRowsAndTruncates(t *testing.T) {
	long := strings.Repeat("é", 500) // 1000 bytes of multi-byte content
	b, fake := newFakeBundler(t, []fakeRule{
		{match: "ORDER BY ts DESC", cols: messageCols,
			rows: [][]any{msgRow(1000, "assistant", "assistant", long)}},
	})

	msgs, err := b.fetchSessionMessages(context.Background(), "s", 10, 100, "")
	if err != nil {
		t.Fatalf("fetchSessionMessages: %v", err)
	}
	if !fake.sawMatching("message_type != 'usage'") {
		t.Error("synthetic usage rows must be filtered in SQL")
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if !msgs[0].Truncated {
		t.Error("over-long content should be flagged truncated")
	}
	// content_chars counts characters, not bytes: 100 two-byte runes must
	// survive as 100 characters, not 50.
	if n := len([]rune(msgs[0].Content)); n != 100 || !isValidUTF8(msgs[0].Content) {
		t.Errorf("content should be cut to 100 runes, got %d", n)
	}
}

// A session that moved between repos has several cwd values. MAX(cwd)
// picks the lexicographic maximum, which can be a directory touched
// twice out of hundreds of events; the label must follow the events.
func TestAttributionUsesDominantCWDNotMaxCWD(t *testing.T) {
	b, _ := newFakeBundler(t, []fakeRule{
		{match: "GROUP BY session_id, cwd", cols: []string{"session_id", "cwd", "agent_source", "events"},
			rows: [][]any{
				{"s-1", "/repo/tma1", "claude_code", float64(410)},
				{"s-1", "/repo/zzz-langfuse", "claude_code", float64(2)},
			}},
	})

	attrs := b.attributionFor(context.Background(), []string{"'s-1'"})
	if got := attrs["s-1"].CWD; got != "/repo/tma1" {
		t.Errorf("cwd = %q, want the directory with the most events", got)
	}
	if got := attrs["s-1"].AgentSource; got != "claude_code" {
		t.Errorf("agent_source = %q, want claude_code", got)
	}
}

// Codex only writes cwd on SessionStart, so most of its rows have none.
// Attribution must still find the one that does.
func TestAttributionSkipsEmptyCWD(t *testing.T) {
	b, _ := newFakeBundler(t, []fakeRule{
		{match: "GROUP BY session_id, cwd", cols: []string{"session_id", "cwd", "agent_source", "events"},
			rows: [][]any{
				{"s-2", "", "codex", float64(300)},
				{"s-2", "/repo/tma1", "codex", float64(1)},
			}},
	})

	attrs := b.attributionFor(context.Background(), []string{"'s-2'"})
	if got := attrs["s-2"].CWD; got != "/repo/tma1" {
		t.Errorf("cwd = %q, want the only non-empty directory", got)
	}
	if got := attrs["s-2"].AgentSource; got != "codex" {
		t.Errorf("agent_source = %q, want codex", got)
	}
}

func TestExecQueryRejectsNonSelectBeforeHittingDB(t *testing.T) {
	b, fake := newFakeBundler(t, nil)
	if _, err := b.ExecQuery(context.Background(), "DROP TABLE tma1_messages", 10, 100); err == nil {
		t.Error("expected DROP to be rejected")
	}
	if len(fake.queries()) != 0 {
		t.Errorf("rejected statement must not reach the database, saw %v", fake.queries())
	}
}

func TestExecQueryCapsRowsAndCells(t *testing.T) {
	rows := [][]any{
		{strings.Repeat("a", 500)},
		{"short"},
		{"third"},
	}
	b, fake := newFakeBundler(t, []fakeRule{
		{match: "SELECT * FROM (", cols: []string{"content"}, rows: rows},
	})

	res, err := b.ExecQuery(context.Background(), "SELECT content FROM tma1_messages", 2, 100)
	if err != nil {
		t.Fatalf("ExecQuery: %v", err)
	}
	if !fake.sawMatching("LIMIT 3") {
		t.Errorf("row cap must be pushed into SQL as limit+1, queries: %v", fake.queries())
	}
	if res.RowCount != 2 || !res.Truncated {
		t.Errorf("row_count=%d truncated=%v, want 2/true", res.RowCount, res.Truncated)
	}
	if cell := res.Rows[0][0].(string); len([]rune(cell)) > 101 {
		t.Errorf("cell should be truncated, got %d runes", len([]rune(cell)))
	}
}

func TestExecQueryPreservesLargeIntegers(t *testing.T) {
	const want = "9007199254740993" // 2^53 + 1; float64 cannot represent it exactly.
	b, _ := newFakeBundler(t, []fakeRule{
		{match: "SELECT * FROM (", cols: []string{"value"}, rows: [][]any{{json.Number(want)}}},
	})

	res, err := b.ExecQuery(context.Background(), "SELECT 9007199254740993 AS value", 1, 100)
	if err != nil {
		t.Fatalf("ExecQuery: %v", err)
	}
	got, ok := res.Rows[0][0].(json.Number)
	if !ok {
		t.Fatalf("value type = %T, want json.Number", res.Rows[0][0])
	}
	if got.String() != want {
		t.Errorf("value = %s, want %s", got, want)
	}
}
