package perception

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
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
	identity       string
}

// stableKey is a session-scoped identity for an anomaly, used by the
// Suppression layer to dedupe across turns.
func (a Anomaly) stableKey() string {
	files := append([]string{}, a.RelatedFiles...)
	sort.Strings(files)
	return a.Kind + "|" + a.Severity + "|" + strings.Join(files, ",") + "|" + a.identity
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

// Detect emits eligible findings across all channels, advancing suppression and
// recording new emissions. Only push paths should call it; use DetectPreview
// for current findings or ListEmittedAnomalies for emission history.
func (d *Detector) Detect(ctx context.Context, sessionID string) []Anomaly {
	return d.DetectByChannel(ctx, sessionID, "")
}

// DetectByChannel applies suppression only after selecting the delivery channel.
// A Stop or PostToolUse hook must not consume a pending next-prompt finding.
func (d *Detector) DetectByChannel(ctx context.Context, sessionID, channel string) []Anomaly {
	if strings.TrimSpace(sessionID) == "" {
		return nil
	}
	candidates, cached := d.cache.get(sessionID)
	if !cached {
		type ruleResult struct {
			hits []Anomaly
			err  error
			name string
		}
		rules := d.rules()
		results := make(chan ruleResult, len(rules))
		var wg sync.WaitGroup
		for name, rule := range rules {
			wg.Add(1)
			go func(name string, rule ruleFunc) {
				defer wg.Done()
				hits, err := rule(ctx, sessionID)
				results <- ruleResult{hits: hits, err: err, name: name}
			}(name, rule)
		}
		wg.Wait()
		close(results)
		for result := range results {
			if result.err != nil {
				d.logger.Debug("anomaly rule failed", "rule", result.name, "session", sessionID, "err", result.err)
				continue
			}
			candidates = append(candidates, result.hits...)
		}
		d.cache.set(sessionID, candidates)
	}
	selected := make([]Anomaly, 0, len(candidates))
	for _, candidate := range candidates {
		if channel == "" || candidate.Channel == channel {
			selected = append(selected, candidate)
		}
	}
	kept := d.suppressWithResolvers(ctx, sessionID, selected)
	d.logEmits(sessionID, kept)
	return kept
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
		"repeated_failed_build":            d.ruleRepeatedFailedBuild,
		"stale_file_view":                  d.ruleStaleFileView,
		"external_modified_during_session": d.ruleExternalModifiedDuringSession,
		"context_pressure":                 d.ruleContextPressure,
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
// after the silence window; external_modified_during_session decays purely
// by time).
func (d *Detector) resolverFor(kind string) resolverFunc {
	switch kind {
	case "stale_file_view":
		return d.resolverReReadHappened

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

// resolverReReadHappened — R-stale-view resolves when the agent completes a
// PostToolUse Read of the related file after lastEmitted. The same Read
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
		   AND event_type = 'PostToolUse' AND tool_name = 'Read'
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

// Successful runs clear only the same full command in the same working directory.
// Prefixes cannot identify commands: different test targets often share them.
func (d *Detector) ruleRepeatedFailedBuild(ctx context.Context, sessionID string) ([]Anomaly, error) {
	sql := fmt.Sprintf(`SELECT tool_input, cwd, event_type, tool_result, CAST(ts AS BIGINT) AS ts_ms
 FROM tma1_hook_events WHERE session_id = '%s' AND tool_name = 'Bash'
 AND event_type IN ('PostToolUse', 'PostToolUseFailure')
 AND ts > now() - INTERVAL '30 minutes' ORDER BY ts DESC LIMIT 1000`, escapeSQL(sessionID))
	_, rows, err := d.client.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	return repeatedBuildFailures(rows), nil
}

func repeatedBuildFailures(rows [][]any) []Anomaly {
	type run struct {
		command, cwd, diagnostic string
		failures                 int
		closed                   bool
		lastFailure              int64
	}
	runs := map[string]*run{}
	for _, row := range rows {
		var input struct {
			Command string `json:"command"`
		}
		if json.Unmarshal([]byte(stringAt(row, 0)), &input) != nil || !isBuildCommand(input.Command) {
			continue
		}
		cwd := stringAt(row, 1)
		if cwd == "" {
			continue
		}
		key := cwd + "\x00" + input.Command
		r := runs[key]
		if r == nil {
			r = &run{command: input.Command, cwd: cwd}
			runs[key] = r
		}
		if r.closed {
			continue
		}
		if stringAt(row, 2) == "PostToolUse" {
			// Equal timestamps do not establish whether the success preceded a failure.
			if r.lastFailure == int64At(row, 4) {
				r.failures = 0
			}
			r.closed = true
			continue
		}
		if r.failures == 0 {
			r.diagnostic = firstErrorLine(stringAt(row, 3))
		}
		r.lastFailure = int64At(row, 4)
		r.failures++
	}
	var out []Anomaly
	for key, r := range runs {
		if r.failures < 3 {
			continue
		}
		suggestion := "Compare the latest diagnostic with the changes since the previous run before retrying. Repeated failures alone do not establish a loop or its cause."
		if r.diagnostic != "" {
			suggestion = "Latest diagnostic: " + oneLine(r.diagnostic, 200) + ". " + suggestion
		}
		out = append(out, Anomaly{Kind: "repeated_failed_build", Severity: SeverityMedium, Channel: ChannelUserPromptSubmit,
			Evidence:   fmt.Sprintf("`%s` in %s has %d observed failures in the last 30 minutes since its most recent observed success (if any).", oneLine(r.command, 160), r.cwd, r.failures),
			Suggestion: suggestion, identity: key})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].identity < out[j].identity })
	if len(out) > 5 {
		out = out[:5]
	}
	return out
}

func isBuildCommand(command string) bool {
	command = strings.TrimSpace(command)
	prefixes := append([]string{"go build", "cargo build", "npm run build", "pnpm run build", "yarn build", "make build", "make test", "make check", "make lint", "cmake --build"}, testRunnerPrefixes...)
	for _, prefix := range prefixes {
		if command == prefix || strings.HasPrefix(command, prefix+" ") || strings.HasPrefix(command, prefix+"\t") {
			return true
		}
	}
	return false
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
	"error[", "error:", // compilers (rustc E-codes, gcc/clang colon)
	"failed:", "failed ", "failure:", // pytest "FAILED ", generic
	"--- fail", "fail:", // go test
	"panic:", "fatal:", // go panic, generic fatal
	"undefined:", "undefined symbol", // linker / compiler "undefined: X"
	"cannot find ", "no such file", // missing-symbol / missing-file diagnostics
	"assertionerror", "typeerror", // python / js runtime types
	"syntaxerror", "valueerror", "runtimeerror",
	"exception", "uncaught", // generic runtime
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
	//   T2 — foo.go changes externally          (T2 > T1)
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
		   AND ((event_type = 'PostToolUse' AND tool_name = 'Read')
             OR (event_type = 'PreToolUse' AND tool_name IN ('Edit','MultiEdit','Write')))
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
		   AND attribution IN ('human','unknown')
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
				"You edited %s after it changed externally at %s, without re-reading first.",
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
//   - reads:   successful PostToolUse Read events
//   - edits:   PreToolUse Edit/MultiEdit/Write events
//   - changes: modifications outside observed agent writes on the same file
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
	for _, read := range reads {
		if read > staleChangeMs {
			return 0
		}
	}
	return staleChangeMs
}

// ───────────────────────────────────────────────────────────────────────
// R-test-stuck — same test name appears in Bash failure outputs 3+ times
// ───────────────────────────────────────────────────────────────────────

// Recognized test invocations; wrapped shell scripts are not inferred.
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

func (d *Detector) ruleExternalModifiedDuringSession(ctx context.Context, sessionID string) ([]Anomaly, error) {
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
		   AND attribution IN ('human','unknown')
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
		Kind:         "external_modified_during_session",
		Severity:     SeverityMedium,
		Channel:      ChannelUserPromptSubmit,
		Evidence:     fmt.Sprintf("%d file(s) changed outside observed agent writes during this session (e.g. %s).", len(files), strings.Join(preview, ", ")),
		Suggestion:   "Re-read the listed files before assuming your in-memory copy is current.",
		RelatedFiles: files,
	}}, nil
}

// Usage rows can be streaming deltas. A large input sample is advisory evidence,
// not an exact occupancy measurement or a percentage of the model's window.
func (d *Detector) ruleContextPressure(ctx context.Context, sessionID string) ([]Anomaly, error) {
	threshold := contextPressureThreshold()
	_, sources, err := d.client.Query(ctx, fmt.Sprintf(`SELECT agent_source FROM tma1_hook_events
 WHERE session_id = '%s' AND agent_source IS NOT NULL AND agent_source <> '' ORDER BY ts DESC LIMIT 1`, escapeSQL(sessionID)))
	if err != nil || len(sources) == 0 {
		return nil, err
	}
	input := "COALESCE(input_tokens, 0)"
	switch stringAt(sources[0], 0) {
	case "codex": // Codex input already includes cached tokens.
	case "claude_code", "opencode":
		input += " + COALESCE(cache_read_tokens, 0) + COALESCE(cache_creation_tokens, 0)"
	default:
		return nil, nil
	}
	sql := fmt.Sprintf(`SELECT %s AS occ FROM tma1_messages
 WHERE session_id = '%s' AND %s > 0
 AND ts > now() - INTERVAL '30 minutes' ORDER BY ts DESC LIMIT 1`, input, escapeSQL(sessionID), input)
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
		Evidence:   fmt.Sprintf("Latest nonzero input-usage sample is %d tokens (configured threshold %d); the model context limit is not inferred.", occupancy, threshold),
		Suggestion: "Check the agent context indicator. If it is near its limit, use the agent’s compaction workflow; input usage alone does not require a new session or a commit.",
	}}, nil
}

// contextPressureThreshold is an absolute input-usage threshold, not a model limit.
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
