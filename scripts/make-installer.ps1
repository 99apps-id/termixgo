<#
.SYNOPSIS
    Assembles the Windows installer bundle for Termixgo.

.DESCRIPTION
    The bundle is a zip holding the binary, the install and uninstall scripts
    and a double-clickable install.bat. The user extracts it anywhere and runs
    install.bat, which copies the binary into a per-user programs directory,
    puts that directory on the user PATH and registers the install.

    This used to be a 7-Zip self-extracting exe, but the SFX modules bundled
    with stock 7-Zip ignore the config file that auto-runs the installer: the
    result only extracted and never installed. The config-reading module
    (7zS.sfx) ships in a separate extra package that cannot be assumed
    present, so a zip plus a batch entry point is the build with no hidden
    dependency.

.PARAMETER Version
    The version being packaged, used in the file name and shown while building.

.PARAMETER Commit
    The revision being packaged, shown while building.

.PARAMETER Binary
    The windows/amd64 binary to embed.

.EXAMPLE
    powershell -File scripts/make-installer.ps1 -Version 0.1.0 -Commit abc1234 -Binary dist/termixgo-windows-amd64.exe
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Version,
    [string]$Commit = "unknown",
    [Parameter(Mandatory = $true)][string]$Binary
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$root = Split-Path -Parent $PSScriptRoot
$dist = Join-Path $root "dist"

if (-not (Test-Path $Binary -PathType Leaf)) {
    throw "the binary to package was not found at $Binary"
}

$stage = Join-Path $dist "installer-stage"
if (Test-Path $stage) { Remove-Item $stage -Recurse -Force }
New-Item -ItemType Directory -Path $stage | Out-Null

Copy-Item $Binary (Join-Path $stage "termixgo.exe") -Force
Copy-Item (Join-Path $root "installer\install.bat") $stage -Force
Copy-Item (Join-Path $root "installer\install.ps1") $stage -Force
Copy-Item (Join-Path $root "installer\uninstall.ps1") $stage -Force
# install.sh travels too, so the same zip carries an entry point for cmd,
# PowerShell and a Linux shell beside the binary.
Copy-Item (Join-Path $root "packaging\linux\install.sh") $stage -Force
# The licence and the notice travel with the install, which is what the Apache
# licence asks for when distributing the binary.
Copy-Item (Join-Path $root "LICENSE") $stage -Force
Copy-Item (Join-Path $root "NOTICE") $stage -Force

$bundle = Join-Path $dist "Termixgo-$Version-windows-amd64-installer.zip"
if (Test-Path $bundle) { Remove-Item $bundle -Force }
$packed = $false
$sevenZip = Get-Command 7z -ErrorAction SilentlyContinue
if ($sevenZip) {
    try {
        & 7z a -tzip -bso0 -bsp0 $bundle (Join-Path $stage "*") 2>$null | Out-Null
        if ($LASTEXITCODE -eq 0 -and (Test-Path $bundle)) { $packed = $true }
    } catch {
        # Fall back if 7z shim fails
    }
}
if (-not $packed -and (Get-Command tar -ErrorAction SilentlyContinue)) {
    Push-Location $stage
    try {
        & tar -a -cf $bundle *
        if ($LASTEXITCODE -eq 0 -and (Test-Path $bundle)) { $packed = $true }
    } catch {}
    finally {
        Pop-Location
    }
}
if (-not $packed) {
    Compress-Archive -Path (Join-Path $stage "*") -DestinationPath $bundle -Force
}

Remove-Item $stage -Recurse -Force

$size = (Get-Item $bundle).Length
Write-Host ("  {0} ({1:N0} bytes, {2})" -f (Split-Path $bundle -Leaf), $size, $Commit)
