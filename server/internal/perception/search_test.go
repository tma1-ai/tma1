package perception

import (
	"strings"
	"testing"
)

func TestBuildSearchScopeSQL(t *testing.T) {
	got := buildSearchScopeSQL("codex", "/Users/dennis/tma1", 60)
	for _, want := range []string{
		"FROM tma1_hook_events",
		"INTERVAL '60 minutes'",
		"AND agent_source = 'codex'",
		"cwd = '/Users/dennis/tma1'",
		"LIMIT 500",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("buildSearchScopeSQL() missing %q:\n%s", want, got)
		}
	}

	noFilters := buildSearchScopeSQL("", "", 60)
	if strings.Contains(noFilters, "agent_source =") || strings.Contains(noFilters, "cwd") {
		t.Errorf("buildSearchScopeSQL() with no filters should not constrain agent or cwd:\n%s", noFilters)
	}
}

func TestBuildSearchCandidatesSQLScoped(t *testing.T) {
	sids := []string{"'a'", "'b'"}
	got := buildSearchCandidatesSQL("peer", MatchModeTerm, sids, 120, 5)
	for _, want := range []string{
		"matches_term(content, 'peer')",
		"AND session_id IN ('a','b')",
		"message_type != 'usage'",
		"LIMIT 5",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("buildSearchCandidatesSQL() missing %q:\n%s", want, got)
		}
	}

	// The substring pass must stay inside the same scope — otherwise the
	// widened search can surface other projects' sessions.
	sub := buildSearchCandidatesSQL("peer", MatchModeSubstring, sids, 120, 5)
	if !strings.Contains(sub, "AND session_id IN ('a','b')") {
		t.Errorf("substring pass dropped the scope:\n%s", sub)
	}
	if !strings.Contains(sub, "content LIKE '%peer%'") {
		t.Errorf("substring pass missing LIKE clause:\n%s", sub)
	}

	unscoped := buildSearchCandidatesSQL("peer", MatchModeTerm, nil, 120, 5)
	if strings.Contains(unscoped, "session_id IN") {
		t.Errorf("unscoped search should not emit an IN clause:\n%s", unscoped)
	}
}

func TestMatchClauseEscapes(t *testing.T) {
	if got, want := matchClause("it's", MatchModeTerm), "matches_term(content, 'it''s')"; got != want {
		t.Errorf("matchClause(term) = %q, want %q", got, want)
	}
	// LIKE wildcards in user input must not widen the pattern.
	if got, want := matchClause("50%_off", MatchModeSubstring), `content LIKE '%50\%\_off%'`; got != want {
		t.Errorf("matchClause(substring) = %q, want %q", got, want)
	}
}

func TestSnippetAround(t *testing.T) {
	short := "a short message about peers"
	if got := snippetAround(short, "peers"); got != short {
		t.Errorf("short content should pass through, got %q", got)
	}

	long := strings.Repeat("x", 400) + "NEEDLE" + strings.Repeat("y", 400)
	got := snippetAround(long, "needle")
	if !strings.Contains(got, "NEEDLE") {
		t.Errorf("snippet lost the match: %q", got)
	}
	if !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "…") {
		t.Errorf("snippet should mark both cuts with ellipses: %q", got)
	}
	if len(got) > snippetRadius*2+len("NEEDLE")+8 {
		t.Errorf("snippet too wide (%d bytes): %q", len(got), got)
	}

	// Term matching can hit on a tokenised form that isn't a literal
	// substring; fall back to the head rather than returning nothing.
	if got := snippetAround(long, "absent"); !strings.HasPrefix(got, "xxx") {
		t.Errorf("no-match fallback should return the head, got %q", got)
	}

	// Multi-byte content must not be cut mid-rune.
	cjk := strings.Repeat("观测", 300) + "关键字" + strings.Repeat("会话", 300)
	if got := snippetAround(cjk, "关键字"); !strings.Contains(got, "关键字") || !isValidUTF8(got) {
		t.Errorf("multi-byte snippet broken: %q", got)
	}
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == 0xFFFD {
			return false
		}
	}
	return true
}

func TestClampInt(t *testing.T) {
	cases := []struct {
		n, min, max, fallback, want int
	}{
		{0, 1, 10, 5, 5},
		{-3, 1, 10, 5, 5},
		{1, 1, 10, 5, 1},
		{7, 1, 10, 5, 7},
		{99, 1, 10, 5, 10},
	}
	for _, c := range cases {
		if got := clampInt(c.n, c.min, c.max, c.fallback); got != c.want {
			t.Errorf("clampInt(%d, %d, %d, %d) = %d, want %d", c.n, c.min, c.max, c.fallback, got, c.want)
		}
	}
}
