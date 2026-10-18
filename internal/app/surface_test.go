package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/provider"
	"github.com/99apps-id/termixgo/internal/secrets"
	"github.com/99apps-id/termixgo/internal/skill"
)

// TestModelNamesTheSelectedModel covers the two accessors the Telegram bridge
// uses, which exist because the Agent interface wants a string.
func TestModelNamesTheSelectedModel(t *testing.T) {
	application := newTestApp(t)
	if got := application.Model(); got != application.ModelLabel() {
		t.Errorf("Model() = %q, want the label %q", got, application.ModelLabel())
	}

	label, err := application.SetModel("qwen2.5-coder")
	if err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	if label == "" {
		t.Fatalf("SetModel should return the label it settled on")
	}
	if application.CurrentModel().Provider != "ollama" {
		t.Errorf("provider = %q, want ollama", application.CurrentModel().Provider)
	}
}

func TestSetModelRejectsAnEmptyQuery(t *testing.T) {
	application := newTestApp(t)
	if _, err := application.SetModel("   "); err == nil {
		t.Fatalf("a blank model id must be rejected")
	}
}

func TestSetModelRejectsAnUnknownProvider(t *testing.T) {
	application := newTestApp(t)
	_, err := application.SetModelByQuery("nope:some-model")
	if err == nil {
		t.Fatalf("a provider prefix that does not exist must fail")
	}
	if !strings.Contains(err.Error(), "unknown provider") {
		t.Errorf("err = %v, want it to name the unknown provider", err)
	}
}

func TestGuessProviderPrefersAKeyedProvider(t *testing.T) {
	application := newTestApp(t)
	if got := application.guessProvider("vendor:model"); got != "vendor" {
		t.Errorf("a qualified id names its own provider, got %q", got)
	}
	if got := application.guessProvider("plain-model"); got != "openai" {
		t.Errorf("with no key stored the default provider is expected, got %q", got)
	}
	if err := application.Secrets().Set(secrets.ProviderKey("anthropic"), "sk-ant-test"); err != nil {
		t.Fatalf("store key: %v", err)
	}
	if got := application.guessProvider("plain-model"); got != "anthropic" {
		t.Errorf("a stored key should name the provider, got %q", got)
	}
}

// TestStopIsSafeWithoutATurn pins the no-op case: /stop on an idle terminal
// must not panic on a nil cancel function.
func TestStopIsSafeWithoutATurn(t *testing.T) {
	application := newTestApp(t)
	application.Stop()
	application.Stop()
	if application.Running() {
		t.Errorf("nothing was started, so nothing should be running")
	}
}

func TestLoadSessionReplacesTheLiveConversation(t *testing.T) {
	application := newTestApp(t)
	before := application.Session()

	application.LoadSession(nil)
	if application.Session() != before {
		t.Errorf("a nil session must be ignored, not installed")
	}

	other := agent.NewSession(application.Workspace(), "qwen2.5-coder:latest")
	other.AddUser("carry the plan over")
	other.SetTodos([]agent.Todo{{ID: "1", Title: "ship it", Status: "pending"}})
	application.LoadSession(other)

	if application.Session() != other {
		t.Fatalf("the session was not replaced")
	}
	items := application.Todos().Items()
	if len(items) != 1 || items[0].Title != "ship it" {
		t.Errorf("the plan should follow the session, got %+v", items)
	}
}

func TestAddUsageAccumulatesOnTheAppAndTheSession(t *testing.T) {
	application := newTestApp(t)
	application.AddUsage(provider.Usage{PromptTokens: 10, CompletionTokens: 2})
	application.AddUsage(provider.Usage{PromptTokens: 5, CompletionTokens: 3})

	usage := application.Usage()
	if usage.PromptTokens != 15 || usage.CompletionTokens != 5 {
		t.Errorf("usage = %+v, want 15 in and 5 out", usage)
	}
	if got := application.Session().Usage(); got.PromptTokens != 15 {
		t.Errorf("the session should see the same total, got %+v", got)
	}
}

func TestEmitFeedsTheObserverAndTheCost(t *testing.T) {
	application := newTestApp(t)
	var seen []agent.Event
	claim := application.SetObserver(func(event agent.Event) { seen = append(seen, event) })
	if claim == 0 {
		t.Fatalf("the observer slot should be free in this test")
	}
	defer application.ClearObserver(claim)

	application.emit(agent.Event{
		Kind:      agent.EventUsage,
		Usage:     provider.Usage{PromptTokens: 7, CompletionTokens: 1},
		CostUSD:   0.125,
		CostKnown: true,
	})

	if len(seen) != 1 {
		t.Fatalf("the observer saw %d events, want 1", len(seen))
	}
	if application.Usage().PromptTokens != 7 {
		t.Errorf("a usage event must be folded into the app total")
	}
	cost, known := application.Cost()
	if !known || cost != 0.125 {
		t.Errorf("cost = %v (known %v), want 0.125 known", cost, known)
	}
	// Drain the display channel so a later test cannot inherit this event.
	select {
	case <-application.Events():
	default:
	}
}

func TestObserverIsClearedByANilSetter(t *testing.T) {
	application := newTestApp(t)
	calls := 0
	claim := application.SetObserver(func(agent.Event) { calls++ })
	application.emit(agent.Event{Kind: agent.EventNotice, Text: "one"})
	application.SetObserver(nil)
	if claim == 0 {
		t.Fatalf("the first caller should be granted the slot")
	}
	application.emit(agent.Event{Kind: agent.EventNotice, Text: "two"})
	if calls != 1 {
		t.Errorf("the observer ran %d times, want 1", calls)
	}
	select {
	case <-application.Events():
	default:
	}
	select {
	case <-application.Events():
	default:
	}
}

// TestApprovalDefaultsToDeny pins the headless default: with no UI attached
// the safe answer is no, and a question is an error rather than a hang.
func TestApprovalDefaultsToDeny(t *testing.T) {
	application := newTestApp(t)
	if got := application.approve(agent.ApprovalRequest{Tool: "write_file"}); got != agent.DecisionDeny {
		t.Errorf("decision = %v, want deny", got)
	}
	if _, err := application.ask("which one?", nil); err == nil {
		t.Errorf("a question without an operator must fail")
	}
}

func TestInteractorReceivesApprovalsAndQuestions(t *testing.T) {
	application := newTestApp(t)
	fake := &fakeInteractor{}
	application.SetInteractor(fake)

	if got := application.approve(agent.ApprovalRequest{Tool: "write_file"}); got != agent.DecisionAllowOnce {
		t.Errorf("decision = %v, want allow once", got)
	}
	answer, err := application.ask("which one?", []string{"a", "b"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if answer != "a" {
		t.Errorf("answer = %q, want a", answer)
	}
	if fake.asked != "which one?" {
		t.Errorf("the interactor was asked %q", fake.asked)
	}
}

func TestRunSubagentNeedsAClient(t *testing.T) {
	application := newTestApp(t)
	_, _, err := application.runSubagent(context.Background(), "general", "look around")
	if err == nil {
		t.Fatalf("a subagent without a client must fail")
	}
	if !strings.Contains(err.Error(), "client") {
		t.Errorf("err = %v, want it to mention the missing client", err)
	}
}

func TestReloadSkillsPicksUpAProjectSkill(t *testing.T) {
	application := newTestApp(t)
	// Only the two builtins ship with a fresh workspace; a project skill
	// joins them rather than standing alone.
	names := func() []string {
		var out []string
		for _, item := range application.Skills() {
			out = append(out, item.Name)
		}
		return out
	}
	if got := names(); len(got) != 2 {
		t.Fatalf("a fresh workspace should have only the builtins, got %v", got)
	}

	dir := filepath.Join(skill.ProjectDir(application.Workspace()), "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	document := "---\nname: demo\ndescription: a probe skill\n---\n\nDo the thing.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(document), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	application.ReloadSkills()
	skills := application.Skills()
	if len(skills) != 3 {
		t.Fatalf("skills = %+v, want the demo skill plus the builtins", names())
	}
	var found *skill.Skill
	for index := range skills {
		if skills[index].Name == "demo" {
			found = &skills[index]
		}
	}
	if found == nil || found.Scope != "project" {
		t.Fatalf("skills = %+v, want the demo project skill among them", names())
	}
	if application.Tools() == nil || application.Processes() == nil {
		t.Errorf("the registry and the process manager should always be available")
	}
}

func TestStatusReportsAPendingTelegramPair(t *testing.T) {
	application := newTestApp(t)
	if err := application.SetTelegramToken("123456:AAaa"); err != nil {
		t.Fatalf("SetTelegramToken: %v", err)
	}
	if _, err := application.RegeneratePairingCode(); err != nil {
		t.Fatalf("RegeneratePairingCode: %v", err)
	}
	if got := application.TelegramStatus(); got != "waiting for /pair" {
		t.Errorf("TelegramStatus = %q, want waiting for /pair", got)
	}
	if !strings.Contains(application.Status(), "telegram: waiting for /pair") {
		t.Errorf("status should carry the bot state: %q", application.Status())
	}
}

func TestPairingCodeIsSixDigits(t *testing.T) {
	application := newTestApp(t)
	code, err := application.EnsurePairingCode()
	if err != nil {
		t.Fatalf("EnsurePairingCode: %v", err)
	}
	if !regexp.MustCompile(`^\d{6}$`).MatchString(code) {
		t.Fatalf("code = %q, want six digits", code)
	}
	again, err := application.EnsurePairingCode()
	if err != nil {
		t.Fatalf("EnsurePairingCode: %v", err)
	}
	if again != code {
		t.Errorf("the code changed on its own: %q then %q", code, again)
	}
}

func TestTelegramTokenMustNotBeEmpty(t *testing.T) {
	application := newTestApp(t)
	if got := application.TelegramToken(); got != "" {
		t.Errorf("a fresh install has no token, got %q", got)
	}
	if err := application.SetTelegramToken("  "); err == nil {
		t.Fatalf("an empty token must be rejected")
	}
	if err := application.SetTelegramToken(" 123456:AAaa "); err != nil {
		t.Fatalf("SetTelegramToken: %v", err)
	}
	if got := application.TelegramToken(); got != "123456:AAaa" {
		t.Errorf("the token should be trimmed, got %q", got)
	}
}

func TestTelegramStatusNamesEveryState(t *testing.T) {
	application := newTestApp(t)
	if got := application.TelegramStatus(); got != "off (no token)" {
		t.Errorf("TelegramStatus = %q, want off (no token)", got)
	}
	if err := application.SetTelegramToken("123456:AAaa"); err != nil {
		t.Fatalf("SetTelegramToken: %v", err)
	}
	if got := application.TelegramStatus(); got != "configured, stopped" {
		t.Errorf("TelegramStatus = %q, want configured, stopped", got)
	}
}

func TestPairingStoresTheChatAndClearForgetsIt(t *testing.T) {
	application := newTestApp(t)
	if err := application.SetTelegramToken("123456:AAaa"); err != nil {
		t.Fatalf("SetTelegramToken: %v", err)
	}
	if err := application.SetTelegramChat(4242, 99); err != nil {
		t.Fatalf("SetTelegramChat: %v", err)
	}
	if got := application.TelegramChatID(); got != 4242 {
		t.Errorf("chat id = %d, want 4242", got)
	}
	if got := application.TelegramStatus(); got != "paired" {
		t.Errorf("TelegramStatus = %q, want paired", got)
	}
	reloaded, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if reloaded.Telegram.OwnerUserID != 99 || !reloaded.Telegram.Enabled {
		t.Errorf("pairing was not persisted: %+v", reloaded.Telegram)
	}
	if reloaded.Telegram.PairingCode != "" {
		t.Errorf("the pairing code should be spent, got %q", reloaded.Telegram.PairingCode)
	}

	if err := application.ClearTelegram(); err != nil {
		t.Fatalf("ClearTelegram: %v", err)
	}
	if application.TelegramChatID() != 0 {
		t.Errorf("the chat should be forgotten")
	}
	if application.TelegramToken() != "" {
		t.Errorf("the token should be deleted")
	}
	if got := application.TelegramStatus(); got != "off (no token)" {
		t.Errorf("TelegramStatus = %q, want off (no token)", got)
	}
}

func TestSetTelegramEnabledNeedsAToken(t *testing.T) {
	application := newTestApp(t)
	if err := application.SetTelegramEnabled(true); err == nil {
		t.Fatalf("enabling without a token must fail")
	}
	if err := application.SetTelegramEnabled(false); err != nil {
		t.Fatalf("disabling must always work: %v", err)
	}
	reloaded, _ := config.Load()
	if reloaded.Telegram.Enabled {
		t.Errorf("disabling should be persisted")
	}
}

// TestStartTelegramNeedsAStoredToken covers the guard. The loop itself is not
// started here: that would put a request on the real Bot API, which a unit
// test has no business doing.
func TestStartTelegramNeedsAStoredToken(t *testing.T) {
	application := newTestApp(t)
	if err := application.StartTelegram(); err == nil {
		t.Fatalf("starting without a token must fail")
	}
}

// TestVerifyTelegramTokenStopsWithTheContext pins that a cancelled context
// ends the check immediately, so /telegram pair cannot hang the wizard.
func TestVerifyTelegramTokenStopsWithTheContext(t *testing.T) {
	application := newTestApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := application.VerifyTelegramToken(ctx, "123456:AAaa"); err == nil {
		t.Fatalf("a cancelled context must end the check")
	}
}

func TestUnpairKeepsTheRestOfTheConfig(t *testing.T) {
	application := newTestApp(t)
	if err := application.SetTelegramChat(0, 0); err != nil {
		t.Fatalf("SetTelegramChat: %v", err)
	}
	if application.TelegramChatID() != 0 {
		t.Errorf("chat id = %d, want 0", application.TelegramChatID())
	}
	reloaded, _ := config.Load()
	if reloaded.Telegram.Enabled {
		t.Errorf("clearing the chat must not enable the bot")
	}
}

func TestShutdownTwiceIsHarmless(t *testing.T) {
	application := newTestApp(t)
	application.Shutdown()
	application.Shutdown()
}

func TestTelegramProgressLinesAreShort(t *testing.T) {
	boom := errors.New("dial failed")
	cases := []struct {
		name  string
		event agent.Event
		want  string
	}{
		{"tool start", agent.Event{Kind: agent.EventToolStart, ToolLabel: "Reading main.go"}, "Reading main.go"},
		{"tool ok", agent.Event{Kind: agent.EventToolEnd, ToolLabel: "Read main.go", ToolOK: true}, "Read main.go"},
		{"tool failed", agent.Event{Kind: agent.EventToolEnd, ToolLabel: "Read main.go"}, "Read main.go (failed)"},
		{"notice", agent.Event{Kind: agent.EventNotice, Text: "compacting"}, "compacting"},
		{"error", agent.Event{Kind: agent.EventError, Err: boom}, "dial failed"},
		{"silent error", agent.Event{Kind: agent.EventError}, ""},
		{"text is not progress", agent.Event{Kind: agent.EventText, Text: "hello"}, ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := telegramProgressLine(testCase.event); got != testCase.want {
				t.Errorf("telegramProgressLine = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestRunPromptReportsProgressAndTheAnswer(t *testing.T) {
	application, server := readyApp(t)
	defer server.Close()

	var lines []string
	answer, err := application.RunPrompt(context.Background(), "hello", func(line string) { lines = append(lines, line) })
	if err != nil {
		t.Fatalf("RunPrompt: %v", err)
	}
	if answer != "ok" {
		t.Errorf("answer = %q, want ok", answer)
	}
	if len(lines) == 0 || lines[0] != "Working..." {
		t.Errorf("progress = %v, want it to start with Working...", lines)
	}
	// The observer must be released once the turn is done, or a second caller
	// would keep feeding a dead progress function.
	application.emit(agent.Event{Kind: agent.EventToolStart, ToolLabel: "Reading"})
	if len(lines) != 1 {
		t.Errorf("the observer outlived the turn: %v", lines)
	}
	select {
	case <-application.Events():
	default:
	}
}

func TestRunPromptWithoutProgressIsQuiet(t *testing.T) {
	application, server := readyApp(t)
	defer server.Close()

	answer, err := application.RunPrompt(context.Background(), "hello", nil)
	if err != nil {
		t.Fatalf("RunPrompt: %v", err)
	}
	if answer != "ok" {
		t.Errorf("answer = %q, want ok", answer)
	}
}

func TestSmallHelpers(t *testing.T) {
	set := stringSet([]string{" a ", "", "b", "  "})
	if len(set) != 2 || !set["a"] || !set["b"] {
		t.Errorf("stringSet = %v, want trimmed non-empty values", set)
	}
	if !containsString([]string{"x", "y"}, "y") || containsString([]string{"x"}, "z") {
		t.Errorf("containsString looked wrong")
	}
	if got := orNone("   "); got != "none" {
		t.Errorf("orNone of blank = %q, want none", got)
	}
	if got := orNone("ollama"); got != "ollama" {
		t.Errorf("orNone = %q, want ollama", got)
	}

	cfg := config.Default()
	cfg.ModelPricing = map[string]config.ModelPrice{"custom-1": {InputPerMillion: 3, OutputPerMillion: 9}}
	price, known := provider.PricedFor(cfg, provider.Model{ID: "custom-1"})
	if !known || price.InputPerMillion != 3 || price.OutputPerMillion != 9 {
		t.Errorf("PricedFor = %+v (known %v)", price, known)
	}
}

// fakeInteractor records what the app asked of the operator.
type fakeInteractor struct {
	asked string
}

func (f *fakeInteractor) Approve(agent.ApprovalRequest) agent.Decision {
	return agent.DecisionAllowOnce
}

func (f *fakeInteractor) Ask(question string, options []string) (string, error) {
	f.asked = question
	if len(options) == 0 {
		return "", nil
	}
	return options[0], nil
}
