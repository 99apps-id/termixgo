package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/provider"
)

// TestApplyPatchWritesNothingWhenALaterOperationFails is the atomicity rule.
//
// Applying as it went left the earlier files changed and reported only an
// error, so the model could not tell that half the patch had landed. Nothing
// may be written until every operation is known to apply.
func TestApplyPatchWritesNothingWhenALaterOperationFails(t *testing.T) {
	env, workspace := patchEnv(t)
	first := filepath.Join(workspace, "a.go")
	second := filepath.Join(workspace, "b.go")
	if err := os.WriteFile(first, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("package b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The first operation applies. The second cannot, and nothing must land.
	document := "*** Begin Patch\n" +
		"*** Update File: a.go\n@@\n package a\n+// one\n" +
		"*** Update File: b.go\n@@\n-wrong context\n+// two\n" +
		"*** End Patch\n"
	tool := &applyPatchTool{}
	result, _ := tool.Run(context.Background(), env, map[string]any{"patch": document})
	if !result.IsError {
		t.Fatalf("a patch that cannot apply in full must fail, got %q", result.Output)
	}
	if !strings.Contains(result.Output, "nothing was written") {
		t.Errorf("the refusal should say nothing landed, got %q", result.Output)
	}
	data, _ := os.ReadFile(first)
	if strings.Contains(string(data), "// one") {
		t.Errorf("the first file was written before the patch was known to apply:\n%s", data)
	}
	data, _ = os.ReadFile(second)
	if strings.Contains(string(data), "// two") {
		t.Errorf("the failing file was written:\n%s", data)
	}
}

// TestApplyPatchPlansAReadOfAMissingFileAsAFailure keeps a patch from writing
// its earlier operations when a later one names a file that is not there.
func TestApplyPatchPlansAReadOfAMissingFileAsAFailure(t *testing.T) {
	env, workspace := patchEnv(t)
	if err := os.WriteFile(filepath.Join(workspace, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	document := "*** Begin Patch\n" +
		"*** Update File: a.go\n@@\n package a\n+// one\n" +
		"*** Update File: missing.go\n@@\n package missing\n+// two\n" +
		"*** End Patch\n"
	tool := &applyPatchTool{}
	result, _ := tool.Run(context.Background(), env, map[string]any{"patch": document})
	if !result.IsError {
		t.Fatalf("updating a missing file must fail, got %q", result.Output)
	}
	data, _ := os.ReadFile(filepath.Join(workspace, "a.go"))
	if strings.Contains(string(data), "// one") {
		t.Errorf("the first file was written before planning finished:\n%s", data)
	}
}

// TestApplyPatchChainsTwoUpdatesOfOneFile pins the reason the plan is keyed by
// path: the second operation has to read the first one's result, not the bytes
// on disk, or one file written twice loses the first half.
func TestApplyPatchChainsTwoUpdatesOfOneFile(t *testing.T) {
	env, workspace := patchEnv(t)
	path := filepath.Join(workspace, "a.go")
	if err := os.WriteFile(path, []byte("package a\n\nvar one = 1\n\nvar two = 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	document := "*** Begin Patch\n" +
		"*** Update File: a.go\n@@\n-var one = 1\n+var one = 11\n" +
		"*** Update File: a.go\n@@\n-var two = 2\n+var two = 22\n" +
		"*** End Patch\n"
	tool := &applyPatchTool{}
	result, _ := tool.Run(context.Background(), env, map[string]any{"patch": document})
	if result.IsError {
		t.Fatalf("result is an error: %q", result.Output)
	}
	data, _ := os.ReadFile(path)
	text := string(data)
	if !strings.Contains(text, "var one = 11") || !strings.Contains(text, "var two = 22") {
		t.Errorf("both hunks should land, got:\n%s", text)
	}
}

// TestApplyPatchRefusesASecondAddOfOneFile keeps a plan from hiding a mistake:
// two add operations for one path cannot both be the creation of it.
func TestApplyPatchRefusesASecondAddOfOneFile(t *testing.T) {
	env, _ := patchEnv(t)
	document := "*** Begin Patch\n" +
		"*** Add File: new.txt\n@@\n+one\n" +
		"*** Add File: new.txt\n@@\n+two\n" +
		"*** End Patch\n"
	tool := &applyPatchTool{}
	result, _ := tool.Run(context.Background(), env, map[string]any{"patch": document})
	if !result.IsError {
		t.Fatalf("adding one path twice must fail, got %q", result.Output)
	}
}

// TestCompactStoredHistoryBoundsTheTranscript is the fix for a transcript that
// grew for the life of a session: every turn appended its tool output, Save
// rewrote the whole file, and nothing ever dropped a message.
func TestCompactStoredHistoryBoundsTheTranscript(t *testing.T) {
	session := NewSession("/work", "test-model")
	big := strings.Repeat("tool output line\n", 400)
	for turn := 0; turn < 6; turn++ {
		session.AddUser("please do the thing")
		session.AddAssistant("working", "", []provider.ToolCall{
			{ID: "call_1", Name: "read_file", Arguments: `{"path":"big.go"}`},
		})
		session.AddToolResult("call_1", "read_file", big)
	}
	budget := HistoryBudget(32000)
	before := EstimateMessages(session.Messages())
	if before <= budget {
		t.Fatalf("the fixture should be over budget, got %d of %d", before, budget)
	}

	if !session.CompactStoredHistory(budget) {
		t.Fatalf("an over-budget transcript must be condensed")
	}
	if after := EstimateMessages(session.Messages()); after > budget {
		t.Errorf("stored transcript = %d tokens, want at most %d", after, budget)
	}
	if !session.Condensed() {
		t.Errorf("the session should record that it was condensed")
	}
	// The rule the request obeys applies here too: a history may not open with
	// a tool result.
	if messages := session.Messages(); len(messages) > 0 && messages[0].Role == provider.RoleTool {
		t.Errorf("the condensed transcript opens with a tool result")
	}
	// The newest turn is what the model needs to answer, so it stays readable.
	messages := session.Messages()
	last := messages[len(messages)-1]
	if last.Role != provider.RoleTool || !strings.Contains(last.Content, "tool output line") {
		t.Errorf("the newest tool result was not kept whole: %q", strings.TrimSpace(last.Content))
	}
}

// TestCompactStoredHistoryLeavesAShortSessionAlone keeps the bound from
// touching a session that fits, and from touching a session whose only turn is
// being named.
func TestCompactStoredHistoryLeavesAShortSessionAlone(t *testing.T) {
	session := NewSession("/work", "test-model")
	session.AddUser("one question")
	session.AddAssistant("one answer", "", nil)
	if session.CompactStoredHistory(10) {
		t.Errorf("a transcript under the budget must not be rewritten")
	}
	if session.Condensed() {
		t.Errorf("nothing was condensed, so the flag must stay false")
	}
	// A single turn with a huge result is left alone: there is no earlier turn
	// to condense, and naming a fresh session reads its turn count.
	single := NewSession("/work", "test-model")
	single.AddUser("one question")
	single.AddAssistant("working", "", nil)
	single.AddToolResult("call_1", "read_file", strings.Repeat("x", 40000))
	if single.CompactStoredHistory(100) {
		t.Errorf("a single-turn session must be left for naming to read")
	}
	if single.Turns() != 1 {
		t.Errorf("turns = %d, want the one turn kept", single.Turns())
	}
}

// TestSessionCondensedSurvivesTheRoundTrip keeps the one-time notice from
// repeating after a resume.
func TestSessionCondensedSurvivesTheRoundTrip(t *testing.T) {
	home := withState(t)
	session := NewSession("/work", "test-model")
	session.AddUser("question")
	session.AddAssistant("answer", "", nil)
	session.condensed = true
	id := session.ID()
	if err := session.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := LoadSession(id)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if !loaded.Condensed() {
		t.Errorf("the condensed flag did not survive the round trip")
	}
	if _, err := os.Stat(filepath.Join(home, "sessions", id+".json")); err != nil {
		t.Fatalf("the session file is missing: %v", err)
	}
}
