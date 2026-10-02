package agent

import (
	"context"
	"strings"
	"testing"
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

	// 3. Read sensitive variable with unmask=true
	res, err = tool.Run(context.Background(), env, map[string]any{
		"name":   "TEST_API_KEY",
		"unmask": true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Output, "TEST_API_KEY=super_secret_token_12345") {
		t.Errorf("expected unmasked value, got: %s", res.Output)
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
