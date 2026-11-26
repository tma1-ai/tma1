package perception

import (
	"slices"
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
	got := buildSearchCandidatesSQL([]string{"peer"}, MatchModeTerm, sids, 120, 5)
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
	sub := buildSearchCandidatesSQL([]string{"peer"}, MatchModeSubstring, sids, 120, 5)
	if !strings.Contains(sub, "AND session_id IN ('a','b')") {
		t.Errorf("substring pass dropped the scope:\n%s", sub)
	}
	if !strings.Contains(sub, "lower(content) LIKE '%peer%'") {
		t.Errorf("substring pass missing case-folded LIKE clause:\n%s", sub)
	}

	unscoped := buildSearchCandidatesSQL([]string{"peer"}, MatchModeTerm, nil, 120, 5)
	if strings.Contains(unscoped, "session_id IN") {
		t.Errorf("unscoped search should not emit an IN clause:\n%s", unscoped)
	}
}

func TestBuildSearchCandidatesSQLMultiTerm(t *testing.T) {
	got := buildSearchCandidatesSQL([]string{"flow", "disk full"}, MatchModeTerm, nil, 120, 5)
	for _, want := range []string{
		"(matches_term(content, 'flow') OR matches_term(content, 'disk full'))",
		"HAVING SUM(CASE WHEN matches_term(content, 'flow') THEN 1 ELSE 0 END) > 0 AND SUM(CASE WHEN matches_term(content, 'disk full') THEN 1 ELSE 0 END) > 0",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("buildSearchCandidatesSQL() missing %q:\n%s", want, got)
		}
	}
}

func TestSplitQuery(t *testing.T) {
	cases := map[string][]string{
		"flow  disk":             {"flow", "disk"},
		`flow "disk full" error`: {"flow", "disk full", "error"},
		`"unclosed phrase`:       {"unclosed phrase"},
		`"" `:                    nil,
		"关键字 检索":                 {"关键字", "检索"},
	}
	for in, want := range cases {
		if got := splitQuery(in); !slices.Equal(got, want) {
			t.Errorf("splitQuery(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMatchClauseEscapes(t *testing.T) {
	if got, want := matchClause("it's", MatchModeTerm), "matches_term(content, 'it''s')"; got != want {
		t.Errorf("matchClause(term) = %q, want %q", got, want)
	}
	// LIKE wildcards in user input must not widen the pattern, and the
	// fallback folds case — it exists partly to rescue a mistyped case.
	if got, want := matchClause("50%_OFF", MatchModeSubstring), `lower(content) LIKE '%50\%\_off%'`; got != want {
		t.Errorf("matchClause(substring) = %q, want %q", got, want)
	}
}

func TestSnippetAround(t *testing.T) {
	short := "a short message about peers"
	if got := snippetAround(short, []string{"peers"}); got != short {
		t.Errorf("short content should pass through, got %q", got)
	}

	long := strings.Repeat("x", 400) + "NEEDLE" + strings.Repeat("y", 400)
	got := snippetAround(long, []string{"needle"})
	if !strings.Contains(got, "NEEDLE") {
		t.Errorf("snippet lost the match: %q", got)
	}
	if !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "…") {
		t.Errorf("snippet should mark both cuts with ellipses: %q", got)
	}
	if n := len([]rune(got)); n > snippetRadius*2+len("NEEDLE")+2 {
		t.Errorf("snippet too wide (%d runes): %q", n, got)
	}

	// A CJK snippet must get the same number of characters as an ASCII
	// one, not a third of them.
	cjkWide := strings.Repeat("观", 400) + "关键字" + strings.Repeat("测", 400)
	wide := snippetAround(cjkWide, []string{"关键字"})
	if n := len([]rune(wide)); n < snippetRadius*2 {
		t.Errorf("CJK snippet only %d runes; radius is measured in runes, not bytes", n)
	}

	// Term matching can hit on a tokenised form that isn't a literal
	// substring; fall back to the head rather than returning nothing.
	if got := snippetAround(long, []string{"absent"}); !strings.HasPrefix(got, "xxx") {
		t.Errorf("no-match fallback should return the head, got %q", got)
	}

	// With several terms the window centres on whichever occurs first.
	twoTerms := strings.Repeat("x", 400) + "FIRST" + strings.Repeat("y", 400) + "SECOND"
	if got := snippetAround(twoTerms, []string{"second", "first"}); !strings.Contains(got, "FIRST") {
		t.Errorf("snippet should centre on the earliest term: %q", got)
	}

	// Multi-byte content must not be cut mid-rune.
	cjk := strings.Repeat("观测", 300) + "关键字" + strings.Repeat("会话", 300)
	if got := snippetAround(cjk, []string{"关键字"}); !strings.Contains(got, "关键字") || !isValidUTF8(got) {
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
