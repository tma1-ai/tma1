package perception

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Severity levels in increasing urgency.
const (
	SeverityLow    = "low"
	SeverityMedium = "medium"
	SeverityHigh   = "high"
)

// Channel tells the handler where to inject this anomaly. Different
// anomalies need different timing: a stale-view warning should reach the
// agent the moment it reads a file, while a long-session reminder is fine
// to drop into the next UserPromptSubmit.
const (
	ChannelUserPromptSubmit = "user_prompt_submit" // included in bundle summary
	ChannelStopBlock        = "stop_block"         // joins HIGH-severity Stop block
	ChannelPostToolUse      = "post_tool_use"      // append to a specific tool result
)

// Anomaly is a single detected issue in an agent session.
//
// Schema is small on purpose — agents must be able to act on `Suggestion`
// without parsing extra structure. The `Channel` field steers the handler
// to the right injection point (Phase 1.7). `FirstEmittedAt` is stamped
// by the suppression layer when the anomaly is first surfaced — it stays
// stable across re-detections so timelines / dashboards can show "this has
// been outstanding since X" rather than the last detection wall-clock.
type Anomaly struct {
	Kind           string    `json:"kind"`
	Severity       string    `json:"severity"`
	Channel        string    `json:"channel,omitempty"`
	Evidence       string    `json:"evidence"`
	Suggestion     string    `json:"suggestion"`
	RelatedFiles   []string  `json:"related_files,omitempty"`
	FirstEmittedAt time.Time `json:"first_emitted_at,omitempty"`
}

// stableKey is a session-scoped identity for an anomaly, used by the
// Suppression layer to dedupe across turns.
func (a Anomaly) stableKey() string {
	files := append([]string{}, a.RelatedFiles...)
	sort.Strings(files)
	return a.Kind + "|" + a.Severity + "|" + strings.Join(files, ",")
}

// Detector runs the Phase 1.7 anomaly rules against a session.
// Detect() returns the current set of active anomalies for sessionID,
// after suppression. Errors from individual rules are swallowed.
type Detector struct {
	client *Client
	cache  *anomalyCache
	logger *slog.Logger
}

// NewDetector returns a Detector targeting localhost:<httpPort>.
func NewDetector(httpPort int, logger *slog.Logger) *Detector {
	if logger == nil {
		logger = slog.Default()
	}
	return &Detector{
		client: NewClient(httpPort),
		cache:  newAnomalyCache(30 * time.Second),
		logger: logger,
	}
}

// Detect runs all rules and returns the post-suppression anomaly set for
// sessionID. The cache merges two purposes:
//   - Short-lived result cache (30s TTL) so a flurry of hook calls don't
//     re-run every SQL
//   - Per-session "emit history" so the Suppression layer can decide
//     whether a candidate anomaly is worth re-telling the agent
func (d *Detector) Detect(ctx context.Context, sessionID string) []Anomaly {
	if strings.TrimSpace(sessionID) == "" {
		return nil
	}
	if cached, ok := d.cache.get(sessionID); ok {
		return cached
	}

	var candidates []Anomaly
	for name, rule := range d.rules() {
		hits, err := rule(ctx, sessionID)
		if err != nil {
			d.logger.Debug("anomaly rule failed", "rule", name, "session", sessionID, "err", err)
			continue
		}
		candidates = append(candidates, hits...)
	}

	// Apply suppression. The Detector layer first asks each candidate's
	// resolver (if any) whether the underlying condition has been
	// addressed since the last emit — if so we reset the emit state so
	// the candidate goes through again immediately, rather than waiting
	// out the silence window.
	kept := d.suppressWithResolvers(ctx, sessionID, candidates)
	d.cache.set(sessionID, kept)
	// Persist the emit log async — feeds the 1.7 validation gates
	// (precision / daily budget / action follow-rate). Cache hits at
	// the top of Detect short-circuit before this point, so each kept
	// anomaly is logged at most once per suppression cycle.
	d.logEmits(sessionID, kept)
	return kept
}

// DetectByChannel filters Detect output to a specific Channel — used by
// the handler to assemble per-injection-point payloads without re-running
// the rules.
func (d *Detector) DetectByChannel(ctx context.Context, sessionID, channel string) []Anomaly {
	all := d.Detect(ctx, sessionID)
	out := all[:0:0]
	for _, a := range all {
		if a.Channel == channel || (channel == "" && a.Channel == "") {
			out = append(out, a)
		}
	}
	return out
}

// Invalidate drops cached anomalies (but NOT emit history) for sessionID
// so the next Detect re-runs the rules. Emit history persists across
// invalidations so suppression survives a cache wipe.
func (d *Detector) Invalidate(sessionID string) {
	d.cache.invalidate(sessionID)
}

type ruleFunc func(ctx context.Context, sessionID string) ([]Anomaly, error)

// rules returns the Phase 1.7 ruleset. Kind names are stable IDs that
// downstream consumers (suppression, UI) key on.
func (d *Detector) rules() map[string]ruleFunc {
	return map[string]ruleFunc{
		"build_broken_after_my_edit":     d.ruleBuildBrokenAfterMyEdit,
		"repeated_failed_build":          d.ruleRepeatedFailedBuild,
		"stale_file_view":                d.ruleStaleFileView,
		"test_stuck":                     d.ruleTestStuck,
		"human_modified_during_session":  d.ruleHumanModifiedDuringSession,
		"context_pressure":               d.ruleContextPressure,
	}
}

// resolverFunc returns true when the anomaly's underlying condition has
// been addressed since lastEmitted — agent re-read the stale file, build
// went green, etc. SHOULD return false when the data is unavailable or
// inconclusive: a false negative just delays re-emit until the silence
// window expires, but a false positive lets a real issue escape suppression
// and spam the agent.
type resolverFunc func(ctx context.Context, sessionID string, a Anomaly, lastEmitted time.Time) bool

// resolverFor returns the resolution check for a given anomaly Kind, or
// nil when the anomaly has no programmatic "resolved" signal (e.g.
// context_pressure is a session-wide measurement that doesn't resolve
// until a new session starts; human_modified_during_session decays purely
// by time).
func (d *Detector) resolverFor(kind string) resolverFunc {
	switch kind {
	case "stale_file_view":
		return d.resolverReReadHappened
	case "build_broken_after_my_edit",
		"repeated_failed_build",
		"test_stuck":
		return d.resolverBashSucceededSince
	default:
		return nil
	}
}

// suppressWithResolvers wraps the cache's history-based suppression with
// per-kind resolution checks. Each candidate's resolver runs OUTSIDE the
// cache lock so SQL latency doesn't serialise concurrent Detect calls.
func (d *Detector) suppressWithResolvers(ctx context.Context, sessionID string, candidates []Anomaly) []Anomaly {
	if len(candidates) == 0 {
		return candidates
	}
	// Phase 1: snapshot last-emitted timestamps for any candidate that
	// already has emit history. Skip the resolver work for fresh ones.
	resolved := make(map[string]bool, len(candidates))
	for _, a := range candidates {
		key := a.stableKey()
		st, ok := d.cache.lastEmittedFor(sessionID, key)
		if !ok {
			continue
		}
		r := d.resolverFor(a.Kind)
		if r == nil {
			continue
		}
		if r(ctx, sessionID, a, st.LastEmittedAt) {
			resolved[key] = true
		}
	}
	return d.cache.suppressWithResolution(sessionID, candidates, resolved)
}

// resolverReReadHappened — R-stale-view resolves when the agent does a
// PreToolUse Read of the related file after lastEmitted. The same Read
// that resolves this anomaly will also push the underlying rule's
// last_read.ts past the change.ts on the next run, so the rule itself
// will stop generating the candidate.
func (d *Detector) resolverReReadHappened(ctx context.Context, sessionID string, a Anomaly, lastEmitted time.Time) bool {
	if len(a.RelatedFiles) == 0 {
		return false
	}
	fp := a.RelatedFiles[0]
	sql := fmt.Sprintf(
		`SELECT COUNT(*) FROM tma1_hook_events
		 WHERE session_id = '%s'
		   AND event_type = 'PreToolUse' AND tool_name = 'Read'
		   AND ts > %d
		   AND (tool_file_path = '%s' OR tool_input LIKE '%%"file_path":"%s"%%')`,
		escapeSQL(sessionID), lastEmitted.UnixMilli(),
		escapeSQL(fp), escapeSQL(fp),
	)
	_, rows, err := d.client.Query(ctx, sql)
	if err != nil || len(rows) == 0 {
		return false
	}
	return intAt(rows[0], 0) > 0
}

// resolverBashSucceededSince — build / test rules resolve when ANY Bash
// PostToolUse (non-failure) lands after lastEmitted. We don't insist on
// the exact command matching: in practice, a successful test run after
// a stretch of failures is the strongest signal "things are recovering".
func (d *Detector) resolverBashSucceededSince(ctx context.Context, sessionID string, a Anomaly, lastEmitted time.Time) bool {
	sql := fmt.Sprintf(
		`SELECT COUNT(*) FROM tma1_hook_events
		 WHERE session_id = '%s'
		   AND event_type = 'PostToolUse' AND tool_name = 'Bash'
		   AND ts > %d`,
		escapeSQL(sessionID), lastEmitted.UnixMilli(),
	)
	_, rows, err := d.client.Query(ctx, sql)
	if err != nil || len(rows) == 0 {
		return false
	}
	return intAt(rows[0], 0) > 0
}

// ───────────────────────────────────────────────────────────────────────
// R-build-broken-mine — agent Edit followed by build/test failure that
// mentions the same file's basename
// ───────────────────────────────────────────────────────────────────────

func (d *Detector) ruleBuildBrokenAfterMyEdit(ctx context.Context, sessionID string) ([]Anomaly, error) {
	// 1) find Edit/Write events on real source files (not docs)
	editsSQL := fmt.Sprintf(
		`SELECT COALESCE(tool_file_path,
		                 regexp_match(tool_input, '"file_path":"([^"]+)"')[1]) AS fp,
		        CAST(MAX(ts) AS BIGINT) AS last_edit_ms,
		        COUNT(*) AS n
		 FROM tma1_hook_events
		 WHERE session_id = '%s'
		   AND event_type = 'PreToolUse'
		   AND tool_name IN ('Edit','Write','MultiEdit')
		   AND ts > now() - INTERVAL '30 minutes'
		 GROUP BY fp
		 HAVING fp IS NOT NULL AND fp != ''`,
		escapeSQL(sessionID),
	)
	_, editRows, err := d.client.Query(ctx, editsSQL)
	if err != nil || len(editRows) == 0 {
		return nil, err
	}

	var out []Anomaly
	for _, er := range editRows {
		fp := stringAt(er, 0)
		lastEdit := time.UnixMilli(int64At(er, 1))
		editCount := intAt(er, 2)
		base := basename(fp)
		if base == "" || isDocFile(base) {
			continue
		}

		// 2) check for Bash failures within ±10min whose tool_input OR
		// tool_result mentions the basename. Generic, not project-tuned.
		failuresSQL := fmt.Sprintf(
			`SELECT COUNT(*) FROM tma1_hook_events
			 WHERE session_id = '%s'
			   AND event_type = 'PostToolUseFailure' AND tool_name = 'Bash'
			   AND ts BETWEEN %d AND %d
			   AND (tool_input LIKE '%%%s%%' OR tool_result LIKE '%%%s%%')`,
			escapeSQL(sessionID),
			lastEdit.Add(-10*time.Minute).UnixMilli(),
			lastEdit.Add(10*time.Minute).UnixMilli(),
			escapeSQL(base), escapeSQL(base),
		)
		_, fRows, err := d.client.Query(ctx, failuresSQL)
		if err != nil || len(fRows) == 0 {
			continue
		}
		failCount := intAt(fRows[0], 0)
		if failCount == 0 {
			continue
		}

		sev := SeverityMedium
		if failCount >= 3 {
			sev = SeverityHigh
		}

		// Action sentence: verb-first, names the file + the symptom + the next move.
		suggestion := fmt.Sprintf(
			"Re-read %s around the error site before editing again — %d build/test failure(s) since you started editing it.",
			shortPath(fp), failCount,
		)
		evidence := fmt.Sprintf(
			"Edited %s %d time(s) in 30 min; %d build/test failure(s) named this file in the same window.",
			shortPath(fp), editCount, failCount,
		)

		channel := ChannelUserPromptSubmit
		if sev == SeverityHigh {
			channel = ChannelStopBlock
		}

		out = append(out, Anomaly{
			Kind:         "build_broken_after_my_edit",
			Severity:     sev,
			Channel:      channel,
			Evidence:     evidence,
			Suggestion:   suggestion,
			RelatedFiles: []string{fp},
		})
	}
	return out, nil
}

// ───────────────────────────────────────────────────────────────────────
// R-build-repeated — same command prefix failed 3+ times in 30 min
// ───────────────────────────────────────────────────────────────────────

func (d *Detector) ruleRepeatedFailedBuild(ctx context.Context, sessionID string) ([]Anomaly, error) {
	const sqlFmt = `WITH bash_fails AS (
		SELECT COALESCE(tool_command_prefix,
		                regexp_match(tool_input, '"command":"([^"]+)"')[1]) AS cmd
		FROM tma1_hook_events
		WHERE session_id = '%s'
		  AND event_type = 'PostToolUseFailure' AND tool_name = 'Bash'
		  AND ts > now() - INTERVAL '30 minutes'
	)
	SELECT substr(cmd, 1, 60) AS prefix, COUNT(*) AS n FROM bash_fails
	WHERE cmd IS NOT NULL
	GROUP BY substr(cmd, 1, 60) HAVING COUNT(*) >= 3
	ORDER BY n DESC LIMIT 5`

	_, rows, err := d.client.Query(ctx, fmt.Sprintf(sqlFmt, escapeSQL(sessionID)))
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	var out []Anomaly
	for _, r := range rows {
		prefix := stringAt(r, 0)
		n := intAt(r, 1)
		out = append(out, Anomaly{
			Kind:       "repeated_failed_build",
			Severity:   SeverityHigh,
			Channel:    ChannelStopBlock,
			Evidence:   fmt.Sprintf("`%s` failed %d times in the last 30 minutes.", prefix, n),
			Suggestion: fmt.Sprintf("Read the actual error output from `%s` before retrying — three consecutive identical failures means the root cause isn't fixed.", prefix),
		})
	}
	return out, nil
}

// ───────────────────────────────────────────────────────────────────────
// R-stale-view — agent Read a file before an external modification but
// no re-Read happened between the modification and a later edit/access
// ───────────────────────────────────────────────────────────────────────

func (d *Detector) ruleStaleFileView(ctx context.Context, sessionID string) ([]Anomaly, error) {
	// Plan scenario (Phase 1.7):
	//   T1 — agent Read foo.go
	//   T2 — human modifies foo.go externally  (T2 > T1)
	//   T3 — agent Edit foo.go with no Re-Read in (T2, T3)
	//
	// The Edit is based on a stale in-memory view of the file. The earlier
	// rule fired on "Read after external change" which over-counts (any
	// post-change Read is a recovery, not a problem) AND under-counts the
	// actual bad case (Edit-without-Re-Read leaves last_read.ts > change.ts
	// because Edit itself was in the GROUP BY and bumped MAX(ts)).
	//
	// Approach: pull Read and Edit events separately, merge in Go. Avoids
	// correlated subqueries (GreptimeDB support is uneven) and keeps the
	// per-edit logic auditable.
	const window = 60

	eventsSQL := fmt.Sprintf(
		`SELECT COALESCE(tool_file_path,
		                 regexp_match(tool_input, '"file_path":"([^"]+)"')[1]) AS fp,
		        CAST(ts AS BIGINT) AS ts_ms,
		        CASE WHEN tool_name = 'Read' THEN 'read' ELSE 'edit' END AS kind
		 FROM tma1_hook_events
		 WHERE session_id = '%s'
		   AND event_type = 'PreToolUse'
		   AND tool_name IN ('Read','Edit','MultiEdit','Write')
		   AND ts > now() - INTERVAL '%d minutes'
		 ORDER BY ts ASC`,
		escapeSQL(sessionID), window,
	)
	_, rows, err := d.client.Query(ctx, eventsSQL)
	if err != nil || len(rows) == 0 {
		return nil, err
	}

	type fileEvents struct {
		reads []int64
		edits []int64
	}
	perFile := map[string]*fileEvents{}
	for _, r := range rows {
		fp := stringAt(r, 0)
		if fp == "" {
			continue
		}
		fe := perFile[fp]
		if fe == nil {
			fe = &fileEvents{}
			perFile[fp] = fe
		}
		ts := int64At(r, 1)
		if stringAt(r, 2) == "read" {
			fe.reads = append(fe.reads, ts)
		} else {
			fe.edits = append(fe.edits, ts)
		}
	}

	// Only files with at least one Edit and one Read can fire the rule.
	var keys []string
	for fp, fe := range perFile {
		if len(fe.edits) > 0 && len(fe.reads) > 0 {
			keys = append(keys, "'"+escapeSQL(fp)+"'")
		}
	}
	if len(keys) == 0 {
		return nil, nil
	}

	changesSQL := fmt.Sprintf(
		`SELECT file_path, CAST(ts AS BIGINT) AS change_ms
		 FROM tma1_external_changes
		 WHERE file_path IN (%s)
		   AND attribution = 'human'
		   AND change_type IN ('file_modified','file_added')
		   AND ts > now() - INTERVAL '%d minutes'
		 ORDER BY ts ASC`,
		strings.Join(keys, ","), window+30,
	)
	_, changeRows, err := d.client.Query(ctx, changesSQL)
	if err != nil {
		return nil, err
	}
	changesByFile := map[string][]int64{}
	for _, r := range changeRows {
		fp := stringAt(r, 0)
		ts := int64At(r, 1)
		if fp != "" && ts > 0 {
			changesByFile[fp] = append(changesByFile[fp], ts)
		}
	}

	var out []Anomaly
	for fp, fe := range perFile {
		staleChangeMs := detectStaleEdit(fe.reads, fe.edits, changesByFile[fp])
		if staleChangeMs == 0 {
			continue
		}

		when := time.UnixMilli(staleChangeMs)
		out = append(out, Anomaly{
			Kind:     "stale_file_view",
			Severity: SeverityHigh,
			Channel:  ChannelUserPromptSubmit,
			Evidence: fmt.Sprintf(
				"You edited %s after a human modified it externally at %s, without re-reading first.",
				shortPath(fp), when.Format("15:04"),
			),
			Suggestion: fmt.Sprintf(
				"Re-read %s before the next edit — your in-memory copy is older than what's on disk.",
				shortPath(fp),
			),
			RelatedFiles: []string{fp},
		})
	}
	return out, nil
}

// detectStaleEdit is the pure decision step of R-stale-view, factored out
// so it's directly unit-testable without a live GreptimeDB.
//
// Inputs are timestamps (ms) for one file in one session:
//   - reads:   PreToolUse Read events
//   - edits:   PreToolUse Edit/MultiEdit/Write events
//   - changes: external human modifications on the same file
//
// Returns the most recent change timestamp that triggered the rule, or 0
// when no Edit was based on a stale view. An Edit triggers when there is
// a Read before it AND at least one external change falls strictly
// between that latest pre-Edit Read and the Edit itself.
func detectStaleEdit(reads, edits, changes []int64) int64 {
	if len(changes) == 0 {
		return 0
	}
	var staleChangeMs int64
	for _, edit := range edits {
		var lastRead int64
		for _, r := range reads {
			if r < edit && r > lastRead {
				lastRead = r
			}
		}
		if lastRead == 0 {
			continue // never read before this Edit; rule doesn't apply
		}
		for _, c := range changes {
			if c > lastRead && c < edit && c > staleChangeMs {
				staleChangeMs = c
			}
		}
	}
	return staleChangeMs
}

// ───────────────────────────────────────────────────────────────────────
// R-test-stuck — same test name appears in Bash failure outputs 3+ times
// ───────────────────────────────────────────────────────────────────────

func (d *Detector) ruleTestStuck(ctx context.Context, sessionID string) ([]Anomaly, error) {
	// Heuristic test-name extraction: pull tokens matching common test
	// shapes (TestXxx for Go, test_xxx / it("...") / "describe(...)"
	// generally identifiable). Generic across languages: just look for
	// substrings of "FAIL" / "--- FAIL" / "Error: <name>".
	//
	// For Phase 1.7 first cut we use a coarser proxy: a Bash failure
	// where tool_result includes "FAIL" or "Error" appearing 3+ times for
	// what looks like the same test identifier (first whitespace token
	// after "FAIL").
	sql := fmt.Sprintf(
		`SELECT regexp_match(tool_result, 'FAIL[: ]\s*([A-Za-z0-9_./:-]+)')[1] AS test_id,
		        COUNT(*) AS n
		 FROM tma1_hook_events
		 WHERE session_id = '%s'
		   AND event_type = 'PostToolUseFailure' AND tool_name = 'Bash'
		   AND ts > now() - INTERVAL '30 minutes'
		   AND tool_result IS NOT NULL
		 GROUP BY test_id
		 HAVING test_id IS NOT NULL AND COUNT(*) >= 3
		 ORDER BY n DESC LIMIT 5`,
		escapeSQL(sessionID),
	)
	_, rows, err := d.client.Query(ctx, sql)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	var out []Anomaly
	for _, r := range rows {
		testID := stringAt(r, 0)
		n := intAt(r, 1)
		if testID == "" {
			continue
		}
		out = append(out, Anomaly{
			Kind:       "test_stuck",
			Severity:   SeverityMedium,
			Channel:    ChannelUserPromptSubmit,
			Evidence:   fmt.Sprintf("%s failed %d times in the last 30 minutes.", testID, n),
			Suggestion: fmt.Sprintf("Inspect the test fixture or mock for %s — three identical failures rarely fix themselves with another code tweak.", testID),
		})
	}
	return out, nil
}

// ───────────────────────────────────────────────────────────────────────
// R-human-during — human-attributed changes during this session
// ───────────────────────────────────────────────────────────────────────

func (d *Detector) ruleHumanModifiedDuringSession(ctx context.Context, sessionID string) ([]Anomaly, error) {
	winSQL := fmt.Sprintf(
		`SELECT CAST(MIN(ts) AS BIGINT) AS start_ms,
		        CAST(MAX(ts) AS BIGINT) AS end_ms,
		        MAX(cwd) AS cwd
		 FROM tma1_hook_events
		 WHERE session_id = '%s' AND cwd IS NOT NULL AND cwd != ''`,
		escapeSQL(sessionID),
	)
	_, rows, err := d.client.Query(ctx, winSQL)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	startMs := int64At(rows[0], 0)
	cwd := stringAt(rows[0], 2)
	if startMs == 0 || cwd == "" {
		return nil, nil
	}
	project := projectBasename(cwd)
	if project == "" {
		return nil, nil
	}

	// Suppress when changes are stale (> 30 min old) — Suppression principle.
	now := time.Now().UnixMilli()
	cutoff := now - 30*60*1000
	low := startMs
	if cutoff > low {
		low = cutoff
	}

	changesSQL := fmt.Sprintf(
		`SELECT file_path FROM tma1_external_changes
		 WHERE project = '%s'
		   AND attribution = 'human'
		   AND change_type IN ('file_modified','file_added')
		   AND ts > %d
		   AND file_path IS NOT NULL AND file_path != ''
		 GROUP BY file_path LIMIT 10`,
		escapeSQL(project), low,
	)
	_, fileRows, err := d.client.Query(ctx, changesSQL)
	if err != nil || len(fileRows) == 0 {
		return nil, err
	}
	files := make([]string, 0, len(fileRows))
	for _, r := range fileRows {
		if fp := stringAt(r, 0); fp != "" {
			files = append(files, fp)
		}
	}
	if len(files) == 0 {
		return nil, nil
	}
	preview := files
	if len(preview) > 3 {
		preview = preview[:3]
	}
	return []Anomaly{{
		Kind:         "human_modified_during_session",
		Severity:     SeverityMedium,
		Channel:      ChannelUserPromptSubmit,
		Evidence:     fmt.Sprintf("A human modified %d file(s) in this project during this session (e.g. %s).", len(files), strings.Join(preview, ", ")),
		Suggestion:   "Re-read the listed files before assuming your in-memory copy is current.",
		RelatedFiles: files,
	}}, nil
}

// ───────────────────────────────────────────────────────────────────────
// R-context-pressure — input_tokens accumulated over the session crosses
// a configurable threshold (default 100k = 50% of CC Sonnet 4's 200k)
// ───────────────────────────────────────────────────────────────────────

func (d *Detector) ruleContextPressure(ctx context.Context, sessionID string) ([]Anomaly, error) {
	threshold := contextPressureThreshold()
	sql := fmt.Sprintf(
		`SELECT COALESCE(SUM(input_tokens), 0)
		 FROM tma1_messages WHERE session_id = '%s'`,
		escapeSQL(sessionID),
	)
	_, rows, err := d.client.Query(ctx, sql)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	tokens := int64At(rows[0], 0)
	if tokens < threshold {
		return nil, nil
	}
	return []Anomaly{{
		Kind:       "context_pressure",
		Severity:   SeverityMedium,
		Channel:    ChannelUserPromptSubmit,
		Evidence:   fmt.Sprintf("Session has consumed %d input tokens (threshold %d).", tokens, threshold),
		Suggestion: "Summarise current progress, commit partial work, and start a fresh session before context window fills.",
	}}, nil
}

// contextPressureThreshold returns the configured token budget.
// `TMA1_CONTEXT_PRESSURE_THRESHOLD=<int>` env var overrides the default of
// 100,000 (≈ 50% of CC Sonnet 4's 200k context window).
func contextPressureThreshold() int64 {
	if v := os.Getenv("TMA1_CONTEXT_PRESSURE_THRESHOLD"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return 100_000
}

// ───────────────────────────────────────────────────────────────────────
// Helpers
// ───────────────────────────────────────────────────────────────────────

// isDocFile returns true for extensions where iterative edits are normal
// (writing prose / configs), so we don't flag "edited X 4 times".
func isDocFile(base string) bool {
	low := strings.ToLower(base)
	for _, ext := range []string{".md", ".json", ".txt", ".yml", ".yaml", ".toml"} {
		if strings.HasSuffix(low, ext) {
			return true
		}
	}
	return false
}

// basename returns the last path segment of p, used to keep the LIKE
// pattern short and likely to match how build tools print errors.
func basename(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}

// projectBasename: basename of the .git-preferred project root for cwd.
func projectBasename(cwd string) string {
	cwd = strings.TrimRight(cwd, "/")
	if cwd == "" {
		return ""
	}
	root := ResolveProjectRoot(cwd)
	if root == "" {
		return ""
	}
	if i := strings.LastIndex(root, "/"); i >= 0 {
		return root[i+1:]
	}
	return root
}

// ───────────────────────────────────────────────────────────────────────
// Cache + Suppression
// ───────────────────────────────────────────────────────────────────────

// anomalyCache holds two kinds of state per session:
//  1. items — short-lived (TTL) cache of Detect results to avoid re-running
//     SQL on every hook
//  2. history — long-lived emit ledger keyed by anomaly.stableKey() so the
//     Suppression layer can dedupe across turns.
type anomalyCache struct {
	mu      sync.Mutex
	items   map[string]anomalyCacheItem
	history map[string]map[string]emitState // sessionID → key → state
	ttl     time.Duration
}

type anomalyCacheItem struct {
	anomalies []Anomaly
	expires   time.Time
}

type emitState struct {
	FirstEmittedAt time.Time
	LastEmittedAt  time.Time
	EmitCount      int
}

func newAnomalyCache(ttl time.Duration) *anomalyCache {
	return &anomalyCache{
		items:   make(map[string]anomalyCacheItem),
		history: make(map[string]map[string]emitState),
		ttl:     ttl,
	}
}

func (c *anomalyCache) get(sessionID string) ([]Anomaly, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	item, ok := c.items[sessionID]
	if !ok {
		return nil, false
	}
	if time.Now().After(item.expires) {
		delete(c.items, sessionID)
		return nil, false
	}
	return item.anomalies, true
}

func (c *anomalyCache) set(sessionID string, anomalies []Anomaly) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[sessionID] = anomalyCacheItem{
		anomalies: anomalies,
		expires:   time.Now().Add(c.ttl),
	}
	// Opportunistic GC: drop any expired entries on each set so the map
	// can't grow unbounded in long-running servers.
	now := time.Now()
	for k, v := range c.items {
		if now.After(v.expires) {
			delete(c.items, k)
		}
	}
}

func (c *anomalyCache) invalidate(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, sessionID)
	// history is NOT cleared — suppression must survive cache wipes.
}

// suppress applies the Suppression principle: drop a candidate anomaly
// when the same key was emitted ≤ suppressionWindow ago. Updates emit
// history for kept anomalies so the next pass can dedupe.
//
// Suppression window is 10 minutes — long enough that repeated quick
// emits are silent, short enough that long-running issues come back.
const suppressionWindow = 10 * time.Minute

func (c *anomalyCache) suppress(sessionID string, candidates []Anomaly) []Anomaly {
	return c.suppressWithResolution(sessionID, candidates, nil)
}

// lastEmittedFor returns the emit state of a key for sessionID, if any.
// Read-only snapshot used by the Detector to decide whether to invoke a
// resolver — done outside the cache lock to keep SQL latency off the
// critical path.
func (c *anomalyCache) lastEmittedFor(sessionID, key string) (emitState, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	sh, ok := c.history[sessionID]
	if !ok {
		return emitState{}, false
	}
	st, ok := sh[key]
	return st, ok
}

// suppressWithResolution is the full suppression decision: like suppress,
// but a `resolved[stableKey]` entry skips the silence window and resets
// the emit state so the anomaly re-emits with a fresh FirstEmittedAt.
//
// resolved == nil collapses to the original suppression behaviour, which
// keeps callers that don't have rule-specific resolvers unchanged.
func (c *anomalyCache) suppressWithResolution(sessionID string, candidates []Anomaly, resolved map[string]bool) []Anomaly {
	if len(candidates) == 0 {
		return candidates
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	sessHistory, ok := c.history[sessionID]
	if !ok {
		sessHistory = make(map[string]emitState)
		c.history[sessionID] = sessHistory
	}

	now := time.Now()
	kept := make([]Anomaly, 0, len(candidates))
	for _, a := range candidates {
		key := a.stableKey()
		st, seen := sessHistory[key]
		if seen && resolved[key] {
			// Underlying condition was addressed since we last emitted —
			// treat as a brand new occurrence so the agent sees it again
			// even within the silence window.
			st = emitState{}
			seen = false
		}
		if seen && now.Sub(st.LastEmittedAt) < suppressionWindow {
			continue // recently emitted; stay silent
		}
		// emit (or re-emit after the window expired)
		if !seen {
			st.FirstEmittedAt = now
		}
		st.LastEmittedAt = now
		st.EmitCount++
		sessHistory[key] = st
		// Carry first-emission timestamp on the anomaly itself so consumers
		// (Stop block, /api/anomalies, dashboard) don't have to round-trip
		// through the cache. Stable across re-detections within the window.
		a.FirstEmittedAt = st.FirstEmittedAt
		kept = append(kept, a)
	}
	return kept
}
