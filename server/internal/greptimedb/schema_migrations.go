package greptimedb

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/tma1-ai/tma1/server/internal/sqlutil"
)

// Migration is one schema-evolution step. Applied in strict Version order
// after consulting the tma1_schema_version ledger; each successful
// application appends a row recording (ts, version, description) so the
// next start picks up where the previous one left off.
//
// Why a ledger instead of inline error-tolerant ALTER:
//   - A migration that is NOT idempotent (e.g. UPDATE backfill, DROP
//     COLUMN) becomes possible. The original "ignore already-exists"
//     approach only worked for ADD COLUMN.
//   - Migrations stay sequenced — adding v8 between two existing
//     versions is a deterministic structural mistake, not a silent one.
//   - Logs show which migration actually applied versus which was
//     skipped because it ran before.
//
// Plan risk section calls for a versioned migration mechanism that
// mirrors the existing settings/configVersion approach; this is the
// schema-side analogue.
type Migration struct {
	Version     int      // strictly increasing across the slice; first is 1
	Description string   // shown in logs + persisted to the ledger
	SQL         []string // statements executed in order
	// IgnoreErr filters errors that should not abort the migration.
	// Use isIgnorableSchemaUpgradeError to swallow the "duplicate column"
	// case so an installed DB whose schema already has the column
	// (because v1 / v2 of the binary ran the old inline-tolerant path)
	// can adopt the ledger without re-creating the table.
	IgnoreErr func(error) bool
}

// schemaMigrations is the canonical, append-only list of schema
// migrations. NEVER renumber or reorder applied entries — production
// installs key off Version, not slice index.
//
// To add a new migration: append with Version = last+1.
var schemaMigrations = []Migration{
	{
		Version:     1,
		Description: "v1 — hook event conversation_id / permission_mode / metadata + message token columns",
		SQL: []string{
			`ALTER TABLE tma1_hook_events ADD COLUMN conversation_id STRING NULL`,
			`ALTER TABLE tma1_hook_events ADD COLUMN permission_mode STRING NULL`,
			`ALTER TABLE tma1_hook_events ADD COLUMN metadata STRING NULL`,
			`ALTER TABLE tma1_messages ADD COLUMN input_tokens BIGINT NULL`,
			`ALTER TABLE tma1_messages ADD COLUMN output_tokens BIGINT NULL`,
			`ALTER TABLE tma1_messages ADD COLUMN cache_read_tokens BIGINT NULL`,
			`ALTER TABLE tma1_messages ADD COLUMN cache_creation_tokens BIGINT NULL`,
			`ALTER TABLE tma1_messages ADD COLUMN duration_ms BIGINT NULL`,
		},
		IgnoreErr: isIgnorableSchemaUpgradeError,
	},
	{
		Version:     2,
		Description: "v1.4 — ingest-side derived columns on hook events",
		SQL: []string{
			`ALTER TABLE tma1_hook_events ADD COLUMN tool_file_path STRING NULL`,
			`ALTER TABLE tma1_hook_events ADD COLUMN tool_command_prefix STRING NULL`,
			`ALTER TABLE tma1_hook_events ADD COLUMN tool_success BOOLEAN NULL`,
			`ALTER TABLE tma1_hook_events ADD COLUMN tool_error_summary STRING NULL`,
		},
		IgnoreErr: isIgnorableSchemaUpgradeError,
	},
	{
		Version: 3,
		Description: "v2 backstop — re-issue CREATE IF NOT EXISTS for the 4 " +
			"tables introduced by this branch (anomaly_emits / build_events / " +
			"external_changes / project_state). " +
			"An earlier draft of this migration did DROP+CREATE to retrofit " +
			"a PRIMARY KEY on installs that pre-dated the layout change; " +
			"that path is gone — the data loss it caused on dogfood instances " +
			"was not worth the perf delta on these append-only tables. " +
			"What remains is an idempotent CREATE IF NOT EXISTS, which is a " +
			"no-op on installs that already have the tables and a safety net " +
			"if init somehow failed to create them earlier in startup. " +
			"tma1_hook_events and tma1_messages are unaffected and never were.",
		SQL: []string{
			// Each DDL is CREATE TABLE IF NOT EXISTS — see the *TableDDL
			// definitions in anomaly_emits.go / build.go / external.go /
			// project.go. Re-issuing them here means the migration ledger
			// stamps v3 applied even on hosts that already ran the init
			// pass, keeping the version line monotonic.
			anomalyEmitsTableDDL,
			buildTableDDL,
			externalChangesTableDDL,
			projectStateTableDDL,
		},
		// CREATE TABLE IF NOT EXISTS shouldn't raise duplicate-column
		// either, but keep the tolerant guard in case a future column
		// addition lands inside one of the *TableDDL strings without a
		// matching migration entry — that would be a separate bug, but
		// silently no-op'ing it here lets the ledger keep advancing
		// instead of wedging the start sequence.
		IgnoreErr: isIgnorableSchemaUpgradeError,
	},
	{
		Version:     4,
		Description: "v2.1 — reasoning_tokens column on tma1_messages for Codex thinking/turn usage",
		SQL: []string{
			`ALTER TABLE tma1_messages ADD COLUMN reasoning_tokens BIGINT NULL`,
		},
		IgnoreErr: isIgnorableSchemaUpgradeError,
	},
}

// schemaVersionDDL creates the migration ledger. Append-only so the
// most-recent-by-ts row is the active version. Same shape pattern as
// the other tma1_* tables for consistency.
//
// "version" is a GreptimeDB reserved keyword and must be quoted in DDL +
// every DML that touches the column.
const schemaVersionDDL = `CREATE TABLE IF NOT EXISTS tma1_schema_version (
    ts          TIMESTAMP TIME INDEX,
    "version"   INT NOT NULL,
    description STRING NULL
) WITH ('append_mode'='true')`

// RunSchemaMigrations applies any migrations whose Version exceeds the
// highest value recorded in tma1_schema_version. Safe to call on every
// start — the ledger query short-circuits a no-op run.
func RunSchemaMigrations(httpPort int, logger *slog.Logger) error {
	sqlURL := fmt.Sprintf("http://localhost:%d/v1/sql", httpPort)

	// Make sure the ledger exists before we read from it.
	if err := execSQL(sqlURL, schemaVersionDDL); err != nil {
		if !isIgnorableSchemaUpgradeError(err) {
			return fmt.Errorf("create tma1_schema_version: %w", err)
		}
	}

	current, err := currentSchemaVersion(sqlURL)
	if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	pending := pendingMigrations(current, schemaMigrations)
	if len(pending) == 0 {
		logger.Info("schema migrations up to date", "version", current)
		return nil
	}

	for _, m := range pending {
		logger.Info("applying schema migration", "version", m.Version, "desc", m.Description)
		for _, stmt := range m.SQL {
			if err := execSQL(sqlURL, stmt); err != nil {
				if m.IgnoreErr != nil && m.IgnoreErr(err) {
					logger.Debug("migration ignored expected error",
						"version", m.Version, "stmt", stmt, "err", err)
					continue
				}
				return fmt.Errorf("migration v%d: %q: %w", m.Version, stmt, err)
			}
		}
		// Record the migration ONCE the SQL has run. If recording fails
		// we surface the error rather than blindly skip — the next
		// start would otherwise re-run the migration.
		if err := recordMigration(sqlURL, m); err != nil {
			return fmt.Errorf("record migration v%d: %w", m.Version, err)
		}
	}

	logger.Info("schema migrations applied",
		"from", current,
		"to", pending[len(pending)-1].Version,
		"applied", len(pending))
	return nil
}

// pendingMigrations returns the entries in `all` whose Version exceeds
// `current`, in ascending Version order. Factored out so the
// version-selection logic is unit-testable without a live GreptimeDB.
//
// The slice is sorted defensively even though schemaMigrations is
// hand-curated -- a mid-list insertion mistake would otherwise apply
// migrations out of order and stamp the wrong ledger version.
func pendingMigrations(current int, all []Migration) []Migration {
	var out []Migration
	for _, m := range all {
		if m.Version > current {
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Version < out[j].Version
	})
	return out
}

// currentSchemaVersion returns the highest version recorded in
// tma1_schema_version, or 0 when the ledger is empty / unreadable.
func currentSchemaVersion(sqlURL string) (int, error) {
	body, err := postSQL(sqlURL, `SELECT MAX("version") FROM tma1_schema_version`)
	if err != nil {
		return 0, err
	}
	var r struct {
		Output []struct {
			Records struct {
				Rows [][]any `json:"rows"`
			} `json:"records"`
		} `json:"output"`
		Code  int    `json:"code"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return 0, fmt.Errorf("parse version response: %w", err)
	}
	if r.Code != 0 || r.Error != "" {
		return 0, fmt.Errorf("greptime: %s", r.Error)
	}
	if len(r.Output) == 0 || len(r.Output[0].Records.Rows) == 0 {
		return 0, nil
	}
	cell := r.Output[0].Records.Rows[0][0]
	switch v := cell.(type) {
	case nil:
		return 0, nil
	case float64:
		return int(v), nil
	case int64:
		return int(v), nil
	case int:
		return v, nil
	}
	return 0, nil
}

// recordMigration writes the success row for `m`.
func recordMigration(sqlURL string, m Migration) error {
	stmt := fmt.Sprintf(
		`INSERT INTO tma1_schema_version (ts, "version", description) VALUES (%d, %d, '%s')`,
		time.Now().UnixMilli(), m.Version, sqlutil.Escape(m.Description),
	)
	return execSQL(sqlURL, stmt)
}

// postSQL is a thin GET-result variant of execSQL — used when a
// statement returns a row payload we need to parse (vs DDL/DML where
// the absence of an HTTP error is enough).
func postSQL(sqlURL, sql string) ([]byte, error) {
	form := url.Values{}
	form.Set("sql", sql)
	resp, err := httpClient.Post(sqlURL, "application/x-www-form-urlencoded", strings.NewReader(form.Encode())) //nolint:gosec
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
	}
	return body, nil
}
