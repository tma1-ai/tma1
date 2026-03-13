#!/usr/bin/env bash
# TMA1 installer — downloads the latest tma1-server binary and registers it as a service.
# Usage: curl -fsSL https://tma1.ai/install.sh | sh
#
# Uninstall:
#   macOS:  launchctl bootout gui/$(id -u) ~/Library/LaunchAgents/ai.tma1.server.plist && rm ~/Library/LaunchAgents/ai.tma1.server.plist
#   Linux:  systemctl --user disable --now tma1-server && rm ~/.config/systemd/user/tma1-server.service
#   Both:   rm -rf ~/.tma1
set -euo pipefail

REPO="tma1-ai/tma1"
INSTALL_DIR="${TMA1_INSTALL_DIR:-$HOME/.tma1/bin}"
TMA1_PORT="${TMA1_PORT:-14318}"

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
  VERSION="$(curl -fsSL -o /dev/null -w '%{redirect_url}' \
    "https://github.com/${REPO}/releases/latest" | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+[^/]*')" \
    || error "Failed to resolve latest version. Set TMA1_VERSION to install a specific version."
}

# --- Download and verify ---
download() {
  local url archive checksum_url tmp_dir
  archive="tma1-server-${OS}-${ARCH}.tar.gz"
  url="https://github.com/${REPO}/releases/download/${VERSION}/${archive}"
  checksum_url="${url}.sha256sum"

  tmp_dir="$(mktemp -d)"
  trap 'rm -rf "$tmp_dir"' EXIT

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
  local data_dir="${TMA1_DATA_DIR:-\$HOME/.tma1}"

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
ExecStart=%h/.tma1/bin/tma1-server
Restart=on-failure
RestartSec=3
Environment=TMA1_DATA_DIR=%h/.tma1
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
  echo '    "OTEL_EXPORTER_OTLP_ENDPOINT": "http://localhost:14318/v1/otlp",'
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
  echo '  endpoint = "http://localhost:14318/v1/logs"'
  echo '  protocol = "binary"'
  echo '  [otel.trace_exporter.otlp-http]'
  echo '  endpoint = "http://localhost:14318/v1/traces"'
  echo '  protocol = "binary"'
  echo '  [otel.metrics_exporter.otlp-http]'
  echo '  endpoint = "http://localhost:14318/v1/metrics"'
  echo '  protocol = "binary"'
  echo ""
  echo "Dashboard: http://localhost:${TMA1_PORT}"
  echo ""
}

# --- Main ---
main() {
  info "Installing TMA1..."
  detect_platform
  resolve_version
  stop_service
  download
  setup_service
  post_install
}

main
