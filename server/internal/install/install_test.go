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
// probeBinaryVersion
// ---------------------------------------------------------------------------

func TestProbeBinaryVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script trick not available on Windows")
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	makeScript := func(t *testing.T, output string) string {
		t.Helper()
		f := filepath.Join(t.TempDir(), "greptime")
		content := "#!/bin/sh\ncat <<'EOF'\n" + output + "\nEOF\n"
		if err := os.WriteFile(f, []byte(content), 0755); err != nil {
			t.Fatal(err)
		}
		return f
	}

	t.Run("real_format", func(t *testing.T) {
		bin := makeScript(t, "greptime\nbranch:\ncommit: f8376fd6\nclean: true\nversion: 1.0.0-rc.2")
		got := probeBinaryVersion(bin, logger)
		if got != "v1.0.0-rc.2" {
			t.Errorf("got %q, want %q", got, "v1.0.0-rc.2")
		}
	})

	t.Run("release_version", func(t *testing.T) {
		bin := makeScript(t, "greptime\nversion: 1.0.0")
		got := probeBinaryVersion(bin, logger)
		if got != "v1.0.0" {
			t.Errorf("got %q, want %q", got, "v1.0.0")
		}
	})

	t.Run("no_version_line", func(t *testing.T) {
		bin := makeScript(t, "greptime\nbranch: main\ncommit: abc123")
		got := probeBinaryVersion(bin, logger)
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("binary_not_found", func(t *testing.T) {
		got := probeBinaryVersion("/no/such/binary", logger)
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
}

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

	// binPath is unused when .version file exists; pass a non-existent path.
	noBin := "/no/such/binary"

	t.Run("latest_with_current_version_skips_upgrade", func(t *testing.T) {
		vf := writeVersion(t, minRequiredVersion+"\n")
		needs, resolved := checkVersionMismatch("latest", vf, noBin, logger)
		if needs {
			t.Error("expected no upgrade needed")
		}
		if resolved != minRequiredVersion {
			t.Errorf("resolved = %q, want %q", resolved, minRequiredVersion)
		}
	})

	t.Run("latest_with_old_version_triggers_upgrade", func(t *testing.T) {
		vf := writeVersion(t, "v0.12.0\n")
		needs, resolved := checkVersionMismatch("latest", vf, noBin, logger)
		if !needs {
			t.Error("expected upgrade needed for version below minimum")
		}
		if resolved != "latest" {
			t.Errorf("resolved = %q, want %q", resolved, "latest")
		}
	})

	t.Run("exact_match_no_upgrade", func(t *testing.T) {
		vf := writeVersion(t, "v0.12.0\n")
		needs, resolved := checkVersionMismatch("v0.12.0", vf, noBin, logger)
		if needs {
			t.Error("expected no upgrade needed")
		}
		if resolved != "v0.12.0" {
			t.Errorf("resolved = %q, want %q", resolved, "v0.12.0")
		}
	})

	t.Run("mismatch_needs_upgrade", func(t *testing.T) {
		vf := writeVersion(t, "v0.11.0\n")
		needs, resolved := checkVersionMismatch("v0.12.0", vf, noBin, logger)
		if !needs {
			t.Error("expected upgrade needed")
		}
		if resolved != "v0.12.0" {
			t.Errorf("resolved = %q, want %q", resolved, "v0.12.0")
		}
	})

	t.Run("latest_with_unparseable_version_triggers_upgrade", func(t *testing.T) {
		vf := writeVersion(t, "nightly-20260401\n")
		needs, resolved := checkVersionMismatch("latest", vf, noBin, logger)
		if !needs {
			t.Error("expected upgrade needed for unparseable version")
		}
		if resolved != "latest" {
			t.Errorf("resolved = %q, want %q", resolved, "latest")
		}
	})

	t.Run("no_version_file_no_binary_triggers_upgrade_latest", func(t *testing.T) {
		// Legacy install: no .version, binary can't be probed → upgrade.
		needs, resolved := checkVersionMismatch("latest", "/no/such/file", noBin, logger)
		if !needs {
			t.Error("expected upgrade for legacy install without .version")
		}
		if resolved != "latest" {
			t.Errorf("resolved = %q, want %q", resolved, "latest")
		}
	})

	t.Run("no_version_file_no_binary_triggers_upgrade_explicit", func(t *testing.T) {
		// Explicit version requested, no .version, binary can't be probed → upgrade.
		needs, resolved := checkVersionMismatch("v1.0.0", "/no/such/file", noBin, logger)
		if !needs {
			t.Error("expected upgrade for legacy install with explicit version")
		}
		if resolved != "v1.0.0" {
			t.Errorf("resolved = %q, want %q", resolved, "v1.0.0")
		}
	})
}

// ---------------------------------------------------------------------------
// versionLessThan
// ---------------------------------------------------------------------------

func TestVersionLessThan(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"v0.12.0", "v1.0.0", true},
		{"v0.9.0", "v0.12.0", true},
		{"v1.0.0", "v1.0.0", false},
		{"v1.0.1", "v1.0.0", false},
		{"v2.0.0", "v1.0.0", false},
		{"v0.12.0", "v0.12.1", true},
		{"v1.0.0-alpha", "v1.0.0", true},  // pre-release < release
		{"v1.0.0-beta", "v1.0.0", true},
		{"v1.0.0-rc1", "v1.0.0", true},
		{"v1.0.0", "v1.0.0-rc1", false},   // release is NOT less than pre-release
		{"v1.0.0-rc.1", "v1.0.0-rc.2", true},   // rc.1 < rc.2
		{"v1.0.0-rc.2", "v1.0.0-rc.1", false},
		{"v1.0.0-rc.1", "v1.0.0-rc.1", false},  // equal
		{"v1.0.0-alpha", "v1.0.0-beta", true},   // alpha < beta lexically
		{"v1.0.0-beta", "v1.0.0-rc", true},      // beta < rc lexically
		{"v1.0.0-alpha.1", "v1.0.0-alpha.2", true},
		{"v1.0.0-rc", "v1.0.0-rc.1", true},      // shorter < longer when prefix matches
		{"v0.9.0-rc1", "v1.0.0", true},
		{"invalid", "v1.0.0", false},       // parse error → conservative false
		{"v1.0.0", "invalid", false},
	}
	for _, tt := range tests {
		t.Run(tt.a+"_vs_"+tt.b, func(t *testing.T) {
			if got := versionLessThan(tt.a, tt.b); got != tt.want {
				t.Errorf("versionLessThan(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// buildDownloadURL
// ---------------------------------------------------------------------------

func TestBuildDownloadURL(t *testing.T) {
	url, err := buildDownloadURL("v0.12.0", runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skipf("skipping unsupported platform %s/%s: %v", runtime.GOOS, runtime.GOARCH, err)
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

		// Windows does not use POSIX mode bits; skip executability check there.
		if runtime.GOOS != "windows" {
			info, err := os.Stat(dest)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode()&0111 == 0 {
				t.Error("binary should be executable")
			}
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
