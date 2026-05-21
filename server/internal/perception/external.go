package perception

import (
	"context"
	"fmt"
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

	// CAST(...) must be aliased; otherwise GreptimeDB complains that two
	// projected columns share the same name as the ORDER BY column.
	humanSQL := fmt.Sprintf(
		`SELECT CAST(ts AS BIGINT) AS ts_ms, change_type, file_path, attribution
		 FROM tma1_external_changes
		 WHERE project = '%s'
		   AND attribution = 'human'
		   AND change_type IN ('file_modified','file_added','file_deleted')
		   AND ts > %d
		 ORDER BY ts DESC LIMIT 20`,
		escapeSQL(project), since.UnixMilli(),
	)
	if _, rows, err := b.client.Query(ctx, humanSQL); err == nil {
		for _, r := range rows {
			out.HumanChanges = append(out.HumanChanges, ExternalChange{
				Timestamp:   time.UnixMilli(int64At(r, 0)),
				ChangeType:  stringAt(r, 1),
				FilePath:    stringAt(r, 2),
				Attribution: stringAt(r, 3),
			})
		}
		out.HumanCount = len(out.HumanChanges)
	}

	gitSQL := fmt.Sprintf(
		`SELECT CAST(ts AS BIGINT) AS ts_ms, change_type, git_sha, git_message
		 FROM tma1_external_changes
		 WHERE project = '%s'
		   AND change_type IN ('git_commit','git_branch_switch')
		   AND ts > %d
		 ORDER BY ts DESC LIMIT 10`,
		escapeSQL(project), since.UnixMilli(),
	)
	if _, rows, err := b.client.Query(ctx, gitSQL); err == nil {
		for _, r := range rows {
			out.GitChanges = append(out.GitChanges, ExternalChange{
				Timestamp:  time.UnixMilli(int64At(r, 0)),
				ChangeType: stringAt(r, 1),
				GitSHA:     stringAt(r, 2),
				GitMessage: stringAt(r, 3),
			})
		}
		out.GitCount = len(out.GitChanges)
	}

	if out.HumanCount == 0 && out.GitCount == 0 {
		return nil, nil
	}
	return out, nil
}

