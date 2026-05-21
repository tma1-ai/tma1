package perception

import (
	"testing"
	"time"
)

func TestInjectionCacheFirstCallIsChange(t *testing.T) {
	c := NewInjectionCache(time.Minute)
	d := Digest{Anomalies: "x"}
	if !c.IfChanged("s1", d) {
		t.Error("first call for a session must return true (no baseline)")
	}
}

func TestInjectionCacheRepeatedSameDigestSuppressed(t *testing.T) {
	c := NewInjectionCache(time.Minute)
	d := Digest{Anomalies: "x", Build: "b"}

	if !c.IfChanged("s1", d) {
		t.Fatal("first call expected to change")
	}
	if c.IfChanged("s1", d) {
		t.Error("repeat of identical digest must NOT count as change")
	}
	if c.IfChanged("s1", d) {
		t.Error("third repeat must still NOT count as change")
	}
}

func TestInjectionCacheDigestDeltaRecognized(t *testing.T) {
	c := NewInjectionCache(time.Minute)
	a := Digest{Anomalies: "x"}
	b := Digest{Anomalies: "x", Build: "new"} // delta on Build

	if !c.IfChanged("s1", a) {
		t.Fatal("first")
	}
	if !c.IfChanged("s1", b) {
		t.Error("digest with a new section must be reported as changed")
	}
}

func TestInjectionCacheExpiryRefreshes(t *testing.T) {
	c := NewInjectionCache(50 * time.Millisecond)
	d := Digest{Anomalies: "x"}
	_ = c.IfChanged("s1", d)
	time.Sleep(80 * time.Millisecond)
	if !c.IfChanged("s1", d) {
		t.Error("expired entry must re-emit on next call")
	}
}

func TestInjectionCacheEmptySessionIDAlwaysChanges(t *testing.T) {
	c := NewInjectionCache(time.Minute)
	d := Digest{Anomalies: "x"}
	for i := 0; i < 5; i++ {
		if !c.IfChanged("", d) {
			t.Errorf("empty session id should always return true (iter %d)", i)
		}
	}
}

func TestInjectionCacheForgetClearsEntry(t *testing.T) {
	c := NewInjectionCache(time.Minute)
	d := Digest{Anomalies: "x"}
	_ = c.IfChanged("s1", d)
	c.Forget("s1")
	if !c.IfChanged("s1", d) {
		t.Error("after Forget the next call should be treated as fresh")
	}
}
