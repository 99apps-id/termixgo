package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/app"
	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/secrets"
	"github.com/99apps-id/termixgo/internal/ui"
)

// TestSignalContextIsCancellable covers the Ctrl+C path: the context the run
// loop receives has to be cancellable, or a stop would never reach it.
func TestSignalContextIsCancellable(t *testing.T) {
	ctx, stop := signalContext()
	defer stop()

	select {
	case <-ctx.Done():
		t.Fatalf("a fresh signal context should not already be cancelled")
	default:
	}

	stop()

	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatalf("stop() did not cancel the context")
	}
}

// TestRunInteractivePlainModeWithPipedStreams walks the entry point the way CI
// does: no terminal, so it must fall back to the plain REPL and return at EOF.
func TestRunInteractivePlainModeWithPipedStreams(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())

	var out bytes.Buffer
	stopped := make(chan error, 1)
	go func() {
		stopped <- runInteractive(strings.NewReader("/help\n/status\n/exit\n"), &out, ui.Options{})
	}()

	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("runInteractive: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("the plain REPL did not return at end of input")
	}

	rendered := out.String()
	for _, want := range []string{"Termixgo", "/model", "workspace:"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("plain output is missing %q:\n%s", want, rendered)
		}
	}
}

// TestEnableTelegramStaysOffWithoutAToken keeps a stray config flag from
// starting a poller that cannot authenticate.
func TestEnableTelegramStaysOffWithoutAToken(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())

	cfg := config.Default()
	cfg.Telegram.Enabled = true // enabled, but no token is stored
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	application, err := app.New("")
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer application.Shutdown()

	enableTelegram(application)

	if status := application.TelegramStatus(); !strings.Contains(status, "off") {
		t.Errorf("telegram should stay off without a token, status = %q", status)
	}
}

// TestEnableTelegramStartsWithATokenButNoNetwork checks that the decision to
// start is made, without depending on Telegram being reachable: the loop is
// started and reported, and the failed verification leaves it stopped.
func TestEnableTelegramStartsWithATokenButNoNetwork(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())

	store, err := secrets.Load()
	if err != nil {
		t.Fatalf("load secrets: %v", err)
	}
	// A well-formed but fake token: the bot id before the colon is what the
	// API path needs, and the call will fail against the real endpoint, which
	// is the point.
	if err := store.Set(secrets.TelegramTokenKey(), "123456:FAKE-TOKEN-FOR-TEST"); err != nil {
		t.Fatalf("store token: %v", err)
	}
	cfg := config.Default()
	cfg.Telegram.Enabled = true
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	application, err := app.New("")
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer application.Shutdown()

	enableTelegram(application)
	// Give the goroutine a moment to attempt verification and give up.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if status := application.TelegramStatus(); strings.Contains(status, "rejected") || strings.Contains(status, "off") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("telegram status = %q, want a rejection or off", application.TelegramStatus())
}

func TestTelegramCommandReportsAndPersists(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)

	stdout, _, err := runCLI(t, "telegram", "status")
	if err != nil {
		t.Fatalf("telegram status: %v", err)
	}
	for _, want := range []string{"token:", "enabled:", "chat:"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("status output is missing %q:\n%s", want, stdout)
		}
	}

	if _, _, err := runCLI(t, "telegram", "on"); err != nil {
		t.Fatalf("telegram on: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !cfg.Telegram.Enabled {
		t.Errorf("telegram should be enabled after 'telegram on'")
	}

	if _, _, err := runCLI(t, "telegram", "off"); err != nil {
		t.Fatalf("telegram off: %v", err)
	}
	cfg, _ = config.Load()
	if cfg.Telegram.Enabled {
		t.Errorf("telegram should be disabled after 'telegram off'")
	}

	if _, _, err := runCLI(t, "telegram", "sideways"); err == nil {
		t.Errorf("an invalid argument must be rejected")
	}
}

// TestMainSeamKeepsTheWorkingDirectory isolates the CLI tests that read the
// current directory for folder trust.
func TestMainSeamKeepsTheWorkingDirectory(t *testing.T) {
	before, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	withState(t)
	if _, _, err := runCLI(t, "trust", "on"); err != nil {
		t.Fatalf("trust on: %v", err)
	}
	after, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if before != after {
		t.Errorf("the working directory changed: %q to %q", before, after)
	}
}

// TestRunSecretReadsAPipedKey covers the non-tty branch of the secret command,
// which is how a key is supplied in a script or a container.
func TestRunSecretReadsAPipedKey(t *testing.T) {
	withState(t)

	var stdout, stderr bytes.Buffer
	err := run([]string{"secret", "anthropic"}, strings.NewReader("sk-ant-from-pipe\n"), &stdout, &stderr)
	if err != nil {
		t.Fatalf("secret from a pipe: %v", err)
	}
	store, err := secrets.Load()
	if err != nil {
		t.Fatalf("load secrets: %v", err)
	}
	if got := store.Get(secrets.ProviderKey("anthropic")); got != "sk-ant-from-pipe" {
		t.Errorf("stored key = %q", got)
	}
	if strings.Contains(stdout.String(), "sk-ant-from-pipe") {
		t.Errorf("the key was echoed: %q", stdout.String())
	}
}

// TestRunSecretRejectsAEmptyPipe keeps a blank line from storing an empty key.
func TestRunSecretRejectsAEmptyPipe(t *testing.T) {
	withState(t)
	var stdout, stderr bytes.Buffer
	if err := run([]string{"secret", "openai"}, strings.NewReader("\n"), &stdout, &stderr); err == nil {
		t.Fatalf("an empty piped key must be rejected")
	}
}

// TestRunPrintsNothingSecret checks that doctor never reveals a stored key,
// which is the whole point of the command.
func TestRunPrintsNothingSecret(t *testing.T) {
	home := withState(t)
	const key = "sk-super-secret-do-not-print"

	if _, _, err := runCLI(t, "secret", "openai", key); err != nil {
		t.Fatalf("secret: %v", err)
	}

	stdout, _, err := runCLI(t, "doctor")
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if strings.Contains(stdout, key) {
		t.Fatalf("doctor printed the key")
	}

	// Nor may it appear in the on-disk config, which is not the secret store.
	configData, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(configData), key) {
		t.Errorf("the key leaked into config.json")
	}
}
