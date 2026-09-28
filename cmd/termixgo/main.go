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
	"os/signal"
	"runtime"
	"strings"

	"golang.org/x/term"

	"github.com/99apps-id/termixgo/internal/app"
	"github.com/99apps-id/termixgo/internal/config"
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
	case "trust":
		return runTrust(args[1:], stdout)
	case "approval":
		return runApproval(args[1:], stdout)
	case "secret":
		return runSecret(args[1:], stdin, stdout)
	case "telegram":
		return runTelegram(args[1:], stdout)
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
  termixgo trust [on|off]         Show or set folder trust for this directory
  termixgo approval [mode]        Show or set ask|edits|all
  termixgo secret <provider> [k]  Store a provider API key
  termixgo telegram [status|on|off]
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
