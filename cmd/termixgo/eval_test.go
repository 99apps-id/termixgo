package main

import (
	"strings"
	"testing"
)

func TestEvalCommandList(t *testing.T) {
	withState(t)

	stdout, _, err := runCLI(t, "eval", "list")
	if err != nil {
		t.Fatalf("eval list: %v", err)
	}
	if !strings.Contains(stdout, "termixgo-core-benchmarks") {
		t.Errorf("expected suite name in list output, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "go-nil-guard") || !strings.Contains(stdout, "go-string-reverse") {
		t.Errorf("expected builtin tasks in list output, got:\n%s", stdout)
	}
}

func TestEvalCommandUnknownFlag(t *testing.T) {
	withState(t)

	_, _, err := runCLI(t, "eval", "--bogus")
	if err == nil {
		t.Fatal("expected error on unknown flag, got nil")
	}
	if !strings.Contains(err.Error(), "unknown eval flag") {
		t.Errorf("unexpected error: %v", err)
	}
}
