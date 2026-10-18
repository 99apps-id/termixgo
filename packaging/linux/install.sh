#!/usr/bin/env bash
# Installs Termixgo for the current user on Linux, including a VPS.
#
# Nothing here needs root: the binary goes to ~/.local/bin, which stays
# inside the account. Re-running the installer replaces the binary in place,
# so an upgrade is the same command as the first install.
#
# Usage:
#   ./install.sh [options]
#
# Options:
#   --binary PATH   termixgo binary to install (default: ./termixgo next to
#                   this script, which is what the release tarball holds)
#   --prefix DIR    where to install (default: ~/.local/bin)
#   --no-path       skip the PATH change
#   -h, --help      show this help
#
# Typical VPS flow:
#   tar xzf termixgo-0.1.0-linux-amd64.tar.gz
#   cd termixgo-0.1.0-linux-amd64 && ./install.sh
#   exec "$SHELL" -l   # or log in again, then: termixgo
set -euo pipefail

PREFIX="$HOME/.local/bin"
BINARY=""
NO_PATH=0

while [ $# -gt 0 ]; do
  case "$1" in
    --binary) BINARY="$2"; shift 2 ;;
    --binary=*) BINARY="${1#--binary=}"; shift ;;
    --prefix) PREFIX="$2"; shift 2 ;;
    --prefix=*) PREFIX="${1#--prefix=}"; shift ;;
    --no-path) NO_PATH=1; shift ;;
    -h|--help)
      sed -n '2,/^set -euo/p' "$0" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *)
      echo "unknown option: $1 (try --help)" >&2
      exit 1
      ;;
  esac
done

HERE=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
if [ -z "$BINARY" ]; then
  BINARY="$HERE/termixgo"
fi
if [ ! -f "$BINARY" ]; then
  echo "termixgo binary not found at $BINARY" >&2
  echo "Download the tarball for your machine (uname -m: $(uname -m))," >&2
  echo "extract it, and run ./install.sh from inside." >&2
  exit 1
fi

echo "Termixgo installer"
echo ""
echo "== install =="
mkdir -p "$PREFIX"
target="$PREFIX/termixgo"
# Copy through a temporary name and rename over the old file, so an
# interrupted install leaves either the old binary or the new one.
staging="$target.new"
cp "$BINARY" "$staging"
chmod +x "$staging"
mv -f "$staging" "$target"
echo "  installed $target"

echo ""
echo "== PATH =="
if [ "$NO_PATH" -eq 1 ]; then
  echo "  skipped, as asked"
else
  case ":$PATH:" in
    *":$PREFIX:"*)
      echo "  $PREFIX is already on PATH"
      ;;
    *)
      shell_name=$(basename -- "${SHELL:-/bin/bash}")
      rc=""
      line="export PATH=\"\$HOME/.local/bin:\$PATH\""
      # A custom prefix cannot be expressed with $HOME, so write it literally.
      if [ "$PREFIX" != "$HOME/.local/bin" ]; then
        line="export PATH=\"$PREFIX:\$PATH\""
      fi
      case "$shell_name" in
        fish)
          config_fish="$HOME/.config/fish/config.fish"
          mkdir -p "$(dirname -- "$config_fish")"
          if ! grep -qF "# added by the Termixgo installer" "$config_fish" 2>/dev/null; then
            printf '\n# added by the Termixgo installer\nfish_add_path %s\n' "$PREFIX" >> "$config_fish"
            echo "  added $PREFIX to fish path (restart the shell)"
          else
            echo "  $PREFIX is already registered in fish config"
          fi
          ;;
        zsh) rc="$HOME/.zshrc" ;;
        *) rc="$HOME/.bashrc" ;;
      esac
      if [ -n "$rc" ]; then
        touch "$rc"
        # The marker (not the path) decides: the written line spells the
        # default prefix as $HOME, so matching the literal path would append
        # a duplicate on every re-run.
        if ! grep -qF "# added by the Termixgo installer" "$rc" 2>/dev/null; then
          printf '\n# added by the Termixgo installer\n%s\n' "$line" >> "$rc"
          echo "  added $PREFIX to PATH in $rc"
        else
          echo "  $PREFIX is already registered in $rc"
        fi
        echo "  restart the shell (or: export PATH=\"$PREFIX:\$PATH\") to use it now"
      fi
      export PATH="$PREFIX:$PATH"
      ;;
  esac
fi

echo ""
if ! "$target" version >/dev/null 2>&1; then
  echo "warning: $target version did not run; the download may be for another CPU" >&2
  echo "check: uname -m reports $(uname -m)" >&2
else
  echo "  verified: $("$target" version)"
fi

echo ""
echo "Termixgo is installed."
echo ""
echo "  termixgo            start the terminal UI (works from any folder)"
echo "  termixgo doctor     check the configuration and the key file"
echo "  termixgo --help     every subcommand"
echo ""
echo "The first launch opens the onboarding wizard, which asks for a provider"
echo "key and a default model. Local models need no key."
