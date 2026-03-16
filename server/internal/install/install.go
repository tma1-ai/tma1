// Package install handles downloading the GreptimeDB binary.
package install

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	githubReleaseBase = "https://github.com/GreptimeTeam/greptimedb/releases"
)

// greptimeBinaryName returns the platform-appropriate binary name.
func greptimeBinaryName() string {
	if runtime.GOOS == "windows" {
		return "greptime.exe"
	}
	return "greptime"
}

var (
	versionClient  = &http.Client{Timeout: 10 * time.Second}
	downloadClient = &http.Client{Timeout: 5 * time.Minute}
)

// EnsureGreptimeDB checks whether the GreptimeDB binary exists in dataDir/bin/.
// If not, or if the installed version doesn't match the requested version,
// it downloads and extracts the requested version.
// version may be "latest" or a specific tag like "v0.12.0".
func EnsureGreptimeDB(dataDir, version string, logger *slog.Logger) (binPath string, err error) {
	binDir := filepath.Join(dataDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return "", fmt.Errorf("install: create bin dir: %w", err)
	}

	binPath = filepath.Join(binDir, greptimeBinaryName())
	versionFile := filepath.Join(binDir, ".version")

	if _, err := os.Stat(binPath); err == nil {
		needsUpgrade, resolvedVer := checkVersionMismatch(version, versionFile, logger)
		if !needsUpgrade {
			logger.Info("greptimedb binary already present", "path", binPath, "version", resolvedVer)
			return binPath, nil
		}
		logger.Info("greptimedb version mismatch, upgrading",
			"installed", readVersionFile(versionFile), "requested", resolvedVer)
	}

	resolvedVersion, err := resolveVersion(version)
	if err != nil {
		return "", fmt.Errorf("install: resolve version: %w", err)
	}
	logger.Info("downloading greptimedb", "version", resolvedVersion)

	downloadURL, err := buildDownloadURL(resolvedVersion, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", err
	}

	tmpFile, err := os.CreateTemp("", "greptime-*.tar.gz")
	if err != nil {
		return "", fmt.Errorf("install: create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	if err := downloadFile(tmpFile, downloadURL); err != nil {
		return "", fmt.Errorf("install: download %s: %w", downloadURL, err)
	}

	if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
		return "", err
	}

	if err := verifyChecksum(tmpFile, resolvedVersion, logger); err != nil {
		return "", fmt.Errorf("install: %w", err)
	}

	if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
		return "", err
	}

	if err := extractBinary(tmpFile, binPath); err != nil {
		return "", fmt.Errorf("install: extract binary: %w", err)
	}

	_ = os.WriteFile(versionFile, []byte(resolvedVersion+"\n"), 0644)
	logger.Info("greptimedb installed", "path", binPath, "version", resolvedVersion)
	return binPath, nil
}

// checkVersionMismatch returns true if the installed version doesn't match the
// requested version and an upgrade is needed.
func checkVersionMismatch(requestedVersion, versionFile string, logger *slog.Logger) (needsUpgrade bool, resolvedVer string) {
	installed := readVersionFile(versionFile)
	if installed == "" {
		// Legacy install without .version file — assume it's fine.
		return false, "unknown"
	}

	if requestedVersion == "latest" {
		// Binary already installed with a known version — skip network check.
		// To upgrade, set TMA1_GREPTIMEDB_VERSION to a specific tag or
		// delete the .version file to force re-resolution.
		return false, installed
	}

	return requestedVersion != installed, requestedVersion
}

func readVersionFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// resolveVersion resolves "latest" to the actual latest release tag via GitHub API redirect.
func resolveVersion(version string) (string, error) {
	if version != "latest" {
		return version, nil
	}

	// GitHub redirects /releases/latest to the actual tag URL.
	resp, err := versionClient.Get(githubReleaseBase + "/latest")
	if err != nil {
		return "", fmt.Errorf("resolve latest version: %w", err)
	}
	defer resp.Body.Close()

	// After redirect, URL is .../releases/tag/v0.x.y
	finalURL := resp.Request.URL.String()
	parts := strings.Split(finalURL, "/")
	tag := parts[len(parts)-1]
	if tag == "" || !strings.HasPrefix(tag, "v") {
		return "", fmt.Errorf("resolve latest: unexpected URL %s", finalURL)
	}
	return tag, nil
}

// buildDownloadURL constructs the download URL for the given OS/arch.
func buildDownloadURL(version, goos, goarch string) (string, error) {
	// Map Go arch names to GreptimeDB release names.
	var osStr, archStr string
	switch goos {
	case "darwin":
		osStr = "darwin"
	case "linux":
		osStr = "linux"
	case "windows":
		osStr = "windows"
	default:
		return "", fmt.Errorf("install: unsupported OS %q", goos)
	}
	switch goarch {
	case "amd64":
		archStr = "amd64"
	case "arm64":
		if goos == "windows" {
			return "", fmt.Errorf("install: unsupported arch %q on %q", goarch, goos)
		}
		archStr = "arm64"
	default:
		return "", fmt.Errorf("install: unsupported arch %q", goarch)
	}

	// Example: greptime-darwin-arm64-v0.12.0.tar.gz
	filename := fmt.Sprintf("greptime-%s-%s-%s.tar.gz", osStr, archStr, version)
	return fmt.Sprintf("%s/download/%s/%s", githubReleaseBase, version, filename), nil
}

func downloadFile(dst io.Writer, url string) error {
	resp, err := downloadClient.Get(url) //nolint:gosec // URL is constructed internally
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d fetching %s", resp.StatusCode, url)
	}

	_, err = io.Copy(dst, resp.Body)
	return err
}

// verifyChecksum downloads the sha256sum file for the given version and verifies
// the tarball's checksum. Non-fatal: logs a warning if the checksum file is
// unavailable but does not block installation.
func verifyChecksum(tarball *os.File, version string, logger *slog.Logger) error {
	downloadURL, err := buildDownloadURL(version, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return nil // can't build URL, skip
	}
	checksumURL := downloadURL + ".sha256sum"

	resp, err := versionClient.Get(checksumURL) //nolint:gosec
	if err != nil {
		logger.Warn("checksum file unavailable, skipping verification", "err", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		logger.Warn("checksum file unavailable, skipping verification", "status", resp.StatusCode)
		return nil
	}

	scanner := bufio.NewScanner(resp.Body)
	var expectedHash string
	filename := filepath.Base(downloadURL)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && strings.HasSuffix(fields[1], filename) {
			expectedHash = fields[0]
			break
		}
	}
	if expectedHash == "" {
		logger.Warn("checksum entry not found in sha256sum file, skipping verification")
		return nil
	}

	if _, err := tarball.Seek(0, io.SeekStart); err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(h, tarball); err != nil {
		return fmt.Errorf("checksum: %w", err)
	}
	actualHash := hex.EncodeToString(h.Sum(nil))

	if actualHash != expectedHash {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expectedHash, actualHash)
	}
	logger.Info("checksum verified", "sha256", actualHash[:16]+"...")
	return nil
}

// extractBinary finds the `greptime` binary inside a .tar.gz and writes it to destPath.
func extractBinary(r io.Reader, destPath string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if filepath.Base(hdr.Name) != greptimeBinaryName() {
			continue
		}

		out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
		if err != nil {
			return fmt.Errorf("create binary: %w", err)
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return fmt.Errorf("write binary: %w", err)
		}
		return out.Close()
	}

	return fmt.Errorf("%s binary not found in archive", greptimeBinaryName())
}
