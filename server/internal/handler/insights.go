package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

type insightRequest struct {
	RubricVersion string `json:"rubric_version"`
	Summary       string `json:"summary"`
	Patterns      any    `json:"patterns"`
	TopTip        string `json:"top_tip"`
	Model         string `json:"model"`
	SampleSize    int    `json:"sample_size"`
	TotalPrompts  int    `json:"total_prompts"`
	AvgScore      *int   `json:"avg_score"`
	SamplePrompts any    `json:"sample_prompts"`
	TimeRange     string `json:"time_range"`
}

// handleSaveInsight persists an AI Insights result to tma1_prompt_insights.
func (s *Server) handleSaveInsight(w http.ResponseWriter, r *http.Request) {
	var req insightRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	now := time.Now()
	insightID := fmt.Sprintf("%s-%03d%04d", now.Format("20060102-150405"), now.UnixMilli()%1000, rand.Intn(10000)) //nolint:gosec
	nowMs := now.UnixMilli()

	patternsJSON, _ := json.Marshal(req.Patterns)
	sampleJSON, _ := json.Marshal(req.SamplePrompts)

	sqlURL := fmt.Sprintf("http://localhost:%d/v1/sql", s.greptimeHTTPPort)
	avgScore := "NULL"
	if req.AvgScore != nil {
		avgScore = strconv.Itoa(*req.AvgScore)
	}
	stmt := fmt.Sprintf(
		"INSERT INTO tma1_prompt_insights "+
			"(ts, insight_id, summary, patterns, top_tip, model, sample_size, total_prompts, avg_score, sample_prompts, time_range, rubric_version) "+
			"VALUES (%d, '%s', '%s', '%s', '%s', '%s', %d, %d, %s, '%s', '%s', '%s')",
		nowMs,
		escapeSQLString(insightID),
		escapeSQLString(req.Summary),
		escapeSQLString(string(patternsJSON)),
		escapeSQLString(req.TopTip),
		escapeSQLString(req.Model),
		req.SampleSize,
		req.TotalPrompts,
		avgScore,
		escapeSQLString(string(sampleJSON)),
		escapeSQLString(req.TimeRange),
		escapeSQLString(req.RubricVersion),
	)

	form := url.Values{}
	form.Set("sql", stmt)
	resp, err := s.httpClient.Post(sqlURL, "application/x-www-form-urlencoded", strings.NewReader(form.Encode())) //nolint:gosec
	if err != nil {
		s.logger.Error("save insight failed", "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to save"})
		return
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if readErr != nil || resp.StatusCode != http.StatusOK || greptimeResponseError(body) != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to save"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"insight_id": insightID})
}

// handleListInsights returns recent insights (summary only, no patterns/sample_prompts).
func (s *Server) handleListInsights(w http.ResponseWriter, r *http.Request) {
	limitStr := r.URL.Query().Get("limit")
	limit := 10
	if n, err := strconv.Atoi(limitStr); err == nil && n > 0 && n <= 50 {
		limit = n
	}

	stmt := fmt.Sprintf(
		"SELECT insight_id, ts, summary, avg_score, sample_size, total_prompts, time_range, rubric_version "+
			"FROM tma1_prompt_insights ORDER BY ts DESC LIMIT %d", limit)

	s.proxySQL(w, stmt)
}

// handleGetInsight returns a single insight with full patterns + sample_prompts.
func (s *Server) handleGetInsight(w http.ResponseWriter, r *http.Request) {
	insightID := chi.URLParam(r, "id")
	if insightID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "insight_id required"})
		return
	}

	stmt := fmt.Sprintf(
		"SELECT insight_id, ts, summary, patterns, top_tip, model, sample_size, total_prompts, avg_score, sample_prompts, time_range, rubric_version "+
			"FROM tma1_prompt_insights WHERE insight_id = '%s' ORDER BY ts DESC LIMIT 1",
		escapeSQLString(insightID))

	s.proxySQL(w, stmt)
}

// proxySQL executes a SQL statement against GreptimeDB and pipes the response to w.
func (s *Server) proxySQL(w http.ResponseWriter, stmt string) {
	sqlURL := fmt.Sprintf("http://localhost:%d/v1/sql", s.greptimeHTTPPort)
	form := url.Values{}
	form.Set("sql", stmt)
	resp, err := s.httpClient.Post(sqlURL, "application/x-www-form-urlencoded", strings.NewReader(form.Encode())) //nolint:gosec
	if err != nil {
		s.logger.Error("insights query failed", "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "database query failed"})
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
