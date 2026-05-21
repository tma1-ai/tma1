package greptimedb

import (
	"fmt"
	"log/slog"
	"strings"
)

// anomalyEmitsTableDDL creates tma1_anomaly_emits.
//
// Each row records one emission of an anomaly to an injection channel.
// Append-only — the suppression layer dedups in memory, this table just
// captures the ground truth of "what did the agent actually see, when".
//
// Drives Phase 1.7 validation gates:
//   - Precision: human-label TP/FP on a sample, target >= 70%
//   - Daily emit budget: COUNT(*) per kind per day, target <= 5
//   - Action follow-rate: did the agent do the suggested action in
//     the next N tool calls? Target >= 30%
var anomalyEmitsTableDDL = `CREATE TABLE IF NOT EXISTS tma1_anomaly_emits (
    ts               TIMESTAMP TIME INDEX,
    session_id       STRING SKIPPING INDEX,
    kind             STRING INVERTED INDEX,
    severity         STRING INVERTED INDEX,
    channel          STRING NULL,
    evidence         STRING NULL,
    suggestion       STRING NULL,
    related_files    STRING NULL,
    first_emitted_at TIMESTAMP NULL
) WITH ('append_mode'='true')`

// InitAnomalyEmitsTable creates tma1_anomaly_emits. Idempotent.
// Kept separate from flows.sql per the plan — flows.sql is Flow/sink only.
func InitAnomalyEmitsTable(httpPort int, logger *slog.Logger) error {
	sqlURL := fmt.Sprintf("http://localhost:%d/v1/sql", httpPort)
	if err := execSQL(sqlURL, anomalyEmitsTableDDL); err != nil {
		if !strings.Contains(strings.ToLower(err.Error()), "already exists") {
			return fmt.Errorf("init anomaly_emits: %w", err)
		}
	}
	logger.Info("anomaly_emits table initialized")
	return nil
}
