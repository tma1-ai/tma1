// Package perception assembles context for AI coding agents: session state,
// build status, anomalies, etc. It exposes a Bundler for the MCP and hook
// layers to share.
//
// Phase 0.1 scope: session state only. Build / external / anomaly sensors
// arrive in later phases.
package perception

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

// Client is a minimal SQL client against the local GreptimeDB HTTP API.
// It exists so perception code doesn't depend on the handler package.
type Client struct {
	httpPort int
	http     *http.Client
}

// NewClient creates a Client targeting localhost:<httpPort>.
func NewClient(httpPort int) *Client {
	return NewClientWithTimeout(httpPort, 3*time.Second)
}

// NewClientWithTimeout is NewClient with an explicit deadline, for
// callers whose queries are allowed to be slower than a sensor lookup.
func NewClientWithTimeout(httpPort int, timeout time.Duration) *Client {
	return &Client{
		httpPort: httpPort,
		http:     &http.Client{Timeout: timeout},
	}
}

// queryResp matches the GreptimeDB HTTP /v1/sql response.
type queryResp struct {
	Output []struct {
		Records struct {
			Schema struct {
				ColumnSchemas []struct {
					Name string `json:"name"`
				} `json:"column_schemas"`
			} `json:"schema"`
			Rows [][]any `json:"rows"`
		} `json:"records"`
	} `json:"output"`
	Code  int    `json:"code"`
	Error string `json:"error"`
}

// Query runs a SQL statement and returns column names + rows.
// On Greptime server errors it returns an error containing the server message.
func (c *Client) Query(ctx context.Context, sql string) ([]string, [][]any, error) {
	target := fmt.Sprintf("http://127.0.0.1:%d/v1/sql", c.httpPort)
	form := url.Values{}
	form.Set("sql", sql)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("perception query: %w", err)
	}
	defer resp.Body.Close()

	// Bound the read so a misbehaving GreptimeDB / proxy can't make us
	// allocate unbounded memory. 8 MB is well above the largest
	// perception query response we've seen in dogfood (a 5-session
	// peer fetch with full message bodies, ~300 KB).
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, nil, fmt.Errorf("perception read response: %w", err)
	}
	// Surface transport-level failures before trying to unmarshal --
	// JSON parse errors on an HTML/empty error body are confusing.
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("perception query: HTTP %d: %s", resp.StatusCode, snippet(body))
	}

	var r queryResp
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, nil, fmt.Errorf("perception parse response: %w (body=%q)", err, snippet(body))
	}
	if r.Code != 0 || r.Error != "" {
		return nil, nil, fmt.Errorf("greptimedb error: %s", r.Error)
	}
	if len(r.Output) == 0 {
		return nil, nil, nil
	}

	cols := make([]string, 0, len(r.Output[0].Records.Schema.ColumnSchemas))
	for _, c := range r.Output[0].Records.Schema.ColumnSchemas {
		cols = append(cols, c.Name)
	}
	return cols, r.Output[0].Records.Rows, nil
}

func snippet(b []byte) string {
	if len(b) > 200 {
		return string(b[:200]) + "..."
	}
	return string(b)
}
