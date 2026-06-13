package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRelayHandoffDefinition(t *testing.T) {
	def := RelayHandoffTool{}.Definition()
	if def.Name != "tma1_handoff" {
		t.Fatalf("name = %q", def.Name)
	}
	if len(def.InputSchema.Required) != 1 || def.InputSchema.Required[0] != "stage" {
		t.Fatalf("required = %v, want [stage]", def.InputSchema.Required)
	}
}

func TestRelayHandoffEmptyStage(t *testing.T) {
	res, err := RelayHandoffTool{}.Call(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("empty stage should yield an error result")
	}
}

func TestRelayHandoffPostsBodyAndToken(t *testing.T) {
	var gotBody map[string]string
	var gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Tma1-Relay-Token")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"dispatched":true}`))
	}))
	defer srv.Close()

	port := strings.TrimPrefix(srv.URL, "http://127.0.0.1:")
	tool := RelayHandoffTool{Port: port, Caller: "codex", Token: "tk", Client: srv.Client()}

	res, err := tool.Call(context.Background(), map[string]any{"stage": "plan_reviewed", "summary": "S"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res)
	}
	if gotToken != "tk" {
		t.Fatalf("token header = %q, want tk", gotToken)
	}
	// Empty Role falls back to reviewer for a codex caller.
	if gotBody["stage"] != "plan_reviewed" || gotBody["role"] != "reviewer" || gotBody["summary"] != "S" {
		t.Fatalf("posted body = %+v", gotBody)
	}
}

func TestRelayHandoffNon2xxIsNotError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid relay token"}`))
	}))
	defer srv.Close()

	port := strings.TrimPrefix(srv.URL, "http://127.0.0.1:")
	tool := RelayHandoffTool{Port: port, Caller: "claude_code", Token: "x", Client: srv.Client()}

	res, err := tool.Call(context.Background(), map[string]any{"stage": "plan_ready"})
	if err != nil {
		t.Fatal(err)
	}
	// A non-2xx must NOT break the agent loop — surfaced as a plain note.
	if res.IsError {
		t.Fatal("non-2xx should be a plain (non-error) result")
	}
	if !strings.Contains(res.Content[0].Text, "not delivered") {
		t.Fatalf("want 'not delivered' note, got %q", res.Content[0].Text)
	}
}
