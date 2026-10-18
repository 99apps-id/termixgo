# Measures the release binary's working set in plain mode.
#
# The child is sampled while it is alive: plain mode blocks on stdin after
# printing the prompt, so reading its output until a known line appears
# synchronises the measurement without a sleep.
$ErrorActionPreference = "Stop"

$binary = Resolve-Path (Join-Path $PSScriptRoot "..\bin\termixgo.exe")
$env:TERMIXGO_HOME = Join-Path $env:TEMP "termixgo-measure"

$startInfo = New-Object System.Diagnostics.ProcessStartInfo
$startInfo.FileName = $binary.Path
$startInfo.RedirectStandardInput = $true
$startInfo.RedirectStandardOutput = $true
$startInfo.RedirectStandardError = $true
$startInfo.UseShellExecute = $false

$process = [System.Diagnostics.Process]::Start($startInfo)
$process.StandardInput.WriteLine("/help")

# Read until the last help line, which guarantees the UI is fully built and
# the process is now idle waiting for input.
$deadline = 200
while ($deadline-- -gt 0) {
    $line = $process.StandardOutput.ReadLine()
    if ($null -eq $line) { break }
    if ($line -match "/exit") { break }
}

$process.Refresh()
$workingSet = $process.WorkingSet64
$privateBytes = $process.PrivateMemorySize64

$process.StandardInput.WriteLine("/exit")
$process.StandardInput.Close()
$null = $process.StandardOutput.ReadToEnd()
$process.WaitForExit()

$size = (Get-Item $binary).Length
Write-Host ("binary size:        {0:N2} MB" -f ($size / 1MB))
Write-Host ("working set (idle): {0:N1} MB" -f ($workingSet / 1MB))
Write-Host ("private bytes:      {0:N1} MB" -f ($privateBytes / 1MB))
Write-Host ("exit code:          {0}" -f $process.ExitCode)
