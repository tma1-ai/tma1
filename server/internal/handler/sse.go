package handler

import (
	"bytes"
	"fmt"
	"net/http"
	"time"
)

// handleHookStream serves an SSE endpoint for real-time hook event streaming.
// Optional query param: ?session_id=xxx to filter events for a specific session.
func (s *Server) handleHookStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming not supported"})
		return
	}

	// Disable write deadline for this long-lived connection.
	// Without this, the server's WriteTimeout (30s) kills the SSE stream.
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch := s.hookBroadcast.Subscribe()
	defer s.hookBroadcast.Unsubscribe(ch)

	filterSession := r.URL.Query().Get("session_id")

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case data, ok := <-ch:
			if !ok {
				return
			}
			if filterSession != "" && !bytes.Contains(data, []byte(`"session_id":"`+filterSession+`"`)) {
				continue
			}
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = fmt.Fprintf(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}
