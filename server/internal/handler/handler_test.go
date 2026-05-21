package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tma1-ai/tma1/server/internal/perception"
)

// newTestServer returns a Server wired with greptimeHTTPPort=0 — the
// "no real DB" sentinel. handleHooks skips the async INSERT, and the
// bundler is nil so injection returns empty unless a test wires its own.
// This keeps test data out of Dennis's live GreptimeDB.
func newTestServer() *Server {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	return New(0, "14318", http.Dir("."), logger, nil, NewHookBroadcaster(), LLMConfig{}, ServerConfig{})
}

func TestHealthEndpoint(t *testing.T) {
	srv := newTestServer()
	r := srv.Router()

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /health: got status %d, want %d", w.Code, http.StatusOK)
	}

	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status = %q, want %q", body["status"], "ok")
	}
}

func TestQueryEndpointRequiresSQL(t *testing.T) {
	srv := newTestServer()
	r := srv.Router()

	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantError  string
	}{
		{
			name:       "empty body",
			body:       `{}`,
			wantStatus: http.StatusBadRequest,
			wantError:  "sql is required",
		},
		{
			name:       "whitespace sql",
			body:       `{"sql": "   "}`,
			wantStatus: http.StatusBadRequest,
			wantError:  "sql is required",
		},
		{
			name:       "invalid json",
			body:       `not json`,
			wantStatus: http.StatusBadRequest,
			wantError:  "invalid JSON",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/query",
				strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, tt.wantStatus)
			}

			var body map[string]string
			if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body["error"] != tt.wantError {
				t.Errorf("error = %q, want %q", body["error"], tt.wantError)
			}
		})
	}
}

func TestQueryEndpointBadGateway(t *testing.T) {
	// Use a port that's not listening to get a connection error.
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	srv := New(19999, "14318", http.Dir("."), logger, nil, NewHookBroadcaster(), LLMConfig{}, ServerConfig{})
	r := srv.Router()

	req := httptest.NewRequest(http.MethodPost, "/api/query",
		strings.NewReader(`{"sql":"SELECT 1"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadGateway)
	}
}

func TestPromProxyGETPassesQueryString(t *testing.T) {
	// Fake GreptimeDB Prometheus API
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/prometheus/api/v1/label/__name__/values" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.RawQuery != "match[]=up" {
			t.Errorf("unexpected query: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"success","data":["metric_a"]}`)
	}))
	defer fake.Close()

	// Extract port from fake server URL
	port := strings.TrimPrefix(fake.URL, "http://127.0.0.1:")
	portNum := 0
	fmt.Sscanf(port, "%d", &portNum)

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	srv := New(portNum, "14318", http.Dir("."), logger, nil, NewHookBroadcaster(), LLMConfig{}, ServerConfig{})
	router := srv.Router()

	req := httptest.NewRequest(http.MethodGet, "/api/prom/label/__name__/values?match[]=up", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	body, _ := io.ReadAll(w.Body)
	if !strings.Contains(string(body), "metric_a") {
		t.Errorf("body = %s, want to contain metric_a", string(body))
	}
}

func TestPromProxyPOSTPassesBody(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/prometheus/api/v1/query_range" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(b), "query=up") {
			t.Errorf("body = %s, want to contain query=up", string(b))
		}
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type = %s", r.Header.Get("Content-Type"))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[]}}`)
	}))
	defer fake.Close()

	port := strings.TrimPrefix(fake.URL, "http://127.0.0.1:")
	portNum := 0
	fmt.Sscanf(port, "%d", &portNum)

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	srv := New(portNum, "14318", http.Dir("."), logger, nil, NewHookBroadcaster(), LLMConfig{}, ServerConfig{})
	router := srv.Router()

	req := httptest.NewRequest(http.MethodPost, "/api/prom/query_range",
		strings.NewReader("query=up&start=1000&end=2000&step=15"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestPromProxyPassesNon200Status(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		fmt.Fprint(w, `{"status":"error","errorType":"bad_data","error":"invalid query"}`)
	}))
	defer fake.Close()

	port := strings.TrimPrefix(fake.URL, "http://127.0.0.1:")
	portNum := 0
	fmt.Sscanf(port, "%d", &portNum)

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	srv := New(portNum, "14318", http.Dir("."), logger, nil, NewHookBroadcaster(), LLMConfig{}, ServerConfig{})
	router := srv.Router()

	req := httptest.NewRequest(http.MethodGet, "/api/prom/query?query=invalid{", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusUnprocessableEntity)
	}
}

func TestPromProxyBadGateway(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	srv := New(19999, "14318", http.Dir("."), logger, nil, NewHookBroadcaster(), LLMConfig{}, ServerConfig{})
	router := srv.Router()

	req := httptest.NewRequest(http.MethodGet, "/api/prom/query", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadGateway)
	}
}

func TestOTLPProxyTraces(t *testing.T) {
	// Fake GreptimeDB OTLP endpoint — verify pipeline header is injected for traces.
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/otlp/v1/traces" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("x-greptime-pipeline-name"); got != "greptime_trace_v1" {
			t.Errorf("x-greptime-pipeline-name = %q, want %q", got, "greptime_trace_v1")
		}
		if got := r.Header.Get("Content-Type"); got != "application/x-protobuf" {
			t.Errorf("Content-Type = %q, want %q", got, "application/x-protobuf")
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{}`)
	}))
	defer fake.Close()

	port := strings.TrimPrefix(fake.URL, "http://127.0.0.1:")
	portNum := 0
	fmt.Sscanf(port, "%d", &portNum)

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	srv := New(portNum, "14318", http.Dir("."), logger, nil, NewHookBroadcaster(), LLMConfig{}, ServerConfig{})
	router := srv.Router()

	req := httptest.NewRequest(http.MethodPost, "/v1/otlp/v1/traces", strings.NewReader("trace-payload"))
	req.Header.Set("Content-Type", "application/x-protobuf")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestOTLPProxyMetrics(t *testing.T) {
	// Verify metrics requests do NOT get the pipeline header.
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/otlp/v1/metrics" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("x-greptime-pipeline-name"); got != "" {
			t.Errorf("x-greptime-pipeline-name = %q, want empty (not injected for metrics)", got)
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{}`)
	}))
	defer fake.Close()

	port := strings.TrimPrefix(fake.URL, "http://127.0.0.1:")
	portNum := 0
	fmt.Sscanf(port, "%d", &portNum)

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	srv := New(portNum, "14318", http.Dir("."), logger, nil, NewHookBroadcaster(), LLMConfig{}, ServerConfig{})
	router := srv.Router()

	req := httptest.NewRequest(http.MethodPost, "/v1/otlp/v1/metrics", strings.NewReader("metrics-payload"))
	req.Header.Set("Content-Type", "application/x-protobuf")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestOTLPDirectProxyTraces(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/otlp/v1/traces" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("x-greptime-pipeline-name"); got != "greptime_trace_v1" {
			t.Errorf("x-greptime-pipeline-name = %q, want %q", got, "greptime_trace_v1")
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{}`)
	}))
	defer fake.Close()

	port := strings.TrimPrefix(fake.URL, "http://127.0.0.1:")
	portNum := 0
	fmt.Sscanf(port, "%d", &portNum)

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	srv := New(portNum, "14318", http.Dir("."), logger, nil, NewHookBroadcaster(), LLMConfig{}, ServerConfig{})
	router := srv.Router()

	req := httptest.NewRequest(http.MethodPost, "/v1/traces", strings.NewReader("trace-payload"))
	req.Header.Set("Content-Type", "application/x-protobuf")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestOTLPDirectProxyLogs(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/otlp/v1/logs" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("x-greptime-pipeline-name"); got != "" {
			t.Errorf("x-greptime-pipeline-name = %q, want empty (not injected for logs)", got)
		}
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{}`)
	}))
	defer fake.Close()

	port := strings.TrimPrefix(fake.URL, "http://127.0.0.1:")
	portNum := 0
	fmt.Sscanf(port, "%d", &portNum)

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	srv := New(portNum, "14318", http.Dir("."), logger, nil, NewHookBroadcaster(), LLMConfig{}, ServerConfig{})
	router := srv.Router()

	req := httptest.NewRequest(http.MethodPost, "/v1/logs", strings.NewReader("log-payload"))
	req.Header.Set("Content-Type", "application/x-protobuf")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusAccepted)
	}
}

func TestOTLPDirectProxyMetrics(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/otlp/v1/metrics" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("x-greptime-pipeline-name"); got != "" {
			t.Errorf("x-greptime-pipeline-name = %q, want empty (not injected for metrics)", got)
		}
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{}`)
	}))
	defer fake.Close()

	port := strings.TrimPrefix(fake.URL, "http://127.0.0.1:")
	portNum := 0
	fmt.Sscanf(port, "%d", &portNum)

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	srv := New(portNum, "14318", http.Dir("."), logger, nil, NewHookBroadcaster(), LLMConfig{}, ServerConfig{})
	router := srv.Router()

	req := httptest.NewRequest(http.MethodPost, "/v1/metrics", strings.NewReader("metrics-payload"))
	req.Header.Set("Content-Type", "application/x-protobuf")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusAccepted)
	}
}

func TestOTLPProxyPassthrough(t *testing.T) {
	// Verify request body and custom headers are forwarded.
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if string(b) != "test-body" {
			t.Errorf("body = %q, want %q", string(b), "test-body")
		}
		if got := r.Header.Get("X-Custom-Header"); got != "custom-value" {
			t.Errorf("X-Custom-Header = %q, want %q", got, "custom-value")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer fake.Close()

	port := strings.TrimPrefix(fake.URL, "http://127.0.0.1:")
	portNum := 0
	fmt.Sscanf(port, "%d", &portNum)

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	srv := New(portNum, "14318", http.Dir("."), logger, nil, NewHookBroadcaster(), LLMConfig{}, ServerConfig{})
	router := srv.Router()

	req := httptest.NewRequest(http.MethodPost, "/v1/otlp/v1/logs", strings.NewReader("test-body"))
	req.Header.Set("X-Custom-Header", "custom-value")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusCreated)
	}
	body, _ := io.ReadAll(w.Body)
	if !strings.Contains(string(body), `"ok":true`) {
		t.Errorf("body = %s, want to contain ok:true", string(body))
	}
}

func TestOTLPProxyBadGateway(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	srv := New(19999, "14318", http.Dir("."), logger, nil, NewHookBroadcaster(), LLMConfig{}, ServerConfig{})
	router := srv.Router()

	req := httptest.NewRequest(http.MethodPost, "/v1/otlp/v1/traces", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadGateway)
	}
}

func TestStatusEndpointDegraded(t *testing.T) {
	// Use a port that's not listening.
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	srv := New(19999, "14318", http.Dir("."), logger, nil, NewHookBroadcaster(), LLMConfig{}, ServerConfig{})
	r := srv.Router()

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}

	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["status"] != "degraded" {
		t.Errorf("status = %q, want %q", body["status"], "degraded")
	}
}

func TestHooksEndpointValid(t *testing.T) {
	srv := newTestServer()
	r := srv.Router()

	payload := `{"session_id":"test-123","hook_event_name":"PreToolUse","tool_name":"Read"}`
	req := httptest.NewRequest(http.MethodPost, "/api/hooks", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	// PreToolUse has no Phase 0.1 injection handler so body must be empty.
	// (UserPromptSubmit / Stop have handlers — body is the hook script's stdout.)
	if w.Body.Len() != 0 {
		t.Errorf("body = %q, want empty for PreToolUse", w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
}

func TestHooksUserPromptSubmitGeneratesEmptyWhenNoData(t *testing.T) {
	// Bundler runs against a non-existent GreptimeDB → BuildBundle returns an
	// empty bundle → RenderSummary returns "". The endpoint must still 200.
	srv := newTestServer()
	r := srv.Router()

	payload := `{"session_id":"abc","hook_event_name":"UserPromptSubmit","cwd":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/hooks", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if w.Body.Len() != 0 {
		t.Errorf("expected empty body when no session data, got %q", w.Body.String())
	}
}

func TestHooksStopWithActiveFlagIsNoOp(t *testing.T) {
	// stop_hook_active=true must short-circuit before any block JSON to
	// prevent the work→stop→block→work loop.
	srv := newTestServer()
	r := srv.Router()

	payload := `{"session_id":"abc","hook_event_name":"Stop","stop_hook_active":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/hooks", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if w.Body.Len() != 0 {
		t.Errorf("expected empty body when stop_hook_active=true, got %q", w.Body.String())
	}
}

func TestFollowedReReadDetection(t *testing.T) {
	// Tests the pure merge logic in isolation — no DB required.
	emit := emitRow{
		TsMs:         1000,
		SessionID:    "s1",
		Kind:         "stale_file_view",
		RelatedFiles: []string{"src/auth.go"},
	}
	cases := []struct {
		name   string
		events []toolCallRow
		window int
		want   bool
	}{
		{
			name: "Read of related file within window — followed",
			events: []toolCallRow{
				{TsMs: 1100, ToolName: "Edit", FilePath: "src/other.go"},
				{TsMs: 1200, ToolName: "Read", FilePath: "src/auth.go"},
			},
			window: 5,
			want:   true,
		},
		{
			name: "Read of related file BEFORE emit — not counted",
			events: []toolCallRow{
				{TsMs: 500, ToolName: "Read", FilePath: "src/auth.go"},
			},
			window: 5,
			want:   false,
		},
		{
			name: "Read of unrelated file — not followed",
			events: []toolCallRow{
				{TsMs: 1100, ToolName: "Read", FilePath: "src/other.go"},
			},
			window: 5,
			want:   false,
		},
		{
			name: "Read of related file AFTER window — not followed",
			// First 3 events are not Read-of-related; 4th IS but is outside window=3
			events: []toolCallRow{
				{TsMs: 1100, ToolName: "Edit", FilePath: "x"},
				{TsMs: 1200, ToolName: "Edit", FilePath: "y"},
				{TsMs: 1300, ToolName: "Edit", FilePath: "z"},
				{TsMs: 1400, ToolName: "Read", FilePath: "src/auth.go"},
			},
			window: 3,
			want:   false,
		},
		{
			name:   "no follow-up events",
			events: nil,
			window: 5,
			want:   false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := followedReRead(emit, c.events, c.window)
			if got != c.want {
				t.Errorf("followedReRead = %v, want %v", got, c.want)
			}
		})
	}
}

func TestAnomaliesFollowRateEndpointGracefulWithoutDB(t *testing.T) {
	srv := newTestServer()
	r := srv.Router()
	req := httptest.NewRequest(http.MethodGet, "/api/anomalies/follow-rate", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, ok := body["by_kind"].(map[string]any); !ok {
		t.Errorf("by_kind missing: %+v", body)
	}
}

func TestAnomaliesBudgetEndpointGracefulWithoutDB(t *testing.T) {
	// greptimeHTTPPort=0 sentinel → endpoint must still 200 with a
	// well-formed payload + a "note" instead of falling over.
	srv := newTestServer()
	r := srv.Router()

	req := httptest.NewRequest(http.MethodGet, "/api/anomalies/budget?days=3&budget=5", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	if body["budget"] != float64(5) {
		t.Errorf("budget echoed wrong: %+v", body["budget"])
	}
	if body["days"] != float64(3) {
		t.Errorf("days echoed wrong: %+v", body["days"])
	}
	if _, ok := body["rows"].([]any); !ok {
		t.Errorf("rows missing or wrong shape: %+v", body["rows"])
	}
	if _, ok := body["totals_by_kind"].(map[string]any); !ok {
		t.Errorf("totals_by_kind missing or wrong shape: %+v", body["totals_by_kind"])
	}
}

func TestIsToolFailureDetection(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want bool
	}{
		{"nil", nil, false},
		{"string-pass-through", "hello", false},
		{"empty-map", map[string]any{"content": "ok"}, false},
		{"isError true (camel)", map[string]any{"isError": true}, true},
		{"isError false", map[string]any{"isError": false}, false},
		{"is_error true (snake)", map[string]any{"is_error": true}, true},
		{"success false", map[string]any{"success": false}, true},
		{"success true", map[string]any{"success": true}, false},
		{"interrupted", map[string]any{"interrupted": true}, true},
		{"error string", map[string]any{"error": "boom"}, true},
		{"error empty string", map[string]any{"error": ""}, false},
		{"error whitespace", map[string]any{"error": "   "}, false},
		{"bash non-zero code", map[string]any{"code": float64(1)}, true},
		{"bash zero code", map[string]any{"code": float64(0)}, false},
		{"exitCode non-zero", map[string]any{"exitCode": float64(127)}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isToolFailure(c.in); got != c.want {
				t.Errorf("isToolFailure(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestHooksPostToolUseSilentByDefault(t *testing.T) {
	// Phase 0.2: PostToolUse dispatch exists but returns empty unless the
	// debug env var is set — the anomaly engine lands in Phase 0.3.
	srv := newTestServer()
	r := srv.Router()

	payload := `{"session_id":"abc","hook_event_name":"PostToolUse","tool_name":"Edit"}`
	req := httptest.NewRequest(http.MethodPost, "/api/hooks", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Errorf("PostToolUse should be silent by default, got %q", w.Body.String())
	}
}

func TestHooksPostToolUseDebugMarker(t *testing.T) {
	// With TMA1_DEBUG_POSTTOOLUSE=1 the server emits a short marker so the
	// user can confirm in CC's transcript JSONL that hook stdout is being
	// appended to tool_result.
	t.Setenv("TMA1_DEBUG_POSTTOOLUSE", "1")

	srv := newTestServer()
	r := srv.Router()

	payload := `{"session_id":"abc12345xyz","hook_event_name":"PostToolUse","tool_name":"Bash"}`
	req := httptest.NewRequest(http.MethodPost, "/api/hooks", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "PostToolUse") || !strings.Contains(body, "tool=Bash") {
		t.Errorf("debug marker missing key fields, got %q", body)
	}
	if strings.Contains(body, "abc12345xyz") {
		t.Errorf("session id should be abbreviated, got %q", body)
	}
	if !strings.Contains(body, "abc12345") {
		t.Errorf("expected abbreviated session prefix in marker, got %q", body)
	}
}

func TestHooksTelemetryCountsInvocations(t *testing.T) {
	srv := newTestServer()
	r := srv.Router()

	for _, payload := range []string{
		`{"session_id":"a","hook_event_name":"UserPromptSubmit"}`,
		`{"session_id":"a","hook_event_name":"PostToolUse","tool_name":"Read"}`,
		`{"session_id":"a","hook_event_name":"PostToolUse","tool_name":"Edit"}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/hooks", strings.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(httptest.NewRecorder(), req)
	}

	srv.hookTelemetry.mu.Lock()
	defer srv.hookTelemetry.mu.Unlock()
	if got := srv.hookTelemetry.calls["UserPromptSubmit"]; got != 1 {
		t.Errorf("UserPromptSubmit calls = %d, want 1", got)
	}
	if got := srv.hookTelemetry.calls["PostToolUse"]; got != 2 {
		t.Errorf("PostToolUse calls = %d, want 2", got)
	}
}

func TestSummarizeAnomaliesGroupsByKindAndLimitsFiles(t *testing.T) {
	// Five HIGH file_loop_edit anomalies — each for a different file — must
	// collapse into ONE bullet that names up to 3 files and reports the
	// rest as "+N more". Different kinds get separate bullets. Previously
	// this rendered as the suggestion text duplicated N times.
	anomalies := []perception.Anomaly{
		{Kind: "file_loop_edit", Severity: perception.SeverityHigh, Suggestion: "try another approach",
			RelatedFiles: []string{"a.go"}},
		{Kind: "file_loop_edit", Severity: perception.SeverityHigh, Suggestion: "try another approach",
			RelatedFiles: []string{"b.go"}},
		{Kind: "file_loop_edit", Severity: perception.SeverityHigh, Suggestion: "try another approach",
			RelatedFiles: []string{"c.go"}},
		{Kind: "file_loop_edit", Severity: perception.SeverityHigh, Suggestion: "try another approach",
			RelatedFiles: []string{"d.go"}},
		{Kind: "file_loop_edit", Severity: perception.SeverityHigh, Suggestion: "try another approach",
			RelatedFiles: []string{"e.go"}},
		{Kind: "repeated_failed_build", Severity: perception.SeverityHigh, Suggestion: "fix the root cause"},
	}
	got := summarizeAnomalies(anomalies)

	// Suggestion must appear ONCE per kind, not once per file.
	if strings.Count(got, "try another approach") != 1 {
		t.Errorf("expected 1 occurrence of file_loop_edit suggestion, got %d\n%s",
			strings.Count(got, "try another approach"), got)
	}
	// First 3 files listed; remaining 2 summarised as "+2 more".
	for _, want := range []string{"a.go", "b.go", "c.go", "+2 more"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in summary, got: %s", want, got)
		}
	}
	if strings.Contains(got, "d.go") || strings.Contains(got, "e.go") {
		t.Errorf("expected files past 3rd to be elided, got: %s", got)
	}
	// repeated_failed_build is a different kind — distinct bullet.
	if !strings.Contains(got, "repeated_failed_build: fix the root cause") {
		t.Errorf("expected second-kind bullet, got: %s", got)
	}
}

func TestHooksSessionStartSilentWhenNoData(t *testing.T) {
	// Bundler is nil (port=0 in test server), so the SessionStart dispatch
	// short-circuits and the hook script gets an empty body — the safe
	// default for a fresh session with no observed state yet.
	srv := newTestServer()
	r := srv.Router()

	payload := `{"session_id":"abc","hook_event_name":"SessionStart","cwd":"/tmp"}`
	req := httptest.NewRequest(http.MethodPost, "/api/hooks", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if w.Body.Len() != 0 {
		t.Errorf("SessionStart with no data should produce empty body, got %q", w.Body.String())
	}
}

func TestHooksInjectionDisabledByEnv(t *testing.T) {
	t.Setenv("TMA1_DISABLE_INJECTION", "1")

	srv := newTestServer()
	r := srv.Router()

	payload := `{"session_id":"abc","hook_event_name":"UserPromptSubmit"}`
	req := httptest.NewRequest(http.MethodPost, "/api/hooks", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Errorf("expected empty body when injection disabled, got %q", w.Body.String())
	}
}

func TestHooksEndpointInvalidJSON(t *testing.T) {
	srv := newTestServer()
	r := srv.Router()

	req := httptest.NewRequest(http.MethodPost, "/api/hooks", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Still returns 200 — never block Claude Code.
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestHooksEndpointMissingFields(t *testing.T) {
	srv := newTestServer()
	r := srv.Router()

	req := httptest.NewRequest(http.MethodPost, "/api/hooks",
		strings.NewReader(`{"session_id":"abc"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestHookStreamSSE(t *testing.T) {
	srv := newTestServer()
	r := srv.Router()

	// Use a cancelable context so the SSE goroutine terminates cleanly.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/api/hooks/stream", nil)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		r.ServeHTTP(w, req)
		close(done)
	}()

	// Give SSE handler time to subscribe.
	time.Sleep(50 * time.Millisecond)

	// Broadcast an event.
	srv.hookBroadcast.Broadcast([]byte(`{"session_id":"s1","hook_event_name":"PreToolUse"}`))
	time.Sleep(50 * time.Millisecond)

	// Stop the SSE handler.
	cancel()
	<-done

	if srv.hookBroadcast == nil {
		t.Fatal("hookBroadcast is nil")
	}
}

// --- Evaluate endpoint tests ---

func TestEvaluateCheckNoAPIKey(t *testing.T) {
	srv := newTestServer() // no LLM config
	r := srv.Router()

	req := httptest.NewRequest(http.MethodGet, "/api/evaluate", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var body map[string]any
	_ = json.NewDecoder(w.Body).Decode(&body)
	if body["available"] != false {
		t.Errorf("available = %v, want false", body["available"])
	}
}

func TestEvaluateCheckWithAPIKey(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	srv := New(14000, "14318", http.Dir("."), logger, nil, NewHookBroadcaster(),
		LLMConfig{APIKey: "sk-test", Provider: "anthropic"}, ServerConfig{})
	r := srv.Router()

	req := httptest.NewRequest(http.MethodGet, "/api/evaluate", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var body map[string]any
	_ = json.NewDecoder(w.Body).Decode(&body)
	if body["available"] != true {
		t.Errorf("available = %v, want true", body["available"])
	}
	if body["provider"] != "anthropic" {
		t.Errorf("provider = %v, want anthropic", body["provider"])
	}
}

func TestEvaluatePostNoAPIKey(t *testing.T) {
	srv := newTestServer()
	r := srv.Router()

	req := httptest.NewRequest(http.MethodPost, "/api/evaluate",
		strings.NewReader(`{"prompt":"test"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	var body map[string]string
	_ = json.NewDecoder(w.Body).Decode(&body)
	if !strings.Contains(body["error"], "not configured") {
		t.Errorf("error = %q, want 'not configured' substring", body["error"])
	}
}

func TestEvaluatePostEmptyPrompt(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	srv := New(14000, "14318", http.Dir("."), logger, nil, NewHookBroadcaster(),
		LLMConfig{APIKey: "sk-test", Provider: "anthropic"}, ServerConfig{})
	r := srv.Router()

	req := httptest.NewRequest(http.MethodPost, "/api/evaluate",
		strings.NewReader(`{"prompt":""}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestEvaluateMethodNotAllowed(t *testing.T) {
	srv := newTestServer()
	r := srv.Router()

	req := httptest.NewRequest(http.MethodPut, "/api/evaluate", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestEvaluateSummaryNoAPIKey(t *testing.T) {
	srv := newTestServer()
	r := srv.Router()

	req := httptest.NewRequest(http.MethodPost, "/api/evaluate/summary",
		strings.NewReader(`{"prompts":[{"content":"test","score":50}]}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

// --- Settings endpoint tests ---

func TestSettingsGetDefault(t *testing.T) {
	srv := newTestServer()
	r := srv.Router()

	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var body settingsResponse
	_ = json.NewDecoder(w.Body).Decode(&body)
	if body.LLMAPIKeySet {
		t.Error("llm_api_key_set should be false with no config")
	}
}

func TestSettingsGetWithKey(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	srv := New(14000, "14318", http.Dir("."), logger, nil, NewHookBroadcaster(),
		LLMConfig{APIKey: "sk-ant-api03-abcdef1234567890", Provider: "anthropic"}, ServerConfig{})
	r := srv.Router()

	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var body settingsResponse
	_ = json.NewDecoder(w.Body).Decode(&body)
	if !body.LLMAPIKeySet {
		t.Error("llm_api_key_set should be true")
	}
	if strings.Contains(body.LLMAPIKeyHint, "abcdef1234567890") {
		t.Error("hint should not contain full key")
	}
	if body.LLMAPIKeyHint == "" {
		t.Error("hint should not be empty")
	}
}

func TestSettingsSaveInvalidProvider(t *testing.T) {
	tmpDir := t.TempDir()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	srv := New(14000, "14318", http.Dir("."), logger, nil, NewHookBroadcaster(),
		LLMConfig{}, ServerConfig{DataDir: tmpDir})
	r := srv.Router()

	req := httptest.NewRequest(http.MethodPost, "/api/settings",
		strings.NewReader(`{"llm_provider":"bad"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestSettingsSaveInvalidLogLevel(t *testing.T) {
	tmpDir := t.TempDir()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	srv := New(14000, "14318", http.Dir("."), logger, nil, NewHookBroadcaster(),
		LLMConfig{}, ServerConfig{DataDir: tmpDir})
	r := srv.Router()

	req := httptest.NewRequest(http.MethodPost, "/api/settings",
		strings.NewReader(`{"log_level":"verbose"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestSettingsSaveInvalidTTL(t *testing.T) {
	tmpDir := t.TempDir()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	srv := New(14000, "14318", http.Dir("."), logger, nil, NewHookBroadcaster(),
		LLMConfig{}, ServerConfig{DataDir: tmpDir})
	r := srv.Router()

	tests := []struct {
		name string
		ttl  string
		want int
	}{
		{"invalid unit", `{"data_ttl":"2months"}`, http.StatusBadRequest},
		{"no unit", `{"data_ttl":"60"}`, http.StatusBadRequest},
		{"valid days", `{"data_ttl":"60d"}`, http.StatusOK},
		{"valid weeks", `{"data_ttl":"1w"}`, http.StatusOK},
		{"valid months", `{"data_ttl":"6M"}`, http.StatusOK},
		{"valid years", `{"data_ttl":"1y"}`, http.StatusOK},
		{"forever", `{"data_ttl":"forever"}`, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/settings",
				strings.NewReader(tt.ttl))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tt.want {
				body, _ := io.ReadAll(w.Body)
				t.Fatalf("ttl %s: status = %d, want %d, body: %s", tt.ttl, w.Code, tt.want, body)
			}
		})
	}
}

func TestSettingsSaveInvalidQueryConcurrency(t *testing.T) {
	tmpDir := t.TempDir()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	srv := New(14000, "14318", http.Dir("."), logger, nil, NewHookBroadcaster(),
		LLMConfig{}, ServerConfig{DataDir: tmpDir})
	r := srv.Router()

	tests := []struct {
		name string
		body string
		want int
	}{
		{"zero (unset)", `{"query_concurrency":0}`, http.StatusOK}, // 0 means "unset", not invalid
		{"negative", `{"query_concurrency":-1}`, http.StatusBadRequest},
		{"too large", `{"query_concurrency":33}`, http.StatusBadRequest},
		{"min", `{"query_concurrency":1}`, http.StatusOK},
		{"max", `{"query_concurrency":32}`, http.StatusOK},
		{"typical", `{"query_concurrency":4}`, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/settings",
				strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tt.want {
				body, _ := io.ReadAll(w.Body)
				t.Fatalf("body %s: status = %d, want %d, response: %s", tt.body, w.Code, tt.want, body)
			}
		})
	}
}

func TestSettingsQueryConcurrencyPersisted(t *testing.T) {
	tmpDir := t.TempDir()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	srv := New(14000, "14318", http.Dir("."), logger, nil, NewHookBroadcaster(),
		LLMConfig{}, ServerConfig{DataDir: tmpDir, QueryConcurrency: 4})
	r := srv.Router()

	// POST a new value.
	postReq := httptest.NewRequest(http.MethodPost, "/api/settings",
		strings.NewReader(`{"query_concurrency":7}`))
	postReq.Header.Set("Content-Type", "application/json")
	postW := httptest.NewRecorder()
	r.ServeHTTP(postW, postReq)
	if postW.Code != http.StatusOK {
		body, _ := io.ReadAll(postW.Body)
		t.Fatalf("save: status = %d, want 200, body: %s", postW.Code, body)
	}

	// POST response itself returns the updated value.
	var postResp map[string]any
	if err := json.Unmarshal(postW.Body.Bytes(), &postResp); err != nil {
		t.Fatalf("decode POST response: %v", err)
	}
	if got := postResp["query_concurrency"]; got != float64(7) {
		t.Fatalf("POST response query_concurrency = %v, want 7", got)
	}

	// Subsequent GET reflects the new value (in-memory hot-reload).
	getReq := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	getW := httptest.NewRecorder()
	r.ServeHTTP(getW, getReq)
	if getW.Code != http.StatusOK {
		t.Fatalf("get: status = %d, want 200", getW.Code)
	}
	var getResp map[string]any
	if err := json.Unmarshal(getW.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("decode GET response: %v", err)
	}
	if got := getResp["query_concurrency"]; got != float64(7) {
		t.Fatalf("GET query_concurrency = %v, want 7", got)
	}
}

func TestSettingsOriginProtection(t *testing.T) {
	tmpDir := t.TempDir()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	srv := New(14000, "14318", http.Dir("."), logger, nil, NewHookBroadcaster(),
		LLMConfig{}, ServerConfig{DataDir: tmpDir})
	r := srv.Router()

	tests := []struct {
		name   string
		origin string
		want   int
	}{
		{"no origin", "", http.StatusOK},
		{"localhost", "http://localhost:14318", http.StatusOK},
		{"127.0.0.1", "http://127.0.0.1:14318", http.StatusOK},
		{"evil site", "http://evil.com", http.StatusForbidden},
		{"wrong port", "http://localhost:9999", http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/settings",
				strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tt.want {
				body, _ := io.ReadAll(w.Body)
				t.Fatalf("origin %q: status = %d, want %d, body: %s", tt.origin, w.Code, tt.want, body)
			}
		})
	}
}

func TestRedactKey(t *testing.T) {
	tests := []struct {
		key  string
		want string
	}{
		{"", ""},
		{"short", "s***"},
		{"12345678", "1***"},
		{"sk-ant-api03-abcdefghijklmnop", "sk-a***mnop"},
	}
	for _, tt := range tests {
		got := redactKey(tt.key)
		if got != tt.want {
			t.Errorf("redactKey(%q) = %q, want %q", tt.key, got, tt.want)
		}
	}
}

func TestHookStreamSessionFilter(t *testing.T) {
	srv := newTestServer()

	// Subscribe manually to verify filter logic.
	ch := srv.hookBroadcast.Subscribe()
	defer srv.hookBroadcast.Unsubscribe(ch)

	srv.hookBroadcast.Broadcast([]byte(`{"session_id":"abc","hook_event_name":"PreToolUse"}`))
	select {
	case data := <-ch:
		if !strings.Contains(string(data), `"session_id":"abc"`) {
			t.Errorf("unexpected data: %s", string(data))
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for broadcast")
	}
}
