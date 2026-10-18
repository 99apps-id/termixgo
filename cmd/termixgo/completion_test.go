package main

import (
	"strings"
	"testing"
)

func TestCompletionCoversEveryShell(t *testing.T) {
	for _, shell := range completionShells {
		script, ok := completionScript(shell)
		if !ok {
			t.Fatalf("missing script for %s", shell)
		}
		for _, want := range []string{"termixgo", "approval", "plan"} {
			if !strings.Contains(script, want) {
				t.Errorf("%s script is missing %q", shell, want)
			}
		}
	}
}

func TestCompletionRejectsUnknownShell(t *testing.T) {
	if _, ok := completionScript("tcsh"); ok {
		t.Errorf("an unknown shell must report an error")
	}
	var out strings.Builder
	if err := runCompletion([]string{"tcsh"}, &out); err == nil {
		t.Errorf("runCompletion should fail for an unknown shell")
	}
}

func TestCompletionDefaultsToBash(t *testing.T) {
	var out strings.Builder
	if err := runCompletion(nil, &out); err != nil {
		t.Fatalf("runCompletion: %v", err)
	}
	if !strings.Contains(out.String(), "complete -F _termixgo") {
		t.Errorf("the default script should be bash:\n%s", out.String())
	}
}
