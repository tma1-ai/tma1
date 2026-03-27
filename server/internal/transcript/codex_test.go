package transcript

import "testing"

func TestCodexSessionGroup(t *testing.T) {
	tests := []struct {
		name     string
		baseName string
		want     string
	}{
		{
			name:     "standard rollout filename",
			baseName: "rollout-2026-03-27T18-10-59-019d2ec6-958f-7cde-b25c-acde48001122",
			want:     "rollout-2026-03-27T18-10-59",
		},
		{
			name:     "unexpected filename falls back to full name",
			baseName: "session-without-timestamp",
			want:     "session-without-timestamp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := codexSessionGroup(tt.baseName); got != tt.want {
				t.Fatalf("codexSessionGroup(%q) = %q, want %q", tt.baseName, got, tt.want)
			}
		})
	}
}

func TestCodexSubagentID(t *testing.T) {
	if got := codexSubagentID("codex:rollout-2026-03-27T18-10-59-a", "review"); got != "codex:rollout-2026-03-27T18-10-59-a" {
		t.Fatalf("codexSubagentID should prefer per-file id, got %q", got)
	}
	if got := codexSubagentID("", "review"); got != "review" {
		t.Fatalf("codexSubagentID should fall back to agent type, got %q", got)
	}
}
