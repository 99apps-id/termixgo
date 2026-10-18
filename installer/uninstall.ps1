<#
.SYNOPSIS
    Removes a per-user Termixgo install.

.DESCRIPTION
    Removes the binary, the PATH entry, the Start Menu shortcut and the
    Add or remove programs registration.

    Configuration and secrets are kept unless -Purge is given. That is the
    deliberate default: reinstalling should not cost you your provider keys,
    and a removal is usually an upgrade or a move rather than a decision to
    stop using the agent.

.PARAMETER Destination
    The install directory. Defaults to %LOCALAPPDATA%\Programs\Termixgo.

.PARAMETER Purge
    Also delete the state directory. This removes your API keys and every saved
    conversation, and cannot be undone.

.PARAMETER Quiet
    Print nothing. Used by the Add or remove programs entry.

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File uninstall.ps1
    powershell -ExecutionPolicy Bypass -File uninstall.ps1 -Purge
#>
[CmdletBinding()]
param(
    [string]$Destination = "",
    [switch]$Purge,
    [switch]$Quiet
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

function Say([string]$Text) { if (-not $Quiet) { Write-Host $Text } }

if (-not $Destination) {
    $Destination = Join-Path $env:LOCALAPPDATA "Programs\Termixgo"
}

Say "Termixgo uninstaller"
Say ""

$running = Get-Process -Name "termixgo" -ErrorAction SilentlyContinue
if ($running) {
    throw "Termixgo is running (pid $($running.Id -join ', ')). Quit it and run the uninstaller again."
}

# The install directory is removed last, after the shortcut and the registry
# entry that point at it, so a partial removal never leaves a shortcut to
# nothing.
$startMenu = Join-Path $env:APPDATA "Microsoft\Windows\Start Menu\Programs\Termixgo.lnk"
if (Test-Path $startMenu) {
    Remove-Item $startMenu -Force
    Say "  removed the Start Menu shortcut"
}

$key = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\Termixgo"
if (Test-Path $key) {
    Remove-Item $key -Recurse -Force
    Say "  removed the Add or remove programs entry"
}

$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath) {
    $entries = $userPath -split ";" | Where-Object { $_ -ne "" }
    # The entry is only touched when it is actually there. Rewriting the PATH
    # unconditionally would report a removal that never happened, and would
    # also drop any empty entry the operator had for a reason.
    if ($entries -contains $Destination) {
        $kept = $entries | Where-Object { $_ -ne $Destination }
        [Environment]::SetEnvironmentVariable("Path", ($kept -join ";"), "User")
        Say "  removed $Destination from the user PATH"
        Say "  open a new terminal for the change to take effect"
    }
    else {
        Say "  $Destination was not on the user PATH"
    }
}

if (Test-Path $Destination) {
    Remove-Item $Destination -Recurse -Force
    Say "  removed $Destination"
}

if ($Purge) {
    # The state directory is either the override or the default, and both are
    # checked because a run with TERMIXGO_HOME set writes to the first.
    $home_ = $env:TERMIXGO_HOME
    if (-not $home_) { $home_ = Join-Path $env:USERPROFILE ".termixgo" }
    if (Test-Path $home_) {
        Remove-Item $home_ -Recurse -Force
        Say "  removed the state directory $home_"
        Say ""
        Say "  Your API keys and conversations are gone. This cannot be undone."
    }
}
else {
    $home_ = $env:TERMIXGO_HOME
    if (-not $home_) { $home_ = Join-Path $env:USERPROFILE ".termixgo" }
    Say ""
    Say "  Kept $home_, which holds your configuration, API keys and sessions."
    Say "  Pass -Purge to delete it as well."
}

Say ""
Say "Termixgo is uninstalled."
