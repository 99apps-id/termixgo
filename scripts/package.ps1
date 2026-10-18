<#
.SYNOPSIS
    Builds the Termixgo release artifacts into dist/.

.DESCRIPTION
    Cross-compiles every release target with a stamped version, writes one
    archive per platform, and records the SHA256 of each so a download can be
    verified before it is run.

    Everything is built with CGO_ENABLED=0. That is not a preference: a
    cross-OS cgo build needs a cross-compiler, and the released binary has to
    be the same static file on every platform. The race detector, which is the
    one thing that needs cgo, runs in the gate rather than here.

.PARAMETER Version
    The version to stamp. Defaults to the git describe of the working tree,
    which is the short commit when no tag exists.

.PARAMETER NoArchive
    Build the binaries only, skipping the archives and the checksum file.

.EXAMPLE
    powershell -File scripts/package.ps1
    powershell -File scripts/package.ps1 -Version 0.1.0
#>
[CmdletBinding()]
param(
    [string]$Version = "",
    [switch]$NoArchive
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$root = Split-Path -Parent $PSScriptRoot
Push-Location $root
try {
    if (-not $Version) {
        # describe gives a tag when there is one and the short commit
        # otherwise, so a local build still names a revision.
        $Version = (& git describe --tags --always --dirty 2>$null)
        if (-not $Version) { $Version = "0.1.0-dev" }
    }
    $commit = (& git rev-parse --short HEAD 2>$null)
    if (-not $commit) { $commit = "unknown" }
    $date = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")

    $module = "github.com/99apps-id/termixgo/internal/version"
    $ldflags = "-s -w -X $module.Version=$Version -X $module.Commit=$commit -X $module.BuildDate=$date"

    # Provider client credentials stay out of the repository. A local .env.local
    # (git-ignored) or the environment supplies the public Antigravity
    # installed-app pair, so a shipped binary needs no prompt at login.
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
    if ($env:TERMIXGO_ANTIGRAVITY_CLIENT_ID) {
        $ldflags += " -X $oauthModule.AntigravityClientID=$($env:TERMIXGO_ANTIGRAVITY_CLIENT_ID)"
    }
    if ($env:TERMIXGO_ANTIGRAVITY_CLIENT_SECRET) {
        $ldflags += " -X $oauthModule.AntigravityClientSecret=$($env:TERMIXGO_ANTIGRAVITY_CLIENT_SECRET)"
    }

    $dist = Join-Path $root "dist"
    if (Test-Path $dist) { Remove-Item $dist -Recurse -Force }
    New-Item -ItemType Directory -Path $dist | Out-Null

    $targets = @(
        @{ OS = "windows"; Arch = "amd64"; Ext = ".exe" },
        @{ OS = "windows"; Arch = "arm64"; Ext = ".exe" },
        @{ OS = "linux";   Arch = "amd64"; Ext = "" },
        @{ OS = "linux";   Arch = "arm64"; Ext = "" },
        @{ OS = "darwin";  Arch = "amd64"; Ext = "" },
        @{ OS = "darwin";  Arch = "arm64"; Ext = "" }
    )

    Write-Host "termixgo $Version ($commit)"
    Write-Host ""
    Write-Host "== compile =="

    $built = @()
    foreach ($target in $targets) {
        $name = "termixgo-$($target.OS)-$($target.Arch)$($target.Ext)"
        $out = Join-Path $dist $name
        $env:CGO_ENABLED = "0"
        $env:GOOS = $target.OS
        $env:GOARCH = $target.Arch
        & go build -trimpath -ldflags $ldflags -o $out ./cmd/termixgo
        if ($LASTEXITCODE -ne 0) { throw "build failed for $($target.OS)/$($target.Arch)" }
        $size = (Get-Item $out).Length
        Write-Host ("  {0,-34} {1,10:N0} bytes" -f $name, $size)
        $built += @{ Target = $target; Name = $name; Path = $out }
    }
    Remove-Item Env:CGO_ENABLED, Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue

    if ($NoArchive) {
        Write-Host ""
        Write-Host "binaries are in dist/"
        return
    }

    # The Windows installer is built here rather than on its own, because it
    # packages the binary that was just compiled and must carry the same
    # version string.
    Write-Host ""
    Write-Host "== windows installer =="
    & (Join-Path $PSScriptRoot "make-installer.ps1") -Version $Version -Commit $commit -Binary (Join-Path $dist "termixgo-windows-amd64.exe")

    Write-Host ""
    Write-Host "== archives =="
    foreach ($item in $built) {
        $target = $item.Target
        # Windows ships only the installer bundle below; a bare binary zip
        # would split the download story for no reason.
        if ($target.OS -eq "windows") { continue }
        # The archives are named for the platform they run on, so a person
        # downloading one does not have to open it to find out.
        $stem = "termixgo-$Version-$($target.OS)-$($target.Arch)"
        $archive = Join-Path $dist "$stem.tar.gz"
        # The tarball carries canonical names: the binary is `termixgo`
        # and the installer sits beside it, so the VPS flow is extract
        # and run with nothing to rename or hunt for.
        $stage = Join-Path $dist "tarball-stage"
        if (Test-Path $stage) { Remove-Item $stage -Recurse -Force }
        New-Item -ItemType Directory -Path $stage | Out-Null
        Copy-Item $item.Path (Join-Path $stage "termixgo") -Force
        Copy-Item (Join-Path $root "packaging\linux\install.sh") $stage -Force
        foreach ($doc in @("LICENSE", "NOTICE", "README.md")) {
            Copy-Item (Join-Path $root $doc) $stage -Force
        }
        # GNU tar reads `C:` as a remote host, so the archive and the
        # stage reach it as relative paths with no colon anywhere.
        Push-Location $dist
        try {
            & tar -czf "$stem.tar.gz" -C "tarball-stage" termixgo install.sh LICENSE NOTICE README.md
        }
        finally {
            Pop-Location
        }
        Remove-Item $stage -Recurse -Force
        if ($LASTEXITCODE -ne 0) { throw "archive failed for $stem" }
        Write-Host ("  {0}" -f (Split-Path $archive -Leaf))
    }

    Write-Host ""
    Write-Host "== checksums =="
    $lines = @()
    Get-ChildItem $dist -File | Where-Object { $_.Name -notlike "SHA256SUMS*" } | Sort-Object Name | ForEach-Object {
        $hash = (Get-FileHash $_.FullName -Algorithm SHA256).Hash.ToLower()
        $lines += "$hash  $($_.Name)"
    }
    # A checksum file must not end with a blank line or the verify loop has to
    # special-case it.
    $sumsPath = Join-Path $dist "SHA256SUMS"
    [System.IO.File]::WriteAllText($sumsPath, ($lines -join "`n") + "`n")
    $lines | ForEach-Object { Write-Host "  $_" }

    Write-Host ""
    Write-Host "artifacts are in dist/"
}
finally {
    Pop-Location
}
