package git

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Hard caps to keep individual rows bounded; same philosophy as the build
// sensor's store.
const (
	maxFilePathLen   = 512
	maxGitMessageLen = 512
	maxProjectLen    = 256
	maxAttribLen     = 16
	maxChangeTypeLen = 32
	maxGitSHALen     = 64
	maxHostLen       = 256
)

// GreptimeStore writes Changes into tma1_external_changes via the
// GreptimeDB HTTP SQL API.
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

// Write inserts c into tma1_external_changes. Synchronous; callers that
// can't tolerate the latency should goroutine it.
func (s *GreptimeStore) Write(ctx context.Context, c Change) error {
	if s.httpPort <= 0 {
		return fmt.Errorf("git store: invalid greptime port %d", s.httpPort)
	}
	if c.Timestamp.IsZero() {
		c.Timestamp = time.Now().UTC()
	}

	sql := fmt.Sprintf(
		"INSERT INTO tma1_external_changes "+
			"(ts, project, change_type, file_path, git_sha, git_message, attribution, host) "+
			"VALUES (%d, %s, %s, %s, %s, %s, %s, %s)",
		c.Timestamp.UnixMilli(),
		quote(c.Project, maxProjectLen),
		quote(c.ChangeType, maxChangeTypeLen),
		quote(c.FilePath, maxFilePathLen),
		quote(c.GitSHA, maxGitSHALen),
		quote(c.GitMessage, maxGitMessageLen),
		quote(c.Attribution, maxAttribLen),
		quote(c.Host, maxHostLen),
	)

	target := fmt.Sprintf("http://127.0.0.1:%d/v1/sql", s.httpPort)
	form := url.Values{}
	form.Set("sql", sql)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("git store: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("git store: POST: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("git store: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// quote returns 'NULL' for empty input, otherwise a SQL string literal with
// embedded quotes escaped and value truncated to maxLen bytes.
func quote(v string, maxLen int) string {
	if v == "" {
		return "NULL"
	}
	if len(v) > maxLen {
		v = v[:maxLen]
	}
	return "'" + strings.ReplaceAll(v, "'", "''") + "'"
}
