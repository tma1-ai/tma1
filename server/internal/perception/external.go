package perception

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ExternalChange is one row from tma1_external_changes.
type ExternalChange struct {
	Timestamp   time.Time `json:"ts"`
	ChangeType  string    `json:"change_type"`
	FilePath    string    `json:"file_path,omitempty"`
	GitSHA      string    `json:"git_sha,omitempty"`
	GitMessage  string    `json:"git_message,omitempty"`
	Attribution string    `json:"attribution,omitempty"` // agent | human | unknown
}

// ExternalChanges is the summarised section for a Bundle.
type ExternalChanges struct {
	Project      string           `json:"project"`
	Since        time.Time        `json:"since"`
	HumanChanges []ExternalChange `json:"human_changes,omitempty"`
	GitChanges   []ExternalChange `json:"git_changes,omitempty"`
	HumanCount   int              `json:"human_count"`
	GitCount     int              `json:"git_count"`
	// PartialError is set when one of the two underlying queries
	// (human-attribution / git-activity) failed but the other returned
	// rows. The MCP tool surfaces it alongside the partial result so
	// callers know the snapshot is incomplete; the field stays empty
	// (and is omitted from JSON) on a clean success.
	PartialError string `json:"partial_error,omitempty"`
}

// GetExternalChanges returns the human-attributed file changes and git
// activity for the given project since the given time. Agent-attributed
// changes are deliberately filtered out — the agent doesn't need to be
// told about edits it just made.
//
// Returns nil if there are no relevant changes.
func (b *Bundler) GetExternalChanges(ctx context.Context, project string, since time.Time) (*ExternalChanges, error) {
	if project == "" {
		return nil, nil
	}
	if since.IsZero() {
		since = time.Now().Add(-30 * time.Minute)
	}

	out := &ExternalChanges{Project: project, Since: since}

	// The human-attribution and git-activity queries are independent —
	// run them concurrently so the call latency is the slower of the two
	// rather than their sum. Each query owns its own error variable
	// (humanErr / gitErr), so partial failures surface without a mutex —
	// the goroutines never write to the same memory location.
	humanSQL := fmt.Sprintf(
		// CAST(...) must be aliased; otherwise GreptimeDB complains that
		// two projected columns share the same name as the ORDER BY column.
		`SELECT CAST(ts AS BIGINT) AS ts_ms, change_type, file_path, attribution
		 FROM tma1_external_changes
		 WHERE project = '%s'
		   AND attribution = 'human'
		   AND change_type IN ('file_modified','file_added','file_deleted')
		   AND ts > %d
		 ORDER BY ts DESC LIMIT 20`,
		escapeSQL(project), since.UnixMilli(),
	)
	gitSQL := fmt.Sprintf(
		`SELECT CAST(ts AS BIGINT) AS ts_ms, change_type, git_sha, git_message
		 FROM tma1_external_changes
		 WHERE project = '%s'
		   AND change_type IN ('git_commit','git_branch_switch')
		   AND ts > %d
		 ORDER BY ts DESC LIMIT 10`,
		escapeSQL(project), since.UnixMilli(),
	)

	var (
		humanErr, gitErr error
		wg               sync.WaitGroup
	)
	wg.Add(2)

	go func() {
		defer wg.Done()
		_, rows, err := b.client.Query(ctx, humanSQL)
		if err != nil {
			humanErr = fmt.Errorf("human changes: %w", err)
			return
		}
		for _, r := range rows {
			out.HumanChanges = append(out.HumanChanges, ExternalChange{
				Timestamp:   time.UnixMilli(int64At(r, 0)),
				ChangeType:  stringAt(r, 1),
				FilePath:    stringAt(r, 2),
				Attribution: stringAt(r, 3),
			})
		}
		out.HumanCount = len(out.HumanChanges)
	}()

	go func() {
		defer wg.Done()
		_, rows, err := b.client.Query(ctx, gitSQL)
		if err != nil {
			gitErr = fmt.Errorf("git changes: %w", err)
			return
		}
		for _, r := range rows {
			out.GitChanges = append(out.GitChanges, ExternalChange{
				Timestamp:  time.UnixMilli(int64At(r, 0)),
				ChangeType: stringAt(r, 1),
				GitSHA:     stringAt(r, 2),
				GitMessage: stringAt(r, 3),
			})
		}
		out.GitCount = len(out.GitChanges)
	}()

	wg.Wait()

	// Partial success: when one query failed but the other returned rows,
	// surface the error alongside the partial result. A caller relying on
	// `err == nil ⇒ complete data` would otherwise treat a GreptimeDB
	// outage as "no external changes", silencing the signal.
	combinedErr := errors.Join(humanErr, gitErr)
	if out.HumanCount == 0 && out.GitCount == 0 {
		return nil, combinedErr
	}
	return out, combinedErr
}

