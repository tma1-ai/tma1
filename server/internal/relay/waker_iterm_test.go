package relay

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestItermUUID(t *testing.T) {
	cases := map[string]string{
		"w1t0p0:5374D768-20C4-4E2A-B307-B5A16EE76AF2": "5374D768-20C4-4E2A-B307-B5A16EE76AF2",
		"5374D768-20C4-4E2A-B307-B5A16EE76AF2":        "5374D768-20C4-4E2A-B307-B5A16EE76AF2",
		"":                                            "",
		"w1t0p0:":                                     "",
		"w1t0p0:not-uuid":                             "",
		"garbage":                                     "",
	}
	for in, want := range cases {
		if got := itermUUID(in); got != want {
			t.Errorf("itermUUID(%q)=%q want %q", in, got, want)
		}
	}
}

// newTestIterm builds an ItermWaker bypassing the darwin/osascript gate so
// the injected runner is what executes.
func newTestIterm(run osaRunner, bracketed, busyGate bool) *ItermWaker {
	return &ItermWaker{enabled: true, osascript: "osascript", bracketed: bracketed, busyGate: busyGate, run: run}
}

func TestItermWakerCanWake(t *testing.T) {
	w := newTestIterm(nil, true, true)
	if w.CanWake(Target{}) {
		t.Fatal("no iterm id → should not CanWake")
	}
	if !w.CanWake(Target{Terminals: map[string]string{"iterm": "w1t0p0:5374D768-20C4-4E2A-B307-B5A16EE76AF2"}}) {
		t.Fatal("valid iterm id → should CanWake")
	}
	if w.CanWake(Target{Terminals: map[string]string{"iterm": "w1t0p0:bogus"}}) {
		t.Fatal("invalid uuid → should not CanWake")
	}
}

func TestItermWakerWakeBracketed(t *testing.T) {
	var gotScript string
	var gotArgv []string
	run := func(_ context.Context, script string, argv ...string) (string, error) {
		gotScript, gotArgv = script, argv
		return "OK", nil
	}
	w := newTestIterm(run, true, true)
	tgt := Target{Terminals: map[string]string{"iterm": "w1t0p0:5374D768-20C4-4E2A-B307-B5A16EE76AF2"}}

	if err := w.Wake(context.Background(), tgt, "line1\nline2"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotScript, "is processing") {
		t.Fatal("script should query is processing")
	}
	if len(gotArgv) != 3 {
		t.Fatalf("want 3 argv, got %v", gotArgv)
	}
	if gotArgv[0] != "5374D768-20C4-4E2A-B307-B5A16EE76AF2" {
		t.Fatalf("argv[0] uuid = %q", gotArgv[0])
	}
	if gotArgv[1] != bracketStart+"line1\nline2"+bracketEnd {
		t.Fatalf("argv[1] not bracketed: %q", gotArgv[1])
	}
	if gotArgv[2] != "1" {
		t.Fatalf("busy gate flag = %q want 1", gotArgv[2])
	}
}

func TestItermWakerSingleLineFallback(t *testing.T) {
	var gotArgv []string
	run := func(_ context.Context, _ string, argv ...string) (string, error) {
		gotArgv = argv
		return "OK", nil
	}
	w := newTestIterm(run, false, false) // bracketed off, gate off
	tgt := Target{Terminals: map[string]string{"iterm": "5374D768-20C4-4E2A-B307-B5A16EE76AF2"}}

	if err := w.Wake(context.Background(), tgt, "line1\n  line2\tline3"); err != nil {
		t.Fatal(err)
	}
	if gotArgv[1] != "line1 line2 line3" {
		t.Fatalf("collapsed text = %q", gotArgv[1])
	}
	if gotArgv[2] != "0" {
		t.Fatalf("busy gate flag = %q want 0", gotArgv[2])
	}
}

func TestItermWakerResultMapping(t *testing.T) {
	tgt := Target{Terminals: map[string]string{"iterm": "5374D768-20C4-4E2A-B307-B5A16EE76AF2"}}
	cases := []struct {
		out  string
		want error
	}{
		{"OK", nil},
		{"BUSY", errTargetBusy},
		{"NOTFOUND", errSessionNotFound},
		{"weird", errSessionNotFound},
	}
	for _, tc := range cases {
		w := newTestIterm(func(_ context.Context, _ string, _ ...string) (string, error) {
			return tc.out, nil
		}, true, true)
		err := w.Wake(context.Background(), tgt, "x")
		if !errors.Is(err, tc.want) {
			t.Errorf("out=%q → err=%v want %v", tc.out, err, tc.want)
		}
	}
}

func TestRegistryBusyDoesNotFallThrough(t *testing.T) {
	busy := &fakeWaker{name: "iterm", can: true, err: errTargetBusy}
	worker := &fakeWaker{name: "worker", can: true}
	r := NewRegistry(busy, worker)

	_, err := r.WakeWith(context.Background(), Target{}, "p")
	if !errors.Is(err, errTargetBusy) {
		t.Fatalf("want errTargetBusy, got %v", err)
	}
	if worker.called != 0 {
		t.Fatal("worker must not run when the real terminal is busy")
	}
}
