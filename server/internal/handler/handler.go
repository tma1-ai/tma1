// Package handler provides the HTTP handlers for tma1-server.
package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/tma1-ai/tma1/server/internal/transcript"
)

// Server holds shared state for all HTTP handlers.
type Server struct {
	greptimeHTTPPort  int
	tma1Port          string
	logger            *slog.Logger
	webFS             http.FileSystem
	httpClient        *http.Client
	otlpClient        *http.Client
	transcriptWatcher *transcript.Watcher
	hookBroadcast     *HookBroadcaster
}

// New creates a new Server.
func New(greptimeHTTPPort int, tma1Port string, webFS http.FileSystem, logger *slog.Logger, tw *transcript.Watcher, bc *HookBroadcaster) *Server {
	if bc == nil {
		bc = NewHookBroadcaster()
	}
	return &Server{
		greptimeHTTPPort:  greptimeHTTPPort,
		tma1Port:          tma1Port,
		logger:            logger,
		webFS:             webFS,
		httpClient:        &http.Client{Timeout: 30 * time.Second},
		otlpClient:        &http.Client{Timeout: 60 * time.Second},
		transcriptWatcher: tw,
		hookBroadcast:     bc,
	}
}

// Router returns the chi router with all routes registered.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	// Health / status
	r.Get("/health", s.handleHealth)
	r.Get("/status", s.handleStatus)

	// SQL proxy — browser JS calls this to query GreptimeDB.
	// Accepts: POST /api/query with body: {"sql": "SELECT ..."}
	r.Post("/api/query", s.handleQuery)

	// Prometheus API proxy — browser JS calls this for PromQL queries.
	r.HandleFunc("/api/prom/*", s.handlePromProxy)

	// Hook events from Claude Code / Codex.
	r.Post("/api/hooks", s.handleHooks)
	r.Get("/api/hooks/stream", s.handleHookStream)

	// OTLP proxy — agents send OTel data here; tma1-server injects
	// the x-greptime-pipeline-name header for trace requests.
	r.HandleFunc("/v1/otlp/*", s.handleOTLPProxy)
	// Also support direct OTLP signal paths used by some SDKs/tools.
	r.HandleFunc("/v1/traces", s.handleOTLPDirectProxy)
	r.HandleFunc("/v1/metrics", s.handleOTLPDirectProxy)
	r.HandleFunc("/v1/logs", s.handleOTLPDirectProxy)

	// Dashboard UI (embedded static files)
	r.Handle("/*", http.FileServer(s.webFS))

	return r
}

// handleHealth returns a simple liveness check.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleStatus checks whether GreptimeDB is reachable.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	healthURL := fmt.Sprintf("http://localhost:%d/health", s.greptimeHTTPPort)
	resp, err := s.httpClient.Get(healthURL) //nolint:gosec
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status":     "degraded",
			"greptimedb": "unreachable",
			"error":      err.Error(),
		})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status":     "degraded",
			"greptimedb": fmt.Sprintf("HTTP %d", resp.StatusCode),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status":     "ok",
		"greptimedb": "running",
		"dashboard":  "http://localhost:" + s.tma1Port,
	})
}

// handleQuery proxies a SQL query to GreptimeDB's HTTP SQL API and returns the result.
// Request body: {"sql": "SELECT ..."}
// Response: raw GreptimeDB JSON response.
func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SQL string `json:"sql"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if strings.TrimSpace(req.SQL) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sql is required"})
		return
	}

	sqlURL := fmt.Sprintf("http://localhost:%d/v1/sql", s.greptimeHTTPPort)
	form := url.Values{}
	form.Set("sql", req.SQL)

	resp, err := s.httpClient.Post(sqlURL, "application/x-www-form-urlencoded", strings.NewReader(form.Encode())) //nolint:gosec
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// handlePromProxy proxies requests to GreptimeDB's Prometheus-compatible HTTP API.
// /api/prom/query_range → http://localhost:{port}/v1/prometheus/api/v1/query_range
func (s *Server) handlePromProxy(w http.ResponseWriter, r *http.Request) {
	subPath := chi.URLParam(r, "*")
	target := fmt.Sprintf("http://localhost:%d/v1/prometheus/api/v1/%s", s.greptimeHTTPPort, subPath)
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}

	proxyReq, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		proxyReq.Header.Set("Content-Type", ct)
	}

	resp, err := s.httpClient.Do(proxyReq)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// handleOTLPProxy proxies OTLP requests to GreptimeDB.
// For trace requests (path contains "v1/traces"), it injects the
// x-greptime-pipeline-name header required by GreptimeDB.
func (s *Server) handleOTLPProxy(w http.ResponseWriter, r *http.Request) {
	subPath := chi.URLParam(r, "*")
	s.proxyOTLP(w, r, subPath)
}

// handleOTLPDirectProxy supports direct signal routes: /v1/traces, /v1/metrics, /v1/logs.
func (s *Server) handleOTLPDirectProxy(w http.ResponseWriter, r *http.Request) {
	subPath := strings.TrimPrefix(r.URL.Path, "/")
	s.proxyOTLP(w, r, subPath)
}

func (s *Server) proxyOTLP(w http.ResponseWriter, r *http.Request, subPath string) {
	target := fmt.Sprintf("http://localhost:%d/v1/otlp/%s", s.greptimeHTTPPort, subPath)
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}

	proxyReq, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Copy all original headers.
	for key, vals := range r.Header {
		for _, v := range vals {
			proxyReq.Header.Add(key, v)
		}
	}

	// Inject pipeline header for trace requests.
	if strings.Contains(subPath, "v1/traces") {
		proxyReq.Header.Set("x-greptime-pipeline-name", "greptime_trace_v1")
	}

	resp, err := s.otlpClient.Do(proxyReq)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	defer resp.Body.Close()

	// Copy response headers.
	for key, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(key, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
