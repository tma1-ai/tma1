package perception

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizePeerAgent(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"  ", ""},
		{"all", ""},
		{"ALL", ""},
		{"*", ""},
		{"codex", "codex"},
		{"CODEX", "codex"},
		{"openclaw", "openclaw"},
		{"copilot", "copilot_cli"},     // alias
		{"Copilot", "copilot_cli"},     // case-insensitive alias
		{"copilot-cli", "copilot_cli"}, // hyphen alias
		{"github-copilot", "copilot_cli"},
		{"copilot_cli", "copilot_cli"},
		// CC aliases — the Codex skill table documents these as valid
		// inputs, so the server must accept them too. Without the
		// server-side fallback, a skill author drift (or a direct MCP
		// caller) typing "cc" hits "invalid agent_source 'cc'".
		{"cc", "claude_code"},
		{"CC", "claude_code"},
		{"claude", "claude_code"},
		{"Claude", "claude_code"},
		{"claude-code", "claude_code"},
		{"claudecode", "claude_code"},
		{"claude_code", "claude_code"},
		{"unknown-agent", "unknown-agent"}, // passes through; validator rejects later
	}
	for _, c := range cases {
		if got := normalizePeerAgent(c.in); got != c.want {
			t.Errorf("normalizePeerAgent(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPeerCwdFilter(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"   ", ""},
		// Absolute path: anchored prefix, not basename. /foo must not match /foobar.
		{"/Users/dennis/tma1", "AND (cwd = '/Users/dennis/tma1' OR cwd LIKE '/Users/dennis/tma1/%') "},
		// Trailing slash normalized.
		{"/Users/dennis/tma1/", "AND (cwd = '/Users/dennis/tma1' OR cwd LIKE '/Users/dennis/tma1/%') "},
		// Bare name now matches either separator before the basename so
		// Windows-stored cwds with the same name still surface. `\\` in
		// the pattern is a LIKE-escaped literal backslash.
		{"tma1", `AND (cwd LIKE '%/tma1%' OR cwd LIKE '%\\tma1%') `},
		// SQL injection in the input gets escaped (single quote doubled).
		{"foo'bar", `AND (cwd LIKE '%/foo''bar%' OR cwd LIKE '%\\foo''bar%') `},
		// LIKE wildcards in project name are neutralised via backslash
		// (GreptimeDB's only supported LIKE escape char).
		{"a%b_c", `AND (cwd LIKE '%/a\%b\_c%' OR cwd LIKE '%\\a\%b\_c%') `},
		// '!' is no longer special — passes through literally.
		{"go!foo", `AND (cwd LIKE '%/go!foo%' OR cwd LIKE '%\\go!foo%') `},
		// Backslash in a bare name gets doubled (literal in pattern) on
		// both alternatives.
		{`a\b`, `AND (cwd LIKE '%/a\\b%' OR cwd LIKE '%\\a\\b%') `},

		// Windows absolute: drive-letter with backslashes. Builds both
		// separator variants so the predicate matches whichever way the
		// agent stored cwd. Backslash in pattern is `\\` (LIKE escape).
		{
			`C:\Users\dennis\tma1`,
			`AND (cwd = 'C:/Users/dennis/tma1' OR cwd LIKE 'C:/Users/dennis/tma1/%' OR cwd = 'C:\Users\dennis\tma1' OR cwd LIKE 'C:\\Users\\dennis\\tma1\\%') `,
		},
		// Windows absolute: drive-letter with forward-slashes (e.g.
		// posted by a shell that pre-normalised). Same dual predicate.
		{
			`C:/Users/dennis/tma1`,
			`AND (cwd = 'C:/Users/dennis/tma1' OR cwd LIKE 'C:/Users/dennis/tma1/%' OR cwd = 'C:\Users\dennis\tma1' OR cwd LIKE 'C:\\Users\\dennis\\tma1\\%') `,
		},
		// Lowercase drive letter is valid Windows too.
		{
			`d:\src\repo`,
			`AND (cwd = 'd:/src/repo' OR cwd LIKE 'd:/src/repo/%' OR cwd = 'd:\src\repo' OR cwd LIKE 'd:\\src\\repo\\%') `,
		},
		// Trailing separator (either style) is normalised away before
		// building the prefix-match LIKE.
		{
			`C:\Users\dennis\tma1\`,
			`AND (cwd = 'C:/Users/dennis/tma1' OR cwd LIKE 'C:/Users/dennis/tma1/%' OR cwd = 'C:\Users\dennis\tma1' OR cwd LIKE 'C:\\Users\\dennis\\tma1\\%') `,
		},
		// UNC path — handled as Windows absolute.
		{
			`\\server\share\repo`,
			`AND (cwd = '//server/share/repo' OR cwd LIKE '//server/share/repo/%' OR cwd = '\\server\share\repo' OR cwd LIKE '\\\\server\\share\\repo\\%') `,
		},
	}
	for _, c := range cases {
		if got := peerCwdFilter(c.in); got != c.want {
			t.Errorf("peerCwdFilter(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestIsWindowsAbsPath(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		// Drive letters, both separators, both cases.
		{`C:\foo`, true},
		{`C:/foo`, true},
		{`d:\src`, true},
		{`z:/x`, true},
		// UNC.
		{`\\server\share`, true},
		// POSIX absolute — not a Windows abs path.
		{`/Users/dennis`, false},
		// Bare names.
		{`tma1`, false},
		{`foo`, false},
		// Drive-letter-shaped but missing separator: not absolute.
		{`C:foo`, false},
		{`C:`, false},
		// Empty / short inputs.
		{``, false},
		{`C`, false},
		{`C:`, false},
		// Non-letter first char rules out drive-letter form.
		{`1:\foo`, false},
		{`#:\foo`, false},
		// Single backslash prefix (not UNC).
		{`\foo`, false},
	}
	for _, c := range cases {
		if got := isWindowsAbsPath(c.in); got != c.want {
			t.Errorf("isWindowsAbsPath(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestValidPeerAgentsCoversAllFourAdapters(t *testing.T) {
	// Each agent that can write to tma1_hook_events / tma1_messages
	// must be a queryable peer from every other agent's MCP tool.
	// Codex calling with agent_source="claude_code" was rejected by
	// an earlier draft that hard-coded CC as the caller; this test
	// guards against that regression.
	for _, want := range []string{"claude_code", "codex", "openclaw", "copilot_cli"} {
		if !validPeerAgents[want] {
			t.Errorf("expected %q to be a valid peer agent", want)
		}
	}
}

func TestClampPeerLimit(t *testing.T) {
	// Behavior the doc claims and the UX needs: limit clamped into [1,5],
	// not silently coerced to 1 on overflow. /tma1-peer codex 10 should
	// behave as if the user typed 5.
	cases := []struct {
		in, want int
	}{
		{-1, 1},
		{0, 1},
		{1, 1},
		{3, 3},
		{5, 5},
		{6, 5},
		{100, 5},
	}
	for _, c := range cases {
		if got := clampPeerLimit(c.in); got != c.want {
			t.Errorf("clampPeerLimit(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestResolveAgentAlias(t *testing.T) {
	// Naming yourself is allowed: "what did I do here earlier" is a
	// real question. Only the empty-agent_source fan-out excludes the
	// caller, and that lives in peerAgentList.
	cases := []struct {
		caller, in, want string
	}{
		{"claude_code", "claude_code", "claude_code"},
		{"claude_code", "cc", "claude_code"},
		{"codex", "CODEX", "codex"},
		{"claude_code", "self", "claude_code"},
		{"claude_code", "me", "claude_code"},
		{"codex", "mine", "codex"},
		{"codex", "claude", "claude_code"},
		{"codex", "", ""},
		{"", "codex", "codex"},
	}
	for _, c := range cases {
		b := &Bundler{Caller: c.caller}
		got, err := b.resolveAgentAlias(c.in)
		if err != nil {
			t.Errorf("caller=%q resolveAgentAlias(%q) = error %v", c.caller, c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("caller=%q resolveAgentAlias(%q) = %q, want %q", c.caller, c.in, got, c.want)
		}
	}
}

func TestResolveAgentAliasErrors(t *testing.T) {
	// "self" without a known caller can't be resolved to anything; the
	// message must tell the agent what to do instead.
	b := &Bundler{Caller: ""}
	if _, err := b.resolveAgentAlias("self"); err == nil {
		t.Error("resolveAgentAlias(self) with no Caller: expected error, got nil")
	} else if !strings.Contains(err.Error(), "explicitly") {
		t.Errorf("error %q should tell the caller to name the agent explicitly", err)
	}

	if _, err := (&Bundler{}).resolveAgentAlias("gemini"); err == nil {
		t.Error("resolveAgentAlias(gemini): expected error for unknown agent")
	}
}

func TestResolveDetail(t *testing.T) {
	cases := []struct {
		detail                  string
		messageLimit            int
		wantMessages, wantChars int
	}{
		{DetailSummary, 0, 0, 0},
		{DetailPreview, 0, 3, 240},
		{DetailFull, 0, 40, 1200},
		{"", 0, 3, 240},
		{"nonsense", 0, 3, 240},
		// The deprecated numeric override wins, and is clamped.
		{DetailSummary, 15, 15, 1200},
		{DetailFull, 500, 100, 1200},
	}
	for _, c := range cases {
		msgs, chars := resolveDetail(c.detail, c.messageLimit)
		if msgs != c.wantMessages || chars != c.wantChars {
			t.Errorf("resolveDetail(%q, %d) = (%d, %d), want (%d, %d)",
				c.detail, c.messageLimit, msgs, chars, c.wantMessages, c.wantChars)
		}
	}
}

func TestPeerAgentListExcludesCaller(t *testing.T) {
	// The empty-agent_source fan-out must exclude the caller so an
	// agent invoking `/tma1-peer` doesn't see its own sessions
	// returned as "peers". With Caller empty (HTTP API path) all
	// four ship.
	cases := []struct {
		caller string
		want   []string
	}{
		{"claude_code", []string{"codex", "copilot_cli", "openclaw"}},
		{"codex", []string{"claude_code", "copilot_cli", "openclaw"}},
		{"openclaw", []string{"claude_code", "codex", "copilot_cli"}},
		{"copilot_cli", []string{"claude_code", "codex", "openclaw"}},
		{"", []string{"claude_code", "codex", "copilot_cli", "openclaw"}},
		{"unknown_agent", []string{"claude_code", "codex", "copilot_cli", "openclaw"}},
	}
	for _, c := range cases {
		b := &Bundler{Caller: c.caller}
		got := b.peerAgentList()
		if len(got) != len(c.want) {
			t.Errorf("caller=%q: len=%d, want %d (got=%v)", c.caller, len(got), len(c.want), got)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("caller=%q: got %v, want %v", c.caller, got, c.want)
				break
			}
		}
	}
}

func TestDedupPeerMessages(t *testing.T) {
	t0 := time.UnixMilli(1_000_000)
	t1 := t0.Add(time.Second)
	long := strings.Repeat("a", 300)

	cases := []struct {
		name string
		in   []PeerMessage
		want int
	}{
		{"nil", nil, 0},
		{"single", []PeerMessage{{Timestamp: t0, Role: "user", Content: "x"}}, 1},
		{
			"three identical collapse to one",
			[]PeerMessage{
				{Timestamp: t0, Role: "assistant", Content: "hello"},
				{Timestamp: t0, Role: "assistant", Content: "hello"},
				{Timestamp: t0, Role: "assistant", Content: "hello"},
			},
			1,
		},
		{
			"different ts preserved",
			[]PeerMessage{
				{Timestamp: t0, Role: "assistant", Content: "hello"},
				{Timestamp: t1, Role: "assistant", Content: "hello"},
			},
			2,
		},
		{
			"different role preserved",
			[]PeerMessage{
				{Timestamp: t0, Role: "user", Content: "hello"},
				{Timestamp: t0, Role: "assistant", Content: "hello"},
			},
			2,
		},
		{
			"content differs beyond the 200-char prefix → collapsed",
			[]PeerMessage{
				{Timestamp: t0, Role: "assistant", Content: long + "X"},
				{Timestamp: t0, Role: "assistant", Content: long + "Y"},
			},
			1,
		},
		{
			"chronological order preserved among unique entries",
			[]PeerMessage{
				{Timestamp: t0, Role: "user", Content: "first"},
				{Timestamp: t0, Role: "user", Content: "first"}, // dup
				{Timestamp: t1, Role: "assistant", Content: "second"},
			},
			2,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := dedupPeerMessages(c.in)
			if len(got) != c.want {
				t.Fatalf("len = %d, want %d (got %+v)", len(got), c.want, got)
			}
		})
	}

	t.Run("order preserved", func(t *testing.T) {
		in := []PeerMessage{
			{Timestamp: t0, Role: "user", Content: "first"},
			{Timestamp: t0, Role: "user", Content: "first"},
			{Timestamp: t1, Role: "assistant", Content: "second"},
		}
		got := dedupPeerMessages(in)
		if got[0].Content != "first" || got[1].Content != "second" {
			t.Errorf("order broken: %+v", got)
		}
	})
}

func TestBuildPeerSessionListSQL(t *testing.T) {
	sids := []string{"'a'", "'b'"}
	got := buildPeerSessionListSQL(sids, 3, 90)

	// Ranking and started/last must use the activity-event set so an
	// exited/resumed shell (newest event is infra) can't outrank real work.
	if !strings.Contains(got, "MAX(CASE WHEN event_type IN ("+activityEventsSQL+") THEN ts END)") {
		t.Errorf("last_ms not computed over activity events:\n%s", got)
	}
	if !strings.Contains(got, "MIN(CASE WHEN event_type IN ("+activityEventsSQL+") THEN ts END)") {
		t.Errorf("started_ms not computed over activity events:\n%s", got)
	}
	// Infra-only / exit shells must be dropped.
	if !strings.Contains(got, "HAVING SUM(CASE WHEN event_type IN ("+activityEventsSQL+") THEN 1 ELSE 0 END) > 0") {
		t.Errorf("missing HAVING activity > 0:\n%s", got)
	}
	// tool_call_count stays on the narrower tool-event set.
	if !strings.Contains(got, "SUM(CASE WHEN event_type IN ("+toolEventsSQL+") THEN 1 ELSE 0 END) AS tool_call_count") {
		t.Errorf("tool_call_count not on tool events:\n%s", got)
	}
	if !strings.Contains(got, "ORDER BY last_ms DESC") {
		t.Errorf("not ordered by last_ms DESC:\n%s", got)
	}
	if !strings.Contains(got, "session_id IN ('a','b')") {
		t.Errorf("session_id IN list missing:\n%s", got)
	}
	if !strings.Contains(got, "ts > now() - INTERVAL '90 minutes'") {
		t.Errorf("since_min activity window missing:\n%s", got)
	}
	if !strings.Contains(got, "LIMIT 3") {
		t.Errorf("limit not applied:\n%s", got)
	}
}

func TestBuildLatestSessionForCWDSQL(t *testing.T) {
	got := buildLatestSessionForCWDSQL("/Users/dennis/tma1")

	// cwd match runs as a subquery over ALL events (Codex sets cwd only on
	// SessionStart), but the winner is ranked by its newest ACTIVITY event.
	if !strings.Contains(got, "WHERE cwd = '/Users/dennis/tma1'") {
		t.Errorf("cwd subquery filter missing:\n%s", got)
	}
	if !strings.Contains(got, "AND event_type IN ("+activityEventsSQL+")") {
		t.Errorf("activity filter on outer query missing:\n%s", got)
	}
	if strings.Count(got, "ts > now() - INTERVAL '6 hours'") != 2 {
		t.Errorf("cwd and activity windows should both be bounded:\n%s", got)
	}
	if !strings.Contains(got, "ORDER BY MAX(ts) DESC") || !strings.Contains(got, "LIMIT 1") {
		t.Errorf("not ranked by MAX(ts) DESC LIMIT 1:\n%s", got)
	}
}

func TestBuildLatestSessionForCWDSQLEscapesQuote(t *testing.T) {
	// A cwd with an apostrophe must not break out of the string literal.
	got := buildLatestSessionForCWDSQL("/Users/o'brien/tma1")
	if !strings.Contains(got, "/Users/o''brien/tma1") {
		t.Errorf("single quote not escaped:\n%s", got)
	}
}
