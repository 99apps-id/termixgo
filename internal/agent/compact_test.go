package agent

import (
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/provider"
	"github.com/99apps-id/termixgo/internal/skill"
)

func TestCompactKeepsTailAndNeverStartsWithToolResult(t *testing.T) {
	messages := []provider.Message{
		{Role: provider.RoleUser, Content: strings.Repeat("u", 4000)},
		{Role: provider.RoleAssistant, Content: strings.Repeat("a", 4000)},
	}
	for index := 0; index < 30; index++ {
		messages = append(messages,
			provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "1", Name: "read_file", Arguments: "{}"}}},
			provider.Message{Role: provider.RoleTool, ToolID: "1", Name: "read_file", Content: strings.Repeat("t", 4000)},
		)
	}
	budget := 2000
	before := EstimateMessages(messages)
	if before <= budget {
		t.Fatalf("the fixture must exceed the budget, got %d", before)
	}

	trimmed := Compact(messages, budget)
	if EstimateMessages(trimmed) >= before {
		t.Fatalf("compaction did not reduce the conversation: %d -> %d", before, EstimateMessages(trimmed))
	}
	if len(trimmed) > 0 && trimmed[0].Role == provider.RoleTool {
		t.Fatalf("a compacted conversation must not start with a tool result")
	}
	// The newest message is preserved verbatim.
	if trimmed[len(trimmed)-1].Content != messages[len(messages)-1].Content {
		t.Errorf("the newest message must be kept intact")
	}
}

// TestCompactNeverLeavesAToolResultAtTheHead covers the shape the floor used
// to break: one assistant turn that asked for several tool calls. Dropping the
// user message leaves [assistant, tool, tool, tool, tool], and a drop that
// stops on the floor instead of on a legal head hands the provider a request
// that starts with a tool result, which it rejects outright.
func TestCompactNeverLeavesAToolResultAtTheHead(t *testing.T) {
	tool := func() provider.Message {
		return provider.Message{Role: provider.RoleTool, ToolID: "1", Name: "read_file", Content: strings.Repeat("t", 4000)}
	}
	messages := []provider.Message{
		{Role: provider.RoleUser, Content: strings.Repeat("u", 400)},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "1", Name: "read_file", Arguments: "{}"}}},
		tool(), tool(), tool(), tool(),
	}
	if EstimateMessages(messages) <= 100 {
		t.Fatal("the fixture must exceed the budget")
	}
	trimmed := Compact(messages, 100)
	if len(trimmed) == 0 {
		t.Fatal("compaction returned no messages at all")
	}
	if trimmed[0].Role == provider.RoleTool {
		t.Fatalf("compacted conversation starts with a %s message; the provider rejects that", trimmed[0].Role)
	}
}

func TestCompactIsNoOpUnderBudget(t *testing.T) {
	messages := []provider.Message{
		{Role: provider.RoleUser, Content: "hello"},
		{Role: provider.RoleAssistant, Content: "hi"},
	}
	trimmed := Compact(messages, 10000)
	if len(trimmed) != len(messages) {
		t.Fatalf("under budget nothing should be dropped")
	}
	if trimmed[0].Content != "hello" {
		t.Errorf("content should be untouched, got %q", trimmed[0].Content)
	}
}

func TestElidePreservesRoles(t *testing.T) {
	message := provider.Message{
		Role:      provider.RoleAssistant,
		Content:   strings.Repeat("x", 2000),
		Reasoning: strings.Repeat("r", 2000),
		ToolCalls: []provider.ToolCall{{Name: "read_file", Arguments: strings.Repeat("a", 500)}},
	}
	elided := elide(message)
	if elided.Role != provider.RoleAssistant {
		t.Errorf("the role must survive elision")
	}
	if len(elided.Reasoning) != 0 {
		t.Errorf("reasoning should be dropped to save context")
	}
	if elided.ToolCalls[0].Arguments != "{}" {
		t.Errorf("tool arguments should be collapsed, got %q", elided.ToolCalls[0].Arguments)
	}
	if len(elided.Content) > elidedTextLimit+32 {
		t.Errorf("text should be clipped, got %d chars", len(elided.Content))
	}
}

// TestElideDoesNotMutateTheStoredMessage pins the aliasing contract: a Message
// shares its ToolCalls slice with the session, so eliding one for a trimmed
// request must not blank the arguments of the stored history. Writing through
// the shared array would lose those arguments from the session file too.
func TestElideDoesNotMutateTheStoredMessage(t *testing.T) {
	original := provider.Message{
		Role:      provider.RoleAssistant,
		Content:   strings.Repeat("x", 2000),
		ToolCalls: []provider.ToolCall{{ID: "1", Name: "read_file", Arguments: `{"path":"main.go"}`}},
	}
	messages := []provider.Message{
		{Role: provider.RoleUser, Content: "hello"},
		original,
	}
	elided := elide(messages[1])
	if elided.ToolCalls[0].Arguments != "{}" {
		t.Fatalf("the elided copy should collapse its arguments, got %q", elided.ToolCalls[0].Arguments)
	}
	if got := messages[1].ToolCalls[0].Arguments; got != `{"path":"main.go"}` {
		t.Fatalf("the stored message was mutated: arguments = %q, want the original JSON", got)
	}

	// The whole compact path must preserve the source as well, since it is the
	// session's own message list that is passed in.
	session := NewSession("ws", "model")
	session.AddUser("hello")
	session.AddAssistant("calling", "", []provider.ToolCall{{ID: "1", Name: "read_file", Arguments: `{"path":"main.go"}`}})
	session.AddToolResult("1", "read_file", "content")
	for index := 0; index < 30; index++ {
		session.AddUser("filler message with enough text to consume the token budget")
		session.AddAssistant("acknowledged", "", nil)
	}
	_ = Compact(session.Messages(), 200)
	if got := session.Messages()[1].ToolCalls[0].Arguments; got != `{"path":"main.go"}` {
		t.Fatalf("Compact mutated the session history: arguments = %q", got)
	}
}

func TestHistoryHint(t *testing.T) {
	messages := []provider.Message{{Role: provider.RoleUser, Content: strings.Repeat("w", 520)}}
	hint := HistoryHint(messages, 1000)
	if !strings.Contains(hint, "tokens") || !strings.Contains(hint, "%") {
		t.Errorf("the hint should mention tokens and a percentage, got %q", hint)
	}
}

func TestFormatEnvironmentBlock(t *testing.T) {
	block := FormatEnvironmentBlock("/work", TrustLabel(true))
	if !strings.HasPrefix(block, "<env>") || !strings.HasSuffix(block, "</env>") {
		t.Fatalf("the env block must be tagged: %q", block)
	}
	if !strings.Contains(block, "workspace_root: /work") || !strings.Contains(block, "folder_trust: trusted") {
		t.Errorf("the env block is missing fields: %q", block)
	}
}

func TestApprovalPolicyModes(t *testing.T) {
	edit := &writeFileTool{}
	command := &runCommandTool{}
	read := &readFileTool{}

	cases := []struct {
		mode         ApprovalMode
		tool         Tool
		wantApproval bool
	}{
		{ApprovalAll, edit, false},
		{ApprovalAll, command, false},
		{ApprovalAsk, edit, true},
		{ApprovalAsk, command, true},
		{ApprovalAsk, read, false},
		{ApprovalEdits, edit, false},
		{ApprovalEdits, command, true},
	}
	for _, testCase := range cases {
		policy := ApprovalPolicy{Mode: testCase.mode}
		if got := policy.NeedsApproval(testCase.tool); got != testCase.wantApproval {
			t.Errorf("mode %s tool %s: NeedsApproval = %v, want %v", testCase.mode, testCase.tool.Name(), got, testCase.wantApproval)
		}
	}
}

func TestApprovalPolicySessionAllowance(t *testing.T) {
	policy := &ApprovalPolicy{Mode: ApprovalAsk}
	tool := &writeFileTool{}
	if !policy.NeedsApproval(tool) {
		t.Fatalf("ask mode should require approval first")
	}
	policy.AllowSession(tool.Name())
	if policy.NeedsApproval(tool) {
		t.Fatalf("a session allowance should skip approval")
	}
}

func TestBuildSystemIncludesWorkspaceAndTrust(t *testing.T) {
	env := testEnv(t)
	system := BuildSystem(env, "claude-sonnet-4-5")
	if !strings.Contains(system, env.Workspace) {
		t.Errorf("the system prompt must name the workspace")
	}
	if !strings.Contains(system, "trusted") {
		t.Errorf("the system prompt must state the trust state")
	}
	if !strings.Contains(system, "claude-sonnet-4-5") {
		t.Errorf("the system prompt must name the model")
	}
}

func TestBuildSystemIncludesSkillsAndPlan(t *testing.T) {
	env := testEnv(t)
	env.Skills = []skill.Skill{{Name: "review", Description: "Review a change set.", Scope: "project"}}
	if err := env.Todos.Write([]Todo{{Title: "Fix the parser", Status: "in_progress"}}); err != nil {
		t.Fatal(err)
	}
	system := BuildSystem(env, "gpt-5.4-mini")
	if !strings.Contains(system, "## SKILLS") || !strings.Contains(system, "review") {
		t.Errorf("the system prompt must list skills, got:\n%s", system)
	}
	if !strings.Contains(system, "## CURRENT PLAN") || !strings.Contains(system, "Fix the parser") {
		t.Errorf("the system prompt must carry the plan, got:\n%s", system)
	}
}
