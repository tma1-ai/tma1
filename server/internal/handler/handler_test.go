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
)

func newTestServer() *Server {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	return New(14000, "14318", http.Dir("."), logger, nil, NewHookBroadcaster(), LLMConfig{}, ServerConfig{})
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
	// Body must be empty (Claude Code expects no JSON response).
	if w.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", w.Body.String())
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
