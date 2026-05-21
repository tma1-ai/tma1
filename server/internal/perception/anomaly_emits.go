package perception

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// logEmits writes one row per anomaly into tma1_anomaly_emits. Each row
// is an INSERT fired in its own goroutine — the hook's response budget
// is ~500ms and we don't want a slow DB to feed back into agent latency.
//
// Failures are intentionally swallowed: the emit log is dogfood
// infrastructure for the Phase 1.7 gates, not part of the agent's
// critical path. A missed row only thins the precision sample.
func (d *Detector) logEmits(sessionID string, anomalies []Anomaly) {
	if d == nil || d.client == nil || len(anomalies) == 0 {
		return
	}
	for _, a := range anomalies {
		a := a // capture
		go d.insertEmit(sessionID, a)
	}
}

func (d *Detector) insertEmit(sessionID string, a Anomaly) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	related := ""
	if len(a.RelatedFiles) > 0 {
		if b, err := json.Marshal(a.RelatedFiles); err == nil {
			related = string(b)
		}
	}

	firstEmittedMs := int64(0)
	if !a.FirstEmittedAt.IsZero() {
		firstEmittedMs = a.FirstEmittedAt.UnixMilli()
	}

	sql := fmt.Sprintf(
		"INSERT INTO tma1_anomaly_emits "+
			"(ts, session_id, kind, severity, channel, evidence, suggestion, related_files, first_emitted_at) "+
			"VALUES (%d, %s, %s, %s, %s, %s, %s, %s, %s)",
		time.Now().UnixMilli(),
		emitQuote(sessionID, 256),
		emitQuote(a.Kind, 64),
		emitQuote(a.Severity, 16),
		emitQuote(a.Channel, 32),
		emitQuote(a.Evidence, 1024),
		emitQuote(a.Suggestion, 1024),
		emitQuote(related, 2048),
		nullableMs(firstEmittedMs),
	)

	if _, _, err := d.client.Query(ctx, sql); err != nil {
		d.logger.Debug("anomaly emit log: insert failed", "err", err, "kind", a.Kind, "session", sessionID)
	}
}

// emitQuote: SQL literal with truncation. Empty string becomes NULL so the
// column distinguishes "no value" from "empty string".
func emitQuote(v string, maxLen int) string {
	if v == "" {
		return "NULL"
	}
	if len(v) > maxLen {
		v = v[:maxLen]
	}
	return "'" + strings.ReplaceAll(v, "'", "''") + "'"
}

func nullableMs(ms int64) string {
	if ms <= 0 {
		return "NULL"
	}
	return fmt.Sprintf("%d", ms)
}
