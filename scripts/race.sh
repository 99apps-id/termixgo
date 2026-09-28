#!/usr/bin/env sh
# Runs the Go race detector over the whole module.
#
# The detector needs cgo, and cgo needs a C compiler. Most toolchains bring one
# already; where they do not, this says what to install instead of failing with
# a Go error that does not explain itself.
#
# Exits non-zero when a race is found, so it is safe to wire into a hook.
set -eu

cd "$(dirname "$0")/.."

compiler=""
for candidate in gcc clang cc; do
    if command -v "$candidate" >/dev/null 2>&1; then
        compiler="$(command -v "$candidate")"
        break
    fi
done

if [ -z "$compiler" ]; then
    echo "No C compiler found, so the race detector cannot run." >&2
    echo
    echo "The detector is built on cgo, which needs a C toolchain:"
    echo "  Debian:  sudo apt install build-essential"
    echo "  Fedora:  sudo dnf install gcc"
    echo "  Alpine:  sudo apk add build-base"
    echo "  macOS:   xcode-select --install"
    echo
    echo "On Linux CI the detector runs on every change, so this is a local convenience." >&2
    exit 2
fi

echo "C compiler: $compiler"
export CGO_ENABLED=1

echo "Running: go test -race ./..."
echo

if go test -race ./... -count=1; then
    echo
    echo "Race detector: clean."
else
    echo
    echo "Race detector: FAILED. The report above names the two accesses that conflict." >&2
    echo "A data race is a real bug, not a flake: fix the access, do not retry." >&2
    exit 1
fi
