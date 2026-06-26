package relay

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateToken(t *testing.T) {
	dir := t.TempDir()

	tok1, err := LoadOrCreateToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(tok1) != 64 { // 32 bytes hex-encoded
		t.Fatalf("want 64 hex chars, got %d (%q)", len(tok1), tok1)
	}

	info, err := os.Stat(filepath.Join(dir, tokenFileName))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("token file perm = %v, want 0600", perm)
	}

	tok2, err := LoadOrCreateToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	if tok2 != tok1 {
		t.Fatalf("token changed across calls: %q != %q", tok1, tok2)
	}
}
