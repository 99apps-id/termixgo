package agent

import (
	"context"
	"strings"
	"testing"
)

func TestArtifactWriteReadRoundtrip(t *testing.T) {
	ctx := context.Background()
	res, err := (&artifactWriteTool{}).Run(ctx, nil, map[string]any{"body": "alpha\nbeta\ngamma", "label": "roundtrip"})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if res.IsError {
		t.Fatalf("write errored: %s", res.Output)
	}
	id := strings.TrimSpace(res.Output)
	if id == "" {
		t.Fatal("write returned empty id")
	}
	got, err := (&artifactReadTool{}).Run(ctx, nil, map[string]any{"id": id})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.Output != "alpha\nbeta\ngamma" {
		t.Fatalf("read mismatch: %q", got.Output)
	}
}

func TestArtifactWriteRejectsEmptyBody(t *testing.T) {
	res, err := (&artifactWriteTool{}).Run(context.Background(), nil, map[string]any{"body": ""})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected error for empty body, got %q", res.Output)
	}
}

func TestArtifactReadUnknownID(t *testing.T) {
	res, err := (&artifactReadTool{}).Run(context.Background(), nil, map[string]any{"id": "artifact-does-not-exist"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected error for unknown id, got %q", res.Output)
	}
}

func TestEnvListRedactsSensitiveNames(t *testing.T) {
	t.Setenv("TERMIXGO_TEST_API_KEY", "supersecretvalue123")
	t.Setenv("TERMIXGO_TEST_PLAIN", "visiblevalue")
	res, err := (&envListTool{}).Run(context.Background(), &Env{}, map[string]any{"prefix": "TERMIXGO_TEST_"})
	if err != nil {
		t.Fatalf("env_list: %v", err)
	}
	if !strings.Contains(res.Output, "TERMIXGO_TEST_API_KEY=[REDACTED]") {
		t.Fatalf("sensitive var not redacted:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "TERMIXGO_TEST_PLAIN=visiblevalue") {
		t.Fatalf("plain var missing:\n%s", res.Output)
	}
	if strings.Contains(res.Output, "supersecretvalue123") {
		t.Fatalf("secret value leaked:\n%s", res.Output)
	}
}
