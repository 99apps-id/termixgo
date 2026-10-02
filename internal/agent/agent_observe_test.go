package agent

import (
	"encoding/json"
	"testing"
)

// TestObserveToolResultTracksApplyPatch keeps verify-on-stop honest when the
// model edits code through the batch patch tool instead of one-file edits.
func TestObserveToolResultTracksApplyPatch(t *testing.T) {
	runner := &Runner{}
	ledger := NewVerifyLedger()
	patch := "*** Begin Patch\n" +
		"*** Update File: main.go\n" +
		"@@\n" +
		"-func main() {}\n" +
		"+func main() { run() }\n" +
		"*** Add File: README.md\n" +
		"@@\n" +
		"+docs only\n" +
		"*** End Patch\n"
	rawArgs := "{" + "\"patch\":" + quoteJSONString(t, patch) + "}"

	runner.observeToolResult(&ledger, nil, "apply_patch", rawArgs, Result{Output: "Applied"})

	if !ledger.ShouldNudgeVerification(false) {
		t.Fatalf("a code patch with no verification should nudge")
	}
	if len(ledger.ChangedCodePaths) != 1 || ledger.ChangedCodePaths[0] != "main.go" {
		t.Fatalf("changed paths = %v, want [main.go]", ledger.ChangedCodePaths)
	}
	if ledger.VerifiedAfterLastEdit {
		t.Fatalf("a patch edit must leave verification debt")
	}
}

func quoteJSONString(t *testing.T, text string) string {
	t.Helper()
	data, err := json.Marshal(text)
	if err != nil {
		t.Fatalf("marshal patch: %v", err)
	}
	return string(data)
}
