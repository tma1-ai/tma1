package git

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestHookAttributorUsesExactPathFromAnyTool(t *testing.T) {
	attributor, queries := newCountingAttributor(t, 1)

	got := attributor.Classify(context.Background(), "/repo/tma1", "/repo/tma1/output.png", time.UnixMilli(10_000))
	if got != AttributionAgent {
		t.Fatalf("Classify() = %q, want %q", got, AttributionAgent)
	}
	if len(*queries) != 1 {
		t.Fatalf("queries = %d, want 1", len(*queries))
	}
	if strings.Contains((*queries)[0], "tool_name IN") {
		t.Fatalf("exact path attribution must not be limited to hard-coded tool names: %s", (*queries)[0])
	}
}

func TestHookAttributorUsesActiveProjectToolForIndirectWrite(t *testing.T) {
	attributor, queries := newCountingAttributor(t, 0, 1)

	got := attributor.Classify(context.Background(), "/repo/tma1", "/repo/tma1/generated/file.go", time.UnixMilli(10_000))
	if got != AttributionAgent {
		t.Fatalf("Classify() = %q, want %q", got, AttributionAgent)
	}
	if len(*queries) != 2 {
		t.Fatalf("queries = %d, want 2", len(*queries))
	}
	activeSQL := (*queries)[1]
	for _, fragment := range []string{
		"LEFT JOIN tma1_hook_events q",
		"q.tool_use_id = p.tool_use_id",
		"q.event_type IN ('PostToolUse','PostToolUseFailure')",
		"p.cwd = '/repo/tma1'",
		"q.tool_use_id IS NULL",
	} {
		if !strings.Contains(activeSQL, fragment) {
			t.Errorf("active-tool query missing %q: %s", fragment, activeSQL)
		}
	}

	got = attributor.Classify(context.Background(), "/repo/tma1", "/repo/tma1/generated/other.go", time.UnixMilli(10_001))
	if got != AttributionAgent {
		t.Fatalf("cached Classify() = %q, want %q", got, AttributionAgent)
	}
	if len(*queries) != 2 {
		t.Fatalf("cached classification issued another query; queries = %d, want 2", len(*queries))
	}
}

func TestHookAttributorDoesNotInferHumanFromMissingHookEvidence(t *testing.T) {
	attributor, _ := newCountingAttributor(t, 0, 0)

	got := attributor.Classify(context.Background(), "/repo/tma1", "/repo/tma1/notes.txt", time.UnixMilli(10_000))
	if got != AttributionUnknown {
		t.Fatalf("Classify() = %q, want %q", got, AttributionUnknown)
	}
}

func newCountingAttributor(t *testing.T, counts ...int) (*HookAttributor, *[]string) {
	t.Helper()
	queries := make([]string, 0, len(counts))
	attributor := NewHookAttributor(1)
	attributor.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		queries = append(queries, r.Form.Get("sql"))
		call := len(queries) - 1
		if call >= len(counts) {
			t.Errorf("unexpected query %d: %s", call+1, queries[call])
			return &http.Response{
				StatusCode: http.StatusInternalServerError,
				Body:       io.NopCloser(strings.NewReader("unexpected query")),
			}, nil
		}
		body := fmt.Sprintf(`{"code":0,"output":[{"records":{"rows":[[%d]]}}]}`, counts[call])
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})}

	return attributor, &queries
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
