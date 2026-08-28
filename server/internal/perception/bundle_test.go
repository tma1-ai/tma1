package perception

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestRenderSummaryEmptyOnNilSessionAndNoAnomalies(t *testing.T) {
	b := &Bundle{GeneratedAt: time.Now()}
	if got := b.RenderSummary(); got != "" {
		t.Errorf("RenderSummary with no session/anomalies = %q, want empty", got)
	}
}

func TestRenderSummaryAnomaliesOnlyStillEmits(t *testing.T) {
	b := &Bundle{
		GeneratedAt: time.Now(),
		Project:     "proj",
		Anomalies: []Anomaly{
			{Kind: "file_loop_edit", Severity: SeverityHigh, Suggestion: "stop looping"},
		},
	}
	got := b.RenderSummary()
	if !strings.Contains(got, "anomalies:") || !strings.Contains(got, "[HIGH]") {
		t.Errorf("expected anomaly-only render, got: %q", got)
	}
}

func TestRenderSummaryTruncatesManyAnomalies(t *testing.T) {
	b := &Bundle{
		GeneratedAt: time.Now(),
		Session:     &SessionState{SessionID: "s", ToolCallCount: 1},
	}
	for i := 0; i < 10; i++ {
		b.Anomalies = append(b.Anomalies, Anomaly{
			Kind: "test_kind", Severity: SeverityMedium, Suggestion: "x",
		})
	}
	got := b.RenderSummary()
	if !strings.Contains(got, "... +6 more") {
		t.Errorf("expected truncation note, got: %q", got)
	}
}

func TestRenderSummaryIncludesKeyFields(t *testing.T) {
	now := time.Now()
	b := &Bundle{
		Project:     "tma1",
		GeneratedAt: now,
		Session: &SessionState{
			SessionID:       "abcdef0123",
			DurationMinutes: 42,
			ToolCallCount:   88,
			TokensInput:     1200,
			TokensOutput:    340,
			CurrentFocus:    "/repo/src/auth.rs",
			RecentTools: []ToolCount{
				{Name: "Edit", Count: 12},
				{Name: "Bash", Count: 7},
			},
			RecentFiles: []string{
				"/repo/src/auth.rs",
				"/repo/src/main.rs",
			},
		},
	}

	got := b.RenderSummary()
	for _, want := range []string{
		"project: tma1",
		"session: abcdef01",
		"duration: 42 min",
		"tool_calls: 88",
		"tokens: in=1200 out=340",
		"current_focus:",
		"Edit×12",
		"Bash×7",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q\nfull summary:\n%s", want, got)
		}
	}
}

func TestRenderSummaryUsesExternalChangeWording(t *testing.T) {
	b := &Bundle{
		Project: "tma1",
		External: &ExternalChanges{
			ExternalCount: 1,
			ExternalChanges: []ExternalChange{
				{FilePath: "/repo/file.go", Attribution: "unknown"},
			},
		},
	}

	got := b.RenderSummary()
	if !strings.Contains(got, "external_changes: 1") {
		t.Fatalf("external change count missing: %q", got)
	}
	if strings.Contains(got, "human") {
		t.Fatalf("external change summary claims a human source: %q", got)
	}
}

func TestRenderSummaryStaysShort(t *testing.T) {
	// 60 tools / 80 files would explode a naive renderer; the bundle should cap each.
	state := &SessionState{
		SessionID:       "s",
		DurationMinutes: 30,
		ToolCallCount:   500,
	}
	for i := 0; i < 60; i++ {
		state.RecentTools = append(state.RecentTools, ToolCount{Name: "Tool", Count: 10})
	}
	for i := 0; i < 80; i++ {
		state.RecentFiles = append(state.RecentFiles, "/a/very/long/path/to/some/file.go")
	}
	b := &Bundle{Project: "p", GeneratedAt: time.Now(), Session: state}
	got := b.RenderSummary()
	// Rough budget: well under 500 tokens (~2 KB).
	if len(got) > 2000 {
		t.Errorf("summary too large: %d bytes\n%s", len(got), got)
	}
}

func TestRenderSummaryDeltaEmitsOnlyFlaggedSections(t *testing.T) {
	// Anomaly-only delta should ship the anomalies block + the project
	// header, but NOT session counters or build status. This is the
	// turn-over-turn case where the only actionable news is a new
	// anomaly — re-emitting duration / tool_calls is pure noise.
	exit := 0
	b := &Bundle{
		Project: "tma1",
		Session: &SessionState{SessionID: "abcdef", DurationMinutes: 42, ToolCallCount: 88},
		Build:   &BuildStatus{Tag: "make", LastExitCode: &exit},
		Anomalies: []Anomaly{
			{Kind: "stale_file_view", Severity: SeverityHigh, Suggestion: "Re-read foo.go"},
		},
	}
	got := b.RenderSummaryDelta(DigestDelta{Anomalies: true})
	if !strings.Contains(got, "anomalies:") {
		t.Errorf("anomalies block missing: %q", got)
	}
	for _, unwanted := range []string{"session:", "duration:", "tool_calls:", "build:"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("delta=Anomalies-only leaked %q\nfull:\n%s", unwanted, got)
		}
	}
	if !strings.Contains(got, "project: tma1") {
		t.Errorf("project header missing on non-empty delta: %q", got)
	}
}

func TestRenderSummaryDeltaEmptyDeltaProducesEmptyString(t *testing.T) {
	b := &Bundle{
		Project:   "tma1",
		Session:   &SessionState{SessionID: "abc", DurationMinutes: 30},
		Anomalies: []Anomaly{{Kind: "x", Severity: SeverityHigh, Suggestion: "y"}},
	}
	if got := b.RenderSummaryDelta(DigestDelta{}); got != "" {
		t.Errorf("empty delta should yield empty render, got %q", got)
	}
}

func TestRenderSummaryDeltaAllSectionsMatchesRenderSummary(t *testing.T) {
	// RenderSummary is now a thin wrapper around RenderSummaryDelta with
	// every section flagged; outputs must be byte-identical so the
	// existing hook protocol stays stable.
	b := &Bundle{
		Project: "tma1",
		Session: &SessionState{SessionID: "abc", DurationMinutes: 10, ToolCallCount: 5, CurrentFocus: "/a/b/c.go"},
		Anomalies: []Anomaly{
			{Kind: "stale_file_view", Severity: SeverityHigh, Suggestion: "Re-read"},
		},
	}
	full := b.RenderSummary()
	delta := b.RenderSummaryDelta(AllSectionsDelta())
	if full != delta {
		t.Errorf("RenderSummary differs from AllSectionsDelta render\nfull:\n%s\n\ndelta:\n%s", full, delta)
	}
}

func TestExtractFilesFocusesOnRecentEdits(t *testing.T) {
	now := time.Now()
	older := now.Add(-30 * time.Minute)
	// Phase 1.4: rows now carry the already-extracted file path (column
	// tool_file_path with regex fallback), not the raw tool_input blob.
	rows := [][]any{
		// 30 min ago: heavy edits to old.rs (outside 10-min focus window).
		{"Edit", "/repo/old.rs", older.UnixMilli()},
		{"Edit", "/repo/old.rs", older.UnixMilli()},
		// Recent: focus.rs edited twice in last 10 min.
		{"Edit", "/repo/focus.rs", now.Add(-1 * time.Minute).UnixMilli()},
		{"Edit", "/repo/focus.rs", now.Add(-2 * time.Minute).UnixMilli()},
		// Recent Read: should appear in RecentFiles but NOT determine focus.
		{"Read", "/repo/notes.rs", now.Add(-3 * time.Minute).UnixMilli()},
	}
	recent, focus := extractFilesFromRows(rows, now)

	if focus != "/repo/focus.rs" {
		t.Errorf("focus = %q, want /repo/focus.rs", focus)
	}
	if len(recent) == 0 {
		t.Fatal("recent files should not be empty")
	}
	found := false
	for _, p := range recent {
		if p == "/repo/focus.rs" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("focus file missing from recent list: %v", recent)
	}
}

func TestProjectNameStripsTrailingSlashAndPath(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"/Users/dennis/programming/go/tma1", "tma1"},
		{"/Users/dennis/programming/go/tma1/", "tma1"},
		{"tma1", "tma1"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := projectName(tc.in); got != tc.want {
			t.Errorf("projectName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestResolveProjectRootFindsGitDir(t *testing.T) {
	root := t.TempDir()
	// Set up: root/.git, root/sub/deep/
	if err := os.MkdirAll(root+"/.git", 0o755); err != nil {
		t.Fatal(err)
	}
	deep := root + "/sub/deep"
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	got := ResolveProjectRoot(deep)
	if got != root {
		t.Errorf("ResolveProjectRoot(%q) = %q, want %q", deep, got, root)
	}
}

func TestResolveProjectRootPrefersGitOverInnerModule(t *testing.T) {
	// Mono-repo layout: .git at the top, go.mod in a server/ subdir. The
	// resolver must return the .git root, not the module root — otherwise
	// each subdir (server/, site/, claude-plugin/, ...) gets its own
	// stray .tma1-context.md instead of one shared at the repo root.
	root := t.TempDir()
	if err := os.MkdirAll(root+"/.git", 0o755); err != nil {
		t.Fatal(err)
	}
	server := root + "/server"
	if err := os.MkdirAll(server, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(server+"/go.mod", []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := ResolveProjectRoot(server); got != root {
		t.Errorf("ResolveProjectRoot(%q) = %q, want %q (.git root)", server, got, root)
	}
}

func TestResolveProjectRootFallsBackToMarkerWhenNoGit(t *testing.T) {
	// No .git anywhere ⇒ fall back to go.mod / package.json marker search.
	root := t.TempDir()
	server := root + "/server"
	if err := os.MkdirAll(server, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(server+"/go.mod", []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := ResolveProjectRoot(server); got != server {
		t.Errorf("ResolveProjectRoot(%q) = %q, want %q (module fallback)", server, got, server)
	}
}
