package build

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Hard limits to keep individual rows reasonable. The full output is still
// captured per-batch (~50 lines, ~5-10 KB), but a single error message
// shouldn't blow past these caps even if the source line is enormous.
const (
	maxMessageLen   = 16 * 1024 // 16 KB
	maxCommandLen   = 512
	maxFilePathLen  = 512
	maxStreamLen    = 16
	maxEventTypeLen = 32
	maxSeverityLen  = 16
	maxHostLen      = 256
	maxTagLen       = 128
)

// GreptimeStore writes Events into tma1_build_events via the GreptimeDB
// HTTP SQL API.
type GreptimeStore struct {
	httpPort int
	http     *http.Client
}

// NewGreptimeStore returns a store targeting localhost:<httpPort>.
func NewGreptimeStore(httpPort int) *GreptimeStore {
	return &GreptimeStore{
		httpPort: httpPort,
		http:     &http.Client{Timeout: 5 * time.Second},
	}
}

// Write inserts evt into tma1_build_events. Returns an error only on
// transport failure; non-2xx GreptimeDB responses also surface as errors.
//
// The function is synchronous; callers that need fan-and-forget behaviour
// should call it from a goroutine.
func (s *GreptimeStore) Write(ctx context.Context, evt Event) error {
	if s.httpPort <= 0 {
		return fmt.Errorf("build store: invalid greptime port %d", s.httpPort)
	}
	if evt.Timestamp.IsZero() {
		evt.Timestamp = time.Now().UTC()
	}

	// `stream`, `message`, `tag` are GreptimeDB reserved keywords — must be
	// quoted everywhere they appear (DDL + DML).
	sql := fmt.Sprintf(
		"INSERT INTO tma1_build_events "+
			"(ts, project, command, event_type, severity, \"stream\", \"message\", "+
			"file_path, line_no, exit_code, duration_ms, host, \"tag\") "+
			"VALUES (%d, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s)",
		evt.Timestamp.UnixMilli(),
		quoteString(evt.Project, maxCommandLen),
		quoteString(evt.Command, maxCommandLen),
		quoteString(evt.EventType, maxEventTypeLen),
		quoteString(evt.Severity, maxSeverityLen),
		quoteString(evt.Stream, maxStreamLen),
		quoteString(evt.Message, maxMessageLen),
		quoteString(evt.FilePath, maxFilePathLen),
		quoteIntOrNull(evt.LineNo),
		quoteExitCode(evt.ExitCode),
		quoteInt64OrNull(evt.DurationMs),
		quoteString(evt.Host, maxHostLen),
		quoteString(evt.Tag, maxTagLen),
	)

	target := fmt.Sprintf("http://127.0.0.1:%d/v1/sql", s.httpPort)
	form := url.Values{}
	form.Set("sql", sql)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("build store: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("build store: POST: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("build store: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// quoteString returns 'NULL' for empty input, otherwise a SQL string literal
// with embedded quotes escaped and the value truncated to maxLen bytes.
func quoteString(v string, maxLen int) string {
	if v == "" {
		return "NULL"
	}
	if len(v) > maxLen {
		v = v[:maxLen]
	}
	return "'" + strings.ReplaceAll(v, "'", "''") + "'"
}

func quoteIntOrNull(v int) string {
	if v == 0 {
		return "NULL"
	}
	return fmt.Sprintf("%d", v)
}

func quoteInt64OrNull(v int64) string {
	if v == 0 {
		return "NULL"
	}
	return fmt.Sprintf("%d", v)
}

func quoteExitCode(p *int) string {
	if p == nil {
		return "NULL"
	}
	return fmt.Sprintf("%d", *p)
}
