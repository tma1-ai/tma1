package greptimedb

import (
	"fmt"
	"log/slog"
	"strings"
)

// externalChangesTableDDL creates tma1_external_changes.
//
// Captures file-system + git events for any project the agent has touched, so
// the perception layer can report changes outside observed agent writes.
// Append-only.
//
// project + change_type + attribution are the dominant filters (every
// R-stale-view / R-external-modified query uses all three) and all are
// low-cardinality. Make them PRIMARY KEY for locality per the
// GreptimeDB design-table guide.
var externalChangesTableDDL = `CREATE TABLE IF NOT EXISTS tma1_external_changes (
    ts          TIMESTAMP TIME INDEX,
    project     STRING,
    change_type STRING,
    attribution STRING,
    file_path   STRING NULL,
    git_sha     STRING NULL,
    git_message STRING NULL,
    host        STRING NULL,
    PRIMARY KEY (project, change_type, attribution)
) WITH ('append_mode'='true')`

// InitExternalChangesTable creates tma1_external_changes. Idempotent.
// Kept separate from flows.sql per the plan — flows.sql is Flow/sink only.
func InitExternalChangesTable(httpPort int, logger *slog.Logger) error {
	sqlURL := fmt.Sprintf("http://localhost:%d/v1/sql", httpPort)
	if err := execSQL(sqlURL, externalChangesTableDDL); err != nil {
		if !strings.Contains(strings.ToLower(err.Error()), "already exists") {
			return fmt.Errorf("init external_changes: %w", err)
		}
	}
	logger.Info("external_changes table initialized")
	return nil
}
