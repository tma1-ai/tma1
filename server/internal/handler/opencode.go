package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/tma1-ai/tma1/server/internal/opencode"
)

func (s *Server) handleOpenCodeImport(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 20<<20)
	var snapshot opencode.Snapshot
	if err := json.NewDecoder(r.Body).Decode(&snapshot); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid OpenCode export (maximum 20 MiB)"})
		return
	}
	count, err := s.openCodeImporter.Import(r.Context(), snapshot)
	if err != nil {
		s.logger.Warn("OpenCode import failed", "err", err)
		status := http.StatusBadGateway
		if errors.Is(err, opencode.ErrInvalidSnapshot) {
			status = http.StatusBadRequest
		}
		writeJSON(w, status, map[string]any{"error": "OpenCode import failed; check the export and server log. Retrying the same export resumes committed records.", "inserted": count})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session_id": "oc:" + snapshot.Info.ID, "inserted": count})
}
