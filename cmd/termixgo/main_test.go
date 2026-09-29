package main

import (
	"bytes"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/provider"
	"github.com/99apps-id/termixgo/internal/secrets"
)

// withState points the whole CLI at a throwaway state directory, which is what
// keeps these tests off the developer's real configuration.
//
// It also moves the process into a temp working directory. The CLI builds its
// app from the working directory, and the app keeps its state in
// <workspace>/.termixgo, so a test that stayed in the package directory would
// leave a search index and an error journal inside the source tree.
func withState(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Chdir(t.TempDir())
	return home
}

// catalogueModel returns the head of the catalogue, which is the fixture a
// command test needs when it has to name a model that really exists. Pinning
// an id here turned every vendor rename into a failure about argument parsing.
func catalogueModel(t *testing.T) provider.Model {
	t.Helper()
	models := provider.Models()
	if len(models) == 0 {
		t.Fatal("the catalogue is empty")
	}
	return models[0]
}

// labelledModel returns a catalogue entry with a multi-word label that appears
// exactly once, which is what the label-resolution tests need: the label has to
// be several words for the joining rule to matter, and unique for the result to
// be predictable.
func labelledModel(t *testing.T) provider.Model {
	t.Helper()
	for _, model := range provider.Models() {
		if !strings.Contains(model.Label, " ") || model.Label == model.ID {
			continue
		}
		unique := true
		for _, other := range provider.Models() {
			if other.Label == model.Label && other.ID != model.ID {
				unique = false
				break
			}
		}
		if unique {
			return model
		}
	}
	t.Fatal("no catalogue model has a unique multi-word label")
	return provider.Model{}
}

// runCLI captures stdout and stderr for one invocation.
func runCLI(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := run(args, strings.NewReader(""), &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

func TestVersionAndHelp(t *testing.T) {
	withState(t)

	stdout, _, err := runCLI(t, "version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.Contains(stdout, "Termixgo") || !strings.Contains(stdout, runtime.GOOS) {
		t.Errorf("version output = %q, want the name and platform", stdout)
	}

	// help is accepted under three spellings and must list the subcommands.
	for _, spelling := range []string{"help", "--help", "-h"} {
		stdout, _, err := runCLI(t, spelling)
		if err != nil {
			t.Fatalf("%s: %v", spelling, err)
		}
		for _, want := range []string{"termixgo run", "termixgo doctor", "termixgo secret"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("%s output is missing %q", spelling, want)
			}
		}
	}
}

func TestUnknownCommandNamesTheHelpCommand(t *testing.T) {
	withState(t)
	_, _, err := runCLI(t, "bogus")
	if err == nil {
		t.Fatalf("an unknown command must fail")
	}
	if !strings.Contains(err.Error(), "termixgo help") {
		t.Errorf("the error should point at help, got %v", err)
	}
}

func TestModelCommandRoundTrip(t *testing.T) {
	withState(t)
	first := catalogueModel(t)

	if _, _, err := runCLI(t, "model", first.ID); err != nil {
		t.Fatalf("model set: %v", err)
	}
	stdout, _, err := runCLI(t, "model")
	if err != nil {
		t.Fatalf("model get: %v", err)
	}
	if !strings.Contains(stdout, first.ID) {
		t.Errorf("model get = %q", stdout)
	}

	// A label resolves to its stable id.
	labelled := labelledModel(t)
	if _, _, err := runCLI(t, "model", labelled.Label); err != nil {
		t.Fatalf("model by label: %v", err)
	}
	loaded, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.DefaultModel != labelled.ID {
		t.Errorf("DefaultModel = %q, want %s", loaded.DefaultModel, labelled.ID)
	}
}

func TestModelWithoutConfigurationSaysSo(t *testing.T) {
	withState(t)
	stdout, _, err := runCLI(t, "model")
	if err != nil {
		t.Fatalf("model: %v", err)
	}
	if !strings.Contains(stdout, "termixgo setup") {
		t.Errorf("an unset model should point at setup, got %q", stdout)
	}
}

func TestApprovalCommandValidatesTheMode(t *testing.T) {
	withState(t)

	if _, _, err := runCLI(t, "approval", "edits"); err != nil {
		t.Fatalf("approval set: %v", err)
	}
	stdout, _, err := runCLI(t, "approval")
	if err != nil {
		t.Fatalf("approval get: %v", err)
	}
	if strings.TrimSpace(stdout) != "edits" {
		t.Errorf("approval = %q, want edits", stdout)
	}
	if _, _, err := runCLI(t, "approval", "nonsense"); err == nil {
		t.Errorf("an invalid mode must be rejected")
	}
}

func TestTrustCommandReportsAndPersists(t *testing.T) {
	withState(t)

	// A fresh state starts untrusted.
	stdout, _, err := runCLI(t, "trust")
	if err != nil {
		t.Fatalf("trust get: %v", err)
	}
	if !strings.Contains(stdout, "untrusted") {
		t.Errorf("trust get = %q, want untrusted", stdout)
	}

	if stdout, _, err = runCLI(t, "trust", "on"); err != nil {
		t.Fatalf("trust on: %v", err)
	}
	// The report must say "trusted", not echo the argument back.
	if !strings.Contains(stdout, "is now trusted") {
		t.Errorf("trust on = %q, want a trusted report", stdout)
	}
	if strings.Contains(stdout, "is now on") {
		t.Errorf("trust on echoed the raw argument: %q", stdout)
	}

	loaded, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	workspace, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.IsTrusted(workspace) {
		t.Errorf("the working directory should be trusted after 'trust on'")
	}

	if _, _, err := runCLI(t, "trust", "off"); err != nil {
		t.Fatalf("trust off: %v", err)
	}
	loaded, _ = config.Load()
	if loaded.IsTrusted(workspace) {
		t.Errorf("'trust off' did not remove the folder")
	}

	if _, _, err := runCLI(t, "trust", "sideways"); err == nil {
		t.Errorf("an invalid argument must be rejected")
	}
}

func TestTrustCommandRejectsUnknownArgument(t *testing.T) {
	withState(t)
	_, _, err := runCLI(t, "trust", "maybe")
	if err == nil {
		t.Fatalf("expected an error")
	}
	if !strings.Contains(err.Error(), "usage") {
		t.Errorf("the error should show usage, got %v", err)
	}
}

func TestSecretCommandStoresAKeyWithoutEchoingIt(t *testing.T) {
	withState(t)

	stdout, _, err := runCLI(t, "secret", "openai", "sk-super-secret-value")
	if err != nil {
		t.Fatalf("secret: %v", err)
	}
	if strings.Contains(stdout, "sk-super-secret-value") {
		t.Fatalf("the key was echoed back: %q", stdout)
	}

	store, err := secrets.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := store.Get(secrets.ProviderKey("openai")); got != "sk-super-secret-value" {
		t.Errorf("stored value = %q", got)
	}
}

func TestSecretCommandRejectsBadInput(t *testing.T) {
	withState(t)

	if _, _, err := runCLI(t, "secret"); err == nil {
		t.Errorf("a missing provider must be rejected")
	}
	if _, _, err := runCLI(t, "secret", "not-a-provider", "sk-x"); err == nil {
		t.Errorf("an unknown provider must be rejected")
	}
	if _, _, err := runCLI(t, "secret", "openai", "   "); err == nil {
		t.Errorf("an empty key must be rejected")
	}
}

func TestModelsCommandListsAndFilters(t *testing.T) {
	withState(t)

	anthropic := provider.ModelsFor("anthropic")
	openai := provider.ModelsFor("openai")
	if len(anthropic) == 0 || len(openai) == 0 {
		t.Fatal("the fixture needs models for both providers")
	}

	stdout, _, err := runCLI(t, "models", "--provider", "anthropic")
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	if !strings.Contains(stdout, anthropic[0].ID) {
		t.Errorf("anthropic models = %q", stdout)
	}
	if strings.Contains(stdout, openai[0].ID) {
		t.Errorf("the provider filter did not apply: %q", stdout)
	}

	if _, _, err := runCLI(t, "models", "--provider"); err == nil {
		t.Errorf("a missing filter value must be rejected")
	}
	if _, _, err := runCLI(t, "models", "--bogus"); err == nil {
		t.Errorf("an unknown option must be rejected")
	}
}

func TestDoctorReportsConfiguration(t *testing.T) {
	home := withState(t)

	if _, _, err := runCLI(t, "secret", "openai", "sk-doctor-check"); err != nil {
		t.Fatalf("secret: %v", err)
	}
	stdout, _, err := runCLI(t, "doctor")
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	for _, want := range []string{
		"state directory: " + home,
		"providers:",
		"key from stored",
		"secrets access:",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("doctor output is missing %q:\n%s", want, stdout)
		}
	}
	// The point of the access line: a stored key must report as owner only.
	if !strings.Contains(stdout, "owner only") {
		t.Errorf("doctor did not confirm the secret file is private:\n%s", stdout)
	}
}

func TestRunWithoutAModelExplainsSetup(t *testing.T) {
	withState(t)
	_, _, err := runCLI(t, "run", "hello")
	if err == nil {
		t.Fatalf("run without a model must fail")
	}
	if !strings.Contains(err.Error(), "termixgo setup") {
		t.Errorf("the error should point at setup, got %v", err)
	}
}

func TestRunNeedsAPrompt(t *testing.T) {
	withState(t)
	if _, _, err := runCLI(t, "run"); err == nil {
		t.Fatalf("run without a prompt must fail")
	}
}
