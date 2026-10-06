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

    Write-Host "== build windows amd64 binary =="
    $binary = Join-Path $root "dist\termixgo-windows-amd64.exe"
    if (-not (Test-Path (Split-Path $binary))) { New-Item -ItemType Directory -Path (Split-Path $binary) | Out-Null }
    $env:CGO_ENABLED = "0"
    $env:GOOS = "windows"
    $env:GOARCH = "amd64"
    & go build -trimpath -ldflags "-s -w" -o $binary ./cmd/termixgo
    if ($LASTEXITCODE -ne 0) { throw "build failed" }
    $size = (Get-Item $binary).Length
    Write-Host ("  {0} ({1:N0} bytes)" -f (Split-Path $binary -Leaf), $size)

    Write-Host ""
    Write-Host "== package installer =="
    $version = (& git describe --tags --always --dirty 2>$null)
    if (-not $version) { $version = "0.1.0-dev" }
    $commit = (& git rev-parse --short HEAD 2>$null)
    if (-not $commit) { $commit = "unknown" }
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
