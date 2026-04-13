#!/usr/bin/env bash
# TMA1 installer — downloads the latest tma1-server binary and registers it as a service.
#
# Install or upgrade:
#   curl -fsSL https://tma1.ai/install.sh | bash
#
# Pin a specific version:
#   curl -fsSL https://tma1.ai/install.sh | TMA1_VERSION=v0.1.0 bash
#
# Force reinstall (wipes all data):
#   curl -fsSL https://tma1.ai/install.sh | TMA1_FORCE=1 bash
#
# Uninstall:
#   macOS:  launchctl bootout gui/$(id -u) ~/Library/LaunchAgents/ai.tma1.server.plist && rm ~/Library/LaunchAgents/ai.tma1.server.plist
#   Linux:  systemctl --user disable --now tma1-server && rm ~/.config/systemd/user/tma1-server.service
#   Both:   rm -rf ~/.tma1
set -euo pipefail

REPO="tma1-ai/tma1"
INSTALL_DIR="${TMA1_INSTALL_DIR:-$HOME/.tma1/bin}"
TMA1_PORT="${TMA1_PORT:-14318}"
TMA1_FORCE="${TMA1_FORCE:-0}"
TMA1_GREPTIMEDB_VERSION="${TMA1_GREPTIMEDB_VERSION:-latest}"

info()  { printf "\033[1;34m==>\033[0m %s\n" "$1"; }
warn()  { printf "\033[1;33mWarning:\033[0m %s\n" "$1"; }
error() { printf "\033[1;31mError:\033[0m %s\n" "$1" >&2; exit 1; }

# --- Detect OS and architecture ---
detect_platform() {
  local os arch
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(uname -m)"

  case "$os" in
    linux)  OS="linux" ;;
    darwin) OS="darwin" ;;
    *)      error "Unsupported OS: $os" ;;
  esac

  case "$arch" in
    x86_64|amd64)   ARCH="amd64" ;;
    arm64|aarch64)   ARCH="arm64" ;;
    *)               error "Unsupported architecture: $arch" ;;
  esac
}

# --- Resolve latest release tag ---
resolve_version() {
  if [ -n "${TMA1_VERSION:-}" ]; then
    VERSION="$TMA1_VERSION"
    return
  fi

  info "Resolving latest version..."
  # Try stable release first (GitHub /releases/latest only returns non-prerelease).
  VERSION="$(curl -fsSL -o /dev/null -w '%{redirect_url}' \
    "https://github.com/${REPO}/releases/latest" | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+[^/]*')" || true
  if [ -z "$VERSION" ]; then
    # Fall back to most recent tag. The tags API returns results in reverse
    # chronological order, unlike the releases API which uses an unstable
    # sort that breaks with prerelease suffixes (e.g. alpha9 > alpha10).
    VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/tags?per_page=1" \
      | grep -oE '"name"\s*:\s*"v[^"]+' | head -1 | grep -oE 'v[0-9]+.*')" \
      || error "Failed to resolve latest version. Set TMA1_VERSION to install a specific version."
  fi
}

# --- Download and verify ---
download() {
  local url archive checksum_url tmp_dir
  archive="tma1-server-${OS}-${ARCH}.tar.gz"
  url="https://github.com/${REPO}/releases/download/${VERSION}/${archive}"
  checksum_url="${url}.sha256sum"

  tmp_dir="$(mktemp -d)"
  # shellcheck disable=SC2064  # Intentional: expand tmp_dir now, not at exit time (local var)
  trap "rm -rf '${tmp_dir}'" EXIT

  info "Downloading ${archive} (${VERSION})..."
  curl -fSL -o "${tmp_dir}/${archive}" "$url" \
    || error "Download failed. Check https://github.com/${REPO}/releases for available binaries."

  info "Verifying checksum..."
  if curl -fsSL -o "${tmp_dir}/checksum.txt" "$checksum_url" 2>/dev/null; then
    cd "$tmp_dir"
    if command -v sha256sum >/dev/null 2>&1; then
      sha256sum -c checksum.txt
    elif command -v shasum >/dev/null 2>&1; then
      shasum -a 256 -c checksum.txt
    else
      info "Warning: no sha256sum or shasum found, skipping checksum verification."
    fi
    cd - >/dev/null
  else
    info "Warning: checksum file not found, skipping verification."
  fi

  info "Extracting to ${INSTALL_DIR}..."
  mkdir -p "$INSTALL_DIR"
  tar -xzf "${tmp_dir}/${archive}" -C "$INSTALL_DIR"
  chmod +x "${INSTALL_DIR}/tma1-server"
}

# --- Download GreptimeDB binary via official install script ---
# Minimum GreptimeDB version required by TMA1. Keep in sync with
# minRequiredVersion in server/internal/install/install.go.
MIN_GREPTIMEDB_VERSION="1.0.0"

# version_lt returns 0 (true) if $1 < $2.
# Compares major.minor.patch numerically; when equal, a pre-release
# version (e.g. 1.0.0-rc.2) is considered less than the release (1.0.0).
# POSIX-compatible — works on macOS and Linux without sort -V.
version_lt() {
  local a_pre="${1#*-}" b_pre="${2#*-}"
  # If no hyphen, *-pattern matches the whole string — clear it.
  [ "$a_pre" = "$1" ] && a_pre=""
  [ "$b_pre" = "$2" ] && b_pre=""
  local a="${1%%-*}" b="${2%%-*}"  # numeric part only
  local a1 a2 a3 b1 b2 b3
  IFS=. read -r a1 a2 a3 <<EOF
$a
EOF
  IFS=. read -r b1 b2 b3 <<EOF
$b
EOF
  a1="${a1:-0}" a2="${a2:-0}" a3="${a3:-0}"
  b1="${b1:-0}" b2="${b2:-0}" b3="${b3:-0}"
  [ "$a1" -lt "$b1" ] 2>/dev/null && return 0
  [ "$a1" -gt "$b1" ] 2>/dev/null && return 1
  [ "$a2" -lt "$b2" ] 2>/dev/null && return 0
  [ "$a2" -gt "$b2" ] 2>/dev/null && return 1
  [ "$a3" -lt "$b3" ] 2>/dev/null && return 0
  [ "$a3" -gt "$b3" ] 2>/dev/null && return 1
  # Same numeric version: pre-release < release.
  [ -n "$a_pre" ] && [ -z "$b_pre" ] && return 0
  return 1
}

download_greptimedb() {
  local greptime_bin="${INSTALL_DIR}/greptime"
  if [ -f "$greptime_bin" ] && [ "$TMA1_FORCE" != "1" ]; then
    # Check if the installed version meets the minimum requirement.
    local installed_ver
    installed_ver=$("$greptime_bin" --version 2>/dev/null | grep '^version:' | awk '{print $2}' || true)
    if [ -n "$installed_ver" ] && ! version_lt "$installed_ver" "$MIN_GREPTIMEDB_VERSION"; then
      info "GreptimeDB ${installed_ver} already installed (>= ${MIN_GREPTIMEDB_VERSION}), skipping download."
      return
    fi
    if [ -n "$installed_ver" ]; then
      info "GreptimeDB ${installed_ver} is below minimum ${MIN_GREPTIMEDB_VERSION}, upgrading..."
    else
      info "Cannot determine GreptimeDB version, upgrading..."
    fi
  fi

  mkdir -p "$INSTALL_DIR"
  info "Downloading GreptimeDB via official install script..."
  local gdb_install_url="https://raw.githubusercontent.com/greptimeteam/greptimedb/main/scripts/install.sh"
  local ok=0
  # The official script installs to the current working directory.
  # It accepts an optional version argument (default: latest).
  if [ "$TMA1_GREPTIMEDB_VERSION" != "latest" ]; then
    (cd "$INSTALL_DIR" && curl -fsSL "$gdb_install_url" | sh -s -- "$TMA1_GREPTIMEDB_VERSION") && ok=1
  else
    (cd "$INSTALL_DIR" && curl -fsSL "$gdb_install_url" | sh) && ok=1
  fi
  if [ "$ok" != "1" ]; then
    warn "GreptimeDB download failed. tma1-server will download it on first start."
    return
  fi
  info "GreptimeDB installed to ${greptime_bin}"
}

# --- Stop existing service before upgrade ---
stop_service() {
  case "$(uname -s)" in
    Darwin)
      local plist_path="$HOME/Library/LaunchAgents/ai.tma1.server.plist"
      if [ -f "$plist_path" ]; then
        info "Stopping existing TMA1 service..."
        launchctl bootout "gui/$(id -u)" "$plist_path" 2>/dev/null || true
      fi
      ;;
    Linux)
      if systemctl --user is-active --quiet tma1-server 2>/dev/null; then
        info "Stopping existing TMA1 service..."
        systemctl --user stop tma1-server 2>/dev/null || true
      fi
      ;;
  esac
}

# --- Wait for health endpoint ---
wait_for_health() {
  local url="http://127.0.0.1:${TMA1_PORT}/health"
  local attempts=0
  local max_attempts=30
  info "Waiting for TMA1 to become ready..."
  while [ "$attempts" -lt "$max_attempts" ]; do
    if curl -sf "$url" >/dev/null 2>&1; then
      info "TMA1 is running and healthy."
      return 0
    fi
    attempts=$((attempts + 1))
    sleep 1
  done
  warn "TMA1 did not become ready within ${max_attempts}s. Check logs for details."
  return 1
}

# --- macOS: launchd ---
setup_launchd() {
  local plist_path="$HOME/Library/LaunchAgents/ai.tma1.server.plist"
  local log_path="$HOME/Library/Logs/tma1-server.log"
  local bin_path="${INSTALL_DIR}/tma1-server"
  local data_dir="${TMA1_DATA_DIR:-$HOME/.tma1}"

  mkdir -p "$HOME/Library/LaunchAgents"
  mkdir -p "$HOME/Library/Logs"

  info "Writing launchd plist to ${plist_path}..."
  cat > "$plist_path" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>ai.tma1.server</string>

  <key>ProgramArguments</key>
  <array>
    <string>${bin_path}</string>
  </array>

  <key>EnvironmentVariables</key>
  <dict>
    <key>TMA1_DATA_DIR</key>
    <string>${data_dir}</string>
    <key>TMA1_PORT</key>
    <string>${TMA1_PORT}</string>
  </dict>

  <key>RunAtLoad</key>
  <true/>

  <key>KeepAlive</key>
  <true/>

  <key>StandardOutPath</key>
  <string>${log_path}</string>

  <key>StandardErrorPath</key>
  <string>${log_path}</string>

  <key>ProcessType</key>
  <string>Background</string>

  <key>SoftResourceLimits</key>
  <dict>
    <key>NumberOfFiles</key>
    <integer>1048576</integer>
  </dict>
  <key>HardResourceLimits</key>
  <dict>
    <key>NumberOfFiles</key>
    <integer>1048576</integer>
  </dict>
</dict>
</plist>
PLIST

  info "Loading TMA1 service via launchctl..."
  launchctl bootstrap "gui/$(id -u)" "$plist_path" 2>/dev/null \
    || launchctl load "$plist_path" 2>/dev/null \
    || warn "Failed to load launchd service. You can start manually: ${bin_path}"

  wait_for_health || true
}

# --- Linux: systemd user service ---
setup_systemd() {
  local unit_dir="$HOME/.config/systemd/user"
  local unit_path="${unit_dir}/tma1-server.service"
  local data_dir="${TMA1_DATA_DIR:-$HOME/.tma1}"

  # systemd --user requires XDG_RUNTIME_DIR and DBUS_SESSION_BUS_ADDRESS
  if ! systemctl --user status >/dev/null 2>&1; then
    warn "systemd user session not available. You can start manually: ${INSTALL_DIR}/tma1-server"
    return
  fi

  mkdir -p "$unit_dir"

  info "Writing systemd unit to ${unit_path}..."
  cat > "$unit_path" <<UNIT
[Unit]
Description=TMA1 Server — LLM Agent Observability
After=network.target

[Service]
Type=simple
ExecStart=${INSTALL_DIR}/tma1-server
LimitNOFILE=infinity
Restart=on-failure
RestartSec=3
Environment=TMA1_DATA_DIR=${data_dir}
Environment=TMA1_PORT=${TMA1_PORT}

[Install]
WantedBy=default.target
UNIT

  info "Enabling and starting TMA1 service..."
  systemctl --user daemon-reload
  systemctl --user enable --now tma1-server

  wait_for_health || true
}

# --- Service registration dispatcher ---
setup_service() {
  case "$(uname -s)" in
    Darwin) setup_launchd ;;
    Linux)  setup_systemd ;;
    *)      warn "Auto-start not supported on this OS. Start manually: ${INSTALL_DIR}/tma1-server" ;;
  esac
}

# --- Add to PATH hint ---
post_install() {
  info "Installed tma1-server to ${INSTALL_DIR}/tma1-server"
  echo ""

  local data_dir="${TMA1_DATA_DIR:-$HOME/.tma1}"
  local greptime_config_path="${data_dir}/config/standalone.toml"

  # Check if already in PATH
  if ! command -v tma1-server >/dev/null 2>&1; then
    echo "Add TMA1 to your PATH:"
    echo ""
    echo "  export PATH=\"${INSTALL_DIR}:\$PATH\""
    echo ""
    echo "Then add the line above to your shell profile (~/.bashrc, ~/.zshrc, etc.)."
    echo ""
  fi

  echo "Configure your agent (e.g. Claude Code ~/.claude/settings.json):"
  echo ""
  echo '  "env": {'
  echo "    \"OTEL_EXPORTER_OTLP_ENDPOINT\": \"http://localhost:${TMA1_PORT}/v1/otlp\","
  echo '    "OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf",'
  echo '    "OTEL_METRICS_EXPORTER": "otlp",'
  echo '    "OTEL_LOGS_EXPORTER": "otlp"'
  echo '  }'
  echo ""
  echo "Codex (~/.codex/config.toml):"
  echo ""
  echo '  [otel]'
  echo '  log_user_prompt = true'
  echo '  [otel.exporter.otlp-http]'
  echo "  endpoint = \"http://localhost:${TMA1_PORT}/v1/logs\""
  echo '  protocol = "binary"'
  echo '  [otel.trace_exporter.otlp-http]'
  echo "  endpoint = \"http://localhost:${TMA1_PORT}/v1/traces\""
  echo '  protocol = "binary"'
  echo '  [otel.metrics_exporter.otlp-http]'
  echo "  endpoint = \"http://localhost:${TMA1_PORT}/v1/metrics\""
  echo '  protocol = "binary"'
  echo ""
  echo "Dashboard: http://localhost:${TMA1_PORT}"
  echo "GreptimeDB config: ${greptime_config_path}"
  echo "  Generated automatically on first start and reused on later restarts."
  echo "  Edit it if you want to tune GreptimeDB CPU or memory limits."
  echo ""
}

# --- Force reinstall: wipe existing data ---
force_clean() {
  if [ "$TMA1_FORCE" != "1" ]; then
    return
  fi
  local data_dir="${TMA1_DATA_DIR:-$HOME/.tma1}"
  warn "TMA1_FORCE=1: removing ${data_dir} (all data, config, and logs will be deleted)"
  rm -rf "$data_dir"
}

# --- Main ---
main() {
  info "Installing TMA1..."
  detect_platform
  resolve_version
  stop_service
  force_clean
  download
  download_greptimedb
  setup_service
  post_install
}

main
