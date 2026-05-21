package perception

import (
	"testing"
	"time"
)

func TestDigestStableAcrossCounterChanges(t *testing.T) {
	// Two bundles that differ only in noisy counters (duration, tool
	// counts, tokens, generated_at) must produce the SAME digest. This
	// is the whole point: those fields change every turn but don't
	// reflect actionable change in the agent's world.
	now := time.Now()
	a := &Bundle{
		Project:     "x",
		GeneratedAt: now,
		Session: &SessionState{
			SessionID:       "abc",
			DurationMinutes: 10,
			ToolCallCount:   100,
			TokensInput:     1000,
			TokensOutput:    500,
			CurrentFocus:    "/repo/main.go",
		},
	}
	b := &Bundle{
		Project:     "x",
		GeneratedAt: now.Add(2 * time.Minute),
		Session: &SessionState{
			SessionID:       "abc",
			DurationMinutes: 12, // changed
			ToolCallCount:   115, // changed
			TokensInput:     1300, // changed
			TokensOutput:    700, // changed
			CurrentFocus:    "/repo/main.go", // same focus
		},
	}
	if !a.Digest().Equal(b.Digest()) {
		t.Errorf("digest changed on counter delta alone:\na=%+v\nb=%+v", a.Digest(), b.Digest())
	}
}

func TestDigestChangesWhenFocusChanges(t *testing.T) {
	a := &Bundle{Session: &SessionState{SessionID: "s", CurrentFocus: "/a.go"}}
	b := &Bundle{Session: &SessionState{SessionID: "s", CurrentFocus: "/b.go"}}
	if a.Digest().Equal(b.Digest()) {
		t.Error("digest should change when CurrentFocus changes")
	}
}

func TestDigestChangesOnAnomalySetChange(t *testing.T) {
	base := []Anomaly{{Kind: "file_loop_edit", Severity: "high", RelatedFiles: []string{"a.go"}}}
	a := &Bundle{Anomalies: base}
	b := &Bundle{Anomalies: append([]Anomaly{}, base...)} // identical content
	c := &Bundle{Anomalies: append([]Anomaly{}, base...)}
	c.Anomalies = append(c.Anomalies, Anomaly{Kind: "long_session_warn", Severity: "medium"})

	if !a.Digest().Equal(b.Digest()) {
		t.Error("identical anomaly sets should produce equal digests")
	}
	if a.Digest().Equal(c.Digest()) {
		t.Error("adding a new anomaly must change the digest")
	}
}

func TestDigestChangesOnBuildExitCode(t *testing.T) {
	exit0, exit1 := 0, 1
	a := &Bundle{Build: &BuildStatus{Tag: "make", LastExitCode: &exit0}}
	b := &Bundle{Build: &BuildStatus{Tag: "make", LastExitCode: &exit1}}
	if a.Digest().Equal(b.Digest()) {
		t.Error("digest must change when build flips green↔red")
	}
}

func TestDigestSameWhenBuildErrorTextUnchanged(t *testing.T) {
	exit := 1
	a := &Bundle{Build: &BuildStatus{Tag: "make", LastExitCode: &exit, LastErrorMessage: "boom"}}
	b := &Bundle{Build: &BuildStatus{Tag: "make", LastExitCode: &exit, LastErrorMessage: "boom"}}
	if !a.Digest().Equal(b.Digest()) {
		t.Error("identical build error text must yield identical digest")
	}
}

func TestEmptyBundleDigestStable(t *testing.T) {
	a := &Bundle{}
	b := &Bundle{}
	if a.Digest() != b.Digest() {
		t.Error("empty bundles must produce equal digests")
	}
}
