package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestShouldDeferToolsCoversDefaultRegistry pins the shared defer rule the
// subagent path uses: a registry the size of the built-in one must not send
// every schema inline on every step.
func TestShouldDeferToolsCoversDefaultRegistry(t *testing.T) {
	count := len(DefaultRegistry().Tools())
	if !ShouldDeferTools(count) {
		t.Fatalf("registry of %d tools must defer to tool search", count)
	}
	if ShouldDeferTools(MaxInlineTools) {
		t.Fatalf("%d tools still fit inline", MaxInlineTools)
	}
	if !ShouldDeferTools(MaxInlineTools + 1) {
		t.Fatalf("%d tools must defer to tool search", MaxInlineTools+1)
	}
}

// TestDeferredStepSchemasCostHalfOfFull pins the saving the defer rule buys:
// the visible set for a step must cost less than half the full catalogue.
func TestDeferredStepSchemasCostHalfOfFull(t *testing.T) {
	full := EstimateToolSchemas(DefaultRegistry().Definitions())
	visible := (&Runner{Tools: DefaultRegistry(), ToolSearch: true}).stepTools(nil)
	deferred := EstimateToolSchemas(visible.Definitions())
	if deferred*2 >= full {
		t.Fatalf("deferred schemas = %d tokens, want less than half of full %d", deferred, full)
	}
}

// TestReadFileOutputBoundedTo32KB pins the smaller read cap: one read must
// not flood the context that every later step resends.
func TestReadFileOutputBoundedTo32KB(t *testing.T) {
	env := testEnv(t)
	huge := strings.Repeat("x", 96*1024)
	if err := os.WriteFile(filepath.Join(env.Workspace, "big.txt"), []byte(huge), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	result, err := (&readFileTool{}).Run(context.Background(), env, map[string]any{"path": "big.txt"})
	if err != nil || result.IsError {
		t.Fatalf("Run: err=%v result=%+v", err, result)
	}
	if len(result.Output) > 32*1024+512 {
		t.Errorf("read output is %d bytes, want bounded near 32 KB; use offset and limit to page", len(result.Output))
	}
}
