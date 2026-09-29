<#
.SYNOPSIS
    Installs Termixgo for the current user on Windows.

.DESCRIPTION
    Copies the binary into a per-user programs directory, puts that directory on
    the user PATH, adds a Start Menu entry, and registers the install so it
    appears in Add or remove programs.

    Nothing here needs administrator rights, and nothing is written outside the
    user profile. That is deliberate: an agent that can run commands on your
    machine should not ask for elevation in order to be installed, and a
    per-user install keeps the blast radius of a mistake inside your own
    account.

    Re-running the installer replaces the binary in place, which is what makes
    an upgrade the same command as the first install.

.PARAMETER Source
    Directory or file holding termixgo.exe. Defaults to the directory this
    script is in, which is where the installer unpacks it.

.PARAMETER Destination
    Where to install. Defaults to %LOCALAPPDATA%\Programs\Termixgo.

.PARAMETER NoPath
    Skip the PATH change. Useful in a locked-down image where the PATH is
    managed elsewhere.

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File install.ps1
#>
[CmdletBinding()]
param(
    [string]$Source = $PSScriptRoot,
    [string]$Destination = "",
    [switch]$NoPath
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

function Write-Step([string]$Text) { Write-Host "  $Text" }

if (-not $Destination) {
    $Destination = Join-Path $env:LOCALAPPDATA "Programs\Termixgo"
}

# The source may be the file itself or the folder holding it, because the
# installer unpacks a folder while a manual run may point straight at the exe.
$binary = $Source
if (Test-Path $Source -PathType Container) {
    $binary = Join-Path $Source "termixgo.exe"
}
if (-not (Test-Path $binary -PathType Leaf)) {
    throw "termixgo.exe was not found at $binary"
}

Write-Host "Termixgo installer"
Write-Host ""

# A running copy cannot be replaced on Windows, and a half-replaced install is
# worse than a refused one, so this stops before touching anything.
$running = Get-Process -Name "termixgo" -ErrorAction SilentlyContinue
if ($running) {
    throw "Termixgo is running (pid $($running.Id -join ', ')). Quit it and run the installer again."
}

Write-Host "== install =="
if (-not (Test-Path $Destination)) {
    New-Item -ItemType Directory -Path $Destination -Force | Out-Null
    Write-Step "created $Destination"
}
$target = Join-Path $Destination "termixgo.exe"

# Copy through a temporary name and then move it over the old one, so an
# interrupted install leaves either the old binary or the new one and never a
# truncated file.
$staging = "$target.new"
Copy-Item $binary $staging -Force
Move-Item $staging $target -Force
Write-Step "installed $target"

# The uninstaller travels with the install rather than living in the repository,
# so Add or remove programs still works after the download is deleted.
$uninstaller = Join-Path $PSScriptRoot "uninstall.ps1"
if (Test-Path $uninstaller) {
    Copy-Item $uninstaller (Join-Path $Destination "uninstall.ps1") -Force
    Write-Step "placed the uninstaller"
}

Write-Host ""
Write-Host "== PATH =="
if ($NoPath) {
    Write-Step "skipped, as asked"
}
else {
    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if (-not $userPath) { $userPath = "" }
    $entries = $userPath -split ";" | Where-Object { $_ -ne "" }
    if ($entries -contains $Destination) {
        Write-Step "$Destination is already on the user PATH"
    }
    else {
        $joined = (@($entries) + $Destination) -join ";"
        [Environment]::SetEnvironmentVariable("Path", $joined, "User")
        Write-Step "added $Destination to the user PATH"
        # The change is in the registry but not in this process, and not in any
        # shell that was already open, which is the one thing that surprises
        # people here.
        Write-Step "open a new terminal for the change to take effect"
    }
    # This session is updated too, so `termixgo version` works before the
    # operator opens anything.
    if (($env:Path -split ";") -notcontains $Destination) {
        $env:Path = "$env:Path;$Destination"
    }
}

Write-Host ""
Write-Host "== start menu =="
$startMenu = Join-Path $env:APPDATA "Microsoft\Windows\Start Menu\Programs"
$shortcut = Join-Path $startMenu "Termixgo.lnk"
try {
    $shell = New-Object -ComObject WScript.Shell
    $link = $shell.CreateShortcut($shortcut)
    $link.TargetPath = $target
    $link.WorkingDirectory = "%USERPROFILE%"
    $link.Description = "The Termixgo terminal coding agent"
    $link.Save()
    Write-Step "created $shortcut"
}
catch {
    # A shortcut is a convenience, not a requirement: the binary on PATH is the
    # whole product. A locked-down profile can refuse COM, so this is not fatal.
    Write-Step "could not create the shortcut: $($_.Exception.Message)"
}

Write-Host ""
Write-Host "== add or remove programs =="
$key = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\Termixgo"
New-Item -Path $key -Force | Out-Null
$version = (& $target version) -replace "Termixgo ", "" -replace " \(.*", ""
Set-ItemProperty -Path $key -Name "DisplayName" -Value "Termixgo"
Set-ItemProperty -Path $key -Name "DisplayVersion" -Value $version
Set-ItemProperty -Path $key -Name "Publisher" -Value "99apps-id"
Set-ItemProperty -Path $key -Name "InstallLocation" -Value $Destination
Set-ItemProperty -Path $key -Name "UninstallString" -Value "powershell.exe -NoProfile -ExecutionPolicy Bypass -File `"$Destination\uninstall.ps1`""
Set-ItemProperty -Path $key -Name "QuietUninstallString" -Value "powershell.exe -NoProfile -ExecutionPolicy Bypass -File `"$Destination\uninstall.ps1`" -Quiet"
Set-ItemProperty -Path $key -Name "NoModify" -Value 1 -Type DWord
Set-ItemProperty -Path $key -Name "NoRepair" -Value 1 -Type DWord
Write-Step "registered as $version"

Write-Host ""
Write-Host "Termixgo is installed."
Write-Host ""
Write-Host "  termixgo            start the terminal UI"
Write-Host "  termixgo doctor     check the configuration and the key file"
Write-Host "  termixgo --help     every subcommand"
Write-Host ""
Write-Host "The first launch opens the onboarding wizard, which asks for a provider"
Write-Host "key and a default model. Local models need no key."
