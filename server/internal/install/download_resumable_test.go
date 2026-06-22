package install

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// rangeHandler serves payload with HTTP Range support. When interruptFirstAt>0
// the very first request writes only that many body bytes and then aborts the
// connection, simulating a mid-stream network cut so a later request must
// resume.
func rangeHandler(payload []byte, interruptFirstAt int) http.HandlerFunc {
	var calls int32
	return func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)

		start := 0
		if rng := r.Header.Get("Range"); rng != "" {
			if _, err := fmt.Sscanf(rng, "bytes=%d-", &start); err != nil {
				start = 0
			}
			if start >= len(payload) {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
			w.Header().Set("Content-Range",
				fmt.Sprintf("bytes %d-%d/%d", start, len(payload)-1, len(payload)))
			w.WriteHeader(http.StatusPartialContent)
		} else {
			w.WriteHeader(http.StatusOK)
		}

		body := payload[start:]
		if interruptFirstAt > 0 && n == 1 {
			cut := interruptFirstAt
			if cut > len(body) {
				cut = len(body)
			}
			_, _ = w.Write(body[:cut])
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			panic(http.ErrAbortHandler) // drop the connection mid-stream
		}
		_, _ = w.Write(body)
	}
}

func TestDownloadFileResumable_Fresh(t *testing.T) {
	payload := bytes.Repeat([]byte("greptime"), 1000) // 8000 bytes
	srv := httptest.NewServer(rangeHandler(payload, 0))
	defer srv.Close()

	dst := filepath.Join(t.TempDir(), "greptime.tar.gz.partial")
	if err := downloadFileResumable(dst, srv.URL, discardLogger()); err != nil {
		t.Fatalf("fresh download: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("content mismatch: got %d bytes, want %d", len(got), len(payload))
	}
}

func TestDownloadFileResumable_Resumes(t *testing.T) {
	payload := bytes.Repeat([]byte("greptime"), 1000) // 8000 bytes
	cut := 3000
	srv := httptest.NewServer(rangeHandler(payload, cut))
	defer srv.Close()

	dst := filepath.Join(t.TempDir(), "greptime.tar.gz.partial")
	logger := discardLogger()

	// First attempt is interrupted mid-stream — it must error but leave the
	// bytes it managed to write on disk for the next attempt to resume from.
	if err := downloadFileResumable(dst, srv.URL, logger); err == nil {
		t.Fatal("expected first (interrupted) attempt to fail")
	}
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("partial file should exist after interruption: %v", err)
	}
	if fi.Size() == 0 || fi.Size() >= int64(len(payload)) {
		t.Fatalf("partial size = %d, want a partial 0 < n < %d", fi.Size(), len(payload))
	}

	// Second attempt resumes via Range and completes the file.
	if err := downloadFileResumable(dst, srv.URL, logger); err != nil {
		t.Fatalf("resume: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("resumed content mismatch: got %d bytes, want %d", len(got), len(payload))
	}
}

func TestDownloadFileResumable_ServerIgnoresRange(t *testing.T) {
	payload := bytes.Repeat([]byte("greptime"), 1000)
	// Server always returns 200 with the full body, ignoring the Range header.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	dst := filepath.Join(t.TempDir(), "greptime.tar.gz.partial")
	// Seed a stale partial that must NOT be appended onto.
	if err := os.WriteFile(dst, bytes.Repeat([]byte("stale"), 200), 0644); err != nil {
		t.Fatal(err)
	}

	if err := downloadFileResumable(dst, srv.URL, discardLogger()); err != nil {
		t.Fatalf("download: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("stale partial was not discarded on 200: got %d bytes, want %d", len(got), len(payload))
	}
}

func TestDownloadFileResumable_AlreadyComplete(t *testing.T) {
	payload := bytes.Repeat([]byte("greptime"), 1000)
	srv := httptest.NewServer(rangeHandler(payload, 0))
	defer srv.Close()

	dst := filepath.Join(t.TempDir(), "greptime.tar.gz.partial")
	// Partial already holds the whole asset → server answers 416 → no-op.
	if err := os.WriteFile(dst, payload, 0644); err != nil {
		t.Fatal(err)
	}

	if err := downloadFileResumable(dst, srv.URL, discardLogger()); err != nil {
		t.Fatalf("already-complete download should be a no-op, got: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("complete file should be left untouched")
	}
}
