package relay

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseTerminals(t *testing.T) {
	got := ParseTerminals("tmux=%5;iterm=w0t0p0:UUID;wezterm=;kitty=")
	want := map[string]string{"tmux": "%5", "iterm": "w0t0p0:UUID"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}

	if ParseTerminals("") != nil {
		t.Fatal("empty header should yield nil")
	}
	if ParseTerminals("tmux=;iterm=") != nil {
		t.Fatal("all-empty values should yield nil")
	}

	// CR/LF must be stripped so a crafted value can't inject a header line.
	got2 := ParseTerminals("tmux=%5\r\nX-Evil: 1")
	for k, v := range got2 {
		if strings.ContainsAny(v, "\r\n") {
			t.Fatalf("CR/LF leaked into %s=%q", k, v)
		}
	}

	// Oversized value is capped.
	long := ParseTerminals("tmux=" + strings.Repeat("a", maxTerminalValue+50))
	if len(long["tmux"]) != maxTerminalValue {
		t.Fatalf("value not capped: len=%d", len(long["tmux"]))
	}
}

func TestValidRole(t *testing.T) {
	if !ValidRole(RoleDriver) || !ValidRole(RoleReviewer) {
		t.Fatal("driver/reviewer should be valid")
	}
	if ValidRole("") || ValidRole("bogus") {
		t.Fatal("empty/bogus should be invalid")
	}
}
