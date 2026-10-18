package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/secrets"
)

func TestEnvGetTool(t *testing.T) {
	tool := &envGetTool{}
	env := &Env{Trusted: true}

	t.Setenv("TEST_VAR_NORMAL", "hello_world")
	t.Setenv("TEST_API_KEY", "super_secret_token_12345")

	// 1. Read normal variable
	res, err := tool.Run(context.Background(), env, map[string]any{
		"name": "TEST_VAR_NORMAL",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "TEST_VAR_NORMAL=hello_world") {
		t.Errorf("expected value in output, got: %s", res.Output)
	}

	// 2. Read sensitive variable masked by default
	res, err = tool.Run(context.Background(), env, map[string]any{
		"name": "TEST_API_KEY",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(res.Output, "super_secret_token_12345") {
		t.Errorf("sensitive value must be redacted, got: %s", res.Output)
	}
	if !strings.Contains(res.Output, "REDACTED") {
		t.Errorf("expected REDACTED in output, got: %s", res.Output)
	}

	// 3. A reveal flag must not bypass the mask. The tool used to accept one.
	res, err = tool.Run(context.Background(), env, map[string]any{
		"name":   "TEST_API_KEY",
		"unmask": true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(res.Output, "super_secret_token_12345") {
		t.Errorf("unmask must not reveal a credential, got: %s", res.Output)
	}

	// 4. Read non-existent variable
	res, err = tool.Run(context.Background(), env, map[string]any{
		"name": "DEFINITELY_NOT_EXISTING_XYZ_123",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Output, "is not set") {
		t.Errorf("expected not set message, got: %s", res.Output)
	}
}

// TestEnvGetMasksAStoredSecretUnderAPlainName covers the case the name check
// misses: a credential the secret store holds, copied into a variable whose
// name says nothing about it.
func TestEnvGetMasksAStoredSecretUnderAPlainName(t *testing.T) {
	store := &secrets.Store{}
	const secret = "stored-credential-value"
	if err := store.Set("openai", secret); err != nil {
		t.Fatalf("set: %v", err)
	}
	t.Setenv("MY_PLAIN_SETTING", secret)

	res, err := (&envGetTool{}).Run(context.Background(), &Env{Secrets: store, Trusted: true}, map[string]any{
		"name": "MY_PLAIN_SETTING",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(res.Output, secret) {
		t.Errorf("a stored credential must be redacted even under a plain name, got: %s", res.Output)
	}
}
