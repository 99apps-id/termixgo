<#
.SYNOPSIS
    Builds a local test installer for Termixgo on Windows.

.DESCRIPTION
    Compiles the local Windows amd64 binary, packages it into the existing
    installer bundle, verifies the artifacts, and runs a short smoke test
    (`--help` and `doctor`) so you can confirm the installer is usable.

.PARAMETER SkipHelp
    Skip the smoke test (`--help` and `doctor`). Useful for CI or unattended runs.
#>
[CmdletBinding()]
param(
    [switch]$SkipHelp
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$root = $PSScriptRoot
Push-Location $root
try {
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
        throw "go is not installed or not on PATH"
    }

    # The version is resolved before the build because the linker stamps it into
    # the binary. The install reads it back to fill DisplayVersion, so a build
    # without the stamp registers every machine as 0.1.0-dev.
    $version = (& git describe --tags --always --dirty 2>$null)
    if (-not $version) { $version = "0.1.0-dev" }
    $commit = (& git rev-parse --short HEAD 2>$null)
    if (-not $commit) { $commit = "unknown" }
    $date = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
    $stamp = "github.com/99apps-id/termixgo/internal/version"
    $ldflags = "-s -w -X $stamp.Version=$version -X $stamp.Commit=$commit -X $stamp.BuildDate=$date"

    Write-Host "== build windows amd64 binary =="
    $binary = Join-Path $root "dist\termixgo-windows-amd64.exe"
    if (-not (Test-Path (Split-Path $binary))) { New-Item -ItemType Directory -Path (Split-Path $binary) | Out-Null }
    $env:CGO_ENABLED = "0"
    $env:GOOS = "windows"
    $env:GOARCH = "amd64"
    & go build -trimpath -ldflags $ldflags -o $binary ./cmd/termixgo
    if ($LASTEXITCODE -ne 0) { throw "build failed" }
    $size = (Get-Item $binary).Length
    Write-Host ("  {0} ({1:N0} bytes, {2})" -f (Split-Path $binary -Leaf), $size, $version)

    Write-Host ""
    Write-Host "== package installer =="
    & (Join-Path $root "scripts\make-installer.ps1") -Version $version -Commit $commit -Binary $binary

    $installer = Join-Path $root "dist\Termixgo-$version-windows-amd64-installer.zip"
    if (-not (Test-Path $installer -PathType Leaf)) { throw "installer zip was not created at $installer" }
    Write-Host ("  {0}" -f (Split-Path $installer -Leaf))

    Write-Host ""
    Write-Host "== smoke test =="
    if (-not $SkipHelp) {
        & $binary --help
        if ($LASTEXITCODE -ne 0) { throw "smoke test --help failed" }
        Write-Host ""
        Write-Host "running doctor..."
        & $binary doctor
        if ($LASTEXITCODE -ne 0) { throw "smoke test doctor failed" }
    }

    Write-Host ""
    Write-Host "installer test passed"
    Write-Host "  binary : $binary"
    Write-Host "  zip    : $installer"
}
finally {
    Remove-Item Env:CGO_ENABLED, Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue
    Pop-Location
}
