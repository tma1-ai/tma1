package install

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type testRoundTripper func(*http.Request) (*http.Response, error)

func (f testRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestDownloadClientHasNoWallClockTransferTimeout(t *testing.T) {
	if downloadClient.Timeout != 0 {
		t.Fatalf("downloadClient.Timeout = %v, want 0 so slow-but-progressing large downloads are not killed by a total request deadline", downloadClient.Timeout)
	}
}

func TestNewDownloadClientHandlesCustomDefaultTransport(t *testing.T) {
	original := http.DefaultTransport
	custom := testRoundTripper(func(*http.Request) (*http.Response, error) { return nil, nil })
	http.DefaultTransport = custom
	t.Cleanup(func() { http.DefaultTransport = original })

	client := newDownloadClient()
	if client == nil {
		t.Fatal("newDownloadClient returned nil")
	}
	if client.Transport != custom {
		t.Fatal("newDownloadClient did not preserve a custom default RoundTripper")
	}
	if client.Timeout != 0 {
		t.Fatalf("custom-transport client Timeout = %v, want 0", client.Timeout)
	}
}

func TestCopyWithIdleTimeoutAllowsSlowProgress(t *testing.T) {
	reader, writer := io.Pipe()
	go func() {
		defer writer.Close()
		for _, chunk := range []string{"a", "b", "c", "d", "e", "f"} {
			_, _ = writer.Write([]byte(chunk))
			time.Sleep(50 * time.Millisecond)
		}
	}()

	var dst bytes.Buffer
	idle := 250 * time.Millisecond
	start := time.Now()
	if err := copyWithIdleTimeout(&dst, reader, idle); err != nil {
		t.Fatalf("copyWithIdleTimeout returned error for progressing stream: %v", err)
	}
	if elapsed := time.Since(start); elapsed <= idle {
		t.Fatalf("test stream completed in %v; want total duration > idle timeout %v to prove progress resets the timer", elapsed, idle)
	}
	if got := dst.String(); got != "abcdef" {
		t.Fatalf("copied body = %q, want %q", got, "abcdef")
	}
}

func TestCopyWithIdleTimeoutStopsStalledRead(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()

	start := time.Now()
	err := copyWithIdleTimeout(io.Discard, reader, 50*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "download stalled") {
		t.Fatalf("copyWithIdleTimeout stalled read error = %v, want download stalled error", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("stalled read took %v, want it aborted promptly", elapsed)
	}
}
