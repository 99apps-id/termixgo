package ui

import (
	"strings"
	"testing"
)

// TestChatModelFocusesComposer pins the focus contract: chat mode owns the
// composer, so a fresh chat model must have it focused or typing goes nowhere.
func TestChatModelFocusesComposer(t *testing.T) {
	model := chatModel(t)
	if !model.composer.Focused() {
		t.Errorf("the composer should be focused in chat mode")
	}
}

// TestSetupDoneRefocusesComposer is the regression for the report that the
// terminal could not be typed in after onboarding. Leaving the wizard must
// hand focus back to the composer.
func TestSetupDoneRefocusesComposer(t *testing.T) {
	model := wizardModel(t)
	model.current = modeSetup
	model.setup.step = setupDone

	closed := press(t, model, "enter")
	if closed.current != modeChat {
		t.Fatalf("mode = %d, want chat", closed.current)
	}
	if !closed.composer.Focused() {
		t.Errorf("the composer should be focused after setup")
	}
	if closed.input.Focused() {
		t.Errorf("the setup field should be blurred after setup")
	}
}

// TestSetupEscRefocusesComposer covers the early exit: escaping the first
// wizard step must also leave a usable composer.
func TestSetupEscRefocusesComposer(t *testing.T) {
	model := wizardModel(t)
	model.current = modeSetup

	closed := press(t, model, "esc")
	if closed.current != modeChat {
		t.Fatalf("mode = %d, want chat", closed.current)
	}
	if !closed.composer.Focused() {
		t.Errorf("the composer should be focused after escaping setup")
	}
}

// TestPickerEscRefocusesComposer covers the non-setup picker: cancelling it
// must leave the composer usable.
func TestPickerEscRefocusesComposer(t *testing.T) {
	model := chatModel(t)
	model.composer.Blur()
	model.openPicker("Pick one", "model", []pickerItem{{ID: "a", Label: "Alpha"}})

	closed := press(t, model, "esc")
	if closed.current != modeChat {
		t.Fatalf("mode = %d, want chat", closed.current)
	}
	if !closed.composer.Focused() {
		t.Errorf("the composer should be focused after cancelling the picker")
	}
}

// TestSetupTitlesShareOneWordmark pins the header refactor: the picker and
// the wizard steps use one branded prefix - the plain wordmark plus the
// build version - and that prefix appears once per screen. The count anchors
// on the full prefix, not the bare name, because the status header also
// carries the name and must not read as a second wordmark.
func TestSetupTitlesShareOneWordmark(t *testing.T) {
	model := wizardModel(t)
	pickerView := stripANSI(display(model))
	if !strings.Contains(pickerView, setupTitlePrefix+": Provider") {
		t.Errorf("provider picker should carry the branded title:\n%s", pickerView)
	}
	if count := strings.Count(pickerView, setupTitlePrefix); count != 1 {
		t.Errorf("the setup wordmark appears %d times in the picker, want once:\n%s", count, pickerView)
	}

	model.current = modeSetup
	model.setup.step = setupKey
	model.setup.message = "a message"
	wizardView := stripANSI(display(model))
	if !strings.Contains(wizardView, setupTitlePrefix+" (") {
		t.Errorf("wizard should carry the branded header:\n%s", wizardView)
	}
	if count := strings.Count(wizardView, setupTitlePrefix); count != 1 {
		t.Errorf("the setup wordmark appears %d times in the wizard, want once:\n%s", count, wizardView)
	}
}
