#!/usr/bin/env bash
# ==============================================================================
# Termixgo Linux Installer
# ==============================================================================
# Installs Termixgo for the current user or system-wide.
#
# Capabilities:
#   - Automatic architecture detection (x86_64 -> amd64, aarch64 -> arm64)
#   - Builds from local source if Go is present, or downloads official GitHub release
#   - Zero-root default: installs to ~/.local/bin without needing sudo
#   - Optional system-wide installation to /usr/local/bin
#   - Configures user PATH in bash, zsh, or fish
#   - Automatically generates and installs shell completions
#   - Creates desktop application shortcut (optional)
#   - Full uninstall support (--uninstall / --purge)
#
# Usage:
#   ./install.sh [options]
#
# Options:
#   --binary PATH     Use a pre-existing termixgo binary
#   --build           Force building from source using Go
#   --download        Force downloading the official binary from GitHub
#   --version VER     Target release version (default: latest, e.g. 0.1.6)
#   --prefix DIR      Install directory (default: ~/.local/bin)
#   --system          Install system-wide to /usr/local/bin (may require sudo)
#   --desktop         Install .desktop application launcher
#   --service         Install and enable 24/7 background systemd service
#   --no-path         Do not modify shell configuration files (.bashrc, etc.)
#   --no-completion   Do not install shell completion scripts
#   -y, --yes         Non-interactive mode; accept defaults automatically
#   --uninstall       Uninstall Termixgo
#   --purge           Used with --uninstall to also delete ~/.termixgo (keys & history)
#   -h, --help        Show this help message
# ==============================================================================

set -euo pipefail

# ANSI styling
BOLD='\033[1m'
GREEN='\033[0;32m'
CYAN='\033[0;36m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
RESET='\033[0m'

say()     { echo -e "${CYAN}▶${RESET} $*"; }
success() { echo -e "${GREEN}✔${RESET} $*"; }
warn()    { echo -e "${YELLOW}▲${RESET} $*"; }
error()   { echo -e "${RED}✖${RESET} $*" >&2; exit 1; }

REPO_OWNER="99apps-id"
REPO_NAME="termixgo"
GITHUB_REPO="${REPO_OWNER}/${REPO_NAME}"

PREFIX="${HOME}/.local/bin"
BINARY=""
FORCE_BUILD=0
FORCE_DOWNLOAD=0
VERSION=""
NO_PATH=0
NO_COMPLETION=0
INSTALL_DESKTOP=0
INSTALL_SERVICE=0
NON_INTERACTIVE=0
DO_UNINSTALL=0
DO_PURGE=0

# Detect root or script location
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"

# Parse arguments
while [ $# -gt 0 ]; do
  case "$1" in
    --binary) BINARY="$2"; shift 2 ;;
    --binary=*) BINARY="${1#--binary=}"; shift ;;
    --build) FORCE_BUILD=1; shift ;;
    --download) FORCE_DOWNLOAD=1; shift ;;
    --version) VERSION="$2"; shift 2 ;;
    --version=*) VERSION="${1#--version=}"; shift ;;
    --prefix) PREFIX="$2"; shift 2 ;;
    --prefix=*) PREFIX="${1#--prefix=}"; shift ;;
    --system) PREFIX="/usr/local/bin"; shift ;;
    --desktop) INSTALL_DESKTOP=1; shift ;;
    --service) INSTALL_SERVICE=1; shift ;;
    --no-path) NO_PATH=1; shift ;;
    --no-completion) NO_COMPLETION=1; shift ;;
    -y|--yes) NON_INTERACTIVE=1; shift ;;
    --uninstall) DO_UNINSTALL=1; shift ;;
    --purge) DO_PURGE=1; shift ;;
    -h|--help)
      echo "Usage: $0 [options]"
      echo ""
      echo "Options:"
      echo "  --binary PATH     Use a pre-existing termixgo binary"
      echo "  --build           Force building from source using Go"
      echo "  --download        Force downloading the official binary from GitHub"
      echo "  --version VER     Target release version (default: latest, e.g. 0.1.6)"
      echo "  --prefix DIR      Install directory (default: ~/.local/bin)"
      echo "  --system          Install system-wide to /usr/local/bin (may require sudo)"
      echo "  --desktop         Install .desktop application launcher"
      echo "  --service         Install and enable 24/7 background systemd service"
      echo "  --no-path         Do not modify shell configuration files (.bashrc, etc.)"
      echo "  --no-completion   Do not install shell completion scripts"
      echo "  -y, --yes         Non-interactive mode; accept defaults automatically"
      echo "  --uninstall       Uninstall Termixgo"
      echo "  --purge           Used with --uninstall to also delete ~/.termixgo (keys & history)"
      echo "  -h, --help        Show this help message"
      exit 0
      ;;
    *)
      error "Unknown option: $1 (try --help)"
      ;;
  esac
done

# ------------------------------------------------------------------------------
# UNINSTALLATION ROUTINE
# ------------------------------------------------------------------------------
if [ "$DO_UNINSTALL" -eq 1 ]; then
  echo -e "\n${BOLD}Termixgo Linux Uninstaller${RESET}\n"
  
  target="${PREFIX}/termixgo"
  if [ -f "$target" ]; then
    # Stop background service if running
    if command -v systemctl >/dev/null 2>&1; then
      if systemctl --user is-active termixgo.service >/dev/null 2>&1; then
        say "Stopping systemd service..."
        systemctl --user stop termixgo.service 2>/dev/null || true
        systemctl --user disable termixgo.service 2>/dev/null || true
      fi
    fi

    # Remove binary
    if [ -w "$(dirname "$target")" ]; then
      rm -f "$target"
    else
      say "Need sudo to remove $target"
      sudo rm -f "$target"
    fi
    success "Removed binary $target"
  else
    warn "Binary not found at $target"
  fi

  # Remove completions
  rm -f "${HOME}/.local/share/bash-completion/completions/termixgo"
  rm -f "${HOME}/.local/share/zsh/site-functions/_termixgo"
  rm -f "${HOME}/.zsh/completion/_termixgo"
  rm -f "${HOME}/.config/fish/completions/termixgo.fish"
  success "Removed shell completions"

  # Remove desktop shortcut
  if [ -f "${HOME}/.local/share/applications/termixgo.desktop" ]; then
    rm -f "${HOME}/.local/share/applications/termixgo.desktop"
    update-desktop-database "${HOME}/.local/share/applications" 2>/dev/null || true
    success "Removed desktop entry"
  fi

  # State directory (~/.termixgo)
  state_dir="${TERMIXGO_HOME:-${HOME}/.termixgo}"
  if [ "$DO_PURGE" -eq 1 ]; then
    if [ -d "$state_dir" ]; then
      rm -rf "$state_dir"
      success "Purged state directory: $state_dir (API keys and sessions deleted)"
    fi
  else
    if [ -d "$state_dir" ]; then
      say "Preserved state directory: $state_dir (holds your API keys and saved sessions)"
      echo "  Use --purge to remove configuration and keys as well."
    fi
  fi

  echo -e "\n${GREEN}${BOLD}Termixgo has been uninstalled.${RESET}\n"
  exit 0
fi

# ------------------------------------------------------------------------------
# INSTALLATION ROUTINE
# ------------------------------------------------------------------------------
echo -e "\n${BOLD}${CYAN}╔══════════════════════════════════════════════════╗${RESET}"
echo -e "${BOLD}${CYAN}║             Termixgo Linux Installer             ║${RESET}"
echo -e "${BOLD}${CYAN}╚══════════════════════════════════════════════════╝${RESET}\n"

TMP_DIR="$(mktemp -d /tmp/termixgo-install.XXXXXX)"
trap 'rm -rf "$TMP_DIR"' EXIT

# Architecture detection
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64)  GOARCH="amd64" ;;
  aarch64|arm64) GOARCH="arm64" ;;
  *) error "Unsupported CPU architecture: $ARCH" ;;
esac
say "Detected platform: Linux / $GOARCH ($ARCH)"

# Helper function to fetch URL to file
fetch_url() {
  local url="$1"
  local dest="$2"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$url" -o "$dest"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$dest" "$url"
  elif command -v python3 >/dev/null 2>&1; then
    python3 -c "import urllib.request; urllib.request.urlretrieve('$url', '$dest')"
  else
    error "Neither curl, wget, nor python3 found to download files."
  fi
}

# Helper function to build from source with version and .env.local OAuth stamps
build_from_source() {
  local out_bin="$1"
  local ver commit build_date ldflags
  ver="$(git -C "$ROOT_DIR" describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)"
  commit="$(git -C "$ROOT_DIR" rev-parse --short HEAD 2>/dev/null || echo unknown)"
  build_date="$(date -u +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo unknown)"
  if [ -f "$ROOT_DIR/.env.local" ]; then
    set -a
    # shellcheck disable=SC1090
    . "$ROOT_DIR/.env.local"
    set +a
  fi
  ldflags="-s -w -X github.com/99apps-id/termixgo/internal/version.Version=${ver} -X github.com/99apps-id/termixgo/internal/version.Commit=${commit} -X github.com/99apps-id/termixgo/internal/version.BuildDate=${build_date}"
  if [ -n "${TERMIXGO_ANTIGRAVITY_CLIENT_ID:-}" ]; then
    ldflags="${ldflags} -X github.com/99apps-id/termixgo/internal/oauth.AntigravityClientID=${TERMIXGO_ANTIGRAVITY_CLIENT_ID}"
  fi
  if [ -n "${TERMIXGO_ANTIGRAVITY_CLIENT_SECRET:-}" ]; then
    ldflags="${ldflags} -X github.com/99apps-id/termixgo/internal/oauth.AntigravityClientSecret=${TERMIXGO_ANTIGRAVITY_CLIENT_SECRET}"
  fi
  (cd "$ROOT_DIR" && CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags "$ldflags" -o "$out_bin" ./cmd/termixgo)
}

# 1. Resolve or Build the Binary
RESOLVED_BIN=""

if [ -n "$BINARY" ]; then
  if [ -f "$BINARY" ]; then
    RESOLVED_BIN="$BINARY"
    say "Using specified binary: $RESOLVED_BIN"
  else
    error "Specified binary not found: $BINARY"
  fi
elif [ "$FORCE_BUILD" -eq 1 ]; then
  say "Building from source..."
  if ! command -v go >/dev/null 2>&1; then
    error "Go compiler ('go') not found in PATH. Install Go or install without --build."
  fi
  build_from_source "$TMP_DIR/termixgo"
  RESOLVED_BIN="$TMP_DIR/termixgo"
  success "Built successfully from source"
elif [ "$FORCE_DOWNLOAD" -eq 0 ] && [ -f "$ROOT_DIR/bin/termixgo" ]; then
  RESOLVED_BIN="$ROOT_DIR/bin/termixgo"
  say "Found local built binary at $RESOLVED_BIN"
elif [ "$FORCE_DOWNLOAD" -eq 0 ] && [ -f "$ROOT_DIR/cmd/termixgo/main.go" ] && command -v go >/dev/null 2>&1; then
  say "Go compiler detected. Building Termixgo from source..."
  build_from_source "$TMP_DIR/termixgo"
  RESOLVED_BIN="$TMP_DIR/termixgo"
  success "Compiled cleanly from source tree"
else
  # Download official pre-built release from GitHub
  say "Resolving release from GitHub (${GITHUB_REPO})..."
  
  if [ -z "$VERSION" ] || [ "$VERSION" = "latest" ]; then
    RELEASE_API="https://api.github.com/repos/${GITHUB_REPO}/releases/latest"
    if command -v python3 >/dev/null 2>&1; then
      VERSION=$(python3 -c "
import urllib.request, json
req = urllib.request.Request('$RELEASE_API', headers={'User-Agent': 'Termixgo-Installer'})
try:
    with urllib.request.urlopen(req) as resp:
        data = json.loads(resp.read().decode())
        print(data.get('tag_name', '').lstrip('v'))
except Exception:
    print('0.1.6')
" 2>/dev/null || echo "0.1.6")
    elif command -v curl >/dev/null 2>&1; then
      VERSION=$(curl -sSL "$RELEASE_API" | grep -oP '"tag_name":\s*"v?\K[^"]+' | head -n1 || echo "0.1.6")
    else
      VERSION="0.1.6"
    fi
  fi
  
  CLEAN_VER="${VERSION#v}"
  TARBALL="termixgo_${CLEAN_VER}_linux_${GOARCH}.tar.gz"
  DOWNLOAD_URL="https://github.com/${GITHUB_REPO}/releases/download/v${CLEAN_VER}/${TARBALL}"
  
  say "Downloading Termixgo v${CLEAN_VER} (${TARBALL})..."
  fetch_url "$DOWNLOAD_URL" "$TMP_DIR/$TARBALL"
  
  tar -xzf "$TMP_DIR/$TARBALL" -C "$TMP_DIR"
  if [ -f "$TMP_DIR/termixgo" ]; then
    RESOLVED_BIN="$TMP_DIR/termixgo"
    success "Extracted release binary"
  else
    error "Failed to locate termixgo binary inside downloaded archive."
  fi
fi

# Verify the binary runs
chmod +x "$RESOLVED_BIN"
if ! "$RESOLVED_BIN" version >/dev/null 2>&1; then
  error "The binary '$RESOLVED_BIN' failed to execute on this architecture."
fi
BIN_VER=$("$RESOLVED_BIN" version)
success "Verified binary: $BIN_VER"

# 2. Install to Target Prefix
say "Installing to destination: $PREFIX..."
USE_SUDO=0
if [ ! -d "$PREFIX" ]; then
  if ! mkdir -p "$PREFIX" 2>/dev/null; then
    USE_SUDO=1
    sudo mkdir -p "$PREFIX"
  fi
elif [ ! -w "$PREFIX" ]; then
  USE_SUDO=1
fi

TARGET_FILE="$PREFIX/termixgo"
STAGING_FILE="$TARGET_FILE.new"

if [ "$USE_SUDO" -eq 1 ]; then
  say "Administrative permissions required for $PREFIX. Using sudo..."
  sudo cp "$RESOLVED_BIN" "$STAGING_FILE"
  sudo chmod 755 "$STAGING_FILE"
  sudo mv -f "$STAGING_FILE" "$TARGET_FILE"
else
  cp "$RESOLVED_BIN" "$STAGING_FILE"
  chmod 755 "$STAGING_FILE"
  mv -f "$STAGING_FILE" "$TARGET_FILE"
fi
success "Installed: $TARGET_FILE"

# 3. Configure Shell PATH
if [ "$NO_PATH" -eq 1 ]; then
  say "PATH configuration skipped (--no-path)."
else
  case ":$PATH:" in
    *":$PREFIX:"*)
      success "$PREFIX is already in your PATH"
      ;;
    *)
      SHELL_NAME=$(basename -- "${SHELL:-/bin/bash}")
      RC_FILE=""
      EXPORT_LINE="export PATH=\"${PREFIX}:\$PATH\""
      [ "$PREFIX" = "$HOME/.local/bin" ] && EXPORT_LINE='export PATH="$HOME/.local/bin:$PATH"'
      
      case "$SHELL_NAME" in
        fish)
          FISH_CONF="${HOME}/.config/fish/config.fish"
          mkdir -p "$(dirname "$FISH_CONF")"
          if ! grep -qF "# added by the Termixgo installer" "$FISH_CONF" 2>/dev/null; then
            printf '\n# added by the Termixgo installer\nfish_add_path %s\n' "$PREFIX" >> "$FISH_CONF"
            success "Added $PREFIX to fish path in $FISH_CONF"
          fi
          ;;
        zsh)  RC_FILE="${HOME}/.zshrc" ;;
        *)    RC_FILE="${HOME}/.bashrc" ;;
      esac

      if [ -n "$RC_FILE" ]; then
        touch "$RC_FILE"
        if ! grep -qF "# added by the Termixgo installer" "$RC_FILE" 2>/dev/null; then
          printf '\n# added by the Termixgo installer\n%s\n' "$EXPORT_LINE" >> "$RC_FILE"
          success "Added $PREFIX to PATH in $RC_FILE"
        else
          say "$PREFIX configuration already present in $RC_FILE"
        fi
      fi
      ;;
  esac
fi

# 4. Install Shell Completion
if [ "$NO_COMPLETION" -eq 0 ]; then
  SHELL_NAME=$(basename -- "${SHELL:-/bin/bash}")
  case "$SHELL_NAME" in
    bash)
      COMP_DIR="${HOME}/.local/share/bash-completion/completions"
      mkdir -p "$COMP_DIR"
      if "$TARGET_FILE" completion bash > "$COMP_DIR/termixgo" 2>/dev/null; then
        success "Installed bash completions to $COMP_DIR/termixgo"
      fi
      ;;
    zsh)
      COMP_DIR="${HOME}/.local/share/zsh/site-functions"
      mkdir -p "$COMP_DIR"
      if "$TARGET_FILE" completion zsh > "$COMP_DIR/_termixgo" 2>/dev/null; then
        success "Installed zsh completions to $COMP_DIR/_termixgo"
      fi
      ;;
    fish)
      COMP_DIR="${HOME}/.config/fish/completions"
      mkdir -p "$COMP_DIR"
      if "$TARGET_FILE" completion fish > "$COMP_DIR/termixgo.fish" 2>/dev/null; then
        success "Installed fish completions to $COMP_DIR/termixgo.fish"
      fi
      ;;
  esac
fi

# 5. Optional Desktop Entry
if [ "$INSTALL_DESKTOP" -eq 1 ] || [ -n "${XDG_CURRENT_DESKTOP:-}" ]; then
  DESKTOP_DIR="${HOME}/.local/share/applications"
  mkdir -p "$DESKTOP_DIR"
  cat << DESKTOPEOF > "$DESKTOP_DIR/termixgo.desktop"
[Desktop Entry]
Name=Termixgo
Comment=Terminal AI Coding Agent
Exec=termixgo
Path=$HOME
Icon=utilities-terminal
Terminal=true
Type=Application
Categories=Development;Utility;
Keywords=ai;coding;terminal;agent;termixgo;
DESKTOPEOF
  chmod +x "$DESKTOP_DIR/termixgo.desktop"
  update-desktop-database "$DESKTOP_DIR" 2>/dev/null || true
  success "Created desktop entry at $DESKTOP_DIR/termixgo.desktop"
fi

# 6. Optional Systemd Background Service
if [ "$INSTALL_SERVICE" -eq 1 ]; then
  say "Configuring 24/7 background systemd user service..."
  "$TARGET_FILE" service install
  success "Systemd background service enabled"
fi

# Summary
echo -e "\n${BOLD}${GREEN}══════════════════════════════════════════════════${RESET}"
echo -e "${BOLD}${GREEN}  Termixgo installation completed successfully!    ${RESET}"
echo -e "${BOLD}${GREEN}══════════════════════════════════════════════════${RESET}\n"

echo -e "Binary installed to: ${BOLD}$TARGET_FILE${RESET}"
echo ""
echo "Quick start:"
echo -e "  ${CYAN}termixgo${RESET}            Start the terminal agent UI"
echo -e "  ${CYAN}termixgo doctor${RESET}     Check provider keys and configuration"
echo -e "  ${CYAN}termixgo --help${RESET}     View all commands and options"
echo ""
if [ "$NO_PATH" -eq 0 ]; then
  echo -e "Note: If 'termixgo' is not found immediately in your current terminal session,"
  echo -e "run:  ${BOLD}export PATH=\"${PREFIX}:\$PATH\"${RESET}  or open a new terminal."
fi
echo ""
