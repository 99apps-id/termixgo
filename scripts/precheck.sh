#!/usr/bin/env bash
# Verify the tree on ext4, which is where POSIX permissions actually hold.
#
# Windows developer:
#   wsl -d kali-linux -e bash /mnt/c/project/termigo-cli/scripts/precheck.sh
#
# The clone must live on ext4, never on /mnt/c: drvfs does not honour POSIX
# permissions, so the file-mode tests fail there for the wrong reason.
#
# Usage: precheck.sh [clone-dir]   (default: $HOME/termixgo-check)
set -euo pipefail

export PATH="$HOME/.local/go/bin:$HOME/go/bin:$PATH"

HERE=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
SRC=$(cd -- "$HERE/.." && pwd)/
DST=${1:-$HOME/termixgo-check}

mkdir -p "$DST"

rsync -a --delete \
  --exclude '.git' \
  --exclude 'dist' \
  --exclude 'bin' \
  --exclude '.gocache' \
  --exclude '.hg' \
  --exclude '.sl' \
  "$SRC" "$DST/"

cd "$DST"

echo "--- gofmt ---"
unformatted=$(gofmt -s -l . || true)
if [ -n "$unformatted" ]; then
  echo "$unformatted"
  echo "FAIL: gofmt found unformatted files"
  exit 1
fi
echo "clean"

echo "--- go vet ---"
go vet ./...
echo "clean"

echo "--- staticcheck ---"
staticcheck ./...
echo "clean"

echo "--- tests ---"
go test ./... -count=1

echo "--- race ---"
go test -race ./... -count=1

echo "--- ALL LINUX CHECKS PASSED ---"
