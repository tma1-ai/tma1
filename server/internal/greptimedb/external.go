package greptimedb

import (
	"fmt"
	"log/slog"
	"strings"
)

// externalChangesTableDDL creates tma1_external_changes.
//
// Captures file-system + git events for any project the agent has touched,
// so the perception layer can tell an agent "while you were away, a human
// modified src/auth.rs and committed README.md". Append-only.
var externalChangesTableDDL = `CREATE TABLE IF NOT EXISTS tma1_external_changes (
    ts          TIMESTAMP TIME INDEX,
    project     STRING SKIPPING INDEX,
    change_type STRING INVERTED INDEX,
    file_path   STRING NULL,
    git_sha     STRING NULL,
    git_message STRING NULL,
    attribution STRING NULL INVERTED INDEX,
    host        STRING NULL
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
