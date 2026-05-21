package greptimedb

import (
	"fmt"
	"log/slog"
	"strings"
)

// projectStateTableDDL creates tma1_project_state.
//
// Stores the most recent indexed snapshot of a project's static structure:
// language, build/test system, key files, framework hints. One logical row
// per project; append-only so the most-recent-by-ts row is the current
// state. (Per the plan we avoid the "one upserted row per PK" pattern to
// stay consistent with every other tma1 table.)
// `root`, `language` are GreptimeDB reserved keywords — must be quoted in
// DDL + DML.
var projectStateTableDDL = `CREATE TABLE IF NOT EXISTS tma1_project_state (
    ts             TIMESTAMP TIME INDEX,
    project        STRING SKIPPING INDEX,
    "root"         STRING NULL,
    "language"     STRING NULL INVERTED INDEX,
    build_system   STRING NULL,
    test_framework STRING NULL,
    frameworks     STRING NULL,
    key_files      STRING NULL,
    module_summary STRING NULL
) WITH ('append_mode'='true')`

// InitProjectStateTable creates tma1_project_state. Idempotent.
// Kept separate from flows.sql per the plan.
func InitProjectStateTable(httpPort int, logger *slog.Logger) error {
	sqlURL := fmt.Sprintf("http://localhost:%d/v1/sql", httpPort)
	if err := execSQL(sqlURL, projectStateTableDDL); err != nil {
		if !strings.Contains(strings.ToLower(err.Error()), "already exists") {
			return fmt.Errorf("init project_state: %w", err)
		}
	}
	logger.Info("project_state table initialized")
	return nil
}
