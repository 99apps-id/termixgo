package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
)

// providerNeedingEndpoint is the id the catalogue gives a server whose address
// carries an account id or points at the operator's own machine. It is the only
// provider that has no default host and needs no key, so it is also the one
// that exercises the key step being optional.
const providerNeedingEndpoint = "openai-compatible"

// TestWizardAsksForTheEndpointBeforeTheKey is the step that used to be missing:
// a provider with no default host must be asked where its server lives, or
// every later request goes to a placeholder address.
func TestWizardAsksForTheEndpointBeforeTheKey(t *testing.T) {
	model := wizardModel(t)

	atEndpoint := choose(t, model, providerNeedingEndpoint)
	if atEndpoint.setup.step != setupEndpoint {
		t.Fatalf("a provider with no default host must ask for the endpoint first: step %d", atEndpoint.setup.step)
	}
	if atEndpoint.current != modeSetup {
		t.Errorf("the endpoint step takes the screen, so mode should be setup, got %d", atEndpoint.current)
	}
	// An address typed into a password field would be invisible as it is
	// checked for typos.
	if atEndpoint.input.EchoMode != textinput.EchoNormal {
		t.Errorf("the endpoint must be echoed while it is typed, got echo mode %d", atEndpoint.input.EchoMode)
	}
	if atEndpoint.input.Placeholder != "https://your-server/v1" {
		t.Errorf("placeholder = %q, want an example address", atEndpoint.input.Placeholder)
	}
	if view := display(atEndpoint); !strings.Contains(view, "base URL") {
		t.Errorf("the step should say what it wants:\n%s", view)
	}

	// The trailing slash is typed on purpose: joining a request path onto an
	// address that keeps it would double the separator.
	atKey := press(t, atEndpoint, "https://box.local:8080/v1/")
	atKey = press(t, atKey, "enter")
	if atKey.setup.errText != "" {
		t.Fatalf("a valid endpoint must not be rejected: %q", atKey.setup.errText)
	}
	if atKey.setup.step != setupKey {
		t.Fatalf("a compatible endpoint is still asked for a key: step %d", atKey.setup.step)
	}
	if got := atKey.app.Config().BaseURLs[providerNeedingEndpoint]; got != "https://box.local:8080/v1" {
		t.Errorf("stored endpoint = %q, want the typed address without the trailing slash", got)
	}
	if summary := strings.Join(atKey.setup.summary, " "); !strings.Contains(summary, "endpoint: https://box.local:8080/v1") {
		t.Errorf("the summary should name the endpoint, got %v", atKey.setup.summary)
	}
}

// TestWizardRefusesAnEndpointThatCannotWork keeps a typo out of the settings
// file, because a saved one fails every request that follows and the operator
// would have to go looking for the cause.
func TestWizardRefusesAnEndpointThatCannotWork(t *testing.T) {
	model := wizardModel(t)
	atEndpoint := choose(t, model, providerNeedingEndpoint)

	empty := press(t, atEndpoint, "enter")
	if empty.setup.errText == "" {
		t.Fatalf("an empty endpoint must be refused")
	}
	if empty.setup.step != setupEndpoint {
		t.Errorf("a refused endpoint must keep the operator on the step, got %d", empty.setup.step)
	}
	if view := display(empty); !strings.Contains(view, "base URL") {
		t.Errorf("the refusal should be visible:\n%s", view)
	}

	schemeless := press(t, atEndpoint, "box.local:8080/v1")
	schemeless = press(t, schemeless, "enter")
	if schemeless.setup.errText == "" {
		t.Fatalf("an address with no scheme must be refused")
	}
	if schemeless.setup.step != setupEndpoint {
		t.Errorf("step = %d, want the endpoint step", schemeless.setup.step)
	}
	if got := schemeless.app.Config().BaseURLs[providerNeedingEndpoint]; got != "" {
		t.Errorf("a refused endpoint must not be stored, got %q", got)
	}
}

// TestWizardLetsACompatibleServerSkipTheKey is why the compatible path asks for
// a key at all rather than assuming none: most self-hosted servers want one,
// but an empty answer has to mean "none" and carry on to the model.
func TestWizardLetsACompatibleServerSkipTheKey(t *testing.T) {
	model := wizardModel(t)

	atKey := choose(t, model, providerNeedingEndpoint)
	atKey = press(t, atKey, "http://localhost:8080/v1")
	atKey = press(t, atKey, "enter")
	if atKey.setup.step != setupKey {
		t.Fatalf("step = %d, want the key step", atKey.setup.step)
	}
	if !strings.Contains(atKey.input.Placeholder, "skips") {
		t.Errorf("the key prompt should say Enter is allowed, got %q", atKey.input.Placeholder)
	}

	atModel := press(t, atKey, "enter")
	if atModel.setup.errText != "" {
		t.Fatalf("an empty key must be allowed for a compatible server: %q", atModel.setup.errText)
	}
	// The catalogue has no models for a server it cannot know, so the wizard
	// falls back to asking for the model id.
	if atModel.setup.step != setupCustomModel {
		t.Fatalf("a provider with no catalogue models must ask for a model id: step %d", atModel.setup.step)
	}
	if !strings.Contains(atModel.setup.message, "without a key") {
		t.Errorf("message = %q, want it to say the key was skipped", atModel.setup.message)
	}

	typed := press(t, atModel, "my-model")
	done := press(t, typed, "enter")
	if done.setup.errText != "" {
		t.Fatalf("a model id must be accepted: %q", done.setup.errText)
	}
	if !done.app.HasModel() {
		t.Errorf("the model should be applied after the id is typed")
	}
}
