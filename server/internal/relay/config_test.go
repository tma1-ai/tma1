package relay

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadTransitionsMissing(t *testing.T) {
	_, err := LoadTransitions(filepath.Join(t.TempDir(), "absent.json"))
	if !os.IsNotExist(err) {
		t.Fatalf("absent file should surface os.IsNotExist, got %v", err)
	}
}

func TestLoadTransitionsOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "relay.json")
	js := `{"transitions":{"plan_ready":{"wake_role":"reviewer","prompt":"custom {{.Project}} review"}}}`
	if err := os.WriteFile(path, []byte(js), 0o600); err != nil {
		t.Fatal(err)
	}
	tbl, err := LoadTransitions(path)
	if err != nil {
		t.Fatal(err)
	}
	tr, ok := tbl[StagePlanReady]
	if !ok || tr.WakeRole != RoleReviewer {
		t.Fatalf("override not loaded: %+v", tbl)
	}
	out, err := renderPrompt(tr, promptData{Project: "p"})
	if err != nil || out != "custom p review" {
		t.Fatalf("rendered=%q err=%v", out, err)
	}
}

func TestLoadTransitionsRejectsBadRoleAndTemplate(t *testing.T) {
	dir := t.TempDir()
	bad := map[string]string{
		"badrole.json": `{"transitions":{"plan_ready":{"wake_role":"nobody","prompt":"x"}}}`,
		"badtmpl.json": `{"transitions":{"plan_ready":{"wake_role":"reviewer","prompt":"{{.Oops"}}}`,
		"empty.json":   `{"transitions":{}}`,
	}
	for name, js := range bad {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(js), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadTransitions(path); err == nil {
			t.Fatalf("%s should fail to load", name)
		}
	}
}

func TestSetTransitionsUsedBySignal(t *testing.T) {
	fw := &fakeWaker{name: "fake", can: true}
	c := newTestCoord(NewRegistry(fw))
	c.SetTransitions(DefaultTransitions()) // exercises the setter path
	c.Register(Target{Role: RoleReviewer, SessionID: "r1", CWD: "/repo"})
	if res, _ := c.Signal(context.Background(), StagePlanReady, RoleDriver, "/repo", "s"); !res.Dispatched {
		t.Fatal("signal should dispatch with default transitions set")
	}
}
