@echo off
rem Double-click entry point for installing Termixgo on Windows.
rem Installs per-user to %LOCALAPPDATA%\Programs\Termixgo without requiring admin rights.
setlocal
cd /d "%~dp0"
if not exist "installer\termixgo.exe" (
    echo Building termixgo.exe...
    go build -trimpath -ldflags "-s -w" -o installer\termixgo.exe ./cmd/termixgo
    if errorlevel 1 (
        echo Build failed.
        pause
        exit /b 1
    )
)
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0installer\install.ps1" -Source "%~dp0installer"
