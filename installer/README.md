# Termixgo Windows installer

Three files, one job: put `termixgo.exe` on your PATH for the current user,
with no administrator rights and nothing written outside your profile.

    install.bat       double-click entry point; it runs install.ps1 under
                      ExecutionPolicy Bypass
    install.ps1       the installer (copy, user PATH, Start Menu entry,
                      Add-or-remove-programs registration)
    uninstall.ps1     removes all of it; keeps your config and keys unless
                      -Purge is given

## Run it from a source tree

The scripts install the `termixgo.exe` that sits beside them. Build it here
first, then double-click `install.bat`:

    go build -o installer/termixgo.exe ./cmd/termixgo
    installer\install.bat

`termixgo.exe` is git-ignored: this folder is the source of the scripts, not
a place to commit binaries.

## Run it from a release bundle

    powershell -File scripts/make-installer.ps1 -Version 0.1.4 -Commit abc1234 -Binary dist/termixgo-windows-amd64.exe

produces `dist/Termixgo-<version>-windows-amd64-installer.zip`, which holds
these scripts plus a stamped `termixgo.exe`, the Linux `install.sh`, and
LICENSE/NOTICE. Extract anywhere and run `install.bat`. `scripts/package.ps1`
wraps the same flow for a full release.

## Options

    powershell -NoProfile -ExecutionPolicy Bypass -File install.ps1 [-Source <dir-or-exe>] [-Destination <dir>] [-NoPath] [-NoPause]

    -Source       where termixgo.exe lives; default: this folder
    -Destination  where it lands; default: %LOCALAPPDATA%\Programs\Termixgo
    -NoPath       do not touch the user PATH (locked-down machines)
    -NoPause      no "press Enter" at the end; use this for scripted runs

Uninstall:

    powershell -NoProfile -ExecutionPolicy Bypass -File uninstall.ps1 [-Purge] [-Quiet]

or use Add or remove programs, where the install registers itself as
"Termixgo".
