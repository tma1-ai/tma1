package project

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

const (
	maxJSONField   = 4 * 1024
	maxProjectLen  = 256
	maxRootLen     = 512
	maxLanguageLen = 64
	maxBuildLen    = 64
	maxTestLen     = 64
)

// EventWriter persists a State row.
type EventWriter interface {
	Write(ctx context.Context, s State) error
}

// GreptimeStore writes State rows into tma1_project_state.
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

// Write inserts one State row. Synchronous.
func (s *GreptimeStore) Write(ctx context.Context, st State) error {
	if s.httpPort <= 0 {
		return fmt.Errorf("project store: invalid greptime port %d", s.httpPort)
	}
	if st.IndexedAt.IsZero() {
		st.IndexedAt = time.Now().UTC()
	}

	frameworks, _ := json.Marshal(st.Frameworks)
	keyFiles, _ := json.Marshal(st.KeyFiles)
	moduleSummary, _ := json.Marshal(map[string]any{
		"top_level_dirs": st.TopLevelDirs,
	})

	// `root`, `language` are GreptimeDB reserved keywords — must be quoted.
	sql := fmt.Sprintf(
		"INSERT INTO tma1_project_state "+
			"(ts, project, \"root\", \"language\", build_system, test_framework, "+
			"frameworks, key_files, module_summary) "+
			"VALUES (%d, %s, %s, %s, %s, %s, %s, %s, %s)",
		st.IndexedAt.UnixMilli(),
		quote(st.Project, maxProjectLen),
		quote(st.Root, maxRootLen),
		quote(st.Language, maxLanguageLen),
		quote(st.BuildSystem, maxBuildLen),
		quote(st.TestFramework, maxTestLen),
		quote(string(frameworks), maxJSONField),
		quote(string(keyFiles), maxJSONField),
		quote(string(moduleSummary), maxJSONField),
	)

	target := fmt.Sprintf("http://127.0.0.1:%d/v1/sql", s.httpPort)
	form := url.Values{}
	form.Set("sql", sql)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("project store: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("project store: POST: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("project store: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// quote returns 'NULL' for empty input, otherwise a SQL string literal with
// embedded quotes escaped and the value truncated to maxLen bytes.
func quote(v string, maxLen int) string {
	if v == "" {
		return "NULL"
	}
	if len(v) > maxLen {
		v = v[:maxLen]
	}
	return "'" + strings.ReplaceAll(v, "'", "''") + "'"
}
