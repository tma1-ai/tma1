package relay

import (
	"strings"
	"testing"
)

func TestLookupAndStages(t *testing.T) {
	for _, s := range []string{StagePlanReady, StagePlanReviewed, StageImplDone, StageCodeReviewed} {
		if _, ok := Lookup(s); !ok {
			t.Fatalf("stage %q should resolve", s)
		}
	}
	if _, ok := Lookup("nope"); ok {
		t.Fatal("unknown stage should miss")
	}
	if got := ValidStages(); len(got) != 4 {
		t.Fatalf("want 4 stages, got %d (%v)", len(got), got)
	}
}

func TestWakeRoles(t *testing.T) {
	cases := map[string]string{
		StagePlanReady:    RoleReviewer,
		StageImplDone:     RoleReviewer,
		StagePlanReviewed: RoleDriver,
		StageCodeReviewed: RoleDriver,
	}
	for stage, want := range cases {
		tr, _ := Lookup(stage)
		if tr.WakeRole != want {
			t.Fatalf("%s wake=%s, want %s", stage, tr.WakeRole, want)
		}
	}
}

func TestRenderPrompt(t *testing.T) {
	tr, _ := Lookup(StagePlanReady)
	out, err := renderPrompt(tr, promptData{Project: "myproj"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "myproj") {
		t.Fatalf("project not substituted: %s", out)
	}

	tr2, _ := Lookup(StagePlanReviewed)
	out2, err := renderPrompt(tr2, promptData{Summary: "FINDINGS-XYZ"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out2, "FINDINGS-XYZ") {
		t.Fatalf("summary not substituted: %s", out2)
	}
}
