package git

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// AttributionWindow is the ±window we look at hook events to classify a
// file change as agent-caused. Plan §Phase 1.2 uses ±5s — short enough
// that human typing isn't mistakenly attributed to the agent, generous
// enough that fsnotify's delivery lag still falls inside it.
const AttributionWindow = 5 * time.Second

// HookAttributor classifies file changes by querying tma1_hook_events for a
// recent Edit/Write/MultiEdit on the same file_path. It's the production
// implementation of Attributor used by the git sensor.
//
// It's deliberately a self-contained struct (no perception import) so the
// sensor package doesn't depend on perception, which would create a cycle
// when perception starts reading external_changes back.
type HookAttributor struct {
	httpPort int
	http     *http.Client
}

// NewHookAttributor returns an Attributor querying GreptimeDB on localhost:<httpPort>.
func NewHookAttributor(httpPort int) *HookAttributor {
	return &HookAttributor{
		httpPort: httpPort,
		http:     &http.Client{Timeout: 1500 * time.Millisecond},
	}
}

// Classify returns "agent" if any hook event within ±AttributionWindow of
// `when` plausibly touched filePath. The signal sources are, in order:
//
//  1. Edit / Write / MultiEdit whose tool_input.file_path matches exactly.
//  2. Bash whose tool_input.command contains the file's basename.
//     This catches mkdir/rm/touch/cp/mv/git checkout/etc., which the
//     git sensor would otherwise mis-attribute to a human because the
//     hook event has no file_path field.
//
// Otherwise "human" (safe default — we over-credit humans rather than
// wrongly absolve the agent). On query error returns "unknown".
func (a *HookAttributor) Classify(ctx context.Context, filePath string, when time.Time) string {
	if a.httpPort <= 0 || filePath == "" {
		return AttributionUnknown
	}
	low := when.Add(-AttributionWindow).UnixMilli()
	high := when.Add(AttributionWindow).UnixMilli()

	// Pass 1: explicit Edit/Write/MultiEdit on this exact file_path.
	// Prefer the ingest-side tool_file_path column; fall back to regex
	// lift for rows written before Phase 1.4.
	editSQL := fmt.Sprintf(
		`SELECT COUNT(*) FROM tma1_hook_events
		 WHERE event_type = 'PreToolUse'
		   AND tool_name IN ('Edit','Write','MultiEdit')
		   AND ts BETWEEN %d AND %d
		   AND COALESCE(tool_file_path,
		                regexp_match(tool_input, '"file_path":"([^"]+)"')[1]) = '%s'`,
		low, high, escapeSQLLiteral(filePath),
	)
	if count, err := a.queryCount(ctx, editSQL); err != nil {
		return AttributionUnknown
	} else if count > 0 {
		return AttributionAgent
	}

	// Pass 2: Bash command whose tool_input mentions the file's basename.
	// LIKE is loose (substring match anywhere) which is precisely what we
	// want — e.g. `git checkout server/internal/foo.go` puts the path
	// inside a longer string, and `mkdir cmd/render-since` puts only the
	// final segment.
	base := basenameOf(filePath)
	if base == "" {
		return AttributionHuman
	}
	bashSQL := fmt.Sprintf(
		`SELECT COUNT(*) FROM tma1_hook_events
		 WHERE event_type = 'PreToolUse'
		   AND tool_name = 'Bash'
		   AND ts BETWEEN %d AND %d
		   AND tool_input LIKE '%%%s%%'`,
		low, high, escapeSQLLiteral(base),
	)
	if count, err := a.queryCount(ctx, bashSQL); err != nil {
		return AttributionUnknown
	} else if count > 0 {
		return AttributionAgent
	}

	return AttributionHuman
}

// basenameOf returns the last path segment of p ("/a/b/c.go" → "c.go").
// Empty input → empty output.
func basenameOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}

func (a *HookAttributor) queryCount(ctx context.Context, sql string) (int, error) {
	target := fmt.Sprintf("http://127.0.0.1:%d/v1/sql", a.httpPort)
	form := url.Values{}
	form.Set("sql", sql)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := a.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
	}

	// Parse just enough of the GreptimeDB response to grab the first cell.
	var r struct {
		Output []struct {
			Records struct {
				Rows [][]any `json:"rows"`
			} `json:"records"`
		} `json:"output"`
		Code  int    `json:"code"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return 0, err
	}
	if r.Code != 0 || r.Error != "" {
		return 0, fmt.Errorf("greptime: %s", r.Error)
	}
	if len(r.Output) == 0 || len(r.Output[0].Records.Rows) == 0 {
		return 0, nil
	}
	switch v := r.Output[0].Records.Rows[0][0].(type) {
	case float64:
		return int(v), nil
	case int64:
		return int(v), nil
	case int:
		return v, nil
	}
	return 0, nil
}

func escapeSQLLiteral(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}
