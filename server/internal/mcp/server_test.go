package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
)

// roundTrip feeds a single JSON-RPC request through the server and returns
// the decoded response (or nil if the server emitted none — notifications).
func roundTrip(t *testing.T, srv *Server, req Request) *Response {
	t.Helper()

	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal req: %v", err)
	}
	in := bytes.NewReader(append(body, '\n'))
	out := &bytes.Buffer{}
	srv.SetIO(in, out)

	if err := srv.Run(context.Background()); err != nil && err != io.EOF {
		t.Fatalf("Run: %v", err)
	}

	if out.Len() == 0 {
		return nil
	}
	var resp Response
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &resp); err != nil {
		t.Fatalf("unmarshal resp (%q): %v", out.String(), err)
	}
	return &resp
}

func newTestServer() *Server {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	// Bundler=nil → tools surface a clear "not configured" IsError result
	// instead of crashing. That is enough to exercise the JSON-RPC plumbing
	// without standing up a GreptimeDB.
	return NewServer(logger, ContextBundleTool{}, SessionStateTool{}, AnomaliesTool{}, BuildStatusTool{}, ExternalChangesTool{}, ProjectStateTool{}, PeerSessionsTool{})
}

func TestInitializeReturnsProtocolVersion(t *testing.T) {
	resp := roundTrip(t, newTestServer(), Request{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
	})
	if resp == nil {
		t.Fatal("expected response, got nil")
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	raw, _ := json.Marshal(resp.Result)
	var init InitializeResult
	if err := json.Unmarshal(raw, &init); err != nil {
		t.Fatalf("decode initialize result: %v", err)
	}
	if init.ProtocolVersion != protocolVersion {
		t.Errorf("ProtocolVersion = %q, want %q", init.ProtocolVersion, protocolVersion)
	}
	if init.ServerInfo.Name != serverName {
		t.Errorf("ServerInfo.Name = %q, want %q", init.ServerInfo.Name, serverName)
	}
	if init.Capabilities.Tools == nil {
		t.Error("Capabilities.Tools should be advertised")
	}
}

func TestToolsListReturnsAllRegisteredTools(t *testing.T) {
	resp := roundTrip(t, newTestServer(), Request{
		JSONRPC: "2.0",
		ID:      2,
		Method:  "tools/list",
	})
	if resp == nil || resp.Error != nil {
		t.Fatalf("unexpected response: %+v", resp)
	}

	raw, _ := json.Marshal(resp.Result)
	var list ToolsListResult
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}

	names := map[string]bool{}
	for _, tool := range list.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"get_context_bundle", "get_session_state", "get_anomalies", "get_build_status", "get_external_changes", "get_project_state", "get_peer_sessions"} {
		if !names[want] {
			t.Errorf("tools/list missing %q (have %v)", want, names)
		}
	}
}

func TestToolsCallSurfacesMissingBundler(t *testing.T) {
	params, _ := json.Marshal(CallToolParams{Name: "get_context_bundle"})
	resp := roundTrip(t, newTestServer(), Request{
		JSONRPC: "2.0",
		ID:      3,
		Method:  "tools/call",
		Params:  params,
	})
	if resp == nil || resp.Error != nil {
		t.Fatalf("unexpected response: %+v", resp)
	}

	raw, _ := json.Marshal(resp.Result)
	var result CallToolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode tools/call: %v", err)
	}
	// With Bundler unset the tool must surface IsError=true rather than panic.
	if !result.IsError {
		t.Errorf("expected IsError=true when bundler missing, got %+v", result)
	}
	if len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, "bundler") {
		t.Errorf("expected message mentioning bundler, got %+v", result.Content)
	}
}

func TestUnknownToolReturnsToolError(t *testing.T) {
	params, _ := json.Marshal(CallToolParams{Name: "does_not_exist"})
	resp := roundTrip(t, newTestServer(), Request{
		JSONRPC: "2.0",
		ID:      4,
		Method:  "tools/call",
		Params:  params,
	})
	if resp == nil {
		t.Fatal("expected response, got nil")
	}
	// MCP convention: unknown tool returns IsError=true in the result, not a JSON-RPC error.
	raw, _ := json.Marshal(resp.Result)
	var result CallToolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true for unknown tool, got %+v", result)
	}
}

func TestUnknownMethodReturnsJSONRPCError(t *testing.T) {
	resp := roundTrip(t, newTestServer(), Request{
		JSONRPC: "2.0",
		ID:      5,
		Method:  "no/such/method",
	})
	if resp == nil || resp.Error == nil {
		t.Fatalf("expected JSON-RPC error, got %+v", resp)
	}
	if resp.Error.Code != -32601 {
		t.Errorf("error code = %d, want -32601", resp.Error.Code)
	}
}

func TestNotificationsHaveNoResponse(t *testing.T) {
	srv := newTestServer()
	body, _ := json.Marshal(Request{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
		// ID intentionally omitted — notifications carry no id.
	})
	out := &bytes.Buffer{}
	srv.SetIO(bytes.NewReader(append(body, '\n')), out)
	if err := srv.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("notification produced output: %q", out.String())
	}
}

func TestMalformedJSONReturnsParseError(t *testing.T) {
	srv := newTestServer()
	out := &bytes.Buffer{}
	srv.SetIO(strings.NewReader("not json at all\n"), out)
	if err := srv.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var resp Response
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &resp); err != nil {
		t.Fatalf("unmarshal: %v (raw=%q)", err, out.String())
	}
	if resp.Error == nil || resp.Error.Code != -32700 {
		t.Errorf("expected parse error -32700, got %+v", resp.Error)
	}
}
