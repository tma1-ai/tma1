package perception

import (
	"testing"
	"time"
)

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
