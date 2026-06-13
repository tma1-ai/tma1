package handler

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"

	"github.com/tma1-ai/tma1/server/internal/relay"
)

const maxRelayBody = 64 << 10 // 64 KB — summary can be a few KB, leave headroom

// relaySignalReq is the body the MCP tma1_handoff tool POSTs.
type relaySignalReq struct {
	Stage     string `json:"stage"`
	Role      string `json:"role"`
	CWD       string `json:"cwd"`
	Summary   string `json:"summary"`
	SessionID string `json:"session_id"`
}

// handleRelaySignal receives a milestone handoff from one agent's MCP
// child and wakes the counterpart's terminal.
//
// Unlike the dashboard's origin-checked write routes, this endpoint
// injects terminal text / spawns worker processes, so an Origin check
// (which a non-browser caller doesn't send anyway) is the wrong guard.
// It validates a shared local token instead. Validation errors return
// 4xx — the "don't break the agent loop" concern is handled in the MCP
// tool, which turns a non-2xx into a plain (non-error) tool result.
func (s *Server) handleRelaySignal(w http.ResponseWriter, r *http.Request) {
	if s.relayToken == "" ||
		subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Tma1-Relay-Token")), []byte(s.relayToken)) != 1 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid relay token"})
		return
	}
	if s.relayCoordinator == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "relay not configured"})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRelayBody))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "read body"})
		return
	}
	var req relaySignalReq
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed json"})
		return
	}
	if _, ok := relay.Lookup(req.Stage); !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "unknown stage", "valid_stages": relay.ValidStages(),
		})
		return
	}
	if !relay.ValidRole(req.Role) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid role (want driver|reviewer)"})
		return
	}

	res, err := s.relayCoordinator.Signal(r.Context(), req.Stage, req.Role, req.CWD, req.Summary)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}
