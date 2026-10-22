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

### Run from a source tree

Build it first, then double-click `install.bat`:

```powershell
go build -o installer/termixgo.exe ./cmd/termixgo
installer\install.bat
```

### Windows Options

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File install.ps1 [-Source <dir-or-exe>] [-Destination <dir>] [-NoPath] [-NoPause]

  -Source       where termixgo.exe lives; default: this folder
  -Destination  where it lands; default: %LOCALAPPDATA%\Programs\Termixgo
  -NoPath       do not touch the user PATH
  -NoPause      no "press Enter" at the end; use this for scripted runs
```

### Windows Uninstall

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File uninstall.ps1 [-Purge] [-Quiet]
```
