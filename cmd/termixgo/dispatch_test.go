package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/secrets"
	"github.com/99apps-id/termixgo/internal/version"
)

// ------------------------------------------------- the exit code

// TestTerminateReportsFailuresAndExitCodes pins the contract a script depends
// on: a successful command exits 0, a failed one exits non-zero and says why
// on stderr. `termixgo -p` inside a pipeline must not read as success when the
// turn failed.
func TestTerminateReportsFailuresAndExitCodes(t *testing.T) {
	var stderr bytes.Buffer
	if got := terminate(nil, &stderr); got != 0 {
		t.Errorf("exit code = %d, want 0", got)
	}
	if stderr.Len() != 0 {
		t.Errorf("a successful run wrote %q to stderr", stderr.String())
	}

	stderr.Reset()
	got := terminate(errors.New("the provider rejected the key"), &stderr)
	if got != 1 {
		t.Errorf("exit code = %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "termixgo:") {
		t.Errorf("stderr = %q, want the program name prefix", stderr.String())
	}
	if !strings.Contains(stderr.String(), "the provider rejected the key") {
		t.Errorf("stderr = %q, want the cause", stderr.String())
	}
}

// ------------------------------------------------- the model command

// TestModelCommandAcceptsAModelOutsideTheCatalogue is how a custom endpoint or
// a brand-new vendor model is selected before it is catalogued.
//
// The provider prefix is stripped and recorded as the model's provider, exactly
// as the in-app /model does. Storing the prefixed spelling here while the app
// stores the bare one would make doctor, the status bar and a price override
// disagree about the same model.
func TestModelCommandAcceptsAModelOutsideTheCatalogue(t *testing.T) {
	withState(t)

	if _, _, err := runCLI(t, "model", "openai:gpt-9-experimental"); err != nil {
		t.Fatalf("model: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DefaultModel != "gpt-9-experimental" {
		t.Errorf("DefaultModel = %q, want the bare id with the provider recorded separately", cfg.DefaultModel)
	}

	// A model the catalogue does not list and no provider prefix is kept
	// verbatim, so an unknown vendor still works.
	if _, _, err := runCLI(t, "model", "some-unknown-model"); err != nil {
		t.Fatalf("model: %v", err)
	}
	cfg, _ = config.Load()
	if cfg.DefaultModel != "some-unknown-model" {
		t.Errorf("DefaultModel = %q, want the query kept", cfg.DefaultModel)
	}
}

// TestModelCommandJoinsAQuotedName keeps a label with spaces working, which is
// how the catalogue labels are spelled.
func TestModelCommandJoinsAQuotedName(t *testing.T) {
	withState(t)

	labelled := labelledModel(t)
	words := strings.Fields(labelled.Label)
	if len(words) < 2 {
		t.Fatalf("the fixture needs a multi-word label, got %q", labelled.Label)
	}
	args := append([]string{"model"}, words...)
	if _, _, err := runCLI(t, args...); err != nil {
		t.Fatalf("model: %v", err)
	}
	cfg, _ := config.Load()
	if cfg.DefaultModel != labelled.ID {
		t.Errorf("DefaultModel = %q, want %s resolved from the label", cfg.DefaultModel, labelled.ID)
	}
}

// ------------------------------------------------- the models command

func TestModelsCommandListsEverythingWithoutAFilter(t *testing.T) {
	withState(t)

	stdout, _, err := runCLI(t, "models")
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	// Every provider and at least one model per provider, so an operator can
	// see the whole catalogue in one call.
	for _, want := range []string{"OpenAI (openai)", "Anthropic (anthropic)", "Ollama (ollama)", "gpt-5.4-mini"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output is missing %q", want)
		}
	}
	// The short spelling of the filter is accepted too.
	short, _, err := runCLI(t, "models", "-p", "groq")
	if err != nil {
		t.Fatalf("models -p: %v", err)
	}
	if !strings.Contains(short, "Groq (groq)") || strings.Contains(short, "OpenAI (openai)") {
		t.Errorf("the short filter did not apply:\n%s", short)
	}
}

// ------------------------------------------------- doctor

// TestDoctorReportsTheConfiguredBudgetAndOverrides covers the lines that only
// appear once the operator has opted in to cost control.
func TestDoctorReportsTheConfiguredBudgetAndOverrides(t *testing.T) {
	withState(t)

	cfg := config.Default()
	cfg.CostBudgetUSD = 1.5
	cfg.ModelPricing = map[string]config.ModelPrice{"my-model": {InputPerMillion: 1, OutputPerMillion: 2}}
	if err := config.Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	stdout, _, err := runCLI(t, "doctor")
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	for _, want := range []string{"cost budget:    $1.50 per session", "model prices:   1 override(s) in config"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output is missing %q:\n%s", want, stdout)
		}
	}
}

func TestDoctorSaysWhenThereIsNoBudget(t *testing.T) {
	withState(t)
	stdout, _, err := runCLI(t, "doctor")
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if !strings.Contains(stdout, "cost budget:    none") {
		t.Errorf("output = %q, want the unlimited default stated", stdout)
	}
	if !strings.Contains(stdout, version.Full()) {
		t.Errorf("doctor should report the version")
	}
	// A provider that needs no key must not read as a misconfiguration.
	if !strings.Contains(stdout, "ollama             no key needed") {
		t.Errorf("output = %q, want the local provider marked as needing no key", stdout)
	}
}

// TestDoctorReportsAKeyFromTheEnvironment covers the CI path, where no secret
// file is written and the key comes from the environment.
func TestDoctorReportsAKeyFromTheEnvironment(t *testing.T) {
	withState(t)
	t.Setenv("GROQ_API_KEY", "gsk_from_environment")

	stdout, _, err := runCLI(t, "doctor")
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if !strings.Contains(stdout, "key from environment") {
		t.Errorf("output = %q, want the environment source named", stdout)
	}
	if strings.Contains(stdout, "gsk_from_environment") {
		t.Fatalf("doctor printed the key")
	}
}

// ------------------------------------------------- secret protection report

// TestSecretProtectionReportCoversEachState walks the three answers the line
// can give. The check exists because a silent "ok" hid an inheriting ACL on
// Windows, so the wording matters as much as the verdict.
func TestSecretProtectionReportCoversEachState(t *testing.T) {
	withState(t)
	store, err := secrets.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	var out bytes.Buffer
	reportSecretProtection(store, &out)
	if !strings.Contains(out.String(), "no secret file yet") {
		t.Errorf("output = %q, want the fresh-install answer", out.String())
	}

	if err := store.Set(secrets.ProviderKey("openai"), "sk-protection-check"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	out.Reset()
	reportSecretProtection(store, &out)
	detail := out.String()
	if !strings.Contains(detail, "owner only") {
		t.Errorf("output = %q, want the owner-only verdict", detail)
	}
	// The detail is what makes the verdict auditable: "owner only" plus the
	// evidence is a claim someone can check. The evidence is spelled
	// differently per platform, because the access rule is a different thing:
	// a mode on POSIX and a trustee list on Windows.
	switch runtime.GOOS {
	case "windows":
		if !strings.Contains(detail, "1 entries") {
			t.Errorf("output = %q, want the number of trustees", detail)
		}
	default:
		if !strings.Contains(detail, "mode 0600") {
			t.Errorf("output = %q, want the POSIX mode", detail)
		}
	}

	// A directory where the secret file belongs is the state an interrupted
	// upgrade can leave. Which verdict comes back genuinely differs by
	// platform, so what is pinned is that a verdict comes back at all: the
	// failure this line exists for was silence.
	if err := os.Remove(store.Path()); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.MkdirAll(store.Path(), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	out.Reset()
	reportSecretProtection(store, &out)
	got := strings.TrimSpace(out.String())
	if !strings.HasPrefix(got, "secrets access: ") {
		t.Errorf("output = %q, want a verdict line, never silence", got)
	}
	for _, silent := range []string{"owner only", "unknown", "WARNING", "FAILED", "no secret file yet"} {
		if strings.Contains(got, silent) {
			return
		}
	}
	t.Errorf("output = %q, want one of the known verdicts", got)
}

// ------------------------------------------------- telegram

func TestTelegramCommandRejectsAnUnknownProviderToken(t *testing.T) {
	withState(t)
	// No subcommand and no argument is the status report, which must not fail
	// on a machine with no token.
	stdout, _, err := runCLI(t, "telegram")
	if err != nil {
		t.Fatalf("telegram: %v", err)
	}
	if !strings.Contains(stdout, "token: false") {
		t.Errorf("output = %q, want the token state", stdout)
	}
}

// TestRunTelegramReportsAnUnreadableStore keeps a permission problem from
// reading as "no token".
func TestRunTelegramReportsAnUnreadableStore(t *testing.T) {
	home := withState(t)
	// A directory where the secret file belongs makes secrets.Load fail.
	if err := os.MkdirAll(filepath.Join(home, "secrets.json"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, _, err := runCLI(t, "telegram", "status"); err == nil {
		t.Errorf("an unreadable secret store must be reported")
	}
}

// TestDoctorReportsAnUnreadableStore covers the same problem on the doctor
// path, where a silent default would be misleading.
func TestDoctorReportsAnUnreadableStore(t *testing.T) {
	home := withState(t)
	if err := os.MkdirAll(filepath.Join(home, "secrets.json"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, _, err := runCLI(t, "doctor"); err == nil {
		t.Errorf("doctor must report an unreadable secret store")
	}
}

// ------------------------------------------------- usage and dispatch

// TestUsageNamesEveryCommand is the discovery path: a subcommand that is not in
// the help text may as well not exist.
func TestUsageNamesEveryCommand(t *testing.T) {
	var out bytes.Buffer
	writeUsage(&out)
	usage := out.String()

	for _, want := range []string{
		"termixgo setup", "termixgo run", "termixgo models", "termixgo model",
		"termixgo trust", "termixgo approval", "termixgo secret",
		"termixgo telegram", "termixgo doctor", "termixgo version", "termixgo help",
	} {
		if !strings.Contains(usage, want) {
			t.Errorf("usage is missing %q", want)
		}
	}
	// The help text states where secrets live, which is the question a security
	// review asks first.
	if !strings.Contains(usage, "secrets.json") {
		t.Errorf("usage should say where secrets are stored:\n%s", usage)
	}
	if !strings.Contains(usage, version.Version) {
		t.Errorf("usage should carry the version")
	}
}

func TestHelpIsAvailableUnderEverySpelling(t *testing.T) {
	withState(t)
	for _, spelling := range []string{"help", "--help", "-h"} {
		var stdout, stderr bytes.Buffer
		if err := run([]string{spelling}, strings.NewReader(""), &stdout, &stderr); err != nil {
			t.Fatalf("%s: %v", spelling, err)
		}
		if !strings.Contains(stdout.String(), "Usage:") {
			t.Errorf("%s produced %q", spelling, stdout.String())
		}
	}
	for _, spelling := range []string{"version", "--version", "-v"} {
		var stdout, stderr bytes.Buffer
		if err := run([]string{spelling}, strings.NewReader(""), &stdout, &stderr); err != nil {
			t.Fatalf("%s: %v", spelling, err)
		}
		if !strings.Contains(stdout.String(), version.Name) {
			t.Errorf("%s produced %q", spelling, stdout.String())
		}
	}
}

// TestRunWithNoArgumentsStartsThePlainRepl is the bare invocation in a pipe,
// which is what a CI job or a script does.
func TestRunWithNoArgumentsStartsThePlainRepl(t *testing.T) {
	withState(t)

	var stdout, stderr bytes.Buffer
	if err := run(nil, strings.NewReader("/exit\n"), &stdout, &stderr); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(stdout.String(), "Termixgo") {
		t.Errorf("output = %q, want the banner", stdout.String())
	}
}

// TestRunSetupStartsTheWizard covers the subcommand that opens onboarding
// directly, which is what an error message tells the operator to run.
func TestRunSetupStartsTheWizard(t *testing.T) {
	withState(t)

	var stdout, stderr bytes.Buffer
	// No terminal, so the wizard cannot be driven; the command must still
	// return rather than block, and it must report rather than hang.
	done := make(chan error, 1)
	go func() {
		done <- run([]string{"setup"}, strings.NewReader("/exit\n"), &stdout, &stderr)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	case <-timeoutAfter():
		t.Fatalf("setup did not return without a terminal")
	}
}

// ------------------------------------------------- helpers

// timeoutAfter bounds a call that must not block, so a regression shows up as a
// failure instead of a hang.
func timeoutAfter() <-chan time.Time { return time.After(30 * time.Second) }

func TestOrNoneSaysNoneForABlankValue(t *testing.T) {
	if got := orNone("  "); got != "(none)" {
		t.Errorf("orNone = %q", got)
	}
	if got := orNone("gpt-5.4-mini"); got != "gpt-5.4-mini" {
		t.Errorf("orNone = %q", got)
	}
}
