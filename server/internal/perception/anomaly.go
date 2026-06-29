package perception

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tma1-ai/tma1/server/internal/pathutil"
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
	// submit, when set, dispatches per-anomaly INSERTs via a bounded
	// queue (Server wires it to a writeq.Sem). nil falls back to a raw
	// `go fn()` so tests + standalone callers still work.
	submit func(func()) bool
}

// SetSubmit installs a bounded dispatcher for the emit-log INSERTs. The
// returned bool from submit isn't consulted by the Detector — callers
// log drops via their own queue's counter.
func (d *Detector) SetSubmit(submit func(func()) bool) {
	d.submit = submit
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
//
// IMPORTANT: Detect is NON-IDEMPOTENT. Every call:
//   - Advances per-rule suppression state (`LastEmittedAt = now`)
//   - INSERTs a row into tma1_anomaly_emits
//   - Consumes the 10-minute silence window for any kept anomaly
//
// Only call Detect from PUSH-CHANNEL paths (hook handlers driving
// UserPromptSubmit / Stop / PreCompact / etc.). Pull-channel paths
// must use one of two side-effect-free alternatives, picked by intent:
//   - Current active anomalies ("what would the next hook surface?") →
//     DetectPreview. Runs the rules + resolvers but never writes
//     sessHistory or the emit log. Used by MCP `get_anomalies`.
//   - Past emitted history ("what has been told to agents already?") →
//     Bundler.ListEmittedAnomalies. Reads tma1_anomaly_emits directly.
//     Used by the dashboard `/api/anomalies` endpoint.
//
// Calling Detect from a pull path consumes suppression silently and
// weakens the next real Stop block — that's the bug both alternatives
// exist to prevent.
func (d *Detector) Detect(ctx context.Context, sessionID string) []Anomaly {
	if strings.TrimSpace(sessionID) == "" {
		return nil
	}
	if cached, ok := d.cache.get(sessionID); ok {
		return cached
	}

	// Run the rules concurrently. Each rule issues 1-3 GreptimeDB
	// queries and is independent of the others' output — the
	// suppression layer below merges per-key. Total wall-clock drops
	// from sum(rule latencies) to max(rule latency).
	//
	// Map iteration is non-deterministic (it always was), so we
	// collect under a mutex; suppression layer sorts by stableKey
	// anyway when deduping.
	type ruleResult struct {
		hits []Anomaly
		err  error
		name string
	}
	rules := d.rules()
	resCh := make(chan ruleResult, len(rules))
	var wg sync.WaitGroup
	for name, rule := range rules {
		wg.Add(1)
		go func(name string, rule ruleFunc) {
			defer wg.Done()
			hits, err := rule(ctx, sessionID)
			resCh <- ruleResult{hits: hits, err: err, name: name}
		}(name, rule)
	}
	wg.Wait()
	close(resCh)

	var candidates []Anomaly
	for r := range resCh {
		if r.err != nil {
			d.logger.Debug("anomaly rule failed", "rule", r.name, "session", sessionID, "err", r.err)
			continue
		}
		candidates = append(candidates, r.hits...)
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

// DetectPreview returns the anomalies a push-channel Detect() WOULD
// surface right now, without mutating any state. Use this from
// pull-channel paths (MCP get_anomalies) where calling Detect() would
// silently consume the 10-minute suppression window and weaken the
// very next real Stop block.
//
// Differences vs Detect:
//   - No 30s result cache lookup: each call re-runs the rules. Pull
//     paths are LLM-initiated and infrequent — caching would confuse
//     the "current state" semantics for the agent.
//   - Suppression decisions are read-only (sessHistory is read but not
//     written). EmitCount and LastEmittedAt are never advanced.
//   - No emit log row is INSERTed into tma1_anomaly_emits.
//   - Returned anomalies carry FirstEmittedAt ONLY when the key has
//     existing emit history; fresh candidates leave it zero so the
//     consumer can tell "agent has been told about this before" from
//     "agent has not seen this yet".
//
// Resolvers run as part of the preview (they're pure SQL reads). A
// resolved-since-last-emit candidate surfaces in the preview exactly
// as it would surface to the next real Detect.
func (d *Detector) DetectPreview(ctx context.Context, sessionID string) ([]Anomaly, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, nil
	}

	type ruleResult struct {
		hits []Anomaly
		err  error
		name string
	}
	rules := d.rules()
	resCh := make(chan ruleResult, len(rules))
	var wg sync.WaitGroup
	for name, rule := range rules {
		wg.Add(1)
		go func(name string, rule ruleFunc) {
			defer wg.Done()
			hits, err := rule(ctx, sessionID)
			resCh <- ruleResult{hits: hits, err: err, name: name}
		}(name, rule)
	}
	wg.Wait()
	close(resCh)

	var candidates []Anomaly
	for r := range resCh {
		if r.err != nil {
			d.logger.Debug("anomaly rule failed (preview)", "rule", r.name, "session", sessionID, "err", r.err)
			continue
		}
		candidates = append(candidates, r.hits...)
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	// Build resolved map exactly like suppressWithResolvers does, but
	// route through the *Preview suppression so sessHistory stays
	// untouched. Resolvers are SQL reads against tma1_hook_events; they
	// don't write any state.
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
	return d.cache.suppressWithResolutionPreview(sessionID, candidates, resolved), nil
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
// context_pressure has no resolver — it simply stops firing once occupancy
// recedes, e.g. after the agent compacts history, and otherwise re-emits
// after the silence window; human_modified_during_session decays purely
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
		escapeSQL(fp), escapeSQLLike(fp),
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
		// tool_result mentions the basename. Pull the failures' results
		// too so we can extract a line number for the suggestion --
		// utility upgrade per the dogfood retro: "re-read around the
		// error site" was vague; "re-read <file>:<line>" is one step.
		failuresSQL := fmt.Sprintf(
			`SELECT tool_result FROM tma1_hook_events
			 WHERE session_id = '%s'
			   AND event_type = 'PostToolUseFailure' AND tool_name = 'Bash'
			   AND ts BETWEEN %d AND %d
			   AND (tool_input LIKE '%%%s%%' OR tool_result LIKE '%%%s%%')
			 ORDER BY ts DESC LIMIT 5`,
			escapeSQL(sessionID),
			lastEdit.Add(-10*time.Minute).UnixMilli(),
			lastEdit.Add(10*time.Minute).UnixMilli(),
			escapeSQLLike(base), escapeSQLLike(base),
		)
		_, fRows, err := d.client.Query(ctx, failuresSQL)
		if err != nil || len(fRows) == 0 {
			continue
		}
		failCount := len(fRows)

		sev := SeverityMedium
		if failCount >= 3 {
			sev = SeverityHigh
		}

		// Best-effort line-number extraction from the most recent failure
		// (rows are ordered DESC by ts). Format `<basename>:<line>` is
		// emitted by virtually every compiler (gcc, clang, rustc, go,
		// tsc, pyright, ...); when the runtime is Python the format
		// becomes `File "path", line N` so we cover that too. When no
		// number is found we drop back to the original "around the
		// error site" phrasing.
		lineHint := extractErrorLine(stringAt(fRows[0], 0), base)

		// Action sentence: verb-first, names the file + the symptom + the next move.
		var suggestion string
		if lineHint != "" {
			suggestion = fmt.Sprintf(
				"Re-read %s near line %s before editing again -- %d build/test failure(s) since you started editing it.",
				shortPath(fp), lineHint, failCount,
			)
		} else {
			suggestion = fmt.Sprintf(
				"Re-read %s around the error site before editing again -- %d build/test failure(s) since you started editing it.",
				shortPath(fp), failCount,
			)
		}
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
		// Pull the most recent failure's stderr/result line so the
		// suggestion names the actual error the agent keeps re-running
		// into. Without this, the suggestion was "read the error
		// output" -- which sent the agent grepping for context the
		// rule already had access to.
		lastErr := d.latestErrorLineForPrefix(ctx, sessionID, prefix)
		var suggestion string
		if lastErr != "" {
			suggestion = fmt.Sprintf(
				"Stop retrying `%s` and address this error first: %s",
				prefix, lastErr,
			)
		} else {
			suggestion = fmt.Sprintf(
				"Read the actual error output from `%s` before retrying -- three consecutive identical failures means the root cause isn't fixed.",
				prefix,
			)
		}
		out = append(out, Anomaly{
			Kind:       "repeated_failed_build",
			Severity:   SeverityHigh,
			Channel:    ChannelStopBlock,
			Evidence:   fmt.Sprintf("`%s` failed %d times in the last 30 minutes.", prefix, n),
			Suggestion: suggestion,
		})
	}
	return out, nil
}

// latestErrorLineForPrefix returns a single representative error line
// from the most recent Bash failure matching prefix. Returns "" when
// the lookup fails or no row carries result text. The returned line is
// truncated via oneLine() so a multi-KB stack trace doesn't bloat the
// anomaly suggestion past the bundle budget.
func (d *Detector) latestErrorLineForPrefix(ctx context.Context, sessionID, prefix string) string {
	sql := fmt.Sprintf(
		`SELECT tool_result FROM tma1_hook_events
		 WHERE session_id = '%s'
		   AND event_type = 'PostToolUseFailure' AND tool_name = 'Bash'
		   AND substr(COALESCE(tool_command_prefix,
		                       regexp_match(tool_input, '"command":"([^"]+)"')[1]), 1, 60) = '%s'
		   AND ts > now() - INTERVAL '30 minutes'
		   AND tool_result IS NOT NULL
		 ORDER BY ts DESC LIMIT 1`,
		escapeSQL(sessionID), escapeSQL(prefix),
	)
	_, rows, err := d.client.Query(ctx, sql)
	if err != nil || len(rows) == 0 {
		return ""
	}
	raw := stringAt(rows[0], 0)
	if raw == "" {
		return ""
	}
	// Pick the first line that looks like an error message; fall back
	// to the last non-empty line if no marker matches. Keeps the
	// suggestion concrete instead of dragging "Compiling..." progress
	// noise into it.
	if line := firstErrorLine(raw); line != "" {
		return oneLine(line, 200)
	}
	return ""
}

// errorLineMarkers are case-insensitive substrings that indicate the
// line is an error / failure / panic message. Tuned for popular build
// tooling output across languages.
//
// Tradeoff in marker design: too narrow (e.g. "error:" only) misses
// real diagnostics like "FAILED test_x.py::..." or "undefined: bar";
// too loose (e.g. bare "error") happily grabs Makefile noise like
// "make: *** [build] Error 1" before the actual compiler line. We
// list both compiler-style colon variants and free-text language-
// specific tokens, ordered so the most discriminating match first.
var errorLineMarkers = []string{
	"error[", "error:",                     // compilers (rustc E-codes, gcc/clang colon)
	"failed:", "failed ", "failure:",       // pytest "FAILED ", generic
	"--- fail", "fail:",                    // go test
	"panic:", "fatal:",                     // go panic, generic fatal
	"undefined:", "undefined symbol",       // linker / compiler "undefined: X"
	"cannot find ", "no such file",         // missing-symbol / missing-file diagnostics
	"assertionerror", "typeerror",          // python / js runtime types
	"syntaxerror", "valueerror", "runtimeerror",
	"exception", "uncaught",                // generic runtime
}

// firstErrorLine scans raw line-by-line looking for the first line
// that contains an errorLineMarkers substring. Returns "" if none
// matches. Empty / whitespace-only lines are skipped.
func firstErrorLine(raw string) string {
	if raw == "" {
		return ""
	}
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		low := strings.ToLower(trimmed)
		for _, m := range errorLineMarkers {
			if strings.Contains(low, m) {
				return trimmed
			}
		}
	}
	return ""
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

// testRunnerPrefixes are the canonical prefixes for test-runner
// invocations across the languages we know agents work in. A Bash
// command whose first token (or first two for "npm run") matches one
// of these is treated as a test run. Listed lowercase; matching is
// LIKE against tool_command_prefix which is already lowercased at
// ingest -- nope, it isn't; we use the LIKE escape pattern instead.
//
// Adding a runner: append a pattern that, when used in
// `tool_command_prefix LIKE '<pattern>%'`, identifies a test
// invocation. The trailing space-or-end is implicit in the 60-char
// prefix store. GreptimeDB's LIKE escape char is backslash, which
// sqlutil.EscapeLike emits inline — no explicit ESCAPE clause needed.
var testRunnerPrefixes = []string{
	"go test",
	"cargo test",
	"cargo nextest",
	"pytest",
	"py.test",
	"python -m pytest",
	"python -m unittest",
	"npm test",
	"npm run test",
	"yarn test",
	"pnpm test",
	"pnpm run test",
	"jest",
	"npx jest",
	"vitest",
	"npx vitest",
	"mocha",
	"npx mocha",
	"phpunit",
	"vendor/bin/phpunit",
	"rspec",
	"bundle exec rspec",
	"mix test",
}

func (d *Detector) ruleTestStuck(ctx context.Context, sessionID string) ([]Anomaly, error) {
	// Cross-language: instead of regex-extracting a test name from
	// tool_result (which only worked for Go-style "--- FAIL: TestX"),
	// we now group Bash failures by the leading test-runner command
	// across all supported runners. Cost: we lose the specific test ID
	// in the evidence; gain: the rule actually fires in pytest / jest /
	// cargo-test / mocha / vitest / rspec / phpunit / mix-test projects
	// instead of being silently dead.
	clauses := make([]string, 0, len(testRunnerPrefixes))
	for _, p := range testRunnerPrefixes {
		clauses = append(clauses,
			fmt.Sprintf("tool_command_prefix LIKE '%s%%'", escapeSQLLike(p)))
	}
	sql := fmt.Sprintf(
		`SELECT tool_command_prefix AS prefix, COUNT(*) AS n
		 FROM tma1_hook_events
		 WHERE session_id = '%s'
		   AND event_type = 'PostToolUseFailure' AND tool_name = 'Bash'
		   AND ts > now() - INTERVAL '30 minutes'
		   AND tool_command_prefix IS NOT NULL
		   AND (%s)
		 GROUP BY tool_command_prefix
		 HAVING COUNT(*) >= 3
		 ORDER BY n DESC LIMIT 5`,
		escapeSQL(sessionID),
		strings.Join(clauses, " OR "),
	)
	_, rows, err := d.client.Query(ctx, sql)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	var out []Anomaly
	for _, r := range rows {
		prefix := stringAt(r, 0)
		n := intAt(r, 1)
		if prefix == "" {
			continue
		}
		out = append(out, Anomaly{
			Kind:       "test_stuck",
			Severity:   SeverityMedium,
			Channel:    ChannelUserPromptSubmit,
			Evidence:   fmt.Sprintf("`%s` failed %d times in the last 30 minutes.", prefix, n),
			Suggestion: fmt.Sprintf("Inspect the test fixture or mock for the failing case in `%s` — three identical test-run failures rarely fix themselves with another code tweak.", prefix),
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
// R-context-pressure — current context-window OCCUPANCY crosses a
// configurable threshold (default 700k ≈ 70% of a 1M-token window).
//
// Occupancy is a point-in-time measurement, NOT a running total. Each
// main-loop request resends the whole conversation, so one request's
// (input_tokens + cache_read_tokens + cache_creation_tokens) is the size of
// the prompt currently in the window. Two reasons the old SUM(input_tokens)
// was wrong: (1) it accumulated across every turn, so it crossed any fixed
// threshold on long sessions regardless of real occupancy and never receded
// after the agent compacted history; (2) input_tokens alone undercounts the
// live prompt badly, because prompt caching moves most of the prefix into
// cache_read_tokens — the uncached remainder can be a few K while the window
// is nearly full. We take the MAX over the most recent usage-bearing rows so
// an occasional small subagent/sidechain request can't mask the main-loop
// occupancy, while the value still recedes once history is compacted.
// ───────────────────────────────────────────────────────────────────────

func (d *Detector) ruleContextPressure(ctx context.Context, sessionID string) ([]Anomaly, error) {
	threshold := contextPressureThreshold()
	sql := fmt.Sprintf(
		`SELECT COALESCE(MAX(occ), 0) FROM (
		   SELECT COALESCE(input_tokens, 0) + COALESCE(cache_read_tokens, 0) + COALESCE(cache_creation_tokens, 0) AS occ
		   FROM tma1_messages
		   WHERE session_id = '%s'
		   ORDER BY ts DESC
		   LIMIT 20
		 ) AS recent`,
		escapeSQL(sessionID),
	)
	_, rows, err := d.client.Query(ctx, sql)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	occupancy := int64At(rows[0], 0)
	if occupancy < threshold {
		return nil, nil
	}
	return []Anomaly{{
		Kind:       "context_pressure",
		Severity:   SeverityMedium,
		Channel:    ChannelUserPromptSubmit,
		Evidence:   fmt.Sprintf("Context window is ~%d tokens full (threshold %d).", occupancy, threshold),
		Suggestion: "Summarise current progress, commit partial work, and start a fresh session before context window fills.",
	}}, nil
}

// contextPressureThreshold returns the configured occupancy threshold, in
// tokens of the current context window. `TMA1_CONTEXT_PRESSURE_THRESHOLD=<int>`
// overrides the default of 700,000 (≈70% of a 1M-token window — the size of
// current frontier coding models: Claude Opus/Sonnet, Gemini, GPT-4.1-class).
// Agents on a smaller window (e.g. a 200k model) should set the env var lower.
func contextPressureThreshold() int64 {
	if v := os.Getenv("TMA1_CONTEXT_PRESSURE_THRESHOLD"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return 700_000
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
// Uses pathutil so a Windows agent's file_path lifts the right name.
func basename(p string) string {
	return pathutil.Basename(p)
}

// extractErrorLine pulls a line number from a compiler / runtime error
// message that mentions base. Cross-language patterns:
//
//	gcc/clang/rustc/go/tsc/pyright: "foo.go:42:7: error: ..." or
//	                                  "foo.go:42: undefined: bar"
//	python tracebacks:               'File "foo.py", line 42, in ...'
//
// Returns "" when no line number is recognisable. The base argument
// scopes the search so a compile error in `bar.go` doesn't leak into
// a suggestion about `foo.go`.
//
// Pure function, easy to unit-test against synthetic error snippets.
func extractErrorLine(result, base string) string {
	if result == "" || base == "" {
		return ""
	}
	// Pattern 1: "<...path...><base>:<line>" -- the dominant compiler
	// shape. We anchor to base + ":" so we don't pick up the line
	// number of an unrelated file mentioned in the same blob.
	if reBase := regexpQuoteBaseColon(base); reBase != nil {
		if m := reBase.FindStringSubmatch(result); len(m) > 1 {
			return m[1]
		}
	}
	// Pattern 2: Python traceback `File "...<base>", line N`.
	if rePy := regexpQuoteBasePyTraceback(base); rePy != nil {
		if m := rePy.FindStringSubmatch(result); len(m) > 1 {
			return m[1]
		}
	}
	return ""
}

// Pre-built regex cache keyed by base would matter at scale; the rule
// fires at most O(edits-per-session) times per Detect, so a tiny
// regex compile per call is fine and keeps the lifecycle simple.
func regexpQuoteBaseColon(base string) *regexp.Regexp {
	pat := `\b` + regexp.QuoteMeta(base) + `:(\d+)`
	re, err := regexp.Compile(pat)
	if err != nil {
		return nil
	}
	return re
}

func regexpQuoteBasePyTraceback(base string) *regexp.Regexp {
	pat := `File\s+"[^"]*` + regexp.QuoteMeta(base) + `",\s*line\s+(\d+)`
	re, err := regexp.Compile(pat)
	if err != nil {
		return nil
	}
	return re
}

// projectBasename: basename of the .git-preferred project root for cwd.
func projectBasename(cwd string) string {
	cwd = strings.TrimRight(cwd, `/\`)
	if cwd == "" {
		return ""
	}
	root := ResolveProjectRoot(cwd)
	if root == "" {
		return ""
	}
	return pathutil.Basename(root)
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

// historyMaxAge bounds how long a session's emit history is retained
// without any new activity. Beyond this, the session is considered dead
// and its history evicted so the map can't grow unbounded across a
// long-running server.
//
// Set generously (24h) so a session that resumes after a workday gap
// still benefits from suppression. Re-emit after eviction is identical
// to a brand-new session -- the worst case is one duplicate anomaly,
// which is far cheaper than a leak.
const historyMaxAge = 24 * time.Hour

// historyGCMinSize is the threshold below which evictHistoryLocked
// short-circuits. Keeps the per-call cost negligible for small servers
// while still bounding the map when it grows.
const historyGCMinSize = 64

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

// suppressWithResolutionPreview applies the same per-key suppression
// decision as suppressWithResolution, but is fully read-only:
//   - sessHistory entries are inspected but NEVER written.
//   - eviction is skipped (eviction would also be a mutation).
//   - the returned slice has FirstEmittedAt populated from existing
//     history where present; otherwise zero. The preview path never
//     materialises a new emit timestamp.
//
// This is the path pull-channel callers (MCP get_anomalies, dashboards
// that need "what would the next hook see") MUST use. Calling
// suppressWithResolution from a pull path advances LastEmittedAt for
// the agent's benefit and silently consumes the 10-minute window,
// weakening the very next push-channel Stop block.
func (c *anomalyCache) suppressWithResolutionPreview(sessionID string, candidates []Anomaly, resolved map[string]bool) []Anomaly {
	if len(candidates) == 0 {
		return candidates
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	// nil-map reads are safe; sessHistory absence simply means no key was
	// ever emitted for this session.
	sessHistory := c.history[sessionID]

	now := time.Now()
	kept := make([]Anomaly, 0, len(candidates))
	for _, a := range candidates {
		key := a.stableKey()
		st, seen := sessHistory[key]
		if seen && resolved[key] {
			// Mirror real suppression: a resolved key behaves as if never
			// emitted — agent should see it again right away.
			st = emitState{}
			seen = false
		}
		if seen && now.Sub(st.LastEmittedAt) < suppressionWindow {
			continue
		}
		// Surface the prior FirstEmittedAt when this anomaly already has
		// emit history — consumers reading "outstanding since X" should
		// still see the original surfacing time. Fresh anomalies leave
		// FirstEmittedAt zero; the preview path deliberately does NOT
		// invent a new emit moment.
		if seen {
			a.FirstEmittedAt = st.FirstEmittedAt
		}
		kept = append(kept, a)
	}
	return kept
}

// suppressWithResolution is the full suppression decision: like suppress,
// but a `resolved[stableKey]` entry skips the silence window and resets
// the emit state so the anomaly re-emits with a fresh FirstEmittedAt.
//
// resolved == nil collapses to the original suppression behaviour, which
// keeps callers that don't have rule-specific resolvers unchanged.
//
// NOTE: this MUTATES sessHistory (records LastEmittedAt = now and bumps
// EmitCount) and triggers eviction. Only push-channel callers (real
// hooks invoking Detector.Detect) should reach this. Pull-channel
// callers (MCP get_anomalies, dashboard polls) MUST use
// suppressWithResolutionPreview instead.
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
	c.evictHistoryLocked(now)
	return kept
}

// evictHistoryLocked drops sessions whose most recent emit landed more
// than historyMaxAge ago. Runs under c.mu (callers already hold it).
//
// Strategy: walk the whole map. The map is small in practice -- a few
// hundred sessions at most on a busy single-developer machine -- so a
// linear scan is cheaper than maintaining a heap or sorted structure.
// historyGCMinSize gates the scan so the common case (fresh server,
// dozens of sessions) pays nothing.
func (c *anomalyCache) evictHistoryLocked(now time.Time) {
	if len(c.history) < historyGCMinSize {
		return
	}
	cutoff := now.Add(-historyMaxAge)
	for sid, keys := range c.history {
		newest := newestEmit(keys)
		if !newest.IsZero() && newest.Before(cutoff) {
			delete(c.history, sid)
		}
	}
}

// newestEmit returns the most recent LastEmittedAt across all keys for a
// session, or the zero time when there are none (treated as "no signal,
// don't evict yet").
func newestEmit(keys map[string]emitState) time.Time {
	var t time.Time
	for _, st := range keys {
		if st.LastEmittedAt.After(t) {
			t = st.LastEmittedAt
		}
	}
	return t
}
