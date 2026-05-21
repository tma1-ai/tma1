// Package git is the external-change sensor. For every project the agent
// has touched (notified via Sensor.Observe(cwd)) it watches the file system
// (fsnotify) and polls `git log -1` so the perception layer can answer
// "what changed outside of the agent's edits?".
//
// Storage is tma1_external_changes (one row per change). Attribution
// (agent vs human) is decided at write time by querying tma1_hook_events.
package git

import (
	"context"
	"time"
)

// Change type constants used in the change_type column of
// tma1_external_changes.
const (
	ChangeTypeFileModified    = "file_modified"
	ChangeTypeFileAdded       = "file_added"
	ChangeTypeFileDeleted     = "file_deleted"
	ChangeTypeGitCommit       = "git_commit"
	ChangeTypeGitBranchSwitch = "git_branch_switch"
)

// Attribution constants for the attribution column.
const (
	AttributionAgent   = "agent"
	AttributionHuman   = "human"
	AttributionUnknown = "unknown"
)

// Change is a single observed external change destined for
// tma1_external_changes.
type Change struct {
	Timestamp   time.Time
	Project     string // project label (basename of git root or marker dir)
	ChangeType  string // file_modified | file_added | file_deleted | git_commit | git_branch_switch
	FilePath    string
	GitSHA      string
	GitMessage  string
	Attribution string // agent | human | unknown
	Host        string
}

// EventWriter persists Changes. Implementations must be safe for concurrent
// use; watcher + poller goroutines call Write from different paths.
type EventWriter interface {
	Write(ctx context.Context, c Change) error
}

// Attributor decides whether a file change came from the agent or a human.
type Attributor interface {
	Classify(ctx context.Context, filePath string, when time.Time) string
}
