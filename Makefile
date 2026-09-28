# Termixgo development tasks. Run `make help` for the list.

BINARY := bin/termixgo
PKG := ./...

# A local build reports a real revision, so a bug report names the commit it
# came from. A release build overrides these through .goreleaser.yaml.
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo unknown)
MODULE := github.com/99apps-id/termixgo/internal/version
LDFLAGS := -s -w \
	-X $(MODULE).Version=$(VERSION) \
	-X $(MODULE).Commit=$(COMMIT) \
	-X $(MODULE).BuildDate=$(DATE)

.PHONY: help build run test lint fmt vet check race clean cross

help:
	@echo "Termixgo targets:"
	@echo "  build   compile the binary to $(BINARY)"
	@echo "  run     build and start the terminal UI"
	@echo "  test    run the unit tests"
	@echo "  race    run the unit tests under the race detector"
	@echo "  lint    go vet plus staticcheck when available"
	@echo "  fmt     format every Go file"
	@echo "  vet     run go vet"
	@echo "  check   fmt check, vet, build, test and race (the pre-push gate)"
	@echo "  cross   build linux, macOS and Windows binaries into dist/"
	@echo "  clean   remove build output"
	@echo ""
	@echo "The race detector needs a C compiler. Windows: scoop install mingw."

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/termixgo

run: build
	$(BINARY)

test:
	go test $(PKG)

# The race detector needs a C compiler and cgo. scripts/race.sh finds a
# compiler and explains what to install when there is none, so the target is
# the same everywhere.
race:
	sh scripts/race.sh

lint:
	go vet $(PKG)
	@command -v staticcheck >/dev/null 2>&1 && staticcheck $(PKG) || echo "staticcheck not installed, skipping"

fmt:
	gofmt -s -w .

vet:
	go vet $(PKG)

check:
	@fmt_out="$$(gofmt -s -l .)"; if [ -n "$$fmt_out" ]; then echo "unformatted files:"; echo "$$fmt_out"; exit 1; fi
	go vet $(PKG)
	go build $(PKG)
	go test $(PKG)
	@set +e; sh scripts/race.sh; race_status=$$?; set -e; \
		if [ "$$race_status" = "2" ]; then echo "race detector skipped: no C toolchain"; \
		elif [ "$$race_status" != "0" ]; then exit "$$race_status"; fi

cross:
	mkdir -p dist
	GOOS=linux GOARCH=amd64 go build -o dist/termixgo-linux-amd64 ./cmd/termixgo
	GOOS=linux GOARCH=arm64 go build -o dist/termixgo-linux-arm64 ./cmd/termixgo
	GOOS=darwin GOARCH=arm64 go build -o dist/termixgo-darwin-arm64 ./cmd/termixgo
	GOOS=windows GOARCH=amd64 go build -o dist/termixgo-windows-amd64.exe ./cmd/termixgo

clean:
	rm -rf bin dist
