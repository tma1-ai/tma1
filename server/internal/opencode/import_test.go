package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const exportFixture = `{"info":{"id":"ses_test","directory":"/tmp/project","time":{"created":1793280000000}},"messages":[
 {"info":{"id":"msg_user","sessionID":"ses_test","role":"user","time":{"created":1793280000001}},"parts":[{"id":"prt_user","sessionID":"ses_test","messageID":"msg_user","type":"text","text":"Fix O'Brien's test"}]},
 {"info":{"id":"msg_answer","sessionID":"ses_test","role":"assistant","modelID":"gpt-5.3-codex","time":{"created":1793280000010,"completed":1793280000100},"tokens":{"input":100,"output":20,"reasoning":30,"cache":{"read":800,"write":10}}},"parts":[
 {"id":"prt_step","sessionID":"ses_test","messageID":"msg_answer","type":"step-finish"},
 {"id":"prt_tool","sessionID":"ses_test","messageID":"msg_answer","type":"tool","callID":"call_one","tool":"bash","state":{"status":"error","input":{"command":"go test"},"error":"test failed","time":{"start":1793280000020,"end":1793280000030}}},
 {"id":"prt_answer","sessionID":"ses_test","messageID":"msg_answer","type":"text","text":"The test failed."}]}
]}`

func fixture(t *testing.T) Snapshot {
	t.Helper()
	var s Snapshot
	if err := json.Unmarshal([]byte(exportFixture), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestNormalizeOpenCodeUsage(t *testing.T) {
	data, err := records(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	usage, failures, starts := 0, 0, 0
	for _, r := range data {
		if r.id == "msg_answer:usage" {
			usage++
			if !strings.Contains(r.values, "100,50,30,800,10,90") {
				t.Fatalf("reasoning must be included once: %s", r.values)
			}
		}
		if strings.Contains(r.values, "PostToolUseFailure") {
			failures++
		}
		if strings.Contains(r.values, "PreToolUse") {
			starts++
		}
		if r.id == "prt_user" && !strings.Contains(r.values, "O''Brien''s") {
			t.Fatal("unescaped prompt")
		}
	}
	if usage != 1 || failures != 1 || starts != 1 {
		t.Fatalf("usage=%d failures=%d starts=%d", usage, failures, starts)
	}
	s := fixture(t)
	s.Messages[1].Info.Time.Completed = 0
	pending, err := records(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range pending {
		if strings.Contains(r.id, "answer") || strings.Contains(r.id, "tool") {
			t.Fatal("unfinished assistant snapshot imported")
		}
	}
	s = fixture(t)
	*s.Messages[1].Info.Tokens.Output = -1
	if _, err := records(s); err == nil {
		t.Fatal("negative usage accepted")
	}
	s = fixture(t)
	s.Messages[1].Info.Tokens.Input = nil
	if _, err := records(s); err == nil {
		t.Fatal("missing input usage accepted as zero")
	}
	s = fixture(t)
	s.Messages[1].Parts[0].SessionID = "ses_other"
	if _, err := records(s); err == nil {
		t.Fatal("cross-session part accepted")
	}
}

func TestImportResumesCommittedRecords(t *testing.T) {
	stored := map[string][]string{"tma1_messages": {}, "tma1_hook_events": {}}
	fail := true
	importer := New(func(_ context.Context, sql string) ([]byte, error) {
		table := "tma1_messages"
		if strings.Contains(sql, "tma1_hook_events") {
			table = "tma1_hook_events"
		}
		if strings.HasPrefix(sql, "SELECT") {
			rows := [][]string{}
			for _, id := range stored[table] {
				rows = append(rows, []string{id})
			}
			return json.Marshal(map[string]any{"output": []any{map[string]any{"records": map[string]any{"rows": rows}}}})
		}
		if fail && table == "tma1_hook_events" {
			fail = false
			return nil, errors.New("database unavailable")
		}
		id := strings.Split(strings.Split(sql, "VALUES ('")[1], "'")[0]
		for _, old := range stored[table] {
			if old == id {
				t.Fatalf("duplicate inserted: %s", id)
			}
		}
		stored[table] = append(stored[table], id)
		return []byte(`{"output":[{"affectedrows":1}]}`), nil
	})
	s := fixture(t)
	if n, err := importer.Import(context.Background(), s); err == nil || n != 3 {
		t.Fatalf("partial import n=%d err=%v", n, err)
	}
	if n, err := importer.Import(context.Background(), s); err != nil || n != 5 {
		t.Fatalf("resume n=%d err=%v", n, err)
	}
	// A fresh importer simulates a process restart; deduplication is persisted.
	restarted := New(importer.query)
	if n, err := restarted.Import(context.Background(), s); err != nil || n != 0 {
		t.Fatalf("replay n=%d err=%v", n, err)
	}
}
