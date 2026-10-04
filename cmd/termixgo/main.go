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
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/app"
	"github.com/99apps-id/termixgo/internal/audit"
	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/cron"
	"github.com/99apps-id/termixgo/internal/mcp"
	"github.com/99apps-id/termixgo/internal/oauth"
	"github.com/99apps-id/termixgo/internal/provider"
	"github.com/99apps-id/termixgo/internal/secrets"
	"github.com/99apps-id/termixgo/internal/ui"
	"github.com/99apps-id/termixgo/internal/version"
)

func main() {
	// On Windows, force pure-Go DNS so the agent's web tools are not
	// derailed by a misbehaving system resolver or a VPN tunnel that
	// breaks the CGO lookup path.
	if runtime.GOOS == "windows" {
		_ = os.Setenv("GODEBUG", "netdns=go")
	}
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
		prompt, autoApprove, err := parseRunArgs(args[1:])
		if err != nil {
			return err
		}
		return runOnce(prompt, autoApprove, stdout, stderr)
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
	case "login":
		return runLogin(args[1:], stdout)
	case "logout":
		return runLogout(args[1:], stdout)
	case "telegram":
		return runTelegram(args[1:], stdout)
	case "serve":
		return runServe(stdout)
	case "cron":
		return runCron(args[1:], stdout)
	case "heartbeat":
		return runHeartbeat(args[1:], stdout)
	case "sessions":
		return runSessions(args[1:], stdout)
	case "audit":
		return runAudit(args[1:], stdout)
	case "worker":
		return runWorker(args[1:], stdout)
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

// parseRunArgs splits the run subcommand's tail into the prompt and the
// auto-approve flag. --yes and -y may appear before or after the prompt, so
// scripts can write either `termixgo run --yes "...`" or `termixgo run "..."
// --yes`. Everything else is the prompt.
func parseRunArgs(args []string) (prompt string, autoApprove bool, err error) {
	var words []string
	for _, arg := range args {
		switch arg {
		case "--yes", "-y":
			autoApprove = true
		default:
			words = append(words, arg)
		}
	}
	prompt = strings.Join(words, " ")
	if strings.TrimSpace(prompt) == "" {
		return "", false, fmt.Errorf("run needs a prompt")
	}
	return prompt, autoApprove, nil
}

func runOnce(prompt string, autoApprove bool, stdout, stderr io.Writer) error {
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
	if autoApprove {
		return ui.RunOnceAutoApprove(ctx, application, prompt, stdout)
	}
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
	if len(args) > 0 {
		switch strings.ToLower(args[0]) {
		case "voice":
			if len(args) == 1 {
				current := cfg.VoiceModel
				if current == "" {
					current = "none (using default Whisper)"
				}
				fmt.Fprintln(stdout, current)
				return nil
			}
			val := strings.Join(args[1:], " ")
			if strings.EqualFold(val, "off") || strings.EqualFold(val, "none") || strings.EqualFold(val, "clear") {
				cfg.VoiceModel = ""
				fmt.Fprintln(stdout, "cleared voice model (using default Whisper)")
			} else {
				cfg.VoiceModel = val
				fmt.Fprintf(stdout, "voice model is now %s\n", cfg.VoiceModel)
			}
			return config.Save(cfg)
		case "image":
			if len(args) == 1 {
				current := cfg.ImageModel
				if current == "" {
					current = "none (using active model)"
				}
				fmt.Fprintln(stdout, current)
				return nil
			}
			val := strings.Join(args[1:], " ")
			if strings.EqualFold(val, "off") || strings.EqualFold(val, "none") || strings.EqualFold(val, "clear") {
				cfg.ImageModel = ""
				if cfg.SubagentModels != nil {
					delete(cfg.SubagentModels, "image")
				}
				fmt.Fprintln(stdout, "cleared image model")
			} else {
				cfg.ImageModel = val
				if cfg.SubagentModels == nil {
					cfg.SubagentModels = map[string]string{}
				}
				cfg.SubagentModels["image"] = val
				fmt.Fprintf(stdout, "image model is now %s\n", cfg.ImageModel)
			}
			return config.Save(cfg)
		}
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
//
// The cap counts bytes and the cut moves back to a rune boundary. A description
// can come from an MCP server, which writes it in whatever language it likes, so
// a slice at a fixed offset splits a multi-byte character and the terminal paints
// a replacement glyph where the text should be.
func firstSentence(text string) string {
	collapsed := strings.Join(strings.Fields(text), " ")
	if len(collapsed) <= 70 {
		return collapsed
	}
	cut := 70
	for cut > 0 && !utf8.RuneStart(collapsed[cut]) {
		cut--
	}
	return collapsed[:cut] + "..."
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
	if provider.UsesOAuth(providerID) {
		return fmt.Errorf("%s logs in with a device code, not an API key; run 'termixgo login %s'", providerID, providerID)
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

// runLogin performs a device login for an OAuth provider and stores the token.
func runLogin(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: termixgo login <%s>", strings.Join(oauth.Supported(), "|"))
	}
	providerID := strings.TrimSpace(args[0])
	if _, ok := oauth.SpecFor(providerID); !ok {
		return fmt.Errorf("%q does not support a login; supported: %s", providerID, strings.Join(oauth.Supported(), ", "))
	}
	store, err := secrets.Load()
	if err != nil {
		return err
	}
	ctx, stop := signalContext()
	defer stop()
	return oauth.Login(ctx, oauth.NewStore(store), providerID, os.Stdin, stdout)
}

// runLogout removes a stored OAuth token.
func runLogout(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: termixgo logout <provider>")
	}
	store, err := secrets.Load()
	if err != nil {
		return err
	}
	if err := oauth.NewStore(store).Delete(strings.TrimSpace(args[0])); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Logged out of %s.\n", strings.TrimSpace(args[0]))
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
		return fmt.Errorf("the Telegram companion is disabled; run 'termixgo telegram on' first")
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
	if err := application.StartScheduler(); err != nil {
		fmt.Fprintf(stdout, "Scheduler: off (%v)\n", err)
	} else {
		fmt.Fprintf(stdout, "Scheduler: on\n")
		fmt.Fprintf(stdout, "Heartbeat: %s\n", application.HeartbeatStatus())
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

// runCron manages the scheduled jobs. It edits the same file the serve
// scheduler reads, so a change takes effect on the next tick.
func runCron(args []string, stdout io.Writer) error {
	store, err := openCronStore()
	if err != nil {
		return err
	}
	if len(args) == 0 || args[0] == "list" {
		jobs := store.List()
		if len(jobs) == 0 {
			fmt.Fprintln(stdout, "No scheduled jobs.")
			fmt.Fprintln(stdout, `Add one with: termixgo cron add "every 30m" <name> <prompt>`)
			return nil
		}
		for _, job := range jobs {
			state := "off"
			if job.Enabled {
				state = "on"
			}
			name := job.Name
			if name == "" {
				name = "(unnamed)"
			}
			fmt.Fprintf(stdout, "%s  %-3s  %-18s  %s\n", job.ID, state, job.Schedule.String(), name)
			if job.Enabled && !job.NextRun.IsZero() {
				fmt.Fprintf(stdout, "     next: %s\n", job.NextRun.Format("2006-01-02 15:04"))
			}
			if job.LastError != "" {
				fmt.Fprintf(stdout, "     last error: %s\n", job.LastError)
			}
		}
		return nil
	}
	switch args[0] {
	case "add":
		if len(args) < 4 {
			return fmt.Errorf(`usage: termixgo cron add "<schedule>" <name> <prompt>`)
		}
		schedule, err := cron.Parse(args[1])
		if err != nil {
			return err
		}
		job, err := store.Add(args[2], strings.Join(args[3:], " "), schedule, time.Now())
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Added job %s (%s), first run %s\n", job.ID, job.Schedule.String(), job.NextRun.Format("2006-01-02 15:04"))
		return nil
	case "remove":
		if len(args) < 2 {
			return fmt.Errorf("usage: termixgo cron remove <id>")
		}
		removed, err := store.Remove(args[1])
		if err != nil {
			return err
		}
		if !removed {
			return fmt.Errorf("no job with id %q", args[1])
		}
		fmt.Fprintf(stdout, "Removed job %s\n", args[1])
		return nil
	case "on", "off":
		if len(args) < 2 {
			return fmt.Errorf("usage: termixgo cron %s <id>", args[0])
		}
		job, ok, err := store.SetEnabled(args[1], args[0] == "on", time.Now())
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("no job with id %q", args[1])
		}
		state := "off"
		if job.Enabled {
			state = "on"
		}
		fmt.Fprintf(stdout, "Job %s is now %s\n", job.ID, state)
		return nil
	case "run":
		if len(args) < 2 {
			return fmt.Errorf("usage: termixgo cron run <id>")
		}
		application, err := app.New("")
		if err != nil {
			return err
		}
		defer application.Shutdown()
		if !application.HasModel() {
			return fmt.Errorf("no model is configured; run 'termixgo setup'")
		}
		ctx, stop := signalContext()
		defer stop()
		output, err := application.CronRun(ctx, args[1])
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, output)
		return nil
	default:
		return fmt.Errorf("usage: termixgo cron [list|add|remove|on|off|run]")
	}
}

func openCronStore() (*cron.Store, error) {
	path, err := cron.DefaultPath()
	if err != nil {
		return nil, err
	}
	return cron.Open(path)
}

// runHeartbeat shows or changes the periodic self-check.
func runHeartbeat(args []string, stdout io.Writer) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(args) == 0 || args[0] == "status" {
		state := "off"
		if cfg.Heartbeat.Enabled {
			state = "on"
		}
		fmt.Fprintf(stdout, "heartbeat: %s\ninterval: %s\n", state, cfg.Heartbeat.Interval)
		return nil
	}
	switch args[0] {
	case "on":
		cfg.Heartbeat.Enabled = true
	case "off":
		cfg.Heartbeat.Enabled = false
	case "interval":
		if len(args) < 2 {
			return fmt.Errorf("usage: termixgo heartbeat interval <duration>")
		}
		interval, err := time.ParseDuration(strings.TrimSpace(args[1]))
		if err != nil || interval < time.Minute {
			return fmt.Errorf("the interval must be at least one minute, such as 30m or 2h")
		}
		cfg.Heartbeat.Interval = strings.TrimSpace(args[1])
	default:
		return fmt.Errorf("usage: termixgo heartbeat [status|on|off|interval <duration>]")
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "heartbeat enabled: %v, interval: %s\n", cfg.Heartbeat.Enabled, cfg.Heartbeat.Interval)
	fmt.Fprintln(stdout, "Restart 'termixgo serve' for the change to take effect.")
	return nil
}

// runAudit prints the newest metadata-only ledger entries.
func runAudit(args []string, stdout io.Writer) error {
	limit := 50
	if len(args) > 0 {
		value, err := strconv.Atoi(strings.TrimSpace(args[0]))
		if err != nil || value <= 0 {
			return fmt.Errorf("usage: termixgo audit [count]")
		}
		limit = value
	}
	path, err := audit.DefaultPath()
	if err != nil {
		return err
	}
	ledger, err := audit.Open(path)
	if err != nil {
		return err
	}
	entries, err := ledger.Tail(limit)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		fmt.Fprintln(stdout, "No audit entries yet.")
		return nil
	}
	for _, entry := range entries {
		status := "ok"
		if !entry.OK {
			status = "fail"
		}
		detail := entry.StopReason
		if entry.Kind == "tool" && entry.Millis > 0 {
			detail = fmt.Sprintf("%dms", entry.Millis)
		}
		fmt.Fprintf(stdout, "%s  %-5s %-16s %-5s %s\n",
			entry.Time.Format("2006-01-02 15:04:05"), entry.Kind, entry.Name, status, detail)
	}
	return nil
}

// runSessions manages saved sessions from the command line.
func runSessions(args []string, stdout io.Writer) error {
	action := "list"
	if len(args) > 0 {
		action = strings.ToLower(strings.TrimSpace(args[0]))
	}
	switch action {
	case "list":
		sessions, err := agent.ListSessions()
		if err != nil {
			return err
		}
		if len(sessions) == 0 {
			fmt.Fprintln(stdout, "No saved sessions yet.")
			return nil
		}
		fmt.Fprintf(stdout, "Saved sessions (%d):\n", len(sessions))
		for _, s := range sessions {
			title := s.Title
			if title == "" {
				title = "(untitled)"
			}
			fmt.Fprintf(stdout, "  %s  %-20s  %s (%d turns)\n", s.ID, s.UpdatedAt.Format("2006-01-02 15:04"), title, s.Turns)
		}
		return nil
	case "search":
		if len(args) < 2 {
			return fmt.Errorf("usage: termixgo sessions search <query>")
		}
		query := strings.Join(args[1:], " ")
		sessions, err := agent.SearchSessions(query)
		if err != nil {
			return err
		}
		if len(sessions) == 0 {
			fmt.Fprintf(stdout, "No sessions match %q.\n", query)
			return nil
		}
		fmt.Fprintf(stdout, "Matching sessions (%d):\n", len(sessions))
		for _, s := range sessions {
			title := s.Title
			if title == "" {
				title = "(untitled)"
			}
			fmt.Fprintf(stdout, "  %s  %-20s  %s (%d turns)\n", s.ID, s.UpdatedAt.Format("2006-01-02 15:04"), title, s.Turns)
		}
		return nil
	case "export":
		if len(args) < 2 {
			return fmt.Errorf("usage: termixgo sessions export <id> [--format markdown|jsonl]")
		}
		id := args[1]
		format := agent.SessionExportMarkdown
		for i := 2; i < len(args); i++ {
			if args[i] == "--format" && i+1 < len(args) {
				format = agent.SessionExportFormat(args[i+1])
				i++
			}
		}
		exported, err := agent.ExportSession(id, format)
		if err != nil {
			return err
		}
		fmt.Fprint(stdout, exported)
		return nil
	case "delete":
		if len(args) < 2 {
			return fmt.Errorf("usage: termixgo sessions delete <id>")
		}
		if err := agent.DeleteSession(args[1]); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Deleted session %s\n", args[1])
		return nil
	case "rename":
		if len(args) < 3 {
			return fmt.Errorf("usage: termixgo sessions rename <id> <title>")
		}
		title := strings.Join(args[2:], " ")
		summary, err := agent.RenameSession(args[1], title)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Renamed session %s to %q\n", summary.ID, summary.Title)
		return nil
	default:
		return fmt.Errorf("unknown sessions action %q; use list, search, export, delete, or rename", action)
	}
}

// runWorker lists the background coding workers and whether each is ready.
//
// Starting a worker is deliberately not a CLI action: a worker is a detached
// process, and a short-lived CLI process would kill it on exit. Start one from
// the terminal UI with /worker start, or from a running `serve` by asking the
// agent to use code_worker.
func runWorker(args []string, stdout io.Writer) error {
	if len(args) > 0 && args[0] != "list" {
		return fmt.Errorf("usage: termixgo worker [list]")
	}
	application, err := app.New("")
	if err != nil {
		return err
	}
	defer application.Shutdown()
	for _, info := range application.WorkerCatalog() {
		state := "missing"
		if info.Available {
			state = "ready"
		}
		fmt.Fprintf(stdout, "%-10s %-7s %s\n", info.Kind, state, info.Detail)
	}
	fmt.Fprintln(stdout, "\nStart one from the terminal UI with /worker start <kind> <task>.")
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
		"/SC", "ONLOGON",
		"/F",
	}...)
	cmd.Stdout = stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("install failed: %v", err)
	}
	fmt.Fprintf(stdout, "Service installed. Starting assistant now in background...\n")
	_ = exec.Command("schtasks.exe", "/Run", "/TN", serviceTaskName).Run()
	fmt.Fprintf(stdout, "Termixgo will run as a 24/7 background companion and start automatically on login.\n")
	return nil
}

func serviceUninstallWindows(stdout io.Writer) error {
	_ = exec.Command("schtasks.exe", "/End", "/TN", serviceTaskName).Run()
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
	// Enable user lingering so the service keeps running 24/7 on headless VPS servers
	// even after closing SSH sessions.
	_ = exec.Command("loginctl", "enable-linger").Run()
	fmt.Fprintf(stdout, "Service installed. Termixgo will run 24/7 as a background service.\n")
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
  termixgo run [--yes] "<prompt>" Run one prompt and stream the answer
                                  --yes auto-approves tool prompts for this run
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
  termixgo login <provider>       Log in with a device code (xai-oauth, openai-codex)
  termixgo logout <provider>       Remove a stored OAuth token
  termixgo telegram [status|on|off]
  termixgo cron [list|add|remove|on|off|run]
                                  Manage scheduled assistant jobs
  termixgo heartbeat [status|on|off|interval <duration>]
                                  Periodic self-check for the 24/7 assistant
  termixgo sessions [list|search|export|delete|rename]
                                  Manage saved agent sessions
  termixgo audit [count]          Show recent audited actions (metadata only)
  termixgo worker [list]          List the background coding workers
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
