//go:build windows

package main

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

//go:embed payload.bin
var embeddedBinary []byte

//go:embed uninstall.ps1
var embeddedUninstaller []byte

const appName = "Termixgo"
const defaultVersion = "0.2.0"
const publisher = "99apps-id"

func main() {
	silent := false
	uninstall := false
	targetDir := ""

	for i := 1; i < len(os.Args); i++ {
		arg := os.Args[i]
		switch {
		case arg == "-s" || arg == "--silent" || arg == "-y" || arg == "--yes":
			silent = true
		case arg == "--uninstall" || arg == "-u":
			uninstall = true
		case (arg == "-d" || arg == "--dir" || arg == "--destination") && i+1 < len(os.Args):
			i++
			targetDir = os.Args[i]
		case arg == "-h" || arg == "--help":
			printUsage()
			return
		}
	}

	if targetDir == "" {
		localAppData := os.Getenv("LOCALAPPDATA")
		if localAppData == "" {
			userProfile := os.Getenv("USERPROFILE")
			localAppData = filepath.Join(userProfile, "AppData", "Local")
		}
		targetDir = filepath.Join(localAppData, "Programs", appName)
	}

	if uninstall {
		err := runUninstall(targetDir, silent)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Uninstallation error: %v\n", err)
			pauseIfInteractive(silent)
			os.Exit(1)
		}
		pauseIfInteractive(silent)
		return
	}

	err := runInstall(targetDir, silent)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Installation error: %v\n", err)
		pauseIfInteractive(silent)
		os.Exit(1)
	}
	pauseIfInteractive(silent)
}

func printUsage() {
	fmt.Printf("%s Windows Setup\n\n", appName)
	fmt.Println("Usage:")
	fmt.Println("  TermixgoSetup.exe [options]")
	fmt.Println()
	fmt.Println("Options:")
	fmt.Println("  -y, -s, --silent       Non-interactive silent mode")
	fmt.Println("  -d, --dir <path>       Target install directory (default: %LOCALAPPDATA%\\Programs\\Termixgo)")
	fmt.Println("  --uninstall            Uninstall Termixgo and clean up registry/PATH")
	fmt.Println("  -h, --help             Show this help")
}

func runInstall(targetDir string, silent bool) error {
	if !silent {
		fmt.Println("======================================================")
		fmt.Printf("               %s Windows Setup\n", appName)
		fmt.Println("======================================================")
		fmt.Println()
	}

	// 1. Verify that payload is present
	if len(embeddedBinary) <= 1 {
		// If payload was not baked into binary, check adjacent termixgo.exe
		adjacent := filepath.Join(filepath.Dir(os.Args[0]), "termixgo.exe")
		data, err := os.ReadFile(adjacent)
		if err != nil {
			return fmt.Errorf("embedded binary not found and no adjacent termixgo.exe found at %s", adjacent)
		}
		embeddedBinary = data
	}

	// 2. Check if termixgo is currently running
	if isProcessRunning("termixgo.exe") {
		return fmt.Errorf("termixgo.exe is currently running; please close it before proceeding")
	}

	step(silent, fmt.Sprintf("Preparing destination: %s", targetDir))
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("create install directory: %w", err)
	}

	// 3. Write binary atomically
	targetExe := filepath.Join(targetDir, "termixgo.exe")
	stagingExe := targetExe + ".new"

	step(silent, "Extracting executable...")
	if err := os.WriteFile(stagingExe, embeddedBinary, 0o755); err != nil {
		return fmt.Errorf("write temporary executable: %w", err)
	}
	_ = os.Remove(targetExe)
	if err := os.Rename(stagingExe, targetExe); err != nil {
		return fmt.Errorf("place executable: %w", err)
	}
	step(silent, fmt.Sprintf("Installed %s", targetExe))

	// 4. Write uninstaller script
	if len(embeddedUninstaller) > 0 {
		uninstallerPath := filepath.Join(targetDir, "uninstall.ps1")
		_ = os.WriteFile(uninstallerPath, embeddedUninstaller, 0o644)
	}

	// 5. Update user PATH in registry
	step(silent, "Updating user PATH environment variable...")
	if err := appendUserPath(targetDir); err != nil {
		fmt.Printf("  warning: failed to update PATH automatically: %v\n", err)
	} else {
		step(silent, "Added to user PATH")
	}

	// 6. Create Start Menu shortcut
	step(silent, "Creating Start Menu shortcut...")
	if err := createStartMenuShortcut(targetExe); err != nil {
		// Non-fatal warning
		step(silent, fmt.Sprintf("Shortcut creation skipped: %v", err))
	} else {
		step(silent, "Created Start Menu shortcut")
	}

	// 7. Register in Windows Add or Remove Programs
	step(silent, "Registering application in Windows settings...")
	if err := registerUninstallKey(targetDir, targetExe); err != nil {
		step(silent, fmt.Sprintf("Registry registration skipped: %v", err))
	} else {
		step(silent, "Registered in Add or Remove Programs")
	}

	if !silent {
		fmt.Println()
		fmt.Printf("%s has been installed successfully.\n\n", appName)
		fmt.Println("To get started, open a new Command Prompt or PowerShell and type:")
		fmt.Println("  termixgo")
		fmt.Println()
		fmt.Println("Commands:")
		fmt.Println("  termixgo            Start interactive terminal coding agent")
		fmt.Println("  termixgo doctor     Verify configuration and model API keys")
		fmt.Println("  termixgo --help     Show all subcommands and flags")
		fmt.Println()
	}

	return nil
}

func runUninstall(targetDir string, silent bool) error {
	if !silent {
		fmt.Printf("Uninstalling %s...\n", appName)
	}

	if isProcessRunning("termixgo.exe") {
		return fmt.Errorf("termixgo.exe is currently running; close it before uninstalling")
	}

	// 1. Remove Start Menu shortcut
	appData := os.Getenv("APPDATA")
	if appData != "" {
		shortcut := filepath.Join(appData, `Microsoft\Windows\Start Menu\Programs`, appName+".lnk")
		_ = os.Remove(shortcut)
	}

	// 2. Remove Uninstall registry key
	_ = registry.DeleteKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Uninstall\`+appName)

	// 3. Remove from user PATH
	_ = removeUserPath(targetDir)

	// 4. Remove installation files and folder
	_ = os.Remove(filepath.Join(targetDir, "termixgo.exe"))
	_ = os.Remove(filepath.Join(targetDir, "uninstall.ps1"))
	_ = os.Remove(targetDir)

	if !silent {
		fmt.Printf("%s has been uninstalled successfully.\n", appName)
	}
	return nil
}

func step(silent bool, msg string) {
	if !silent {
		fmt.Printf("  [+] %s\n", msg)
	}
}

func pauseIfInteractive(silent bool) {
	if silent {
		return
	}
	fmt.Print("Press Enter to finish...")
	var b [1]byte
	_, _ = os.Stdin.Read(b[:])
}

func isProcessRunning(name string) bool {
	cmd := exec.Command("tasklist", "/FI", "IMAGENAME eq "+name, "/NH")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(out)), strings.ToLower(name))
}

func appendUserPath(dir string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()

	existingPath, _, err := k.GetStringValue("Path")
	if err != nil && err != registry.ErrNotExist {
		return err
	}

	entries := strings.Split(existingPath, ";")
	cleanDir := filepath.Clean(dir)
	for _, entry := range entries {
		if strings.EqualFold(filepath.Clean(strings.TrimSpace(entry)), cleanDir) {
			// Already in PATH
			return nil
		}
	}

	var newPath string
	if strings.TrimSpace(existingPath) == "" {
		newPath = dir
	} else {
		newPath = strings.TrimRight(existingPath, ";") + ";" + dir
	}

	if err := k.SetStringValue("Path", newPath); err != nil {
		return err
	}

	broadcastEnvironmentChange()
	return nil
}

func removeUserPath(dir string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()

	existingPath, _, err := k.GetStringValue("Path")
	if err != nil {
		return err
	}

	entries := strings.Split(existingPath, ";")
	cleanDir := filepath.Clean(dir)
	var kept []string
	changed := false

	for _, entry := range entries {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			continue
		}
		if strings.EqualFold(filepath.Clean(trimmed), cleanDir) {
			changed = true
			continue
		}
		kept = append(kept, trimmed)
	}

	if !changed {
		return nil
	}

	if err := k.SetStringValue("Path", strings.Join(kept, ";")); err != nil {
		return err
	}

	broadcastEnvironmentChange()
	return nil
}

func broadcastEnvironmentChange() {
	user32 := windows.NewLazySystemDLL("user32.dll")
	proc := user32.NewProc("SendMessageTimeoutW")
	if proc == nil {
		return
	}
	envPtr, err := windows.UTF16PtrFromString("Environment")
	if err != nil {
		return
	}
	var res uintptr
	_, _, _ = proc.Call(
		0xFFFF, // HWND_BROADCAST
		0x001A, // WM_SETTINGCHANGE
		0,
		uintptr(unsafe.Pointer(envPtr)),
		0x0002, // SMTO_ABORTIFHUNG
		5000,
		uintptr(unsafe.Pointer(&res)),
	)
}

func createStartMenuShortcut(targetExe string) error {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return fmt.Errorf("APPDATA not defined")
	}
	programsDir := filepath.Join(appData, `Microsoft\Windows\Start Menu\Programs`)
	shortcutPath := filepath.Join(programsDir, appName+".lnk")
	userProfile := os.Getenv("USERPROFILE")

	script := fmt.Sprintf(`$w = New-Object -ComObject WScript.Shell; $s = $w.CreateShortcut('%s'); $s.TargetPath = '%s'; $s.WorkingDirectory = '%s'; $s.Description = 'Termixgo terminal coding agent'; $s.Save()`,
		shortcutPath, targetExe, userProfile)

	cmd := exec.Command("powershell", "-NoProfile", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Run()
}

func registerUninstallKey(installDir, targetExe string) error {
	keyPath := `Software\Microsoft\Windows\CurrentVersion\Uninstall\` + appName
	k, _, err := registry.CreateKey(registry.CURRENT_USER, keyPath, registry.ALL_ACCESS)
	if err != nil {
		return err
	}
	defer k.Close()

	version := defaultVersion
	cmd := exec.Command(targetExe, "version")
	if out, err := cmd.Output(); err == nil {
		s := strings.TrimSpace(string(out))
		s = strings.TrimPrefix(s, "Termixgo ")
		if idx := strings.Index(s, " "); idx != -1 {
			s = s[:idx]
		}
		if s != "" {
			version = s
		}
	}

	uninstallScript := filepath.Join(installDir, "uninstall.ps1")

	_ = k.SetStringValue("DisplayName", appName)
	_ = k.SetStringValue("DisplayVersion", version)
	_ = k.SetStringValue("Publisher", publisher)
	_ = k.SetStringValue("InstallLocation", installDir)
	_ = k.SetStringValue("DisplayIcon", targetExe)
	_ = k.SetStringValue("UninstallString", fmt.Sprintf(`powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%s"`, uninstallScript))
	_ = k.SetStringValue("QuietUninstallString", fmt.Sprintf(`powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%s" -Quiet`, uninstallScript))
	_ = k.SetDWordValue("NoModify", 1)
	_ = k.SetDWordValue("NoRepair", 1)

	return nil
}
