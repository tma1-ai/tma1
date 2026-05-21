package build

import "testing"

func TestQuoteStringNullAndEscaping(t *testing.T) {
	if got := quoteString("", 100); got != "NULL" {
		t.Errorf("empty => %q, want NULL", got)
	}
	if got := quoteString("abc", 100); got != "'abc'" {
		t.Errorf("plain => %q", got)
	}
	if got := quoteString("a'b", 100); got != "'a''b'" {
		t.Errorf("escape => %q", got)
	}
	if got := quoteString("ABCDEFG", 3); got != "'ABC'" {
		t.Errorf("truncate => %q", got)
	}
}

func TestQuoteIntAndExitCodeNullness(t *testing.T) {
	if got := quoteIntOrNull(0); got != "NULL" {
		t.Errorf("0 => %q", got)
	}
	if got := quoteIntOrNull(42); got != "42" {
		t.Errorf("42 => %q", got)
	}
	if got := quoteInt64OrNull(0); got != "NULL" {
		t.Errorf("0 => %q", got)
	}
	if got := quoteExitCode(nil); got != "NULL" {
		t.Errorf("nil => %q", got)
	}
	v := 7
	if got := quoteExitCode(&v); got != "7" {
		t.Errorf("&7 => %q", got)
	}
	z := 0
	if got := quoteExitCode(&z); got != "0" {
		t.Errorf("&0 should keep 0 (not NULL): got %q", got)
	}
}
