#!/usr/bin/env sh
# Termixgo pre-push gate for Linux and macOS.
#
# Mirrors `make check`: formatting, vet, build and tests. The script exits on
# the first failure so it is safe to wire into a hook or CI.
set -eu

cd "$(dirname "$0")/.."

echo
echo "== format (gofmt -l) =="
unformatted="$(gofmt -s -l .)"
if [ -n "$unformatted" ]; then
    echo "$unformatted"
    echo "Run 'gofmt -s -w .' to fix." >&2
    exit 1
fi

echo
echo "== vet (go vet ./...) =="
go vet ./...

echo
echo "== build (go build ./...) =="
go build ./...

# A cross-build catches a build tag or an OS-specific file that only breaks on
# the platform nobody tested locally. cgo is disabled: a cross-OS cgo build
# needs a cross-compiler.
echo
echo "== cross build (linux, darwin) =="
for target in linux darwin; do
    CGO_ENABLED=0 GOOS="$target" GOARCH=amd64 go build -o /dev/null ./cmd/termixgo
    echo "  $target/amd64 ok"
done

echo
echo "== test (go test ./...) =="
go test ./...

# Delegated to scripts/race.sh so the compiler detection and the guidance when
# it is missing live in one place. Exit code 2 means no C toolchain, which is
# reported as skipped rather than failed.
echo
echo "== race (go test -race ./...) =="
set +e
sh scripts/race.sh
race_status=$?
set -e
if [ "$race_status" = "2" ]; then
    echo "race detector skipped: no C toolchain."
elif [ "$race_status" != "0" ]; then
    echo "FAILED: race (go test -race ./...)" >&2
    exit "$race_status"
fi

if command -v staticcheck >/dev/null 2>&1; then
    echo
    echo "== lint (staticcheck) =="
    staticcheck ./...
fi

echo
echo "All checks passed."
