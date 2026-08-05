package relay

import (
	"strings"
	"time"
)

// Roles. A project has at most one driver and one reviewer registered
// at a time (Phase 1 assumption).
const (
	RoleDriver   = "driver"
	RoleReviewer = "reviewer"
)

const (
	maxTerminalHeader = 1024
	maxTerminalValue  = 256
)

// Target identifies where a role's terminal lives. It is populated from
// identifiers the hook script collects — the script runs inside the
// agent's terminal and can read $TMUX_PANE / $ITERM_SESSION_ID / etc.,
// whereas tma1-server is a separate long-lived process whose own env is
// unrelated to any agent terminal.
type Target struct {
	Role      string            // RoleDriver | RoleReviewer
	SessionID string            // agent session id (best-effort; may be empty)
	Agent     string            // "claude_code" | "codex" — selects the worker fallback command
	CWD       string            // working dir, resolved to a project key by the Coordinator
	Terminals map[string]string // "tmux"->$TMUX_PANE, "iterm"->$ITERM_SESSION_ID, ...
	LastSeen  time.Time
}

// ValidRole reports whether r is a recognised relay role.
func ValidRole(r string) bool {
	return r == RoleDriver || r == RoleReviewer
}

// ParseTerminals decodes the X-Tma1-Terminal header value
// ("tmux=%5;iterm=w0t0p0:UUID;wezterm=;kitty=") into a map, dropping
// empty values. It defends against CR/LF injection and oversized input
// since the value originates from a client-controlled header.
func ParseTerminals(header string) map[string]string {
	if header == "" {
		return nil
	}
	if len(header) > maxTerminalHeader {
		header = header[:maxTerminalHeader]
	}
	header = strings.NewReplacer("\r", "", "\n", "").Replace(header)

	out := map[string]string{}
	for _, pair := range strings.Split(header, ";") {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			continue
		}
		k := strings.TrimSpace(kv[0])
		v := strings.TrimSpace(kv[1])
		if k == "" || v == "" {
			continue
		}
		if len(v) > maxTerminalValue {
			v = v[:maxTerminalValue]
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
