<#
.SYNOPSIS
    Builds the official Windows installers for Termixgo (standalone Setup.exe and zip bundle).

.DESCRIPTION
    Compiles the latest Windows binary with version and commit stamping, packages
    both the standalone single-file TermixgoSetup.exe and the portable installer
    zip bundle into dist/.

.PARAMETER Version
    The version to stamp. Defaults to git describe or 0.2.0.

.PARAMETER NoZip
    Build only the standalone TermixgoSetup.exe, skipping the zip bundle.

.EXAMPLE
    powershell -File scripts/build-windows-installer.ps1
    powershell -File scripts/build-windows-installer.ps1 -Version 0.2.0
#>
[CmdletBinding()]
param(
    [string]$Version = "",
    [switch]$NoZip
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$root = Split-Path -Parent $PSScriptRoot
Push-Location $root

try {
    # 1. Resolve version and commit
    if (-not $Version) {
        $Version = (& git describe --tags --always 2>$null)
        if (-not $Version) { $Version = "0.2.0" }
    }
    $commit = (& git rev-parse --short HEAD 2>$null)
    if (-not $commit) { $commit = "unknown" }
    $date = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")

    Write-Host "======================================================"
    Write-Host " Building Termixgo Windows Installer"
    Write-Host " Version: $Version ($commit) - $date"
    Write-Host "======================================================"
    Write-Host ""

    $module = "github.com/99apps-id/termixgo/internal/version"
    $ldflags = "-s -w -X $module.Version=$Version -X $module.Commit=$commit -X $module.BuildDate=$date"

    # Read credentials if present in .env.local
    $envFile = Join-Path $root ".env.local"
    if (Test-Path $envFile) {
        foreach ($line in Get-Content $envFile) {
            $trimmed = $line.Trim()
            if (-not $trimmed -or $trimmed.StartsWith("#")) { continue }
            $eq = $trimmed.IndexOf("=")
            if ($eq -lt 1) { continue }
            $key = $trimmed.Substring(0, $eq).Trim()
            $value = $trimmed.Substring($eq + 1).Trim().Trim('"')
            Set-Item -Path "env:$key" -Value $value
        }
    }
    $oauthModule = "github.com/99apps-id/termixgo/internal/oauth"
    if ([string]::IsNullOrWhiteSpace($env:TERMIXGO_ANTIGRAVITY_CLIENT_ID)) {
        throw "TERMIXGO_ANTIGRAVITY_CLIENT_ID is required to build the Windows installer"
    }
    if ([string]::IsNullOrWhiteSpace($env:TERMIXGO_ANTIGRAVITY_CLIENT_SECRET)) {
        throw "TERMIXGO_ANTIGRAVITY_CLIENT_SECRET is required to build the Windows installer"
    }
    $ldflags += " -X $oauthModule.AntigravityClientID=$($env:TERMIXGO_ANTIGRAVITY_CLIENT_ID)"
    $ldflags += " -X $oauthModule.AntigravityClientSecret=$($env:TERMIXGO_ANTIGRAVITY_CLIENT_SECRET)"
    Write-Host "  Antigravity OAuth credentials: embedded"

    $dist = Join-Path $root "dist"
    if (-not (Test-Path $dist)) {
        New-Item -ItemType Directory -Path $dist | Out-Null
    }

    # 2. Build termixgo-windows-amd64.exe
    $winBinary = Join-Path $dist "termixgo-windows-amd64.exe"
    Write-Host "== [1/3] Compiling termixgo binary =="
    $env:CGO_ENABLED = "0"
    $env:GOOS = "windows"
    $env:GOARCH = "amd64"
    & go build -trimpath -ldflags $ldflags -o $winBinary ./cmd/termixgo
    if ($LASTEXITCODE -ne 0) { throw "go build failed for cmd/termixgo" }

    $sizeBin = (Get-Item $winBinary).Length
    Write-Host ("  {0} ({1:N0} bytes)" -f (Split-Path $winBinary -Leaf), $sizeBin)

    # Keep installer/termixgo.exe fresh
    $installerBin = Join-Path $root "installer\termixgo.exe"
    Copy-Item $winBinary $installerBin -Force

    # 3. Build standalone TermixgoSetup.exe
    Write-Host ""
    Write-Host "== [2/3] Building standalone TermixgoSetup.exe =="
    $setupDir = Join-Path $root "cmd\termixgo-setup"
    $payloadFile = Join-Path $setupDir "payload.bin"
    Copy-Item $winBinary $payloadFile -Force
    Copy-Item (Join-Path $root "installer\uninstall.ps1") (Join-Path $setupDir "uninstall.ps1") -Force

    $setupOut = Join-Path $dist "TermixgoSetup.exe"
    try {
        & go build -trimpath -ldflags "-s -w" -o $setupOut ./cmd/termixgo-setup
        if ($LASTEXITCODE -ne 0) { throw "go build failed for cmd/termixgo-setup" }
        $sizeSetup = (Get-Item $setupOut).Length
        Write-Host ("  {0} ({1:N0} bytes)" -f (Split-Path $setupOut -Leaf), $sizeSetup)
    }
    finally {
        # Restore dummy placeholder so git status stays clean
        [System.IO.File]::WriteAllBytes($payloadFile, [byte[]]@(48))
    }

    # 4. Build zip installer bundle
    if (-not $NoZip) {
        Write-Host ""
        Write-Host "== [3/3] Assembling installer zip bundle =="
        & (Join-Path $PSScriptRoot "make-installer.ps1") -Version $Version -Commit $commit -Binary $winBinary
    }

    Write-Host ""
    Write-Host "Windows installers are ready in dist/:"
    Get-ChildItem $dist -File | Where-Object { $_.Name -like "*Setup.exe" -or $_.Name -like "*windows*installer.zip" } | ForEach-Object {
        $hash = (Get-FileHash $_.FullName -Algorithm SHA256).Hash.ToLower()
        Write-Host ("  {0,-48} {1,10:N0} bytes  SHA256: {2}" -f $_.Name, $_.Length, $hash)
    }
}
finally {
    Remove-Item Env:CGO_ENABLED, Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue
    Pop-Location
}
