package perception

import "testing"

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
		{"copilot", "copilot_cli"}, // alias
		{"Copilot", "copilot_cli"}, // case-insensitive alias
		{"copilot_cli", "copilot_cli"},
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
		// Bare name falls back to legacy basename LIKE.
		{"tma1", "AND cwd LIKE '%/tma1%' "},
		// SQL injection in the input gets escaped (single quote doubled).
		{"foo'bar", "AND cwd LIKE '%/foo''bar%' "},
	}
	for _, c := range cases {
		if got := peerCwdFilter(c.in); got != c.want {
			t.Errorf("peerCwdFilter(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestValidPeerAgentsExcludesClaudeCode(t *testing.T) {
	// The whole point of get_peer_sessions is "peer" — claude_code is the
	// caller. Make sure it never sneaks into the validator.
	if validPeerAgents["claude_code"] {
		t.Error("claude_code must not be a valid peer agent (caller is CC)")
	}
	for _, want := range []string{"codex", "openclaw", "copilot_cli"} {
		if !validPeerAgents[want] {
			t.Errorf("expected %q to be a valid peer agent", want)
		}
	}
}
