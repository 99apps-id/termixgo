<#
.SYNOPSIS
    Assembles a single-file Windows installer for Termixgo.

.DESCRIPTION
    A Go binary is already self-contained, so the installer only has to unpack
    it, put it on the PATH and register the result. That is what this builds: a
    self-extracting executable that carries the binary and the install script,
    runs the script, and leaves nothing behind.

    The alternative is Inno Setup, which is not present on every machine and
    would add a build dependency for a task whose whole content is a file copy
    and a registry write. The 7-Zip SFX module is already needed to make the
    release archives, so this reuses it.

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

# The SFX module ships with 7-Zip. It is located rather than hard-coded, because
# the install path differs between a scoop install, a system install and a
# portable one. The console module is preferred: this installer is for a
# command line tool, so a console window showing progress suits it.
function Find-SfxModule {
    $candidates = New-Object System.Collections.Generic.List[string]

    $sevenZip = Get-Command 7z -ErrorAction SilentlyContinue
    if ($sevenZip) {
        # A scoop shim sits in its own directory, so the app tree is searched
        # rather than assumed to be a sibling of the shim.
        $shimDir = Split-Path $sevenZip.Source -Parent
        $searchRoots = @($shimDir, (Split-Path $shimDir -Parent), (Join-Path (Split-Path $shimDir -Parent) "apps\7zip"))
        foreach ($searchRoot in $searchRoots) {
            if (-not $searchRoot -or -not (Test-Path $searchRoot)) { continue }
            foreach ($found in (Get-ChildItem $searchRoot -Recurse -Filter "*7z*.sfx" -ErrorAction SilentlyContinue)) {
                $candidates.Add($found.FullName)
            }
        }
    }

    foreach ($path in @(
            "$env:ProgramFiles\7-Zip\7zCon.sfx",
            "${env:ProgramFiles(x86)}\7-Zip\7zCon.sfx",
            "$env:ProgramFiles\7-Zip\7z.sfx")) {
        if (Test-Path $path) { $candidates.Add($path) }
    }

    # The console module first, then the graphical one, because a console
    # installer shows the script's own output.
    foreach ($preferred in @("7zCon.sfx", "7z.sfx")) {
        foreach ($candidate in $candidates) {
            if ((Split-Path $candidate -Leaf) -eq $preferred) { return $candidate }
        }
    }
    if ($candidates.Count -gt 0) { return $candidates[0] }
    return ""
}

$sfx = Find-SfxModule
if (-not $sfx) {
    throw "no 7-Zip SFX module was found. Install 7-Zip (scoop install 7zip) and try again."
}

$stage = Join-Path $dist "installer-stage"
if (Test-Path $stage) { Remove-Item $stage -Recurse -Force }
New-Item -ItemType Directory -Path $stage | Out-Null

Copy-Item $Binary (Join-Path $stage "termixgo.exe") -Force
Copy-Item (Join-Path $root "packaging\windows\install.ps1") $stage -Force
Copy-Item (Join-Path $root "packaging\windows\uninstall.ps1") $stage -Force
# The licence and the notice travel with the install, which is what the Apache
# licence asks for when distributing the binary.
Copy-Item (Join-Path $root "LICENSE") $stage -Force
Copy-Item (Join-Path $root "NOTICE") $stage -Force

# The SFX module reads this verbatim, so it is written without a byte order mark
# and with the delimiter the format requires. The install script is invoked
# through powershell.exe because double-clicking a .ps1 runs Notepad instead of
# the script under the default file association.
$config = @'
;!@Install@!UTF-8!
Title="Termixgo __VERSION__"
RunProgram="powershell.exe -NoProfile -ExecutionPolicy Bypass -File install.ps1"
;!@InstallEnd@!
'@
$config = $config.Replace("__VERSION__", $Version)
$configPath = Join-Path $stage "sfx-config.txt"
[System.IO.File]::WriteAllText($configPath, $config)

$archive = Join-Path $dist "installer-payload.7z"
& 7z a -t7z -mx=9 -bso0 -bsp0 $archive (Join-Path $stage "*") | Out-Null
if ($LASTEXITCODE -ne 0) { throw "packing the payload failed" }

# The installer is the module, then the config, then the archive, concatenated
# in that order. Anything else and the module will not find its payload.
$installer = Join-Path $dist "Termixgo-$Version-windows-amd64-setup.exe"
$output = [System.IO.File]::Create($installer)
try {
    foreach ($part in @($sfx, $configPath, $archive)) {
        $bytes = [System.IO.File]::ReadAllBytes($part)
        $output.Write($bytes, 0, $bytes.Length)
    }
}
finally {
    $output.Dispose()
}

Remove-Item $stage -Recurse -Force
Remove-Item $archive -Force

$size = (Get-Item $installer).Length
Write-Host ("  {0} ({1:N0} bytes, from {2})" -f (Split-Path $installer -Leaf), $size, (Split-Path $sfx -Leaf))
