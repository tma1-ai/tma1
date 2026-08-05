// Package relay implements the TMA1 driver↔reviewer handoff coordinator.
//
// It maintains per-project in-memory state mapping the two roles
// (driver / reviewer) to the terminal each runs in, and on a milestone
// signal wakes the counterpart terminal via a pluggable Waker (tmux /
// worker). The package is platform-independent: terminal-specific
// delivery lives behind the Waker interface.
package relay

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const tokenFileName = "relay_token"

// LoadOrCreateToken reads <dataDir>/relay_token, generating a random
// 256-bit secret (hex) on first use and persisting it 0600. Both the
// server (which validates the token on /api/relay/signal) and the
// install subcommand (which injects it into the MCP child's env) call
// this against the same dataDir, so the two always agree.
//
// The /api/relay/signal endpoint injects terminal text and spawns
// worker processes; an Origin check can't gate it (non-browser callers
// send no Origin), so a shared local secret is the Phase 1 guard.
func LoadOrCreateToken(dataDir string) (string, error) {
	path := filepath.Join(dataDir, tokenFileName)
	if b, err := os.ReadFile(path); err == nil {
		if tok := strings.TrimSpace(string(b)); tok != "" {
			// Repair permission drift — the token guards an endpoint that
			// injects terminal text / spawns workers, so it must stay 0600
			// even if a previous run (or the user) left it more permissive.
			if info, statErr := os.Stat(path); statErr == nil && info.Mode().Perm() != 0o600 {
				_ = os.Chmod(path, 0o600) // best-effort: the token is still usable if this fails
			}
			return tok, nil
		}
	}

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate relay token: %w", err)
	}
	tok := hex.EncodeToString(buf)

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return "", fmt.Errorf("create data dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(tok), 0o600); err != nil {
		return "", fmt.Errorf("write relay token: %w", err)
	}
	return tok, nil
}

// LoadToken reads an existing relay token WITHOUT creating one, returning
// ("", false) when no token file exists yet. Used by dry-run install so
// it can surface an already-provisioned token without the file-write side
// effect of LoadOrCreateToken.
func LoadToken(dataDir string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(dataDir, tokenFileName))
	if err != nil {
		return "", false
	}
	tok := strings.TrimSpace(string(b))
	return tok, tok != ""
}
