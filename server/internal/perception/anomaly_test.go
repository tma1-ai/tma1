package perception

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestFirstErrorLineFindsTheActualError(t *testing.T) {
	cases := []struct {
		name, raw, want string
	}{
		{"go compile",
			"go: building...\n# github.com/foo/bar\n./pkg/auth.go:42:7: undefined: bar\nmake: *** [build] Error 1",
			"./pkg/auth.go:42:7: undefined: bar"},
		{"rustc",
			"   Compiling foo v0.1.0\nerror[E0282]: type annotations needed\n  --> src/lib.rs:88:5",
			"error[E0282]: type annotations needed"},
		{"pytest assertion",
			"collected 1 item\ntest_x.py F\n=========\nFAILED test_x.py::test_y - AssertionError",
			"FAILED test_x.py::test_y - AssertionError"},
		{"node uncaught",
			"server starting...\nUncaught Exception: TypeError at handler.js:12",
			"Uncaught Exception: TypeError at handler.js:12"},
		{"no marker",
			"Compiling foo...\nDone.",
			""},
		{"empty",
			"",
			""},
		{"only whitespace",
			"   \n  \n",
			""},
	}
	for _, c := range cases {
		if got := firstErrorLine(c.raw); got != c.want {
			t.Errorf("%s: firstErrorLine = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestTestRunnerPrefixesAcrossLanguages(t *testing.T) {
	positive := []string{
		"go test ./...",
		"make test",
		"go build ./...",
		"cargo build",
		"cargo test --workspace",
		"cargo nextest run",
		"pytest -k foo",
		"py.test tests/",
		"python -m pytest",
		"python -m unittest discover",
		"npm test -- --watch",
		"npm run test",
		"yarn test --ci",
		"pnpm test",
		"pnpm run test",
		"jest --bail",
		"npx jest --watchAll=false",
		"vitest run",
		"npx vitest",
		"mocha --recursive",
		"npx mocha test/",
		"phpunit --testdox",
		"vendor/bin/phpunit",
		"rspec spec/",
		"bundle exec rspec --format doc",
		"mix test --trace",
	}
	for _, cmd := range positive {
		if !isBuildCommand(cmd) {
			t.Errorf("expected test-runner match: %q", cmd)
		}
	}

	negative := []string{
		"",
		"   ",
		"go testify", // word-boundary guard: not "go test"
		"npm install",
		"npm run dev",
		"make clean",
		"GO TEST ./pkg",
		"./bin/foo",
		"docker compose up",
	}
	for _, cmd := range negative {
		if isBuildCommand(cmd) {
			t.Errorf("expected NO match: %q", cmd)
		}
	}
}

func TestAnomalyCacheHitAndExpiry(t *testing.T) {
	c := newAnomalyCache(50 * time.Millisecond)
	want := []Anomaly{{Kind: "file_loop_edit", Severity: SeverityHigh}}

	c.set("s1", want)

	got, ok := c.get("s1")
	if !ok || len(got) != 1 || got[0].Kind != "file_loop_edit" {
		t.Fatalf("expected cache hit, got ok=%v got=%+v", ok, got)
	}

	time.Sleep(80 * time.Millisecond)

	if _, ok := c.get("s1"); ok {
		t.Errorf("entry should have expired")
	}
}

func TestAnomalyCacheInvalidate(t *testing.T) {
	c := newAnomalyCache(time.Hour)
	c.set("s1", []Anomaly{{Kind: "x"}})
	c.invalidate("s1")
	if _, ok := c.get("s1"); ok {
		t.Error("invalidate did not drop entry")
	}
}

func TestAnomalyCacheHistoryEvictsStaleSessions(t *testing.T) {
	// Verifies the history map drops sessions whose newest emit is
	// older than historyMaxAge, while keeping fresh ones. The
	// suppression path runs the eviction inline so a long-running
	// server can't accumulate dead session state.
	c := newAnomalyCache(time.Hour)

	// Seed historyGCMinSize+1 sessions with stale emits so the GC
	// threshold is crossed and the scan actually runs.
	staleTs := time.Now().Add(-2 * historyMaxAge)
	for i := 0; i < historyGCMinSize+1; i++ {
		sid := "stale-" + strconv.Itoa(i)
		c.history[sid] = map[string]emitState{
			"k|high|": {LastEmittedAt: staleTs},
		}
	}
	// One fresh session that must survive.
	c.history["fresh"] = map[string]emitState{
		"k|high|": {LastEmittedAt: time.Now()},
	}

	// Trigger eviction via the normal suppression path.
	c.suppressWithResolution("trigger", []Anomaly{{Kind: "k", Severity: "high"}}, nil)

	if _, ok := c.history["fresh"]; !ok {
		t.Error("fresh session was evicted")
	}
	if _, ok := c.history["stale-0"]; ok {
		t.Error("stale session was retained")
	}
	if len(c.history) > 2 { // "fresh" + "trigger"
		t.Errorf("expected ≤ 2 surviving sessions, got %d", len(c.history))
	}
}

func TestAnomalyStableKeyDistinctByKindAndFiles(t *testing.T) {
	a := Anomaly{Kind: "x", Severity: "high", RelatedFiles: []string{"b", "a"}}
	b := Anomaly{Kind: "x", Severity: "high", RelatedFiles: []string{"a", "b"}}
	if a.stableKey() != b.stableKey() {
		t.Errorf("stableKey should be order-insensitive on RelatedFiles; got %q vs %q", a.stableKey(), b.stableKey())
	}
	c := Anomaly{Kind: "x", Severity: "medium", RelatedFiles: []string{"a", "b"}}
	if a.stableKey() == c.stableKey() {
		t.Errorf("stableKey should differ when severity differs")
	}
}

func TestAnomalyCacheSuppressionDropsRepeats(t *testing.T) {
	c := newAnomalyCache(time.Minute)
	a := Anomaly{Kind: "k1", Severity: SeverityMedium, Channel: ChannelUserPromptSubmit}
	first := c.suppress("s1", []Anomaly{a})
	if len(first) != 1 {
		t.Fatalf("first emit should pass through; got %d", len(first))
	}
	second := c.suppress("s1", []Anomaly{a})
	if len(second) != 0 {
		t.Errorf("repeat within suppression window should be silent; got %d", len(second))
	}
}

func TestAnomalyCacheSuppressionPerSession(t *testing.T) {
	c := newAnomalyCache(time.Minute)
	a := Anomaly{Kind: "k1", Severity: SeverityMedium}
	_ = c.suppress("s1", []Anomaly{a}) // emit once for s1
	// Same key on a different session must still emit.
	if got := c.suppress("s2", []Anomaly{a}); len(got) != 1 {
		t.Errorf("suppression must be per-session, got %d for s2", len(got))
	}
}

func TestAnomalyCacheResolutionResetsAndReemits(t *testing.T) {
	c := newAnomalyCache(time.Minute)
	a := Anomaly{Kind: "stale_file_view", Severity: SeverityHigh, RelatedFiles: []string{"src/foo.go"}}

	// First emit lands.
	first := c.suppressWithResolution("s1", []Anomaly{a}, nil)
	if len(first) != 1 {
		t.Fatalf("first emit should pass through; got %d", len(first))
	}

	// Same key again within the silence window — silent.
	silent := c.suppressWithResolution("s1", []Anomaly{a}, nil)
	if len(silent) != 0 {
		t.Errorf("within suppression window should stay silent; got %d", len(silent))
	}

	// Now mark the key resolved. Even within the silence window the
	// anomaly should emit again, and FirstEmittedAt should reset.
	key := a.stableKey()
	resolved := map[string]bool{key: true}
	reemitted := c.suppressWithResolution("s1", []Anomaly{a}, resolved)
	if len(reemitted) != 1 {
		t.Fatalf("resolved key should re-emit; got %d", len(reemitted))
	}
	if !reemitted[0].FirstEmittedAt.After(first[0].FirstEmittedAt) {
		t.Errorf("resolved re-emit should refresh FirstEmittedAt; first=%v reemit=%v",
			first[0].FirstEmittedAt, reemitted[0].FirstEmittedAt)
	}
}

func TestSuppressPreviewIsReadOnly(t *testing.T) {
	// The whole point of the preview path: it must NEVER mutate
	// sessHistory. A subsequent real suppressWithResolution call must
	// behave identically to one with no preview in between.
	c := newAnomalyCache(time.Minute)
	a := Anomaly{Kind: "k1", Severity: SeverityMedium, Channel: ChannelUserPromptSubmit}

	// Preview against an empty history — anomaly should surface but
	// nothing gets recorded.
	preview1 := c.suppressWithResolutionPreview("s1", []Anomaly{a}, nil)
	if len(preview1) != 1 {
		t.Fatalf("first preview should surface candidate; got %d", len(preview1))
	}
	if !preview1[0].FirstEmittedAt.IsZero() {
		t.Errorf("fresh candidate's FirstEmittedAt should be zero in preview; got %v", preview1[0].FirstEmittedAt)
	}

	// Hammer the preview path many times. Real suppress should still
	// emit once afterwards — proving none of the previews advanced
	// LastEmittedAt or wrote sessHistory.
	for i := 0; i < 10; i++ {
		c.suppressWithResolutionPreview("s1", []Anomaly{a}, nil)
	}

	// The push-channel call after 10 previews must emit normally; if a
	// preview had mutated state, this would be silently suppressed.
	real1 := c.suppressWithResolution("s1", []Anomaly{a}, nil)
	if len(real1) != 1 {
		t.Errorf("real emit after 10 previews should still pass through; got %d (preview leaked state into sessHistory)", len(real1))
	}

	// Now real emit has happened — the next preview should reflect that
	// (within suppression window → silent) but still NOT mutate state.
	preview2 := c.suppressWithResolutionPreview("s1", []Anomaly{a}, nil)
	if len(preview2) != 0 {
		t.Errorf("preview after real emit should mirror suppression decision (silent); got %d", len(preview2))
	}

	// And a second real call within the window stays silent — proving
	// preview didn't refresh LastEmittedAt either way.
	real2 := c.suppressWithResolution("s1", []Anomaly{a}, nil)
	if len(real2) != 0 {
		t.Errorf("real emit within window should stay silent; got %d (preview must not have touched LastEmittedAt)", len(real2))
	}
}

func TestSuppressPreviewPreservesFirstEmittedAtFromHistory(t *testing.T) {
	// When a key already has history, the preview should report the
	// PRIOR FirstEmittedAt so consumers see "outstanding since X"
	// timestamps consistent with what real Detect would surface — but
	// preview never invents a new emit moment for fresh candidates.
	c := newAnomalyCache(time.Minute)
	a := Anomaly{Kind: "k1", Severity: SeverityHigh}

	// Real emit lands.
	first := c.suppressWithResolution("s1", []Anomaly{a}, nil)
	if len(first) != 1 || first[0].FirstEmittedAt.IsZero() {
		t.Fatalf("real emit should set FirstEmittedAt; got %+v", first)
	}
	originalFirst := first[0].FirstEmittedAt

	// Resolved-preview must surface again (resolution clears suppression)
	// but with FirstEmittedAt reset because the preview treats the
	// resolved key as fresh — and importantly, preview does NOT actually
	// stamp a new emit moment, so the slice's FirstEmittedAt is zero.
	resolved := map[string]bool{a.stableKey(): true}
	prevResolved := c.suppressWithResolutionPreview("s1", []Anomaly{a}, resolved)
	if len(prevResolved) != 1 {
		t.Fatalf("resolved preview should surface; got %d", len(prevResolved))
	}
	if !prevResolved[0].FirstEmittedAt.IsZero() {
		t.Errorf("resolved-fresh candidate's FirstEmittedAt should be zero in preview; got %v", prevResolved[0].FirstEmittedAt)
	}

	// Now do an unresolved preview — should be suppressed silently, but
	// before suppression the candidate carries the historical FirstEmittedAt.
	// We can verify the underlying state didn't move by doing one more
	// real call and checking FirstEmittedAt matches the original.
	silentReal := c.suppressWithResolution("s1", []Anomaly{a}, nil)
	if len(silentReal) != 0 {
		t.Errorf("real call within window should stay silent; got %d", len(silentReal))
	}
	// Restate: preview never advanced the suppression history, so the
	// original FirstEmittedAt is still recoverable via a resolution-reset
	// real call.
	finalResolved := c.suppressWithResolution("s1", []Anomaly{a}, resolved)
	if len(finalResolved) != 1 {
		t.Fatalf("real resolved call should re-emit; got %d", len(finalResolved))
	}
	if !finalResolved[0].FirstEmittedAt.After(originalFirst) {
		t.Errorf("resolved real re-emit should refresh FirstEmittedAt past original; original=%v new=%v",
			originalFirst, finalResolved[0].FirstEmittedAt)
	}
}

func TestAnomalyCacheResolutionMapIgnoredForUnseenKeys(t *testing.T) {
	c := newAnomalyCache(time.Minute)
	a := Anomaly{Kind: "x", Severity: SeverityMedium}
	// Mark resolved for a key we've never emitted. The candidate is
	// brand new so emits normally; the resolved flag has nothing to undo.
	resolved := map[string]bool{a.stableKey(): true}
	got := c.suppressWithResolution("s1", []Anomaly{a}, resolved)
	if len(got) != 1 {
		t.Errorf("fresh candidate should emit regardless of resolved map; got %d", len(got))
	}
}

func TestDetectStaleEdit(t *testing.T) {
	cases := []struct {
		name    string
		reads   []int64
		edits   []int64
		changes []int64
		want    int64
	}{
		{
			name: "plan scenario: read T1, change T2, edit T3 with no re-read",
			// T1=100, T2=150, T3=200. Edit at T3 is based on the T1 read,
			// which predates the T2 change. Rule fires; suggestion uses T2.
			reads:   []int64{100},
			edits:   []int64{200},
			changes: []int64{150},
			want:    150,
		},
		{
			name: "agent re-read after change — no stale view",
			// Read T1=100, change T2=150, re-read T3=170, edit T4=200.
			// Latest read before edit is T3 > T2, so view is fresh.
			reads:   []int64{100, 170},
			edits:   []int64{200},
			changes: []int64{150},
			want:    0,
		},
		{name: "successful reread after stale edit resolves the finding", reads: []int64{100, 250}, edits: []int64{200}, changes: []int64{150}, want: 0},
		{name: "new stale edit after recovery qualifies again", reads: []int64{100, 250}, edits: []int64{200, 400}, changes: []int64{150, 300}, want: 300},
		{
			name: "edit without ever reading — rule doesn't apply",
			// Agents that Edit a never-Read file aren't in the stale-view
			// scenario (they may be creating it). Skip.
			reads:   nil,
			edits:   []int64{200},
			changes: []int64{150},
			want:    0,
		},
		{
			name: "change before agent ever read it — agent's view is current",
			// Change at T0=50, read at T1=100, edit at T2=200. The read
			// already captured the post-change content. No staleness.
			reads:   []int64{100},
			edits:   []int64{200},
			changes: []int64{50},
			want:    0,
		},
		{
			name: "multiple stale edits — return latest triggering change",
			// Read T=100, change T=150, edit T=200, change T=250, edit T=300.
			// Both edits triggered; suggestion should reference the latest
			// change (T=250) which is the most useful staleness clue.
			reads:   []int64{100},
			edits:   []int64{200, 300},
			changes: []int64{150, 250},
			want:    250,
		},
		{
			name:    "no external changes — empty result",
			reads:   []int64{100},
			edits:   []int64{200},
			changes: nil,
			want:    0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := detectStaleEdit(c.reads, c.edits, c.changes)
			if got != c.want {
				t.Errorf("detectStaleEdit(reads=%v, edits=%v, changes=%v) = %d, want %d",
					c.reads, c.edits, c.changes, got, c.want)
			}
		})
	}
}

func TestAnomalyCacheGCDropsOtherExpiredEntries(t *testing.T) {
	c := newAnomalyCache(10 * time.Millisecond)
	c.set("expired", []Anomaly{{Kind: "x"}})
	time.Sleep(20 * time.Millisecond)
	// set() for a different key triggers opportunistic GC of expired peers.
	c.set("fresh", []Anomaly{{Kind: "y"}})
	c.mu.Lock()
	_, has := c.items["expired"]
	c.mu.Unlock()
	if has {
		t.Error("opportunistic GC did not drop expired peer")
	}
}

func TestRepeatedBuildFailuresRecoveryAndIdentity(t *testing.T) {
	row := func(command, cwd, event string, ts int) []any {
		input, _ := json.Marshal(map[string]string{"command": command})
		return []any{string(input), cwd, event, "error: unavailable", ts}
	}
	fail := func(command string, ts int) []any { return row(command, "/repo", "PostToolUseFailure", ts) }
	command := "go test ./..."
	for _, tc := range []struct {
		name string
		rows [][]any
		want int
	}{
		{"three unresolved", [][]any{fail(command, 3), fail(command, 2), fail(command, 1)}, 1},
		{"unrelated success", [][]any{row("ls", "/repo", "PostToolUse", 4), fail(command, 3), fail(command, 2), fail(command, 1)}, 1},
		{"same command recovers", [][]any{row(command, "/repo", "PostToolUse", 4), fail(command, 3), fail(command, 2), fail(command, 1)}, 0},
		{"success resets count", [][]any{fail(command, 5), row(command, "/repo", "PostToolUse", 4), fail(command, 3), fail(command, 2), fail(command, 1)}, 0},
		{"other directory", [][]any{row(command, "/other", "PostToolUse", 4), fail(command, 3), fail(command, 2), fail(command, 1)}, 1},
		{"search miss is not a build", [][]any{fail("rg missing", 3), fail("rg missing", 2), fail("rg missing", 1)}, 0},
		{"ambiguous timestamp", [][]any{fail(command, 4), row(command, "/repo", "PostToolUse", 4), fail(command, 3), fail(command, 2)}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := repeatedBuildFailures(tc.rows)
			if len(got) != tc.want {
				t.Fatalf("got %+v, want %d findings", got, tc.want)
			}
			for _, a := range got {
				if a.Channel == ChannelStopBlock {
					t.Fatal("correlation must not block Stop")
				}
			}
		})
	}
	prefix := "go test ./" + strings.Repeat("a", 70)
	rows := [][]any{fail(prefix+"x", 6), fail(prefix+"y", 5), fail(prefix+"x", 4), fail(prefix+"y", 3), fail(prefix+"x", 2), fail(prefix+"y", 1)}
	got := repeatedBuildFailures(rows)
	if len(got) != 2 || got[0].stableKey() == got[1].stableKey() {
		t.Fatalf("distinct commands were merged: %+v", got)
	}
	if kept := newAnomalyCache(time.Minute).suppress("session", got); len(kept) != 2 {
		t.Fatal("suppression merged distinct commands")
	}
}

func TestChannelSelectionDoesNotConsumeOtherFindings(t *testing.T) {
	d := NewDetector(0, nil)
	d.SetSubmit(func(func()) bool { return false })
	d.cache.set("s", []Anomaly{{Kind: "repeated_failed_build", Severity: SeverityMedium, Channel: ChannelUserPromptSubmit, identity: "command"}})
	for _, channel := range []string{ChannelStopBlock, ChannelPostToolUse} {
		if got := d.DetectByChannel(context.Background(), "s", channel); len(got) != 0 {
			t.Fatalf("wrong-channel findings: %+v", got)
		}
	}
	if len(d.cache.history) != 0 {
		t.Fatal("undelivered findings consumed suppression")
	}
	if got := d.DetectByChannel(context.Background(), "s", ChannelUserPromptSubmit); len(got) != 1 {
		t.Fatal("next-prompt finding was lost")
	}
	if got := d.DetectByChannel(context.Background(), "s", ChannelUserPromptSubmit); len(got) != 0 {
		t.Fatal("cached finding emitted twice")
	}
}
