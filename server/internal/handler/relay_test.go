package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tma1-ai/tma1/server/internal/relay"
)

func newRelayServer() *Server {
	coord := relay.NewCoordinator(nil, relay.NewRegistry(), func(cwd string) string { return cwd })
	return &Server{relayToken: "secret", relayCoordinator: coord}
}

func postSignal(t *testing.T, s *Server, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/relay/signal", strings.NewReader(body))
	if token != "" {
		req.Header.Set("X-Tma1-Relay-Token", token)
	}
	rec := httptest.NewRecorder()
	s.handleRelaySignal(rec, req)
	return rec
}

func TestRelaySignalAuth(t *testing.T) {
	s := newRelayServer()
	if rec := postSignal(t, s, "", `{"stage":"plan_ready","role":"driver"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token: want 401, got %d", rec.Code)
	}
	if rec := postSignal(t, s, "wrong", `{"stage":"plan_ready","role":"driver"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: want 401, got %d", rec.Code)
	}
}

func TestRelaySignalValidation(t *testing.T) {
	s := newRelayServer()
	if rec := postSignal(t, s, "secret", `{bad json`); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body: want 400, got %d", rec.Code)
	}
	if rec := postSignal(t, s, "secret", `{"stage":"bogus","role":"driver"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown stage: want 400, got %d", rec.Code)
	}
	if rec := postSignal(t, s, "secret", `{"stage":"plan_ready","role":"nope"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid role: want 400, got %d", rec.Code)
	}
}

func TestRelaySignalValidNoTarget(t *testing.T) {
	s := newRelayServer()
	rec := postSignal(t, s, "secret", `{"stage":"plan_ready","role":"driver","cwd":"/repo"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid signal: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"target_found":false`) {
		t.Fatalf("want target_found=false (no reviewer registered), body=%s", rec.Body.String())
	}
}

// postHook drives handleHooks with a minimal hook body + relay role header.
// greptimeHTTPPort is 0 (test sentinel) so no DB writes happen.
func postHook(s *Server, event, role, cwd, sessionID string) {
	body := `{"hook_event_name":"` + event + `","session_id":"` + sessionID + `","cwd":"` + cwd + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/hooks", strings.NewReader(body))
	req.Header.Set("X-Tma1-Role", role)
	s.handleHooks(httptest.NewRecorder(), req)
}

// TestStopDoesNotUnregisterDriver locks the P1 fix: CC fires Stop at every
// turn end, so Stop must refresh (Touch) the target, not delete it —
// otherwise the driver vanishes the moment it calls tma1_handoff and the
// reviewer can never wake it back. SessionEnd is the only unregister event.
func TestStopDoesNotUnregisterDriver(t *testing.T) {
	coord := relay.NewCoordinator(nil, relay.NewRegistry(), func(c string) string { return c })
	s := &Server{relayCoordinator: coord} // greptimeHTTPPort 0 → no DB writes

	postHook(s, "SessionStart", "driver", "/repo", "d1")
	postHook(s, "Stop", "driver", "/repo", "d1") // turn end — must NOT remove the target
	if res, _ := coord.Signal(context.Background(), relay.StagePlanReviewed, relay.RoleReviewer, "/repo", ""); !res.TargetFound {
		t.Fatal("Stop must not unregister the driver (it fires at every turn end)")
	}

	postHook(s, "SessionEnd", "driver", "/repo", "d1") // real session end — removes
	if res, _ := coord.Signal(context.Background(), relay.StagePlanReviewed, relay.RoleReviewer, "/repo", ""); res.TargetFound {
		t.Fatal("SessionEnd should unregister the driver")
	}
}
