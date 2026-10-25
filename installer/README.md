# Termixgo Installers

One-click and scripted installers for Termixgo on Linux and Windows. Both install per-user with no root/administrator privileges required by default.

---

## Linux

### Quick Install (Single command)

Run from the root of the repository:

```bash
./install.sh
```

Or from the `installer/` directory:

```bash
cd installer && ./install.sh
```

### What the Linux Installer Does

1. **Auto-Detection:** Detects CPU architecture (`x86_64` / `aarch64`).
2. **Binary Resolution:** Automatically builds from source if Go is present, or downloads the verified official release binary from GitHub.
3. **Zero Root:** Installs to `~/.local/bin/termixgo` (no `sudo` needed).
4. **Shell Integration:** Configures `PATH` in `~/.bashrc`, `~/.zshrc`, or `~/.config/fish/config.fish`.
5. **Shell Completion:** Generates and installs autocompletion scripts for bash, zsh, and fish.
6. **Desktop Launcher:** Creates `~/.local/share/applications/termixgo.desktop`.

### Linux Options

```bash
./install.sh [options]

  --binary PATH     Use a pre-existing termixgo binary
  --build           Force building from source using Go
  --download        Force downloading the official binary from GitHub
  --version VER     Target release version (default: latest, e.g. 0.1.6)
  --prefix DIR      Install directory (default: ~/.local/bin)
  --system          Install system-wide to /usr/local/bin (requires sudo)
  --desktop         Install .desktop application launcher
  --service         Install and enable 24/7 background systemd service
  --no-path         Do not modify shell configuration files
  --no-completion   Do not install shell completion scripts
  -y, --yes         Non-interactive mode
  --uninstall       Uninstall Termixgo
  --purge           Used with --uninstall to also delete ~/.termixgo (keys & history)
```

### Linux Uninstall

```bash
./installer/uninstall.sh
# or to purge configuration and stored API keys:
./installer/uninstall.sh --purge
```

---

## Windows

### Quick Install (One-click from source)

Double-click `install.bat` in the root of the repository (or run `install.bat` in a terminal). It compiles `termixgo.exe` if not already built and installs cleanly to `%LOCALAPPDATA%\Programs\Termixgo`.

### Standalone Executable Installer

Download and run `TermixgoSetup.exe` (from `dist/`). It is a standalone zero-dependency installer:
- Installs to `%LOCALAPPDATA%\Programs\Termixgo`
- Configures user PATH in registry and broadcasts environment update
- Creates Start Menu shortcut
- Registers in Windows Installed apps (Add or remove programs)
- Supports silent install: `TermixgoSetup.exe --silent`
- Supports clean uninstall: `TermixgoSetup.exe --uninstall`

### Building the Windows Installer

Run the builder script from PowerShell:

```powershell
powershell -File scripts/build-windows-installer.ps1
```

This compiles the latest binary with version stamping and produces:
- `dist/TermixgoSetup.exe` (standalone single-file installer)
- `dist/Termixgo-<Version>-windows-amd64-installer.zip` (portable installer bundle)

### Manual Script Options

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File installer\install.ps1 [-Source <dir-or-exe>] [-Destination <dir>] [-NoPath] [-NoPause]

  -Source       where termixgo.exe lives; default: this folder
  -Destination  where it lands; default: %LOCALAPPDATA%\Programs\Termixgo
  -NoPath       do not touch the user PATH
  -NoPause      no "press Enter" at the end; use this for scripted runs
```

### Windows Uninstall

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File installer\uninstall.ps1 [-Purge] [-Quiet]
# or:
TermixgoSetup.exe --uninstall
```

