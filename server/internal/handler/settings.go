package handler

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/tma1-ai/tma1/server/internal/config"
)

// validTTL matches GreptimeDB TTL values — same pattern as greptimedb.validTTL.
var validTTL = regexp.MustCompile(`^\d+[smhdwMy]$`)

// queryConcurrency hard bounds. 1 prevents deadlock; 32 prevents pathological
// fan-out that itself can OOM GreptimeDB.
const (
	queryConcurrencyMin     = 1
	queryConcurrencyMax     = 32
	queryConcurrencyDefault = 4
)

func clampQueryConcurrency(n int) int {
	if n < queryConcurrencyMin {
		return queryConcurrencyDefault
	}
	if n > queryConcurrencyMax {
		return queryConcurrencyMax
	}
	return n
}

type settingsResponse struct {
	LLMAPIKeySet     bool     `json:"llm_api_key_set"`
	LLMAPIKeyHint    string   `json:"llm_api_key_hint"`
	LLMProvider      string   `json:"llm_provider"`
	LLMModel         string   `json:"llm_model"`
	LogLevel         string   `json:"log_level"`
	DataTTL          string   `json:"data_ttl"`
	QueryConcurrency int      `json:"query_concurrency"`
	EnvOverrides     []string `json:"env_overrides"`
}

type settingsRequest struct {
	LLMAPIKey        string `json:"llm_api_key"`
	LLMProvider      string `json:"llm_provider"`
	LLMModel         string `json:"llm_model"`
	LogLevel         string `json:"log_level"`
	DataTTL          string `json:"data_ttl"`
	QueryConcurrency int    `json:"query_concurrency"`
}

func redactKey(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return key[:1] + "***"
	}
	return key[:4] + "***" + key[len(key)-4:]
}

func (s *Server) handleGetSettings(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	llm := s.llmConfig
	dataTTL := s.dataTTL
	queryConcurrency := s.queryConcurrency
	s.mu.RUnlock()

	resp := settingsResponse{
		LLMAPIKeySet:     llm.APIKey != "",
		LLMAPIKeyHint:    redactKey(llm.APIKey),
		LLMProvider:      llm.Provider,
		LLMModel:         llm.Model,
		LogLevel:         s.logLevel(),
		DataTTL:          dataTTL,
		QueryConcurrency: queryConcurrency,
		EnvOverrides:     config.EnvOverrides(),
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	var req settingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	// Validate provider.
	if req.LLMProvider != "" && req.LLMProvider != "anthropic" && req.LLMProvider != "openai" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "provider must be 'anthropic' or 'openai'"})
		return
	}

	// Validate log level.
	validLevels := map[string]bool{"debug": true, "info": true, "warn": true, "error": true, "": true}
	if !validLevels[strings.ToLower(req.LogLevel)] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "log_level must be debug, info, warn, or error"})
		return
	}

	// Validate data TTL format — same rules as greptimedb.SetDatabaseTTL.
	if req.DataTTL != "" && req.DataTTL != "forever" && !validTTL.MatchString(req.DataTTL) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "data_ttl must be <number><unit> (s/m/h/d/w/M/y) or 'forever'"})
		return
	}

	// Validate query concurrency. 0 means "unset" — fall through to existing value.
	if req.QueryConcurrency != 0 && (req.QueryConcurrency < queryConcurrencyMin || req.QueryConcurrency > queryConcurrencyMax) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("query_concurrency must be between %d and %d", queryConcurrencyMin, queryConcurrencyMax),
		})
		return
	}

	// Read current file settings to preserve fields not sent.
	current := config.LoadSettings(s.dataDir)

	// Merge: start from persisted values, only overwrite non-locked, non-empty fields.
	// Env-locked fields are silently ignored (runtime value comes from env, not file).
	settings := current
	if !s.isEnvLocked("llm_api_key") {
		if req.LLMAPIKey == "__clear__" {
			settings.LLMAPIKey = ""
		} else if req.LLMAPIKey != "" {
			settings.LLMAPIKey = req.LLMAPIKey
		}
	}
	if !s.isEnvLocked("llm_provider") && req.LLMProvider != "" {
		settings.LLMProvider = req.LLMProvider
	}
	if !s.isEnvLocked("llm_model") {
		settings.LLMModel = req.LLMModel // empty = use default, always accept
	}
	if !s.isEnvLocked("log_level") && req.LogLevel != "" {
		settings.LogLevel = req.LogLevel
	}
	if !s.isEnvLocked("data_ttl") && req.DataTTL != "" {
		settings.DataTTL = req.DataTTL
	}
	if !s.isEnvLocked("query_concurrency") && req.QueryConcurrency != 0 {
		settings.QueryConcurrency = req.QueryConcurrency
	}

	// Save to file.
	if err := config.SaveSettings(s.dataDir, settings); err != nil {
		s.logger.Error("failed to save settings", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save: " + err.Error()})
		return
	}

	// Hot-reload LLM config (only if not locked by env var).
	newLLM := s.getLLMConfig()
	if !s.isEnvLocked("llm_api_key") {
		newLLM.APIKey = settings.LLMAPIKey
	}
	if !s.isEnvLocked("llm_provider") && settings.LLMProvider != "" {
		newLLM.Provider = settings.LLMProvider
	}
	if !s.isEnvLocked("llm_model") {
		newLLM.Model = settings.LLMModel
	}
	s.setLLMConfig(newLLM)

	// Hot-reload log level.
	if !s.isEnvLocked("log_level") && settings.LogLevel != "" {
		if s.logLevelVar != nil {
			s.logLevelVar.Set(parseLogLevel(settings.LogLevel))
		}
	}

	// Update stored data TTL (used for display, actual TTL applied on next startup).
	if !s.isEnvLocked("data_ttl") && settings.DataTTL != "" {
		s.mu.Lock()
		s.dataTTL = settings.DataTTL
		s.mu.Unlock()
	}

	// Hot-reload query concurrency. The save response (handleGetSettings below)
	// returns the updated value, and the frontend's saveSettings() applies it to
	// the in-memory limiter. Page reload also picks up the new value via initViews().
	if !s.isEnvLocked("query_concurrency") && settings.QueryConcurrency > 0 {
		s.mu.Lock()
		s.queryConcurrency = clampQueryConcurrency(settings.QueryConcurrency)
		s.mu.Unlock()
	}

	s.logger.Info("settings saved", "llm_available", newLLM.Available(), "provider", newLLM.Provider)

	// Return updated settings.
	s.handleGetSettings(w, nil)
}

func (s *Server) isEnvLocked(key string) bool {
	for _, k := range config.EnvOverrides() {
		if k == key {
			return true
		}
	}
	return false
}

func (s *Server) getLLMConfig() LLMConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.llmConfig
}

func (s *Server) setLLMConfig(cfg LLMConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.llmConfig = cfg
}

func (s *Server) logLevel() string {
	if s.logLevelVar == nil {
		return "info"
	}
	switch s.logLevelVar.Level() {
	case slog.LevelDebug:
		return "debug"
	case slog.LevelWarn:
		return "warn"
	case slog.LevelError:
		return "error"
	default:
		return "info"
	}
}

func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
