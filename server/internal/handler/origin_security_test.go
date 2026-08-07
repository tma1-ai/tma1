package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestQueryEndpointRejectsForeignBrowserOrigin(t *testing.T) {
	srv := newTestServer()
	r := srv.Router()

	req := httptest.NewRequest(http.MethodPost, "/api/query", strings.NewReader(`{"sql":"DROP TABLE tma1_messages"}`))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("foreign-origin POST /api/query: got status %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestLocalOriginGuardDoesNotTrustArbitraryHostHeader(t *testing.T) {
	srv := newTestServer()
	protected := srv.requireLocalOrigin(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "http://attacker.example/api/settings", nil)
	req.Host = "attacker.example"
	req.Header.Set("Origin", "http://attacker.example")
	w := httptest.NewRecorder()
	protected(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("origin matching attacker-controlled Host was accepted: got status %d, want %d", w.Code, http.StatusForbidden)
	}
}
