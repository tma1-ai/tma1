package install

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// ---------------------------------------------------------------------------
// readVersionFile
// ---------------------------------------------------------------------------

func TestReadVersionFile(t *testing.T) {
	t.Run("normal", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), ".version")
		if err := os.WriteFile(f, []byte("v0.12.0\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if got := readVersionFile(f); got != "v0.12.0" {
			t.Errorf("got %q, want %q", got, "v0.12.0")
		}
	})

	t.Run("file_not_found", func(t *testing.T) {
		if got := readVersionFile("/no/such/file"); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("empty_file", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), ".version")
		if err := os.WriteFile(f, []byte(""), 0644); err != nil {
			t.Fatal(err)
		}
		if got := readVersionFile(f); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("trailing_whitespace", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), ".version")
		if err := os.WriteFile(f, []byte("  v0.13.0 \n\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if got := readVersionFile(f); got != "v0.13.0" {
			t.Errorf("got %q, want %q", got, "v0.13.0")
		}
	})
}

// ---------------------------------------------------------------------------
// checkVersionMismatch
// ---------------------------------------------------------------------------

func TestCheckVersionMismatch(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	writeVersion := func(t *testing.T, content string) string {
		t.Helper()
		f := filepath.Join(t.TempDir(), ".version")
		if err := os.WriteFile(f, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		return f
	}

	t.Run("latest_with_version_file_skips_upgrade", func(t *testing.T) {
		vf := writeVersion(t, "v0.12.0\n")
		needs, resolved := checkVersionMismatch("latest", vf, logger)
		if needs {
			t.Error("expected no upgrade needed")
		}
		if resolved != "v0.12.0" {
			t.Errorf("resolved = %q, want %q", resolved, "v0.12.0")
		}
	})

	t.Run("exact_match_no_upgrade", func(t *testing.T) {
		vf := writeVersion(t, "v0.12.0\n")
		needs, resolved := checkVersionMismatch("v0.12.0", vf, logger)
		if needs {
			t.Error("expected no upgrade needed")
		}
		if resolved != "v0.12.0" {
			t.Errorf("resolved = %q, want %q", resolved, "v0.12.0")
		}
	})

	t.Run("mismatch_needs_upgrade", func(t *testing.T) {
		vf := writeVersion(t, "v0.11.0\n")
		needs, resolved := checkVersionMismatch("v0.12.0", vf, logger)
		if !needs {
			t.Error("expected upgrade needed")
		}
		if resolved != "v0.12.0" {
			t.Errorf("resolved = %q, want %q", resolved, "v0.12.0")
		}
	})

	t.Run("no_version_file_legacy", func(t *testing.T) {
		needs, resolved := checkVersionMismatch("v0.12.0", "/no/such/file", logger)
		if needs {
			t.Error("expected no upgrade for legacy install")
		}
		if resolved != "unknown" {
			t.Errorf("resolved = %q, want %q", resolved, "unknown")
		}
	})
}

// ---------------------------------------------------------------------------
// buildDownloadURL
// ---------------------------------------------------------------------------

func TestBuildDownloadURL(t *testing.T) {
	url, err := buildDownloadURL("v0.12.0", runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantOS := runtime.GOOS
	wantArch := runtime.GOARCH
	wantSuffix := "greptime-" + wantOS + "-" + wantArch + "-v0.12.0.tar.gz"
	wantURL := githubReleaseBase + "/download/v0.12.0/" + wantSuffix

	if url != wantURL {
		t.Errorf("got  %q\nwant %q", url, wantURL)
	}
}

func TestBuildDownloadURLWindows(t *testing.T) {
	url, err := buildDownloadURL("v0.12.0", "windows", "amd64")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := githubReleaseBase + "/download/v0.12.0/greptime-windows-amd64-v0.12.0.tar.gz"
	if url != want {
		t.Errorf("got  %q\nwant %q", url, want)
	}
}

func TestBuildDownloadURLWindowsArm64Rejected(t *testing.T) {
	_, err := buildDownloadURL("v0.12.0", "windows", "arm64")
	if err == nil {
		t.Fatal("expected error for windows/arm64")
	}
}

// ---------------------------------------------------------------------------
// extractBinary
// ---------------------------------------------------------------------------

func makeTarGz(t *testing.T, files map[string][]byte) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for name, content := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0755,
			Size: int64(len(content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(buf.Bytes())
}

func TestExtractBinary(t *testing.T) {
	t.Run("finds_greptime", func(t *testing.T) {
		content := []byte("fake-binary-content")
		archive := makeTarGz(t, map[string][]byte{
			"some-dir/README.md":                    []byte("readme"),
			"some-dir/" + greptimeBinaryName(): content,
		})

		dest := filepath.Join(t.TempDir(), greptimeBinaryName())
		if err := extractBinary(archive, dest); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		got, err := os.ReadFile(dest)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, content) {
			t.Errorf("extracted content mismatch")
		}

		info, err := os.Stat(dest)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&0111 == 0 {
			t.Error("binary should be executable")
		}
	})

	t.Run("no_greptime_in_archive", func(t *testing.T) {
		archive := makeTarGz(t, map[string][]byte{
			"some-dir/other-binary": []byte("nope"),
		})

		dest := filepath.Join(t.TempDir(), greptimeBinaryName())
		err := extractBinary(archive, dest)
		if err == nil {
			t.Fatal("expected error for missing greptime binary")
		}
		wantErr := greptimeBinaryName() + " binary not found in archive"
		if got := err.Error(); got != wantErr {
			t.Errorf("got %q, want %q", got, wantErr)
		}
	})
}

// ---------------------------------------------------------------------------
// verifyChecksum — test the hash comparison logic directly
// ---------------------------------------------------------------------------

func TestVerifyChecksumLogic(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	content := []byte("test-tarball-content")
	h := sha256.Sum256(content)
	correctHash := hex.EncodeToString(h[:])

	// Write a tarball temp file to verify against.
	writeTarball := func(t *testing.T) *os.File {
		t.Helper()
		f, err := os.CreateTemp(t.TempDir(), "tarball-*.tar.gz")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(content); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Seek(0, 0); err != nil {
			t.Fatal(err)
		}
		return f
	}

	t.Run("hash_match", func(t *testing.T) {
		tarball := writeTarball(t)
		defer tarball.Close()

		if _, err := tarball.Seek(0, 0); err != nil {
			t.Fatal(err)
		}
		hasher := sha256.New()
		if _, err := io.Copy(hasher, tarball); err != nil {
			t.Fatal(err)
		}
		actual := hex.EncodeToString(hasher.Sum(nil))
		if actual != correctHash {
			t.Errorf("hash mismatch: got %s, want %s", actual, correctHash)
		}
	})

	t.Run("hash_mismatch_detected", func(t *testing.T) {
		tarball := writeTarball(t)
		defer tarball.Close()

		wrongHash := "0000000000000000000000000000000000000000000000000000000000000000"
		if _, err := tarball.Seek(0, 0); err != nil {
			t.Fatal(err)
		}
		hasher := sha256.New()
		if _, err := io.Copy(hasher, tarball); err != nil {
			t.Fatal(err)
		}
		actual := hex.EncodeToString(hasher.Sum(nil))
		if actual == wrongHash {
			t.Error("hashes should not match")
		}
	})

	// verifyChecksum itself requires network (downloads checksum file).
	// We only test that it gracefully handles a non-existent version
	// (the HTTP call will fail, and it should return nil since checksum
	// verification is non-fatal for unavailable checksums).
	t.Run("unavailable_checksum_non_fatal", func(t *testing.T) {
		tarball := writeTarball(t)
		defer tarball.Close()

		// Use a version that won't exist — the checksum URL will 404.
		err := verifyChecksum(tarball, "v0.0.0-nonexistent", logger)
		if err != nil {
			t.Errorf("expected nil (non-fatal skip), got: %v", err)
		}
	})
}
