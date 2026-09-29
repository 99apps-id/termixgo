# Runs the Go race detector over the whole module.
#
# The detector needs cgo, and cgo on Windows needs a C compiler: mingw-w64
# provides gcc, and `scoop install mingw` installs it without administrator
# rights. Linux and macOS ship a compiler with the toolchain, so this usually
# just works there.
#
# Exits non-zero when a race is found, so it is safe to wire into a hook.
$ErrorActionPreference = "Stop"

Push-Location (Join-Path $PSScriptRoot "..")
try {
    $compiler = $null
    foreach ($candidate in @("gcc", "clang", "cc")) {
        $found = Get-Command $candidate -ErrorAction SilentlyContinue
        if ($found) { $compiler = $found.Source; break }
    }

    # A scoop mingw install has no shim, so its gcc is on disk but not on the
    # PATH. Looking in the known location beats telling the developer to install
    # a toolchain they already have.
    if (-not $compiler) {
        $mingw = Join-Path $env:USERPROFILE "scoop\apps\mingw\current\bin\gcc.exe"
        if (Test-Path $mingw) { $compiler = $mingw }
    }

    if (-not $compiler) {
        Write-Host "No C compiler found, so the race detector cannot run." -ForegroundColor Yellow
        Write-Host ""
        Write-Host "The detector is built on cgo, which needs a C toolchain:"
        Write-Host "  Windows:  scoop install mingw      (no administrator rights needed)"
        Write-Host "  Debian:   sudo apt install build-essential"
        Write-Host "  macOS:    xcode-select --install"
        Write-Host ""
        Write-Host "On Linux CI the detector runs on every change, so this is a local convenience."
        exit 2
    }

    Write-Host "C compiler: $compiler"
    $env:CGO_ENABLED = "1"
    # cgo shells out to `gcc` by name, so resolving the path is not enough:
    # the directory has to be on the PATH or the build fails with a message
    # that reads like a missing toolchain.
    $compilerDir = Split-Path -Parent $compiler
    if ($env:PATH -notlike "*$compilerDir*") {
        $env:PATH = "$compilerDir;$env:PATH"
    }

    # The detector instruments memory access, so a run is slower and the output
    # is only meaningful if it completes: a timeout would look like a pass.
    Write-Host "Running: go test -race ./..."
    Write-Host ""
    & go test -race ./... -count=1
    $code = $LASTEXITCODE

    Write-Host ""
    if ($code -eq 0) {
        Write-Host "Race detector: clean." -ForegroundColor Green
    }
    else {
        Write-Host "Race detector: FAILED. The report above names the two accesses that conflict." -ForegroundColor Red
        Write-Host "A data race is a real bug, not a flake: fix the access, do not retry." -ForegroundColor Red
    }
    exit $code
}
finally {
    Pop-Location
}
