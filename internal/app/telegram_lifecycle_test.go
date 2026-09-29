package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/provider"
	"github.com/99apps-id/termixgo/internal/secrets"
	"github.com/99apps-id/termixgo/internal/telegram"
)

// ------------------------------------------------- telegram lifecycle

// TestStartTelegramIsIdempotent covers the guard that makes a double start
// harmless, which is what lets the config change and the wizard both ask for
// the bot without coordinating.
func TestStartTelegramIsIdempotent(t *testing.T) {
	application := newTestApp(t)
	// A bot is already registered, so a second start must be a no-op rather
	// than a second poll loop against the same token.
	application.bot = &telegram.Bot{}
	application.botStatus = "already running"

	if err := application.StartTelegram(); err != nil {
		t.Fatalf("a second start must be a no-op, got %v", err)
	}
	if application.botStatus != "already running" {
		t.Errorf("status = %q, want it left alone", application.botStatus)
	}
}

func TestStartTelegramNeedsAToken(t *testing.T) {
	application := newTestApp(t)
	if err := application.StartTelegram(); err == nil {
		t.Fatalf("starting without a token must fail")
	}
	if !strings.Contains(application.TelegramStatus(), "off") {
		t.Errorf("status = %q, want it still off", application.TelegramStatus())
	}
}

// TestStopTelegramClearsTheStateAndIsIdempotent pins that stopping always
// leaves a clean slate, so a later start does not think a loop is still up.
func TestStopTelegramClearsTheStateAndIsIdempotent(t *testing.T) {
	application := newTestApp(t)
	application.bot = &telegram.Bot{}
	application.botStatus = "running"

	application.StopTelegram()
	if application.bot != nil {
		t.Errorf("the bot should be released")
	}
	if application.botStatus != "" {
		t.Errorf("status = %q, want it cleared", application.botStatus)
	}
	// Calling it again must not panic on the already-cleared state.
	application.StopTelegram()
	if got := application.TelegramStatus(); got != "off (no token)" {
		t.Errorf("status = %q", got)
	}
}

// TestSetTelegramChatUpdatesARunningBot is the pairing handshake seen from the
// bot side: after /pair the running bot must hold the new chat and owner, or
// every later message would be rejected as a stranger.
func TestSetTelegramChatUpdatesARunningBot(t *testing.T) {
	application := newTestApp(t)
	application.bot = &telegram.Bot{}

	if err := application.SetTelegramChat(1234, 5678); err != nil {
		t.Fatalf("SetTelegramChat: %v", err)
	}
	if chatID, owner := application.bot.Pairing(); chatID != 1234 || owner != 5678 {
		t.Errorf("bot holds chat %d owner %d, want 1234 and 5678", chatID, owner)
	}
	if application.botStatus != "paired" {
		t.Errorf("status = %q, want paired", application.botStatus)
	}
}

// TestUnpairingKeepsTheTokenButDropsTheChat separates the two things the
// operator can mean: stop using this chat, and forget the bot.
func TestUnpairingKeepsTheTokenButDropsTheChat(t *testing.T) {
	application := newTestApp(t)
	if err := application.SetTelegramToken("123456:AAaa"); err != nil {
		t.Fatalf("SetTelegramToken: %v", err)
	}
	if err := application.SetTelegramChat(42, 7); err != nil {
		t.Fatalf("SetTelegramChat: %v", err)
	}

	// Setting the chat back to zero is what /unpair does on the bot side.
	if err := application.SetTelegramChat(0, 0); err != nil {
		t.Fatalf("SetTelegramChat: %v", err)
	}
	if application.TelegramChatID() != 0 {
		t.Errorf("the chat should be forgotten")
	}
	if application.TelegramToken() == "" {
		t.Errorf("unpairing must not delete the token")
	}
	if application.TelegramStatus() != "configured, stopped" {
		t.Errorf("status = %q, want it back to configured", application.TelegramStatus())
	}

	// ClearTelegram is the stronger action: it forgets the bot entirely.
	if err := application.ClearTelegram(); err != nil {
		t.Fatalf("ClearTelegram: %v", err)
	}
	if application.TelegramToken() != "" {
		t.Errorf("the token should be gone")
	}
	reloaded, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if reloaded.Telegram.Enabled || reloaded.Telegram.ChatID != 0 || reloaded.Telegram.PairingCode != "" {
		t.Errorf("the pairing should be fully cleared, got %+v", reloaded.Telegram)
	}
}

// TestRegeneratePairingCodePersistsAndInvalidatesTheOldOne is the security
// property: a code the operator regenerated must stop working.
func TestRegeneratePairingCodePersistsAndInvalidatesTheOldOne(t *testing.T) {
	application := newTestApp(t)
	first, err := application.RegeneratePairingCode()
	if err != nil {
		t.Fatalf("RegeneratePairingCode: %v", err)
	}
	second, err := application.RegeneratePairingCode()
	if err != nil {
		t.Fatalf("RegeneratePairingCode: %v", err)
	}
	if first == second {
		t.Errorf("the second code equals the first, so regenerating does nothing")
	}
	stored, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if stored.Telegram.PairingCode != second {
		t.Errorf("stored code = %q, want the newest %q", stored.Telegram.PairingCode, second)
	}
	// A running bot must be handed the new code, or /pair would keep failing
	// until the app restarts.
	application.bot = &telegram.Bot{}
	application.bot.SetPairingCode(first)
	third, err := application.RegeneratePairingCode()
	if err != nil {
		t.Fatalf("RegeneratePairingCode: %v", err)
	}
	if got := application.bot.PairingCode(); got != third {
		t.Errorf("the running bot kept %q, want %q", got, third)
	}
}

// TestStartTelegramRegistersTheConfiguredBot records the synchronous effects of
// a start. The verification itself talks to the Bot API, so only what the app
// owns is asserted here.
func TestStartTelegramRegistersTheConfiguredBot(t *testing.T) {
	application := newTestApp(t)
	if err := application.SetTelegramToken("123456:AAaa"); err != nil {
		t.Fatalf("SetTelegramToken: %v", err)
	}
	if err := application.SetTelegramChat(99, 11); err != nil {
		t.Fatalf("SetTelegramChat: %v", err)
	}
	if _, err := application.RegeneratePairingCode(); err != nil {
		t.Fatalf("RegeneratePairingCode: %v", err)
	}

	if err := application.StartTelegram(); err != nil {
		t.Fatalf("StartTelegram: %v", err)
	}
	// The loop is registered before it is verified, so the operator sees a
	// state rather than silence.
	if application.bot == nil {
		t.Fatalf("no bot was registered")
	}
	if chatID, owner := application.bot.Pairing(); chatID != 99 || owner != 11 {
		t.Errorf("the bot did not inherit the pairing: chat %d owner %d", chatID, owner)
	}
	if application.bot.PairingCode() == "" {
		t.Errorf("the bot should carry the active pairing code")
	}
	if status := application.TelegramStatus(); status == "" {
		t.Errorf("a status should be reported while the token is verified")
	}
	application.StopTelegram()
}

// TestTelegramTokenRoundTrip covers the store interaction, including that the
// token is trimmed and never lands in the config file.
func TestTelegramTokenRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	application, err := newApp(t, t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if application.TelegramToken() != "" {
		t.Fatalf("a fresh install has no token")
	}
	if err := application.SetTelegramToken("  123456:AAaa  "); err != nil {
		t.Fatalf("SetTelegramToken: %v", err)
	}
	if got := application.TelegramToken(); got != "123456:AAaa" {
		t.Errorf("token = %q, want it trimmed", got)
	}

	// It must live in the secret store, never in the settings file.
	raw, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err == nil && strings.Contains(string(raw), "123456:AAaa") {
		t.Fatalf("the bot token was written to the settings file")
	}
	store, err := secrets.Load()
	if err != nil {
		t.Fatalf("secrets.Load: %v", err)
	}
	if store.Get(secrets.TelegramTokenKey()) != "123456:AAaa" {
		t.Errorf("the token is not in the secret store")
	}
}

// ------------------------------------------------- subagents

// TestRunSubagentDrivesAReadOnlyNestedRun is the capability behind the
// delegate tool: a nested run against the same client and model.
func TestRunSubagentDrivesAReadOnlyNestedRun(t *testing.T) {
	application, server := readyApp(t)
	defer server.Close()

	report, err := application.runSubagent(context.Background(), "explore", "what does main.go do?")
	if err != nil {
		t.Fatalf("runSubagent: %v", err)
	}
	if !strings.Contains(report, "ok") {
		t.Errorf("report = %q, want the nested answer", report)
	}
}

// ------------------------------------------------- prompt and session

// TestRunPromptPropagatesAFailedTurn keeps the Telegram bridge from reporting a
// cheerful empty answer for a turn that failed.
func TestRunPromptPropagatesAFailedTurn(t *testing.T) {
	application := newTestApp(t)
	var lines []string
	answer, err := application.RunPrompt(context.Background(), "hello", func(line string) { lines = append(lines, line) })
	if err == nil {
		t.Fatalf("a turn with no model must fail")
	}
	if answer != "" {
		t.Errorf("answer = %q, want empty on failure", answer)
	}
	// The progress callback still sees the first line, so the operator is not
	// left staring at a chat with no acknowledgement.
	if len(lines) == 0 || lines[0] != "Working..." {
		t.Errorf("progress = %v, want the opening line", lines)
	}
}

// TestRunPromptRestoresTheObserverOnFailure is the leak guard: a failed turn
// must not leave the bridge's observer installed for the next caller.
func TestRunPromptRestoresTheObserverOnFailure(t *testing.T) {
	application := newTestApp(t)
	_, _ = application.RunPrompt(context.Background(), "hello", func(string) {})

	application.mu.Lock()
	observer := application.observer
	application.mu.Unlock()
	if observer != nil {
		t.Errorf("the observer outlived the failed turn")
	}
}

// ------------------------------------------------- config and session edges

// TestNewWithoutAWorkspaceUsesTheWorkingDirectory is the bare `termixgo` case:
// the folder is inferred rather than demanded.
//
// The working directory is moved to a temp dir first. The app keeps its state
// in <workspace>/.termixgo, so a test that inferred the package directory would
// write a database into the source tree.
func TestNewWithoutAWorkspaceUsesTheWorkingDirectory(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	t.Chdir(t.TempDir())
	application, err := newApp(t, "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(application.Shutdown)
	if strings.TrimSpace(application.Workspace()) == "" {
		t.Fatalf("the workspace should be inferred from the working directory")
	}
	// The folder is resolved, not left relative, because it is shown to the
	// operator and stored in the trust list.
	if !filepath.IsAbs(application.Workspace()) {
		t.Errorf("workspace = %q, want an absolute path", application.Workspace())
	}
}

// TestNewSucceedsWithAModelWhoseKeyIsMissing keeps a half-finished onboarding
// from failing construction: the UI has to open in order to offer /setup.
func TestNewSucceedsWithAModelWhoseKeyIsMissing(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	cfg := config.Default()
	cfg.DefaultModel = "claude-sonnet-4-5"
	if err := config.Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	application, err := newApp(t, t.TempDir())
	if err != nil {
		t.Fatalf("New should still succeed: %v", err)
	}
	if application.HasModel() {
		t.Errorf("there is no key, so no client should be built")
	}
	if !application.NeedsSetup() {
		t.Errorf("the app should ask for setup")
	}
}

// TestUpdateConfigAppliesAndPersists covers the single path every preference
// change goes through.
func TestUpdateConfigAppliesAndPersists(t *testing.T) {
	application := newTestApp(t)
	if err := application.UpdateConfig(func(cfg *config.Config) {
		cfg.Language = "id"
		cfg.MaxSteps = 7
		cfg.CostBudgetUSD = 2.5
	}); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
	if got := application.Config(); got.Language != "id" || got.MaxSteps != 7 {
		t.Errorf("the change was not applied: %+v", got)
	}
	reloaded, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if reloaded.Language != "id" || reloaded.CostBudgetUSD != 2.5 {
		t.Errorf("the change was not persisted: %+v", reloaded)
	}
}

// TestAllowToolIsIdempotent keeps a repeated "always allow" from growing the
// saved list on every prompt.
func TestAllowToolIsIdempotent(t *testing.T) {
	application := newTestApp(t)
	application.AllowTool("write_file")
	application.AllowTool("write_file")

	reloaded, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	count := 0
	for _, name := range reloaded.AlwaysAllowedTools {
		if name == "write_file" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("the tool appears %d times, want 1: %v", count, reloaded.AlwaysAllowedTools)
	}
}

// TestNewSessionClearsThePlan covers the /new contract: a fresh conversation
// starts with an empty plan, not the previous one.
func TestNewSessionClearsThePlan(t *testing.T) {
	application := newTestApp(t)
	application.Todos().Set([]agent.Todo{{ID: "1", Title: "old step", Status: "pending"}})
	before := application.Session().ID()

	application.NewSession()

	if len(application.Todos().Items()) != 0 {
		t.Errorf("the plan should be cleared: %+v", application.Todos().Items())
	}
	if application.Session().ID() == before {
		t.Errorf("a new session needs a new id")
	}
	// The new session inherits the model, so the operator does not have to
	// pick one again.
	if application.Session().Model() != application.CurrentModel().ID {
		t.Errorf("session model = %q, want %q", application.Session().Model(), application.CurrentModel().ID)
	}
}

// TestStatusReportsEveryLine walks the block the operator pastes into a bug
// report, so a missing line is a real loss of information.
func TestStatusReportsEveryLine(t *testing.T) {
	application := newTestApp(t)
	application.Todos().Set([]agent.Todo{
		{ID: "1", Title: "done", Status: "completed"},
		{ID: "2", Title: "doing", Status: "in_progress"},
	})
	application.AddUsage(provider.Usage{PromptTokens: 12, CompletionTokens: 4})

	status := application.Status()
	for _, want := range []string{
		"workspace:", "model:", "approval:", "context:", "session:", "plan: 1/2",
		"tokens: 12 in, 4 out", "telegram:",
	} {
		if !strings.Contains(status, want) {
			t.Errorf("status is missing %q:\n%s", want, status)
		}
	}
}

// TestRunTurnReportsAnUnknownProvider keeps a hand-edited config from being
// accepted silently.
func TestRunTurnReportsAnUnknownProvider(t *testing.T) {
	application := newTestApp(t)
	if _, err := application.SetModelByQuery("not-a-provider:some-model"); err == nil {
		t.Fatalf("an unknown provider must be refused")
	}
	// The app is still usable, which is what lets the operator fix it with
	// /model rather than restarting.
	if application.Session() == nil {
		t.Errorf("the session should survive a failed model change")
	}
}

// TestEphemeralFlagIsReadable keeps the one-shot path self-describing.
func TestEphemeralFlagIsReadable(t *testing.T) {
	application := newTestApp(t)
	if application.Ephemeral() {
		t.Errorf("an interactive app should save its session")
	}
	application.SetEphemeral(true)
	if !application.Ephemeral() {
		t.Errorf("the flag was not applied")
	}
}

// TestShutdownClosesTheTelegramLoop covers the full teardown the command calls
// on exit.
func TestShutdownClosesTheTelegramLoop(t *testing.T) {
	application := newTestApp(t)
	if err := application.SetTelegramToken("123456:AAaa"); err != nil {
		t.Fatalf("SetTelegramToken: %v", err)
	}
	application.bot = &telegram.Bot{}
	application.botStatus = "running"

	application.Shutdown()
	if application.bot != nil || application.botStatus != "" {
		t.Errorf("the telegram loop survived the shutdown")
	}
}

// TestVerifyTelegramTokenReportsATransportFailure keeps a wrong token from
// looking like a network problem without saying so.
func TestVerifyTelegramTokenReportsATransportFailure(t *testing.T) {
	application := newTestApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := application.VerifyTelegramToken(ctx, "123456:AAaa")
	if err == nil {
		t.Fatalf("a cancelled verification must fail")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want the cancellation preserved", err)
	}
}
