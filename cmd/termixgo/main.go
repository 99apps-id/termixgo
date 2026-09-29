// Command termixgo is the Termixgo terminal agent: a single binary that runs
// the agent loop entirely in the terminal, with no web view and no runtime
// beyond the binary itself.
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/app"
	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/mcp"
	"github.com/99apps-id/termixgo/internal/provider"
	"github.com/99apps-id/termixgo/internal/secrets"
	"github.com/99apps-id/termixgo/internal/ui"
	"github.com/99apps-id/termixgo/internal/version"
)

func main() {
	os.Exit(terminate(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr), os.Stderr))
}

// terminate reports a failure and gives the process its exit code. A non-zero
// code on failure is a contract scripts depend on: `termixgo -p` in a pipeline
// must not read as success when the turn failed.
func terminate(err error, stderr io.Writer) int {
	if err == nil {
		return 0
	}
	fmt.Fprintln(stderr, "termixgo:", err)
	return 1
}

// signalContext returns a context cancelled by Ctrl+C or a termination
// request. Cancelling lets the run loop stop between steps and save state,
// which a hard exit mid-turn would skip.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return runInteractive(stdin, stdout, ui.Options{})
	}
	switch args[0] {
	case "help", "--help", "-h":
		writeUsage(stdout)
		return nil
	case "version", "--version", "-v":
		_, err := fmt.Fprintf(stdout, "%s (%s/%s)\n", version.Full(), runtime.GOOS, runtime.GOARCH)
		return err
	case "setup":
		return runInteractive(stdin, stdout, ui.Options{StartSetup: true})
	case "run", "-p", "--print":
		if len(args) < 2 {
			return fmt.Errorf("run needs a prompt")
		}
		return runOnce(strings.Join(args[1:], " "), stdout, stderr)
	case "models":
		return runModels(args[1:], stdout)
	case "model":
		return runModel(args[1:], stdout)
	case "endpoint":
		return runEndpoint(args[1:], stdout)
	case "trust":
		return runTrust(args[1:], stdout)
	case "approval":
		return runApproval(args[1:], stdout)
	case "harness":
		return runHarness(args[1:], stdout)
	case "mcp":
		return runMCP(args[1:], stdout)
	case "secret":
		return runSecret(args[1:], stdin, stdout)
	case "telegram":
		return runTelegram(args[1:], stdout)
	case "serve":
		return runServe(stdout)
	case "service":
		return runService(args[1:], stdout)
	case "completion":
		return runCompletion(args[1:], stdout)
	case "doctor":
		return runDoctor(stdout)
	default:
		return fmt.Errorf("unknown command %q; run 'termixgo help'", args[0])
	}
}

// runInteractive starts the full-screen UI when the streams are terminals and
// the plain REPL otherwise.
func runInteractive(stdin io.Reader, stdout io.Writer, options ui.Options) error {
	application, err := app.New("")
	if err != nil {
		return err
	}
	defer application.Shutdown()
	enableTelegram(application)
	if !ui.IsInteractive(stdin, stdout) {
		ctx, stop := signalContext()
		defer stop()
		return ui.RunPlainWithContext(ctx, application, stdin, stdout)
	}
	return ui.RunWithOptions(application, options)
}

// enableTelegram starts the bot when the configuration says it should run.
func enableTelegram(application *app.App) {
	cfg := application.Config()
	if cfg.Telegram.Enabled && application.TelegramToken() != "" {
		_ = application.StartTelegram()
	}
}

func runOnce(prompt string, stdout, stderr io.Writer) error {
	application, err := app.New("")
	if err != nil {
		return err
	}
	// The index and the MCP pool hold open handles, so they are released on the
	// way out. A one-shot run is short-lived, and leaving a handle behind would
	// keep a temp directory busy for as long as the process lives.
	defer application.Shutdown()
	if !application.HasModel() {
		return fmt.Errorf("no model is configured; run 'termixgo setup'")
	}
	// A one-shot run is a command, not a conversation: it must not leave a
	// session file behind on every invocation.
	application.SetEphemeral(true)
	ctx, stop := signalContext()
	defer stop()
	return ui.RunOnceWithContext(ctx, application, prompt, stdout)
}

func runModels(args []string, stdout io.Writer) error {
	providerFilter := ""
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--provider", "-p":
			if index+1 >= len(args) {
				return fmt.Errorf("--provider needs an id")
			}
			providerFilter = args[index+1]
			index++
		default:
			return fmt.Errorf("unknown models option %q", args[index])
		}
	}
	for _, info := range provider.Providers() {
		if providerFilter != "" && info.ID != providerFilter {
			continue
		}
		fmt.Fprintf(stdout, "%s (%s)\n", info.Label, info.ID)
		for _, model := range provider.ModelsFor(info.ID) {
			fmt.Fprintf(stdout, "  %-32s %s\n", model.ID, model.Description)
		}
	}
	return nil
}

func runModel(args []string, stdout io.Writer) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(args) == 0 {
		if cfg.DefaultModel == "" {
			fmt.Fprintln(stdout, "no default model is set; run 'termixgo setup'")
			return nil
		}
		fmt.Fprintln(stdout, cfg.DefaultModel)
		return nil
	}
	query := strings.Join(args, " ")
	if model, ok := provider.ModelFromQuery(query); ok {
		cfg.DefaultModel = model.ID
	} else {
		// A model the catalogue does not list is accepted verbatim, which is
		// how a custom endpoint is selected before anything knows about it.
		cfg.DefaultModel = query
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "default model is now %s\n", cfg.DefaultModel)
	return nil
}

// runEndpoint reads or sets a provider's base URL. A custom OpenAI-compatible
// server has no default host, so the CLI needs its address explicitly: the
// address set in the desktop app is not shared with this binary.
func runEndpoint(args []string, stdout io.Writer) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(args) == 0 {
		if len(cfg.BaseURLs) == 0 {
			fmt.Fprintln(stdout, "no custom endpoints are set")
			return nil
		}
		ids := make([]string, 0, len(cfg.BaseURLs))
		for id := range cfg.BaseURLs {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			fmt.Fprintf(stdout, "%s %s\n", id, cfg.BaseURLs[id])
		}
		return nil
	}
	if len(args) < 2 {
		return fmt.Errorf("usage: termixgo endpoint <provider> <url>")
	}
	providerID := strings.ToLower(strings.TrimSpace(args[0]))
	if _, ok := provider.ByID(providerID); !ok {
		return fmt.Errorf("unknown provider %q", providerID)
	}
	endpoint, err := provider.NormalizeBaseURL(strings.Join(args[1:], " "))
	if err != nil {
		return err
	}
	if cfg.BaseURLs == nil {
		cfg.BaseURLs = map[string]string{}
	}
	cfg.BaseURLs[providerID] = endpoint
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s endpoint is now %s\n", providerID, endpoint)
	return nil
}

func runTrust(args []string, stdout io.Writer) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	workspace, err := os.Getwd()
	if err != nil {
		return err
	}
	state := "untrusted"
	if cfg.IsTrusted(workspace) {
		state = "trusted"
	}
	if len(args) == 0 {
		fmt.Fprintf(stdout, "%s is %s\n", config.CleanFolder(workspace), state)
		return nil
	}
	switch strings.ToLower(args[0]) {
	case "on", "yes", "true":
		cfg = cfg.Trust(workspace)
		state = "trusted"
	case "off", "no", "false":
		cfg = cfg.Untrust(workspace)
		state = "untrusted"
	default:
		return fmt.Errorf("usage: termixgo trust [on|off]")
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s is now %s\n", config.CleanFolder(workspace), state)
	return nil
}

func runApproval(args []string, stdout io.Writer) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(args) == 0 {
		fmt.Fprintln(stdout, cfg.ApprovalMode)
		return nil
	}
	mode, err := config.ParseApprovalMode(args[0])
	if err != nil {
		return err
	}
	cfg.ApprovalMode = mode
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "approval mode is now %s\n", mode)
	return nil
}

// runMCP reports the configured MCP servers and the tools each one contributes.
//
// It connects for real rather than reading the config alone, because the
// interesting failure is a server whose command is wrong or whose package is
// missing, and only starting it shows that.
func runMCP(args []string, stdout io.Writer) error {
	if len(args) > 0 {
		switch strings.TrimSpace(args[0]) {
		case "list", "":
		default:
			return fmt.Errorf("usage: termixgo mcp [list]")
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(cfg.MCPServers) == 0 {
		fmt.Fprintln(stdout, "No MCP servers are configured.")
		fmt.Fprintln(stdout, "Add them to mcpServers in", config.FileName+", for example:")
		fmt.Fprintln(stdout, `  {"name": "files", "command": "npx", "args": ["-y", "@modelcontextprotocol/server-filesystem", "."]}`)
		return nil
	}

	workspace, err := os.Getwd()
	if err != nil {
		return err
	}
	enabled := make([]mcp.Options, 0, len(cfg.MCPServers))
	disabled := make([]mcp.Options, 0, len(cfg.MCPServers))
	for _, server := range cfg.MCPServers {
		options := mcp.Options{
			Name:    server.Name,
			Command: server.Command,
			Args:    server.Args,
			Env:     server.Env,
			Dir:     workspace,
		}
		if server.Disabled {
			disabled = append(disabled, options)
			continue
		}
		enabled = append(enabled, options)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := mcp.NewPool()
	defer pool.Close()
	pool.Connect(ctx, disabled, enabled)

	for _, entry := range pool.Status() {
		switch {
		case entry.Disabled:
			fmt.Fprintf(stdout, "%s (off)\n", entry.Name)
		case entry.Err != nil:
			fmt.Fprintf(stdout, "%s (failed)\n", entry.Name)
			fmt.Fprintf(stdout, "  command: %s\n", entry.Command)
			fmt.Fprintf(stdout, "  error: %v\n", entry.Err)
			if entry.Stderr != "" {
				fmt.Fprintf(stdout, "  stderr: %s\n", strings.Join(strings.Fields(entry.Stderr), " "))
			}
		default:
			fmt.Fprintf(stdout, "%s (ready, %d tool(s))\n", entry.Name, entry.ToolCount)
		}
	}
	for _, bound := range pool.Tools() {
		fmt.Fprintf(stdout, "  %-28s %s\n", bound.Name, firstSentence(bound.Tool.Description))
	}

	// A failure here is the whole point of the command, so it is reported as a
	// non-zero exit rather than printed and swallowed.
	if failures := pool.Failures(); len(failures) > 0 {
		return fmt.Errorf("%d MCP server(s) failed to start", len(failures))
	}
	return nil
}

// firstSentence clips a tool description to one line for the listing.
func firstSentence(text string) string {
	collapsed := strings.Join(strings.Fields(text), " ")
	if len(collapsed) <= 70 {
		return collapsed
	}
	return collapsed[:70] + "..."
}

// runHarness shows or selects the agent harness profile.
func runHarness(args []string, stdout io.Writer) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(args) == 0 {
		active := agent.GetHarnessProfile(cfg.HarnessProfile)
		fmt.Fprintf(stdout, "%s (%s)\n", active.ID, active.Label)
		for _, profile := range agent.HarnessProfiles() {
			if profile.ID == active.ID {
				continue
			}
			fmt.Fprintf(stdout, "  %-22s %s\n", profile.ID, profile.Description)
		}
		return nil
	}
	id := strings.TrimSpace(args[0])
	profile, ok := agent.BuiltinHarnessProfiles[id]
	if !ok {
		return fmt.Errorf("unknown harness %q; try one of %s", id, strings.Join(agent.HarnessProfileIDs(), ", "))
	}
	cfg.HarnessProfile = profile.ID
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "harness is now %s\n", profile.ID)
	return nil
}

func runSecret(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: termixgo secret <provider> [key]")
	}
	providerID := args[0]
	if _, ok := provider.ByID(providerID); !ok {
		return fmt.Errorf("unknown provider %q", providerID)
	}
	value := ""
	if len(args) > 1 {
		value = args[1]
	} else {
		fmt.Fprintf(stdout, "API key for %s (input hidden): ", providerID)
		if file, ok := stdin.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
			raw, err := term.ReadPassword(int(file.Fd()))
			fmt.Fprintln(stdout)
			if err != nil {
				return err
			}
			value = string(raw)
		} else {
			reader := bufio.NewReader(stdin)
			line, err := reader.ReadString('\n')
			if err != nil && line == "" {
				return err
			}
			value = strings.TrimSpace(line)
		}
	}
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("the key is empty")
	}
	store, err := secrets.Load()
	if err != nil {
		return err
	}
	if err := store.Set(secrets.ProviderKey(providerID), value); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "stored a key for %s\n", providerID)
	return nil
}

func runTelegram(args []string, stdout io.Writer) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(args) == 0 || args[0] == "status" {
		store, loadErr := secrets.Load()
		if loadErr != nil {
			return loadErr
		}
		hasToken := store.Has(secrets.TelegramTokenKey())
		fmt.Fprintf(stdout, "token: %v\nenabled: %v\nchat: %d\n", hasToken, cfg.Telegram.Enabled, cfg.Telegram.ChatID)
		return nil
	}
	switch args[0] {
	case "on":
		cfg.Telegram.Enabled = true
	case "off":
		cfg.Telegram.Enabled = false
	default:
		return fmt.Errorf("usage: termixgo telegram [status|on|off]")
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "telegram enabled is now %v\n", cfg.Telegram.Enabled)
	return nil
}

func runServe(stdout io.Writer) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	store, loadErr := secrets.Load()
	if loadErr != nil {
		return loadErr
	}
	token := strings.TrimSpace(store.Get(secrets.TelegramTokenKey()))
	if token == "" {
		return fmt.Errorf("no Telegram token configured; run 'termixgo setup' or 'termixgo telegram on'")
	}
	if !cfg.Telegram.Enabled {
		return fmt.Errorf("Telegram is disabled; run 'termixgo telegram on' first")
	}

	fmt.Fprintf(stdout, "Starting Termixgo assistant (Ctrl+C to stop)...\n")
	fmt.Fprintf(stdout, "Telegram: enabled\n")
	fmt.Fprintf(stdout, "Chat: %d\n", cfg.Telegram.ChatID)

	application, err := app.New("")
	if err != nil {
		return err
	}
	defer application.Shutdown()

	// Fail before pairing rather than on every message with a bare "no model
	// is configured". The label can name a configured model while its key or
	// endpoint is missing, which is the confusing case this names.
	if !application.HasModel() {
		problem := application.ModelError()
		if problem == nil {
			problem = app.ErrNoModel
		}
		return fmt.Errorf("the assistant has no usable model: %v\nSet it up for this CLI, which keeps its own settings:\n  termixgo endpoint <provider> <url>\n  termixgo secret <provider>\n  termixgo model <provider>:<model-id>\nA custom endpoint set in the Termigo app is not shared with this binary", problem)
	}

	if err := application.StartTelegram(); err != nil {
		return err
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-sigChan
		cancel()
	}()

	<-ctx.Done()
	fmt.Fprintf(stdout, "\nAssistant stopped.\n")
	return nil
}

const (
	serviceTaskName = "TermixgoAssistant"
	launchdLabel    = "com.termixgo.assistant"
	systemdService  = "termixgo.service"
)

func runService(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: termixgo service [install|uninstall|status]")
	}
	sub := strings.ToLower(strings.TrimSpace(args[0]))
	switch sub {
	case "install":
		return serviceInstall(stdout)
	case "uninstall":
		return serviceUninstall(stdout)
	case "status":
		return serviceStatus(stdout)
	default:
		return fmt.Errorf("unknown service command %q; use install, uninstall, or status", sub)
	}
}

func serviceInstall(stdout io.Writer) error {
	binary, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot resolve binary path: %v", err)
	}
	switch runtime.GOOS {
	case "windows":
		return serviceInstallWindows(stdout, binary)
	case "darwin":
		return serviceInstallDarwin(stdout, binary)
	case "linux":
		return serviceInstallLinux(stdout, binary)
	default:
		return fmt.Errorf("auto-start is not supported on %s", runtime.GOOS)
	}
}

func serviceUninstall(stdout io.Writer) error {
	switch runtime.GOOS {
	case "windows":
		return serviceUninstallWindows(stdout)
	case "darwin":
		return serviceUninstallDarwin(stdout)
	case "linux":
		return serviceUninstallLinux(stdout)
	default:
		return fmt.Errorf("auto-start is not supported on %s", runtime.GOOS)
	}
}

func serviceStatus(stdout io.Writer) error {
	switch runtime.GOOS {
	case "windows":
		return serviceStatusWindows(stdout)
	case "darwin":
		return serviceStatusDarwin(stdout)
	case "linux":
		return serviceStatusLinux(stdout)
	default:
		return fmt.Errorf("auto-start is not supported on %s", runtime.GOOS)
	}
}

// windows -------------------------------------------------------------

func serviceInstallWindows(stdout io.Writer, binary string) error {
	command := fmt.Sprintf(`"%s" serve`, binary)
	cmd := exec.Command("schtasks.exe", []string{
		"/Create",
		"/TN", serviceTaskName,
		"/TR", command,
		"/SC", "ONSTART",
		"/RU", "SYSTEM",
		"/F",
	}...)
	cmd.Stdout = stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("install failed: %v", err)
	}
	fmt.Fprintf(stdout, "Service installed. Termixgo will start automatically after reboot.\n")
	return nil
}

func serviceUninstallWindows(stdout io.Writer) error {
	cmd := exec.Command("schtasks.exe", []string{
		"/Delete",
		"/TN", serviceTaskName,
		"/F",
	}...)
	cmd.Stdout = stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("uninstall failed: %v", err)
	}
	fmt.Fprintf(stdout, "Service uninstalled.\n")
	return nil
}

func serviceStatusWindows(stdout io.Writer) error {
	cmd := exec.Command("schtasks.exe", []string{
		"/Query",
		"/TN", serviceTaskName,
		"/FO", "LIST",
		"/V",
	}...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		text := string(output)
		if strings.Contains(text, "does not exist") || strings.Contains(text, "not found") {
			fmt.Fprintf(stdout, "Service is not installed.\n")
			return nil
		}
		return fmt.Errorf("status check failed: %v\n%s", err, strings.TrimSpace(text))
	}
	fmt.Fprintf(stdout, "%s", output)
	return nil
}

// darwin -------------------------------------------------------------

func serviceInstallDarwin(stdout io.Writer, binary string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot resolve home: %v", err)
	}
	agentDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		return fmt.Errorf("cannot create LaunchAgents dir: %v", err)
	}
	plistPath := filepath.Join(agentDir, launchdLabel+".plist")
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>serve</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>/tmp/termixgo-stdout.log</string>
	<key>StandardErrorPath</key>
	<string>/tmp/termixgo-stderr.log</string>
</dict>
</plist>`, launchdLabel, binary)
	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		return fmt.Errorf("cannot write plist: %v", err)
	}
	cmd := exec.Command("launchctl", "load", plistPath)
	cmd.Stdout = stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("launchctl load failed: %v", err)
	}
	fmt.Fprintf(stdout, "Service installed. Termixgo will start automatically after reboot/login.\n")
	return nil
}

func serviceUninstallDarwin(stdout io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	plistPath := filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")
	_ = exec.Command("launchctl", "unload", plistPath).Run()
	_ = os.Remove(plistPath)
	fmt.Fprintf(stdout, "Service uninstalled.\n")
	return nil
}

func serviceStatusDarwin(stdout io.Writer) error {
	cmd := exec.Command("launchctl", "list")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl list failed: %v\n%s", err, strings.TrimSpace(string(output)))
	}
	if strings.Contains(string(output), launchdLabel) {
		fmt.Fprintf(stdout, "Service is installed and loaded.\n")
	} else {
		fmt.Fprintf(stdout, "Service is not loaded.\n")
	}
	return nil
}

// linux -------------------------------------------------------------

func serviceInstallLinux(stdout io.Writer, binary string) error {
	systemDir, err := systemdUserDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(systemDir, 0o755); err != nil {
		return fmt.Errorf("cannot create systemd user dir: %v", err)
	}
	servicePath := filepath.Join(systemDir, systemdService)
	unit := fmt.Sprintf(`[Unit]
Description=Termixgo Assistant
After=network.target

[Service]
Type=simple
ExecStart=%s serve
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`, binary)
	if err := os.WriteFile(servicePath, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("cannot write service file: %v", err)
	}
	cmds := [][]string{
		{"systemctl", "--user", "daemon-reload"},
		{"systemctl", "--user", "enable", "--now", systemdService},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Stdout = stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s failed: %v", strings.Join(args, " "), err)
		}
	}
	fmt.Fprintf(stdout, "Service installed. Termixgo will start automatically after login.\n")
	return nil
}

func serviceUninstallLinux(stdout io.Writer) error {
	systemDir, err := systemdUserDir()
	if err != nil {
		return err
	}
	servicePath := filepath.Join(systemDir, systemdService)
	_ = exec.Command("systemctl", "--user", "disable", "--now", systemdService).Run()
	_ = os.Remove(servicePath)
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	fmt.Fprintf(stdout, "Service uninstalled.\n")
	return nil
}

func serviceStatusLinux(stdout io.Writer) error {
	cmd := exec.Command("systemctl", "--user", "status", systemdService, "--no-pager")
	output, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(output), "could not be found") {
			fmt.Fprintf(stdout, "Service is not installed.\n")
			return nil
		}
		return fmt.Errorf("status check failed: %v\n%s", err, strings.TrimSpace(string(output)))
	}
	fmt.Fprintf(stdout, "%s", output)
	return nil
}

func systemdUserDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user"), nil
}

func runDoctor(stdout io.Writer) error {
	fmt.Fprintf(stdout, "%s (%s/%s)\n\n", version.Full(), runtime.GOOS, runtime.GOARCH)
	home, err := config.Home()
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "state directory: %s\n", home)
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "default model:  %s\n", orNone(cfg.DefaultModel))
	fmt.Fprintf(stdout, "approval mode:  %s\n", cfg.ApprovalMode)
	fmt.Fprintf(stdout, "trusted folders: %d\n", len(cfg.TrustedFolders))
	store, err := secrets.Load()
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "telegram token: %v\n", store.Has(secrets.TelegramTokenKey()))
	if cfg.CostBudgetUSD > 0 {
		fmt.Fprintf(stdout, "cost budget:    $%.2f per session\n", cfg.CostBudgetUSD)
		// A cap with no price behind it can never fire. Reporting the budget
		// without saying so would let the operator believe they are protected
		// while an unpriced model runs unbounded.
		if !budgetCanFire(cfg) {
			fmt.Fprintln(stdout, "                WARNING: no price is known for this model, so the cap cannot fire.")
			fmt.Fprintln(stdout, "                Add one with modelPricing, for example {\"my-model\":{\"inputPerMillion\":3,\"outputPerMillion\":9}}.")
		}
	} else {
		fmt.Fprintln(stdout, "cost budget:    none (set costBudgetUsd to cap a session)")
	}
	if len(cfg.ModelPricing) > 0 {
		fmt.Fprintf(stdout, "model prices:   %d override(s) in config\n", len(cfg.ModelPricing))
	}
	reportSecretProtection(store, stdout)

	fmt.Fprintln(stdout, "providers:")
	for _, info := range provider.Providers() {
		source := provider.KeySource(store, info.ID)
		state := "no key"
		if !info.NeedsKey {
			state = "no key needed"
		}
		if source != "" {
			state = "key from " + source
		}
		fmt.Fprintf(stdout, "  %-18s %s\n", info.ID, state)
	}
	fmt.Fprintf(stdout, "\nnetwork: providers are called over HTTPS; check connectivity if requests fail\n")
	return nil
}

func orNone(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(none)"
	}
	return value
}

// budgetCanFire reports whether the cost cap can actually stop a run, which it
// cannot without a price for the configured model.
func budgetCanFire(cfg config.Config) bool {
	_, known := provider.PricedByID(cfg, cfg.DefaultModel)
	return known
}

func writeUsage(stdout io.Writer) {
	fmt.Fprintf(stdout, `Termixgo %s - the terminal coding agent.

Usage:
  termixgo                        Start the terminal UI (setup runs on first use)
  termixgo setup                  Start the UI in the onboarding wizard
  termixgo run "<prompt>"         Run one prompt and stream the answer
  termixgo models [--provider id] List the model catalogue
  termixgo model [id]             Show or set the default model
  termixgo endpoint [provider url]
                                  Show or set a provider's custom base URL
  termixgo trust [on|off]         Show or set folder trust for this directory
  termixgo approval [mode]        Show or set ask|edits|all|plan
  termixgo harness [id]           Show or set the agent harness profile
  termixgo mcp                    List the MCP servers and the tools they add
  termixgo completion [shell]     Print shell completion (bash|zsh|fish|powershell)
  termixgo secret <provider> [k]  Store a provider API key
  termixgo telegram [status|on|off]
  termixgo serve                  Run the Telegram assistant 24/7
  termixgo service [install|uninstall|status]
                                  Manage auto-start (systemd, launchd, schtasks)
  termixgo doctor                 Inspect configuration and provider keys
  termixgo version                Print the version
  termixgo help                   Show this help

Inside the UI, type /help for the full command list. First-run onboarding asks
for a provider key, a default model and optionally a Telegram bot token.

Secrets are stored in ~/.termixgo/secrets.json with mode 0600. Nothing is
written to the workspace except .termixgo/ skills and memory.
`, version.Version)
}

// reportSecretProtection prints who can read the secret file.
//
// The check is the point of the line: on Windows the file used to inherit the
// parent directory's grants, so other local accounts could read provider keys.
// A silent "ok" is what made that invisible.
func reportSecretProtection(store *secrets.Store, stdout io.Writer) {
	if err := store.ProtectionError(); err != nil {
		fmt.Fprintf(stdout, "secrets access: FAILED: %v\n", err)
		return
	}
	access, err := secrets.Inspect(store.Path())
	if err != nil {
		// An absent file is the normal fresh-install state, not a problem.
		if _, statErr := os.Stat(store.Path()); statErr != nil {
			fmt.Fprintln(stdout, "secrets access: (no secret file yet)")
			return
		}
		fmt.Fprintf(stdout, "secrets access: unknown: %v\n", err)
		return
	}
	if access.OwnerOnly {
		fmt.Fprintf(stdout, "secrets access: owner only (%s)\n", access.Detail)
		return
	}
	fmt.Fprintf(stdout, "secrets access: WARNING, readable beyond the owner: %s\n", strings.Join(access.Entries, ", "))
}
