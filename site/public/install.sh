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
# Wire one or both agent adapters (hooks + MCP + /tma1-peer skill) in one shot:
#   curl -fsSL https://tma1.ai/install.sh | TMA1_ADAPTER=claude-code bash
#   curl -fsSL https://tma1.ai/install.sh | TMA1_ADAPTER=codex bash
#   curl -fsSL https://tma1.ai/install.sh | TMA1_ADAPTER=claude-code,codex bash
#   curl -fsSL https://tma1.ai/install.sh | TMA1_ADAPTER=all bash        # same as both
# (Adapter install via curl-pipe writes only global files — hooks, MCP entries,
# skills/commands. Project-local CLAUDE.md / AGENTS.md blocks are NOT touched
# because curl-pipe runs in whatever cwd the user happens to be in; run
# `tma1-server install --adapter <name>` from a project dir to seed those.)
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
# Adapter(s) to wire into agents. Empty = skip. Accepts a comma-separated list
# or the alias `all` (= claude-code,codex). Each adapter registers hooks, MCP,
# and the /tma1-peer skill globally. Project-local files are skipped here —
# see register_adapter() for why.
TMA1_ADAPTER="${TMA1_ADAPTER:-}"

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

  # Create a `tma1` shorthand alongside tma1-server. The long name is the
  # canonical binary (referenced by launchd/systemd units, GitHub release
  # artifacts, and existing docs); the short name is a developer-facing
  # alias for `tma1 install`, `tma1 build`, etc. Both invoke the same
  # binary because Go's main.go dispatches on os.Args[1] regardless of
  # argv[0]. Replace any pre-existing file/symlink at the target.
  ln -sf "tma1-server" "${INSTALL_DIR}/tma1"
}

# --- Download GreptimeDB binary via official install script ---
# Minimum GreptimeDB version required by TMA1. Keep in sync with
# minRequiredVersion in server/internal/install/install.go.
MIN_GREPTIMEDB_VERSION="1.1.2"

# version_lt returns 0 (true) if $1 < $2.
# Compares major.minor.patch numerically; when equal, a pre-release
# version (e.g. 1.0.0-rc.2) is considered less than the release (1.0.0).
# Accepts an optional leading 'v' prefix (e.g. v1.0.0).
# Bash-compatible — works on macOS and Linux without requiring sort -V.
version_lt() {
  local ver_a="${1#v}" ver_b="${2#v}"  # strip optional v prefix
  local a_pre="${ver_a#*-}" b_pre="${ver_b#*-}"
  # If no hyphen, *-pattern matches the whole string — clear it.
  [ "$a_pre" = "$ver_a" ] && a_pre=""
  [ "$b_pre" = "$ver_b" ] && b_pre=""
  local a="${ver_a%%-*}" b="${ver_b%%-*}"  # numeric part only
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
    installed_ver=$("$greptime_bin" --version 2>/dev/null | grep '^[[:space:]]*version:' | awk '{print $2}' || true)
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

# --- Post-install hints ---
# Branches on whether TMA1_ADAPTER was set:
#  - set     → adapter(s) wired; tell user how to add project-local files later
#  - empty   → wiring options (one-shot adapter vs manual OTel env vars)
post_install() {
  info "Installed tma1-server to ${INSTALL_DIR}/tma1-server  (alias: tma1)"
  echo ""

  local data_dir="${TMA1_DATA_DIR:-$HOME/.tma1}"
  local greptime_config_path="${data_dir}/config/standalone.toml"

  # PATH guidance is the first actionable item — everything else below
  # assumes `tma1` is callable. Conditional so repeat installs stay quiet
  # for users whose PATH is already set.
  if ! command -v tma1 >/dev/null 2>&1 && ! command -v tma1-server >/dev/null 2>&1; then
    info "Add ${INSTALL_DIR} to your PATH (one-time):"
    echo "  export PATH=\"${INSTALL_DIR}:\$PATH\""
    echo "  (append the line above to ~/.bashrc, ~/.zshrc, or your shell profile)"
    echo ""
  fi

  echo "Dashboard:  http://localhost:${TMA1_PORT}"
  echo "Data dir:   ${data_dir}"
  echo ""
  echo "Useful commands:"
  echo "  tma1 install --adapter claude-code    # wire Claude Code (hooks + MCP + skill)"
  echo "  tma1 install --adapter codex          # wire Codex"
  echo "  tma1 build -- <command>               # wrap a build, tee output to TMA1"
  echo "  tma1 uninstall --adapter <name>       # reverse an adapter install"
  echo ""

  if [ -n "$TMA1_ADAPTER" ]; then
    echo "Adapter(s) wired globally: ${TMA1_ADAPTER}"
    echo "  - Hooks, MCP server entry, and /tma1-peer skill installed for each."
    echo "  - Project-local CLAUDE.md / AGENTS.md blocks were NOT written here."
    echo "    To seed the TMA1 context block in a project, cd into it and run:"
    echo "      tma1 install --adapter <claude-code|codex>"
    echo ""
  else
    echo "Next: wire TMA1 into an agent."
    echo ""
    echo "Option A — One-shot adapter (recommended; hooks + MCP + /tma1-peer):"
    echo "  tma1 install --adapter claude-code"
    echo "  tma1 install --adapter codex"
    echo "  (run from a project directory to also seed CLAUDE.md / AGENTS.md)"
    echo ""
    echo "  Or re-run this installer with TMA1_ADAPTER set:"
    echo "    curl -fsSL https://tma1.ai/install.sh | TMA1_ADAPTER=claude-code bash"
    echo "    curl -fsSL https://tma1.ai/install.sh | TMA1_ADAPTER=claude-code,codex bash"
    echo ""
    echo "Option B — Manual OTel config only (no hooks, no MCP, no skill):"
    echo "  Claude Code (~/.claude/settings.json):"
    echo '    "env": {'
    echo "      \"OTEL_EXPORTER_OTLP_ENDPOINT\": \"http://localhost:${TMA1_PORT}/v1/otlp\","
    echo '      "OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf",'
    echo '      "OTEL_METRICS_EXPORTER": "otlp",'
    echo '      "OTEL_LOGS_EXPORTER": "otlp"'
    echo '    }'
    echo ""
    echo "  Codex (~/.codex/config.toml):"
    echo '    [otel]'
    echo '    log_user_prompt = true'
    echo '    [otel.exporter.otlp-http]'
    echo "    endpoint = \"http://localhost:${TMA1_PORT}/v1/logs\""
    echo '    protocol = "binary"'
    echo '    [otel.trace_exporter.otlp-http]'
    echo "    endpoint = \"http://localhost:${TMA1_PORT}/v1/traces\""
    echo '    protocol = "binary"'
    echo '    [otel.metrics_exporter.otlp-http]'
    echo "    endpoint = \"http://localhost:${TMA1_PORT}/v1/metrics\""
    echo '    protocol = "binary"'
    echo ""
  fi

  echo "GreptimeDB config:  ${greptime_config_path}"
  echo "  (generated on first start; edit to tune CPU / memory limits)"
  echo ""
}

# --- Adapter setup: register tma1 into one or more agents ---
# Parses TMA1_ADAPTER as a comma-separated list (or `all`, which expands to
# claude-code,codex) and invokes `tma1-server install --adapter <name>
# --skip-project-files` for each. Idempotent on repeat install.
#
# Project-local files (CLAUDE.md / AGENTS.md instructions block, .gitignore
# entries) are intentionally skipped here: curl-pipe runs in whatever cwd
# the user happens to be in, so writing a block to a random directory's
# CLAUDE.md is worse than not writing one. Users wire project-local files
# later by `cd <project> && tma1-server install --adapter <name>`.
register_adapter() {
  if [ -z "$TMA1_ADAPTER" ]; then
    return
  fi
  local bin="${INSTALL_DIR}/tma1-server"
  if [ ! -x "$bin" ]; then
    warn "Adapter requested ('${TMA1_ADAPTER}') but ${bin} is not executable; skipping."
    return
  fi

  local list="$TMA1_ADAPTER"
  if [ "$list" = "all" ]; then
    list="claude-code,codex"
  fi

  local adapters name
  IFS=',' read -ra adapters <<< "$list"
  for name in "${adapters[@]}"; do
    # Strip surrounding whitespace so "claude-code, codex" is accepted.
    name="${name#"${name%%[![:space:]]*}"}"
    name="${name%"${name##*[![:space:]]}"}"
    [ -z "$name" ] && continue
    case "$name" in
      claude-code|codex) ;;
      *)
        warn "Unknown adapter '${name}' — skipping. Valid: claude-code, codex, all."
        continue
        ;;
    esac
    info "Registering ${name} adapter (hooks + MCP + skill, global-only)..."
    if ! "$bin" install --adapter "$name" --skip-project-files 2>&1; then
      warn "Adapter '${name}' registration failed. Retry: ${bin} install --adapter ${name}"
    fi
  done
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
  register_adapter
  post_install
}

main
