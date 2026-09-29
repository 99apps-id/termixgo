package ui

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/app"
	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/provider"
)

// plainApp builds an app ready for plain-mode tests, with a local model so no
// key is needed and nothing is called over the network.
func plainApp(t *testing.T) *app.App {
	t.Helper()
	t.Setenv(config.EnvHome, t.TempDir())

	cfg := config.Default()
	cfg.DefaultModel = "qwen2.5-coder:latest"
	cfg.ApprovalMode = config.ApprovalAll
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	application := testApp(t)
	return application
}

// ------------------------------------------------------------------ printer

// TestPlainPrinterRendersEveryEvent walks the event kinds the printer has a
// branch for. A missing branch means a real run would drop information in CI,
// which is where plain mode is mostly used.
func TestPlainPrinterRendersEveryEvent(t *testing.T) {
	cases := []struct {
		name  string
		event agent.Event
		want  string
	}{
		{"text", agent.Event{Kind: agent.EventText, Text: "an answer"}, "an answer"},
		{"thinking", agent.Event{Kind: agent.EventThinking, Text: "considering"}, "considering"},
		{"reasoned", agent.Event{Kind: agent.EventReasoned, Text: "done thinking"}, ""},
		{"tool start", agent.Event{Kind: agent.EventToolStart, ToolLabel: "Reading main.go"}, "Reading main.go"},
		{"tool end ok", agent.Event{Kind: agent.EventToolEnd, ToolLabel: "Read main.go", ToolOK: true}, "Read main.go"},
		{"tool end failed", agent.Event{Kind: agent.EventToolEnd, ToolLabel: "Ran tests", ToolOK: false}, "Ran tests"},
		{"notice", agent.Event{Kind: agent.EventNotice, Text: "a note"}, "a note"},
		{"error", agent.Event{Kind: agent.EventError, Err: errFixture{}}, "fixture failure"},
		{"plan", agent.Event{Kind: agent.EventPlan, Plan: []agent.Todo{{Title: "ship it", Status: "in_progress"}}}, "ship it"},
		{"turn end", agent.Event{Kind: agent.EventTurnEnd, StopReason: "stop"}, ""},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var out bytes.Buffer
			printer := newPlainPrinter(&out, false)
			printer.print(testCase.event)

			if testCase.want != "" && !strings.Contains(out.String(), testCase.want) {
				t.Errorf("output = %q, want it to contain %q", out.String(), testCase.want)
			}
			// Piped output must stay free of escapes or CI logs fill with
			// garbage.
			if strings.Contains(out.String(), "\x1b[") {
				t.Errorf("uncoloured output contains escapes: %q", out.String())
			}
		})
	}
}

func TestPlainPrinterColoursOnlyWhenAsked(t *testing.T) {
	var plain bytes.Buffer
	newPlainPrinter(&plain, false).print(agent.Event{Kind: agent.EventToolStart, ToolLabel: "Reading x"})
	if strings.Contains(plain.String(), "\x1b[") {
		t.Errorf("coloured output was produced when colour was off")
	}

	var coloured bytes.Buffer
	newPlainPrinter(&coloured, true).print(agent.Event{Kind: agent.EventToolStart, ToolLabel: "Reading x"})
	if !strings.Contains(coloured.String(), "\x1b[") {
		t.Errorf("colour was requested but no escape was produced")
	}
}

// TestPlainPrinterSeparatesThinkingFromTheAnswer guards the one display rule
// that matters in a terminal: reasoning must not run into the answer.
func TestPlainPrinterSeparatesThinkingFromTheAnswer(t *testing.T) {
	var out bytes.Buffer
	printer := newPlainPrinter(&out, false)

	printer.print(agent.Event{Kind: agent.EventThinking, Text: "weighing options"})
	printer.print(agent.Event{Kind: agent.EventText, Text: "The answer."})

	rendered := out.String()
	if strings.Contains(rendered, "weighing optionsThe answer.") {
		t.Errorf("the answer must start on its own line: %q", rendered)
	}
	if !strings.Contains(rendered, "The answer.") {
		t.Errorf("the answer is missing: %q", rendered)
	}
}

// ------------------------------------------------------------------ interactor

func TestPlainInteractorApproval(t *testing.T) {
	cases := map[string]agent.Decision{
		"y\n":        agent.DecisionAllowOnce,
		"yes\n":      agent.DecisionAllowOnce,
		"s\n":        agent.DecisionAllowSession,
		"a\n":        agent.DecisionAllowAlways,
		"always\n":   agent.DecisionAllowAlways,
		"n\n":        agent.DecisionDeny,
		"\n":         agent.DecisionDeny,
		"whatever\n": agent.DecisionDeny,
	}
	for input, want := range cases {
		var out bytes.Buffer
		interactor := &plainInteractor{in: strings.NewReader(input), out: &out}
		got := interactor.Approve(agent.ApprovalRequest{Tool: "run_command", Risk: "command", Detail: "Running tests"})
		if got != want {
			t.Errorf("input %q produced %v, want %v", strings.TrimSpace(input), got, want)
		}
		if !strings.Contains(out.String(), "run_command") {
			t.Errorf("the prompt should name the tool: %q", out.String())
		}
	}
}

// TestPlainInteractorKeepsTheNextLine covers the reader lifecycle. A
// bufio.Reader buffers ahead of the line it returns, so building a new one for
// every question throws away whatever else arrived in the same read. Over a
// pipe that is the operator's next request, which then never runs.
func TestPlainInteractorKeepsTheNextLine(t *testing.T) {
	var out bytes.Buffer
	interactor := &plainInteractor{in: strings.NewReader("y\nrun the tests\n"), out: &out}

	if got := interactor.Approve(agent.ApprovalRequest{Tool: "run_command"}); got != agent.DecisionAllowOnce {
		t.Fatalf("approval = %v, want allow once", got)
	}
	answer, err := interactor.Ask("what next?", nil)
	if err != nil {
		t.Fatalf("the line after the approval was lost: %v", err)
	}
	if answer != "run the tests" {
		t.Errorf("answer = %q, want the queued request", answer)
	}
}

// TestPlainInteractorApprovalDefaultsToDenyOnEOF is the safe failure: a closed
// stdin must not be read as consent.
func TestPlainInteractorApprovalDefaultsToDenyOnEOF(t *testing.T) {
	var out bytes.Buffer
	interactor := &plainInteractor{in: strings.NewReader(""), out: &out}
	if got := interactor.Approve(agent.ApprovalRequest{Tool: "write_file"}); got != agent.DecisionDeny {
		t.Errorf("EOF produced %v, want deny", got)
	}
}

func TestPlainInteractorAskAcceptsNumberAndText(t *testing.T) {
	var out bytes.Buffer
	interactor := &plainInteractor{in: strings.NewReader("2\n"), out: &out}
	answer, err := interactor.Ask("which port?", []string{"3000", "8080"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if answer != "8080" {
		t.Errorf("answer = %q, want the second option", answer)
	}

	var freeform bytes.Buffer
	second := &plainInteractor{in: strings.NewReader("something else\n"), out: &freeform}
	answer, err = second.Ask("which port?", []string{"3000", "8080"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if answer != "something else" {
		t.Errorf("answer = %q, want the free text", answer)
	}
}

func TestPlainInteractorAskWithNoOptions(t *testing.T) {
	var out bytes.Buffer
	interactor := &plainInteractor{in: strings.NewReader("just text\n"), out: &out}
	answer, err := interactor.Ask("what do you want?", nil)
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if answer != "just text" {
		t.Errorf("answer = %q", answer)
	}
}

func TestParseIndex(t *testing.T) {
	cases := map[string]int{
		"1":   1,
		"12":  12,
		"":    -1,
		"x":   -1,
		"1a":  -1,
		" 1 ": -1,
	}
	for input, want := range cases {
		if got := parseIndex(input); got != want {
			t.Errorf("parseIndex(%q) = %d, want %d", input, got, want)
		}
	}
}

// ------------------------------------------------------------------ plain REPL

// TestRunPlainWithContextHandlesSlashCommands drives the CI entry point the way
// a pipeline would: commands in, output out, then EOF.
func TestRunPlainWithContextHandlesSlashCommands(t *testing.T) {
	application := plainApp(t)
	input := strings.NewReader(strings.Join([]string{
		"/help",
		"/status",
		"/trust on",
		"/approval ask",
		"/ps",
		"/cost",
		"/model",
		"/exit",
	}, "\n") + "\n")

	var out bytes.Buffer
	if err := RunPlainWithContext(context.Background(), application, input, &out); err != nil {
		t.Fatalf("RunPlainWithContext: %v", err)
	}

	rendered := out.String()
	for _, want := range []string{"Termixgo", "workspace:", "trusted", "ask", "no background processes", "tokens:"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("plain output is missing %q:\n%s", want, rendered)
		}
	}
	// /model with no argument reports the current model.
	if !strings.Contains(rendered, "Qwen2.5 Coder") {
		t.Errorf("/model should report the current model:\n%s", rendered)
	}
	if strings.Contains(rendered, "\x1b[") {
		t.Errorf("piped output must not contain escapes")
	}
}

// TestRunPlainWithContextStopsOnACancelledContext is the Ctrl+C path in a pipe.
func TestRunPlainWithContextStopsOnACancelledContext(t *testing.T) {
	application := plainApp(t)
	// A reader that never ends: without the context check the loop would block
	// on it forever.
	reader := &endlessReader{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var out bytes.Buffer
	if err := RunPlainWithContext(ctx, application, reader, &out); err != nil {
		t.Fatalf("RunPlainWithContext: %v", err)
	}
	if !strings.Contains(out.String(), "Termixgo") {
		t.Errorf("the welcome should still print before the loop notices: %q", out.String())
	}
}

// endlessReader never ends, so only a context check can stop the loop.
type endlessReader struct{}

func (endlessReader) Read(p []byte) (int, error) {
	for index := range p {
		p[index] = 0
	}
	// NUL is not a newline, so the scanner keeps waiting for one.
	return len(p), nil
}

func TestRunOnceWithoutAModelFails(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	application := testApp(t)
	var out bytes.Buffer
	if err := RunOnceWithContext(context.Background(), application, "hello", &out); err == nil {
		t.Fatalf("a one-shot run without a model must fail")
	}
}

func TestPlainWelcomeNamesTheFolderAndTrust(t *testing.T) {
	application := plainApp(t)
	var out bytes.Buffer
	printPlainWelcome(application, &out, false)

	rendered := out.String()
	if !strings.Contains(rendered, application.Workspace()) {
		t.Errorf("the welcome should name the folder:\n%s", rendered)
	}
	if !strings.Contains(rendered, "untrusted") {
		t.Errorf("the welcome should state the trust:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Termixgo") {
		t.Errorf("the welcome should name the program:\n%s", rendered)
	}
	// The wordmark is ASCII art, so the letters are not literal text in it.
	// What the banner renders is what has to be on screen.
	if !strings.Contains(rendered, bannerRows()[0]) {
		t.Errorf("the welcome should show the wordmark art:\n%s", rendered)
	}
}

func TestPlainPromptColoursOnlyWhenAsked(t *testing.T) {
	if got := plainPrompt(false); strings.Contains(got, "\x1b[") {
		t.Errorf("plain prompt should have no escapes, got %q", got)
	}
	if got := plainPrompt(true); !strings.Contains(got, "\x1b[") {
		t.Errorf("coloured prompt should have escapes, got %q", got)
	}
}

func TestOrNone(t *testing.T) {
	if got := orNone(""); !strings.Contains(got, "setup") {
		t.Errorf("an empty value should point at setup, got %q", got)
	}
	if got := orNone("gpt-5.4-mini"); got != "gpt-5.4-mini" {
		t.Errorf("a real value should pass through, got %q", got)
	}
}

// ------------------------------------------------------------------ terminal detection

func TestIsInteractiveIsFalseForNonTerminals(t *testing.T) {
	var out bytes.Buffer
	if IsInteractive(strings.NewReader(""), &out) {
		t.Errorf("a buffer is not a terminal")
	}
	if isTerminalWriter(&out) {
		t.Errorf("a buffer is not a terminal writer")
	}
	if isTerminalReader(strings.NewReader("x")) {
		t.Errorf("a string reader is not a terminal")
	}
}

// ------------------------------------------------------------------ setup wizard

// TestSetupWizardStartsAtTheProviderStep is the first-run contract.
func TestSetupWizardStartsAtTheProviderStep(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	application := testApp(t)

	model := New(application)
	if model.current != modePicker {
		t.Fatalf("a fresh install should open the wizard, got mode %d", model.current)
	}
	resize(model, 120, 40)
	if model.picker.action != "setup-provider" {
		t.Errorf("the picker action = %q, want setup-provider", model.picker.action)
	}
	if len(model.picker.items) == 0 {
		t.Errorf("the provider list should be populated")
	}
	if view := stripANSI(model.View()); !strings.Contains(view, "Setup") {
		t.Errorf("the wizard should say it is setup:\n%s", view)
	}
}

// TestSetupWizardRefusesAnEmptyKey keeps a blank Enter from storing nothing and
// silently moving on.
func TestSetupWizardRefusesAnEmptyKey(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	application := testApp(t)
	model := New(application)
	model.setup.providerID = "anthropic"
	model.setup.step = setupKey
	model.current = modeSetup
	resize(model, 120, 40)
	model.input.SetValue("   ")

	next, _ := model.handleSetupKey(key("enter"))
	final := next.(*Model)

	if final.setup.step != setupKey {
		t.Errorf("an empty key must not advance the wizard, got step %d", final.setup.step)
	}
	if final.setup.errText == "" {
		t.Errorf("the refusal should be explained")
	}
}

// TestSetupWizardStoresAKeyAndOffersModels walks the path an operator takes.
func TestSetupWizardStoresAKeyAndOffersModels(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	application := testApp(t)
	model := New(application)
	model.setup.providerID = "anthropic"
	model.setup.step = setupKey
	model.current = modeSetup
	resize(model, 120, 40)
	model.input.SetValue("sk-ant-test-key")

	next, _ := model.handleSetupKey(key("enter"))
	final := next.(*Model)

	if final.setup.step != setupModel {
		t.Fatalf("the wizard should move to model selection, got step %d", final.setup.step)
	}
	if final.current != modePicker {
		t.Errorf("model selection should open the picker, got mode %d", final.current)
	}
	if final.input.Value() != "" {
		t.Errorf("the key should be cleared from the field, got %q", final.input.Value())
	}
	if got := final.app.Secrets().Get("provider:anthropic"); got != "sk-ant-test-key" {
		t.Errorf("the key was not stored, got %q", got)
	}
	// The secret must never be echoed back to the screen.
	if strings.Contains(stripANSI(final.View()), "sk-ant-test-key") {
		t.Errorf("the key must not appear on screen")
	}
}

// TestSetupWizardCustomModelRequiresText covers the hand-typed model path.
func TestSetupWizardCustomModelRequiresText(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	application := testApp(t)
	model := New(application)
	model.setup.providerID = "openai-compatible"
	model.setup.step = setupCustomModel
	model.current = modeSetup
	resize(model, 120, 40)
	model.input.SetValue("")

	next, _ := model.handleSetupKey(key("enter"))
	final := next.(*Model)
	if final.setup.errText == "" {
		t.Errorf("an empty model id should be refused with a message")
	}
	if final.setup.step != setupCustomModel {
		t.Errorf("the wizard should stay on the same step")
	}
}

// TestSetupWizardFinishesAndReturnsToChat checks the end of onboarding.
func TestSetupWizardFinishesAndReturnsToChat(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	application := testApp(t)
	model := New(application)
	model.setup.step = setupTelegramAsk
	model.current = modeSetup
	resize(model, 120, 40)

	// Declining Telegram is the shortest path to the end.
	next, _ := model.handleSetupKey(key("n"))
	final := next.(*Model)

	if final.setup.step != setupDone {
		t.Fatalf("declining should finish the wizard, got step %d", final.setup.step)
	}
	if view := stripANSI(final.View()); !strings.Contains(view, "Setup complete") {
		t.Errorf("the wizard should confirm it is done:\n%s", view)
	}

	next, _ = final.handleSetupKey(key("enter"))
	done := next.(*Model)
	if done.current != modeChat {
		t.Errorf("finishing should return to chat, got mode %d", done.current)
	}
	if view := stripANSI(done.View()); !strings.Contains(view, "Onboarding complete") {
		t.Errorf("the transcript should record completion:\n%s", view)
	}
}

func TestSetupProgressCounts(t *testing.T) {
	cases := map[setupStep]int{
		setupProvider:      1,
		setupKey:           2,
		setupModel:         2,
		setupCustomModel:   2,
		setupTelegramAsk:   3,
		setupTelegramToken: 3,
		setupTelegramPair:  3,
		setupDone:          4,
	}
	for step, want := range cases {
		if got := setupProgress(step); got != want {
			t.Errorf("setupProgress(%d) = %d, want %d", step, got, want)
		}
	}
}

func TestSetupProviderItemsCoverEveryProvider(t *testing.T) {
	items := setupProviderItems()
	if len(items) != len(provider.Providers()) {
		t.Errorf("items = %d, providers = %d", len(items), len(provider.Providers()))
	}
	for _, item := range items {
		if item.ID == "" || item.Label == "" {
			t.Errorf("an item is incomplete: %+v", item)
		}
		if item.Extra == "" {
			t.Errorf("%s should say whether a key is needed", item.ID)
		}
	}
}

func TestProviderNeedsKey(t *testing.T) {
	if !providerNeedsKey("anthropic") {
		t.Errorf("anthropic needs a key")
	}
	if providerNeedsKey("ollama") {
		t.Errorf("ollama needs no key")
	}
	if !providerNeedsKey("not-a-provider") {
		t.Errorf("an unknown provider should be treated as needing a key")
	}
}

// TestSaveTelegramTokenRefusesAnEmptyToken keeps a stray Enter from starting a
// bot verification against an empty token.
func TestSaveTelegramTokenRefusesAnEmptyToken(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	application := testApp(t)
	model := New(application)
	model.setup.step = setupTelegramToken
	model.current = modeSetup
	resize(model, 120, 40)
	model.input.SetValue("   ")

	next, _ := model.handleSetupKey(key("enter"))
	final := next.(*Model)

	if final.setup.step != setupTelegramToken {
		t.Errorf("an empty token must not advance the wizard, got step %d", final.setup.step)
	}
	if final.setup.errText == "" {
		t.Errorf("the refusal should be explained")
	}
	if final.app.TelegramToken() != "" {
		t.Errorf("no token should have been stored")
	}
}

// TestSaveTelegramTokenEscapeSkipsTelegram is the "not now" path at the token
// step, which must finish onboarding rather than trapping the operator.
func TestSaveTelegramTokenEscapeSkipsTelegram(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	application := testApp(t)
	model := New(application)
	model.setup.providerID = "ollama"
	model.setup.step = setupTelegramToken
	model.current = modeSetup
	resize(model, 120, 40)

	next, _ := model.handleSetupKey(key("esc"))
	final := next.(*Model)

	if final.setup.step != setupDone {
		t.Errorf("escaping the token step should finish the setup, got step %d", final.setup.step)
	}
	if final.current != modeSetup {
		t.Errorf("the summary should stay on screen, got mode %d", final.current)
	}
}
