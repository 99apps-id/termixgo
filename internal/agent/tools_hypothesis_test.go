package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHypothesisVerifyWithFileInspection(t *testing.T) {
	env := testEnv(t)
	filePath := filepath.Join(env.Workspace, "sample.txt")
	if err := os.WriteFile(filePath, []byte("export const TIMEOUT = 5000;\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := &hypothesisVerifyTool{}

	// Case 1: Pattern confirmed
	res, err := tool.Run(context.Background(), env, map[string]any{
		"hypothesis": "TIMEOUT constant is configured as 5000",
		"file_path":  "sample.txt",
		"pattern":    "TIMEOUT = 5000",
	})
	if err != nil || res.IsError {
		t.Fatalf("hypothesis_verify failed: %v, out: %s", err, res.Output)
	}
	if !strings.Contains(res.Output, "Verdict:           CONFIRMED") {
		t.Errorf("expected CONFIRMED verdict, got:\n%s", res.Output)
	}

	// Case 2: Pattern refuted
	res, err = tool.Run(context.Background(), env, map[string]any{
		"hypothesis": "TIMEOUT constant is configured as 9999",
		"file_path":  "sample.txt",
		"pattern":    "TIMEOUT = 9999",
	})
	if err != nil || res.IsError {
		t.Fatalf("hypothesis_verify failed: %v, out: %s", err, res.Output)
	}
	if !strings.Contains(res.Output, "Verdict:           REFUTED") {
		t.Errorf("expected REFUTED verdict, got:\n%s", res.Output)
	}
}

func TestHypothesisVerifyRequiresEvidenceSource(t *testing.T) {
	env := testEnv(t)
	tool := &hypothesisVerifyTool{}

	res, _ := tool.Run(context.Background(), env, map[string]any{
		"hypothesis": "Something broke",
	})
	if !res.IsError {
		t.Errorf("expected error when neither command nor file_path is provided")
	}
}
