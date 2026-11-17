package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rememberEnv builds the smallest environment the remember tool needs: a
// throwaway workspace with learned memory attached.
func rememberEnv(workspace string) *Env {
	return &Env{
		Workspace: workspace,
		Memory:    NewMemory(workspace),
	}
}

func rememberFact(t *testing.T, env *Env, fact string) Result {
	t.Helper()
	result, err := (&rememberTool{}).Run(context.Background(), env, map[string]any{
		"fact":  fact,
		"scope": "project",
	})
	if err != nil {
		t.Fatalf("remember tool: %v", err)
	}
	return result
}

func memoryFileExists(t *testing.T, workspace string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(workspace, ".termixgo", "memory.md"))
	if err == nil {
		return true
	}
	if os.IsNotExist(err) {
		return false
	}
	t.Fatalf("stat memory file: %v", err)
	return false
}

// Tokens below are assembled at runtime so no secret-shaped literal lives in
// this file; each matches the scrubber's token shapes once concatenated.
func secretShapedFacts() map[string]string {
	return map[string]string{
		"api key":      "the deploy token is " + "sk-" + strings.Repeat("k", 24),
		"aws key":      "backup key " + "AKIA" + strings.Repeat("A", 16) + " for the old account",
		"private key":  "the host key is " + "-----BEGIN " + "RSA PRIVATE KEY----- " + strings.Repeat("M", 8),
		"github token": "ci uses " + "ghp_" + strings.Repeat("g", 24) + " for releases",
	}
}

// A fact carrying secret material must be refused, not stored: memory rides
// into every future session prompt, so a stored key would leak to the
// provider on every later turn. The refusal must not echo the secret back.
func TestRememberRefusesSecretShapedFacts(t *testing.T) {
	for name, fact := range secretShapedFacts() {
		workspace := t.TempDir()
		result := rememberFact(t, rememberEnv(workspace), fact)
		if !result.IsError {
			t.Errorf("%s: a secret-shaped fact must be refused", name)
		}
		// The refusal is one fixed sentence, so by construction it cannot
		// echo the secret back; pin it exactly.
		const wantRefusal = "that looks like it contains secret material, so it was not remembered; rephrase it without the secret"
		if result.Output != wantRefusal {
			t.Errorf("%s: refusal must be the fixed sentence without secret material", name)
		}
		if memoryFileExists(t, workspace) {
			t.Errorf("%s: no memory file may be written for a refused fact", name)
		}
	}
}

// A fact that only talks about secrets, without carrying any, still stores:
// the guard must not make ordinary conventions unstorable.
func TestRememberKeepsFactAboutSecretsWithoutValue(t *testing.T) {
	workspace := t.TempDir()
	env := rememberEnv(workspace)
	result := rememberFact(t, env, "the api key lives in the operator vault, ask before using it")
	if result.IsError {
		t.Fatalf("a valueless convention must be stored, got %q", result.Output)
	}
	if block := env.Memory.PromptBlock(); !strings.Contains(block, "operator vault") {
		t.Errorf("stored fact missing from prompt block:\n%s", block)
	}
}
