#!/usr/bin/env bash
# Termixgo root installer entry point
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
exec bash "$SCRIPT_DIR/installer/install.sh" "$@"
