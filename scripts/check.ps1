# Termixgo pre-push gate for Windows PowerShell 5.1.
#
# Mirrors `make check`: formatting, vet, build and tests. Every step prints a
# clear status and the script exits non-zero on the first failure so the gate
# is safe to wire into a hook or CI.
$ErrorActionPreference = "Stop"

function Step($name, [scriptblock]$action) {
    Write-Host ""
    Write-Host "== $name ==" -ForegroundColor Cyan
    & $action
    if ($LASTEXITCODE -ne 0) {
        Write-Host "FAILED: $name" -ForegroundColor Red
        exit $LASTEXITCODE
    }
}

Push-Location (Join-Path $PSScriptRoot "..")
try {
    Step "format (gofmt -l)" {
        $unformatted = gofmt -s -l .
        if ($unformatted) {
            Write-Host $unformatted
            Write-Host "Run 'gofmt -s -w .' to fix." -ForegroundColor Yellow
            $global:LASTEXITCODE = 1
            exit 1
        }
        $global:LASTEXITCODE = 0
    }

    Step "vet (go vet ./...)" { go vet ./... }
    Step "build (go build ./...)" { go build ./... }

    # A cross-build catches a build tag or an OS-specific file that only
    # breaks on the platform nobody tested locally. cgo must be off: a
    # cross-OS cgo build needs a cross-compiler, and this machine has a
    # native one.
    Write-Host ""
    Write-Host "== cross build (linux, darwin) ==" -ForegroundColor Cyan
    $savedCgo = $env:CGO_ENABLED
    $env:CGO_ENABLED = "0"
    try {
        foreach ($target in @("linux", "darwin")) {
            $env:GOOS = $target
            $env:GOARCH = "amd64"
            go build -o (Join-Path $env:TEMP "termixgo-cross-$target") ./cmd/termixgo
            if ($LASTEXITCODE -ne 0) {
                Write-Host "FAILED: cross build for $target" -ForegroundColor Red
                exit $LASTEXITCODE
            }
            Write-Host "  $target/amd64 ok"
        }
    }
    finally {
        Remove-Item Env:\GOOS -ErrorAction SilentlyContinue
        Remove-Item Env:\GOARCH -ErrorAction SilentlyContinue
        if ($savedCgo) { $env:CGO_ENABLED = $savedCgo } else { Remove-Item Env:\CGO_ENABLED -ErrorAction SilentlyContinue }
    }

    Step "test (go test ./...)" { go test ./... }

    Write-Host ""
    Write-Host "== race (go test -race ./...) ==" -ForegroundColor Cyan
    # Delegated to scripts/race.ps1 so the compiler detection and the guidance
    # when it is missing live in one place. A missing toolchain exits 2, which
    # is reported as skipped rather than failed: the gate should still be
    # useful on a machine with no C compiler.
    & (Join-Path $PSScriptRoot "race.ps1")
    if ($LASTEXITCODE -eq 2) {
        Write-Host "race detector skipped: no C toolchain." -ForegroundColor Yellow
    }
    elseif ($LASTEXITCODE -ne 0) {
        Write-Host "FAILED: race (go test -race ./...)" -ForegroundColor Red
        exit $LASTEXITCODE
    }

    Write-Host ""
    Write-Host "All checks passed." -ForegroundColor Green
}
finally {
    Pop-Location
}
