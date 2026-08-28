package perception

import (
	"context"
	"time"

	"github.com/tma1-ai/tma1/server/internal/sqlutil"
	"github.com/tma1-ai/tma1/server/internal/strutil"
)

// QueryResult is a plain column/row grid rather than GreptimeDB's
// nested JSON envelope.
type QueryResult struct {
	SQL         string   `json:"sql"`
	Columns     []string `json:"columns"`
	Rows        [][]any  `json:"rows"`
	RowCount    int      `json:"row_count"`
	Truncated   bool     `json:"truncated"`
	ExecutionMS int64    `json:"execution_ms"`
}

// ExecQuery runs one caller-supplied SELECT against GreptimeDB.
//
// The row cap is pushed into SQL rather than applied after transfer.
// Cells are truncated because one tma1_messages.content value can be
// tens of KB.
func (b *Bundler) ExecQuery(ctx context.Context, sql string, rowLimit, cellChars int) (*QueryResult, error) {
	stmt, err := sqlutil.ValidateSelect(sql)
	if err != nil {
		return nil, err
	}
	rowLimit = clampInt(rowLimit, 1, 1000, 100)
	cellChars = clampInt(cellChars, 100, 20000, 2000)

	started := time.Now()
	cols, rows, err := b.queryClient().Query(ctx, sqlutil.LimitedSelect(stmt, rowLimit))
	if err != nil {
		return nil, err
	}
	out := &QueryResult{
		SQL:         stmt,
		Columns:     cols,
		ExecutionMS: time.Since(started).Milliseconds(),
	}
	if len(rows) > rowLimit {
		rows = rows[:rowLimit]
		out.Truncated = true
	}
	for _, r := range rows {
		for i, cell := range r {
			s, ok := cell.(string)
			if !ok {
				continue
			}
			if cut, truncated := strutil.TruncateRunes(s, cellChars); truncated {
				r[i] = cut + "…"
			}
		}
	}
	out.Rows = rows
	out.RowCount = len(rows)
	return out, nil
}

// queryClient has a longer deadline than the sensors': an agent-authored
// aggregate may legitimately take seconds, a hook-blocking lookup may not.
func (b *Bundler) queryClient() *Client {
	if b.execClient == nil {
		return b.client
	}
	return b.execClient
}
