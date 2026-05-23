package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
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

func TestSessionStateToolSchemaWiresVerbose(t *testing.T) {
	// Plan §Phase 0.1 promises `verbose=true` → raw action list. Earlier
	// the field was in the schema but documented as "Ignored in Phase
	// 0.1", which kept agents from using it. Lock the wiring so neither
	// the field nor the description regress.
	def := SessionStateTool{}.Definition()
	props := def.InputSchema.Properties
	for _, want := range []string{"session_id", "verbose", "action_limit"} {
		if _, ok := props[want]; !ok {
			t.Errorf("schema missing property %q (have %v)", want, props)
		}
	}
	if strings.Contains(strings.ToLower(props["verbose"].Description), "ignored") ||
		strings.Contains(strings.ToLower(props["verbose"].Description), "reserved") {
		t.Errorf("verbose description still implies it's a stub: %q", props["verbose"].Description)
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

// TestNullIDRequestStillResponds guards Codex's correction: JSON-RPC 2.0
// allows `"id": null` as a discouraged-but-legal request id. Treating it
// as a notification (no response) would hang any client that sent one;
// the server must reply, echoing id:null.
func TestNullIDRequestStillResponds(t *testing.T) {
	srv := newTestServer()
	out := &bytes.Buffer{}
	srv.SetIO(strings.NewReader(`{"jsonrpc":"2.0","id":null,"method":"ping"}`+"\n"), out)
	if err := srv.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Len() == 0 {
		t.Fatal("expected response for id:null request, got nothing")
	}
	var raw map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &raw); err != nil {
		t.Fatalf("unmarshal response: %v (raw=%q)", err, out.String())
	}
	// The "id" field must be present in the response and equal to null.
	if _, has := raw["id"]; !has {
		t.Errorf("response missing id field: %s", out.String())
	}
	if raw["id"] != nil {
		t.Errorf("expected id:null in response, got %v", raw["id"])
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

// slowTool is a test-only ToolHandler that blocks until the test
// closes its release channel, used to verify the server's
// concurrency contract: a slow tool MUST NOT block other replies.
type slowTool struct {
	name    string
	release chan struct{}
}

func (t *slowTool) Definition() Tool {
	return Tool{
		Name:        t.name,
		Description: "test slow tool",
		InputSchema: InputSchema{Type: "object"},
	}
}

func (t *slowTool) Call(ctx context.Context, _ map[string]any) (CallToolResult, error) {
	select {
	case <-t.release:
	case <-ctx.Done():
	}
	return CallToolResult{Content: []ContentBlock{{Type: "text", Text: t.name + " done"}}}, nil
}

// TestServerConcurrentToolsDoNotBlockEachOther pins down the fix for
// the "Codex 卡住" report: previously a single stuck tools/call would
// wedge the whole stdio loop, blocking every subsequent reply. After
// the fix each call runs in its own goroutine, so a slow call still
// pending must not delay a fast one's response.
func TestServerConcurrentToolsDoNotBlockEachOther(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	slow := &slowTool{name: "slow", release: make(chan struct{})}
	fast := &slowTool{name: "fast", release: make(chan struct{})}
	srv := NewServer(logger, slow, fast)

	// Pre-release fast so its Call returns immediately; slow waits.
	close(fast.release)

	// Pipes simulate the stdio protocol — we drive stdin from one
	// goroutine and read responses concurrently from stdout.
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	srv.SetIO(inR, outW)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var runErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		runErr = srv.Run(ctx)
	}()

	// Send slow first, fast second.
	write := func(req Request) {
		body, _ := json.Marshal(req)
		_, _ = inW.Write(append(body, '\n'))
	}
	write(Request{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: mustParams(`{"name":"slow","arguments":{}}`)})
	write(Request{JSONRPC: "2.0", ID: 2, Method: "tools/call", Params: mustParams(`{"name":"fast","arguments":{}}`)})

	// Read until we see ID=2 land. If concurrency is broken, this
	// times out because slow is still blocked.
	reader := bufio.NewReader(outR)
	deadline := time.After(2 * time.Second)
	seenFast := false
	for !seenFast {
		select {
		case <-deadline:
			t.Fatalf("fast reply did not arrive while slow was blocked — handler still serial?")
		default:
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read out: %v", err)
		}
		var resp Response
		if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &resp); err != nil {
			continue
		}
		if idFloat, ok := resp.ID.(float64); ok && int(idFloat) == 2 {
			seenFast = true
		}
	}

	// Now unblock slow so the server can return.
	close(slow.release)

	// Drain the slow response, then close stdin so Run unwinds.
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatalf("read slow reply: %v", err)
	}
	_ = inW.Close()
	_ = outW.Close()
	<-done
	if runErr != nil && runErr != io.EOF && runErr != context.Canceled {
		t.Logf("Run returned: %v", runErr)
	}
}

func mustParams(s string) json.RawMessage {
	return json.RawMessage(s)
}
