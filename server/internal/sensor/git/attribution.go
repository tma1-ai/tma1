package git

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/tma1-ai/tma1/server/internal/sqlutil"
)

// AttributionWindow bounds exact file-path correlation around an fsnotify event.
const AttributionWindow = 5 * time.Second

// activeToolLookback bounds the scan for a tool invocation that was still
// running when fsnotify delivered the change. Unmatched changes remain unknown,
// so an invocation older than this cannot turn into a false human attribution.
const activeToolLookback = 30 * time.Minute

// activeToolCacheTTL coalesces the burst of per-path fsnotify events produced
// by one tool without keeping a stale attribution across normal user actions.
const activeToolCacheTTL = 250 * time.Millisecond

// HookAttributor classifies file changes by correlating them with
// tma1_hook_events. It's the production implementation of Attributor used by
// the git sensor.
//
// It's deliberately a self-contained struct (no perception import) so the
// sensor package doesn't depend on perception, which would create a cycle
// when perception starts reading external_changes back.
type HookAttributor struct {
	httpPort int
	http     *http.Client
	mu       sync.Mutex
	active   map[string]activeToolCacheEntry
}

type activeToolCacheEntry struct {
	checkedAt time.Time
	active    bool
}

// NewHookAttributor returns an Attributor querying GreptimeDB on localhost:<httpPort>.
func NewHookAttributor(httpPort int) *HookAttributor {
	return &HookAttributor{
		httpPort: httpPort,
		http:     &http.Client{Timeout: 1500 * time.Millisecond},
		active:   make(map[string]activeToolCacheEntry),
	}
}

// Classify returns "agent" when hook data ties a file change to agent activity.
// The signal sources are, in order:
//
//  1. A recent PreToolUse whose extracted file path matches exactly.
//  2. A tool running in the same project when fsnotify delivered the change.
//
// Hook telemetry cannot prove that an unmatched change came from a human.
// Missing evidence and query failures therefore return "unknown".
func (a *HookAttributor) Classify(ctx context.Context, projectRoot, filePath string, when time.Time) string {
	if a.httpPort <= 0 || projectRoot == "" || filePath == "" {
		return AttributionUnknown
	}
	root := strings.TrimRight(projectRoot, `/\`)
	if root == "" {
		return AttributionUnknown
	}
	if active, ok := a.cachedActiveTool(root); ok && active {
		return AttributionAgent
	}
	low := when.Add(-AttributionWindow).UnixMilli()
	high := when.Add(AttributionWindow).UnixMilli()

	// Prefer the ingest-side tool_file_path column; fall back to extraction
	// for rows written before the derived column was introduced.
	editSQL := fmt.Sprintf(
		`SELECT COUNT(*) FROM tma1_hook_events
		 WHERE event_type = 'PreToolUse'
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

	// Indirect writes such as builds, git commands, screenshot tools, and
	// atomic-save temporary files often do not name every changed path. Pair
	// PreToolUse/PostToolUse by tool_use_id and only consider invocations whose
	// cwd is the watched project or one of its subdirectories.
	activeLow := when.Add(-activeToolLookback).UnixMilli()
	if active, ok := a.cachedActiveTool(root); ok {
		if active {
			return AttributionAgent
		}
		return AttributionUnknown
	}
	activeSQL := fmt.Sprintf(
		`SELECT COUNT(*) FROM tma1_hook_events p
		 LEFT JOIN tma1_hook_events q
		   ON q.agent_source = p.agent_source
		  AND q.session_id = p.session_id
		  AND q.tool_use_id = p.tool_use_id
		  AND q.event_type IN ('PostToolUse','PostToolUseFailure')
		  AND q.ts >= p.ts
		 WHERE p.event_type = 'PreToolUse'
		   AND p.tool_use_id IS NOT NULL AND p.tool_use_id != ''
		   AND p.ts BETWEEN %d AND %d
		   AND (p.cwd = '%s' OR p.cwd LIKE '%s/%%' OR p.cwd LIKE '%s\\%%')
		   AND q.tool_use_id IS NULL`,
		activeLow, high,
		escapeSQLLiteral(root),
		escapeSQLLikeLiteral(root),
		escapeSQLLikeLiteral(root),
	)
	count, err := a.queryCount(ctx, activeSQL)
	if err != nil {
		return AttributionUnknown
	}
	a.storeActiveTool(root, count > 0)
	if count > 0 {
		return AttributionAgent
	}

	return AttributionUnknown
}

func (a *HookAttributor) cachedActiveTool(root string) (bool, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	entry, ok := a.active[root]
	if !ok || time.Since(entry.checkedAt) >= activeToolCacheTTL {
		return false, false
	}
	return entry.active, true
}

func (a *HookAttributor) storeActiveTool(root string, active bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.active[root] = activeToolCacheEntry{checkedAt: time.Now(), active: active}
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

// escapeSQLLiteral / escapeSQLLikeLiteral are thin aliases over the
// shared sqlutil package, kept local so existing call sites read the
// same. sqlutil owns the implementation.
func escapeSQLLiteral(s string) string     { return sqlutil.Escape(s) }
func escapeSQLLikeLiteral(s string) string { return sqlutil.EscapeLike(s) }
