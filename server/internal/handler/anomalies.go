package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// handleAnomaliesQuery returns the latest anomalies across all sessions in
// the project for the dashboard's Anomalies tab. Optional ?session_id=
// narrows to a single session.
//
// Response shape:
//
//	{ "anomalies": [ { kind, severity, evidence, suggestion, related_files,
//	                   session_id, project, ts } ], "count": N }
func (s *Server) handleAnomaliesQuery(w http.ResponseWriter, r *http.Request) {
	if s.bundler == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"anomalies": []any{},
			"note":      "bundler not configured",
		})
		return
	}

	sessionID := r.URL.Query().Get("session_id")
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}

	ctx := r.Context()
	out := s.collectAnomalies(ctx, sessionID, limit)
	writeJSON(w, http.StatusOK, map[string]any{
		"anomalies": out,
		"count":     len(out),
	})
}

// collectAnomalies returns anomalies that were actually emitted to
// agents over the last 24h, read from tma1_anomaly_emits.
//
// The dashboard MUST NOT call Detector.Detect here: Detect mutates the
// suppression history and writes emit-log rows. With the 10s polling
// cadence in anomalies.js, every poll would (a) consume the rule's
// 10-minute suppression window before the agent's UserPromptSubmit
// hook could see it, and (b) inflate the emit log with rows that no
// agent ever saw -- corrupting precision / follow-rate / 5-per-day
// budget gates.
//
// Reading the emit log instead means the dashboard shows the ground
// truth of "what the agent was told", which is also what the
// validation gates compute against. Strictly side-effect free.
func (s *Server) collectAnomalies(ctx context.Context, sessionID string, totalLimit int) []map[string]any {
	if s.greptimeHTTPPort <= 0 {
		return nil
	}
	if totalLimit <= 0 {
		totalLimit = 50
	}

	var where strings.Builder
	where.WriteString("ts > now() - INTERVAL '24 hours'")
	if sessionID != "" {
		fmt.Fprintf(&where, " AND session_id = '%s'", escapeSQLString(sessionID))
	}
	sql := fmt.Sprintf(
		`SELECT CAST(ts AS BIGINT) AS ts_ms, session_id, kind, severity,
		        "channel", evidence, suggestion, related_files,
		        CAST(first_emitted_at AS BIGINT) AS first_ms
		 FROM tma1_anomaly_emits
		 WHERE %s
		 ORDER BY ts DESC LIMIT %d`,
		where.String(), totalLimit,
	)
	body, err := s.querySQL(ctx, sql)
	if err != nil {
		s.logger.Debug("anomalies: read emit log failed", "err", err)
		return nil
	}
	var resp struct {
		Output []struct {
			Records struct {
				Rows [][]any `json:"rows"`
			} `json:"records"`
		} `json:"output"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || len(resp.Output) == 0 {
		return nil
	}

	out := make([]map[string]any, 0, len(resp.Output[0].Records.Rows))
	for _, r := range resp.Output[0].Records.Rows {
		if len(r) < 8 {
			continue
		}
		// Prefer first_emitted_at as the user-facing timestamp -- it's
		// the moment the rule first fired for the agent, not the most
		// recent re-detection. Falls back to ts when the column is null
		// (rows written before first_emitted_at was added).
		tsMs := int64OrZero(r[0])
		if firstMs := int64OrZero(r[8]); firstMs > 0 {
			tsMs = firstMs
		}
		var files []string
		if raw := stringOrEmpty(r[7]); raw != "" {
			_ = json.Unmarshal([]byte(raw), &files)
		}
		out = append(out, map[string]any{
			"kind":          stringOrEmpty(r[2]),
			"severity":      stringOrEmpty(r[3]),
			"channel":       stringOrEmpty(r[4]),
			"evidence":      stringOrEmpty(r[5]),
			"suggestion":    stringOrEmpty(r[6]),
			"related_files": files,
			"session_id":    stringOrEmpty(r[1]),
			"ts":            time.UnixMilli(tsMs).Format(time.RFC3339),
		})
	}
	return out
}

// handleAnomaliesBudget aggregates tma1_anomaly_emits into per-day,
// per-kind counts. Drives Phase 1.7 gate 2 (≤ 5 emits / kind / day).
//
// Query params:
//   - days: lookback window in days (default 7, max 30)
//   - budget: per-day budget threshold (default 5); rows above are flagged
//
// Response:
//
//	{
//	  "budget": 5,
//	  "days":   7,
//	  "rows":   [{date, kind, count, over_budget}],
//	  "totals_by_kind": {kind: total}
//	}
func (s *Server) handleAnomaliesBudget(w http.ResponseWriter, r *http.Request) {
	days := 7
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 30 {
			days = n
		}
	}
	budget := 5
	if v := r.URL.Query().Get("budget"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			budget = n
		}
	}
	if s.greptimeHTTPPort <= 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"budget": budget, "days": days, "rows": []any{}, "totals_by_kind": map[string]any{},
			"note": "GreptimeDB not configured",
		})
		return
	}

	sql := fmt.Sprintf(
		`SELECT date_trunc('day', ts) AS day, kind, COUNT(*) AS n
		 FROM tma1_anomaly_emits
		 WHERE ts > now() - INTERVAL '%d days'
		 GROUP BY day, kind
		 ORDER BY day DESC, n DESC`,
		days,
	)
	body, err := s.querySQL(r.Context(), sql)
	if err != nil {
		s.logger.Debug("anomalies budget: query failed", "err", err)
		writeJSON(w, http.StatusOK, map[string]any{
			"budget": budget, "days": days, "rows": []any{}, "totals_by_kind": map[string]any{},
			"note": "query failed (table may be empty on first run)",
		})
		return
	}
	var resp struct {
		Output []struct {
			Records struct {
				Rows [][]any `json:"rows"`
			} `json:"records"`
		} `json:"output"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"budget": budget, "days": days, "rows": []any{}, "totals_by_kind": map[string]any{},
			"note": "parse failed",
		})
		return
	}

	rows := []map[string]any{}
	totals := map[string]int{}
	if len(resp.Output) > 0 {
		for _, raw := range resp.Output[0].Records.Rows {
			if len(raw) < 3 {
				continue
			}
			day := stringOrEmpty(raw[0])
			kind := stringOrEmpty(raw[1])
			count := intOrZero(raw[2])
			rows = append(rows, map[string]any{
				"date":        day,
				"kind":        kind,
				"count":       count,
				"over_budget": count > budget,
			})
			totals[kind] += count
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"budget":         budget,
		"days":           days,
		"rows":           rows,
		"totals_by_kind": totals,
	})
}

// handleAnomaliesFollowRate computes per-kind action follow-rate.
//
// "Followed" = within the next `window` PreToolUse events in the same
// session, the agent ran a Read tool against a file listed in the
// anomaly's related_files. This is the canonical action for the
// related-file rules (stale_file_view, build_broken_after_my_edit,
// human_modified_during_session). Rules without related_files are
// returned with status="no-signal" so Dennis can see the denominator
// without misinterpreting a zero rate.
//
// Query params:
//   - days:   lookback (default 7, max 30)
//   - window: tool-call window per emit (default 5, max 50)
//
// Response:
//
//	{
//	  "days": 7, "window": 5,
//	  "by_kind": {kind: {emits, followed, rate, status}}
//	}
func (s *Server) handleAnomaliesFollowRate(w http.ResponseWriter, r *http.Request) {
	days := 7
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 30 {
			days = n
		}
	}
	window := 5
	if v := r.URL.Query().Get("window"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 50 {
			window = n
		}
	}
	if s.greptimeHTTPPort <= 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"days": days, "window": window, "by_kind": map[string]any{},
			"note": "GreptimeDB not configured",
		})
		return
	}

	emits, err := s.fetchEmits(r.Context(), days)
	if err != nil || len(emits) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"days": days, "window": window, "by_kind": map[string]any{},
			"note": "no emits in window",
		})
		return
	}
	sessions := uniqueSessions(emits)
	calls, err := s.fetchToolCallsForSessions(r.Context(), sessions, days+1)
	if err != nil {
		s.logger.Debug("follow-rate: fetch tool calls failed", "err", err)
	}

	type bucket struct{ emits, followed int }
	by := map[string]*bucket{}
	hasFiles := map[string]bool{}

	for _, e := range emits {
		b := by[e.Kind]
		if b == nil {
			b = &bucket{}
			by[e.Kind] = b
		}
		b.emits++
		if len(e.RelatedFiles) > 0 {
			hasFiles[e.Kind] = true
			if followedReRead(e, calls[e.SessionID], window) {
				b.followed++
			}
		}
	}

	out := map[string]any{}
	for kind, b := range by {
		entry := map[string]any{"emits": b.emits, "followed": b.followed}
		if !hasFiles[kind] {
			entry["status"] = "no-signal"
			entry["rate"] = 0.0
		} else {
			entry["status"] = "ok"
			if b.emits > 0 {
				entry["rate"] = float64(b.followed) / float64(b.emits)
			} else {
				entry["rate"] = 0.0
			}
		}
		out[kind] = entry
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"days": days, "window": window, "by_kind": out,
	})
}

// emitRow is one row from tma1_anomaly_emits.
type emitRow struct {
	TsMs         int64
	SessionID    string
	Kind         string
	RelatedFiles []string
}

// toolCallRow is one (session, ts, tool_name, file_path) tuple from
// tma1_hook_events, sorted by ts ASC within a session.
type toolCallRow struct {
	TsMs     int64
	ToolName string
	FilePath string
}

func (s *Server) fetchEmits(ctx context.Context, days int) ([]emitRow, error) {
	sql := fmt.Sprintf(
		`SELECT CAST(ts AS BIGINT) AS ts_ms, session_id, kind, related_files
		 FROM tma1_anomaly_emits
		 WHERE ts > now() - INTERVAL '%d days'
		 ORDER BY ts ASC`,
		days,
	)
	body, err := s.querySQL(ctx, sql)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Output []struct {
			Records struct {
				Rows [][]any `json:"rows"`
			} `json:"records"`
		} `json:"output"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || len(resp.Output) == 0 {
		return nil, err
	}
	out := make([]emitRow, 0, len(resp.Output[0].Records.Rows))
	for _, r := range resp.Output[0].Records.Rows {
		if len(r) < 4 {
			continue
		}
		e := emitRow{
			TsMs:      int64OrZero(r[0]),
			SessionID: stringOrEmpty(r[1]),
			Kind:      stringOrEmpty(r[2]),
		}
		if rawFiles := stringOrEmpty(r[3]); rawFiles != "" {
			_ = json.Unmarshal([]byte(rawFiles), &e.RelatedFiles)
		}
		out = append(out, e)
	}
	return out, nil
}

func (s *Server) fetchToolCallsForSessions(ctx context.Context, sessions []string, days int) (map[string][]toolCallRow, error) {
	if len(sessions) == 0 {
		return map[string][]toolCallRow{}, nil
	}
	quoted := make([]string, 0, len(sessions))
	for _, sid := range sessions {
		quoted = append(quoted, "'"+strings.ReplaceAll(sid, "'", "''")+"'")
	}
	sql := fmt.Sprintf(
		`SELECT session_id, CAST(ts AS BIGINT) AS ts_ms, tool_name,
		        COALESCE(tool_file_path,
		                 regexp_match(tool_input, '"file_path":"([^"]+)"')[1]) AS fp
		 FROM tma1_hook_events
		 WHERE session_id IN (%s)
		   AND event_type = 'PreToolUse'
		   AND ts > now() - INTERVAL '%d days'
		 ORDER BY session_id, ts ASC`,
		strings.Join(quoted, ","), days,
	)
	body, err := s.querySQL(ctx, sql)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Output []struct {
			Records struct {
				Rows [][]any `json:"rows"`
			} `json:"records"`
		} `json:"output"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || len(resp.Output) == 0 {
		return map[string][]toolCallRow{}, nil
	}
	out := map[string][]toolCallRow{}
	for _, r := range resp.Output[0].Records.Rows {
		if len(r) < 4 {
			continue
		}
		sid := stringOrEmpty(r[0])
		out[sid] = append(out[sid], toolCallRow{
			TsMs:     int64OrZero(r[1]),
			ToolName: stringOrEmpty(r[2]),
			FilePath: stringOrEmpty(r[3]),
		})
	}
	return out, nil
}

func uniqueSessions(emits []emitRow) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(emits))
	for _, e := range emits {
		if !seen[e.SessionID] && e.SessionID != "" {
			seen[e.SessionID] = true
			out = append(out, e.SessionID)
		}
	}
	return out
}

// followedReRead returns true when within the next `window` PreToolUse
// events in the same session the agent ran a Read on one of the
// emit's related files. The events slice MUST be sorted by ts ASC.
func followedReRead(e emitRow, events []toolCallRow, window int) bool {
	if len(e.RelatedFiles) == 0 || len(events) == 0 {
		return false
	}
	files := map[string]bool{}
	for _, f := range e.RelatedFiles {
		files[f] = true
	}
	n := 0
	for _, c := range events {
		if c.TsMs <= e.TsMs {
			continue
		}
		n++
		if n > window {
			break
		}
		if c.ToolName == "Read" && files[c.FilePath] {
			return true
		}
	}
	return false
}

func int64OrZero(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	}
	return 0
}

func stringOrEmpty(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func intOrZero(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int64:
		return int(n)
	case int:
		return n
	}
	return 0
}

// querySQL is an internal helper that POSTs SQL to GreptimeDB and returns
// the response body. Used by anomaly aggregation; the public /api/query
// endpoint goes through a separate path.
func (s *Server) querySQL(ctx context.Context, sql string) ([]byte, error) {
	target := fmt.Sprintf("http://localhost:%d/v1/sql", s.greptimeHTTPPort)
	form := url.Values{}
	form.Set("sql", sql)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
	}
	if err := greptimeResponseError(body); err != nil {
		return nil, err
	}
	return body, nil
}

func greptimeResponseError(body []byte) error {
	var r struct {
		Code  int    `json:"code"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}
	if r.Code != 0 || r.Error != "" {
		return fmt.Errorf("greptimedb error %d: %s", r.Code, r.Error)
	}
	return nil
}
