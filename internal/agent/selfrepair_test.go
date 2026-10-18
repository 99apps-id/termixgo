package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/provider"
)

func TestCanonicalToolNameFoldsSpellings(t *testing.T) {
	cases := map[string]string{
		"ReadFile":           "read_file",
		"gitDiff":            "git_diff",
		"multi-edit":         "multi_edit",
		"list directory":     "list_directory",
		"run.Command":        "run_command",
		"GetTerminalOutput":  "get_terminal_output",
		"read_file":          "read_file",
		"TOOL":               "tool",
		"http2Server":        "http2_server",
		"already_canonical1": "already_canonical1",
	}
	for input, want := range cases {
		if got := canonicalToolName(input); got != want {
			t.Errorf("canonicalToolName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRegistryLookupFoldsCamelAndHyphenSpellings(t *testing.T) {
	registry := DefaultRegistry()
	folded := map[string]string{
		"ReadFile":       "read_file",
		"gitDiff":        "git_diff",
		"multi-edit":     "multi_edit",
		"runCommand":     "run_command",
		"GIT_STATUS":     "git_status",
		"Review Changes": "review_changes",
	}
	for input, wantName := range folded {
		tool, ok := registry.Lookup(input)
		if !ok {
			t.Errorf("Lookup(%q) missed; the fold should resolve it", input)
			continue
		}
		if tool.Name() != wantName {
			t.Errorf("Lookup(%q) resolved %q, want %q", input, tool.Name(), wantName)
		}
	}
	if _, ok := registry.Lookup("totally_bogus_tool"); ok {
		t.Error("a bogus name must stay unknown so the did-you-mean path still fires")
	}
}

func TestGitDiffArgvShapes(t *testing.T) {
	legacy, err := gitDiffArgv(true, false, "", nil)
	if err != nil || strings.Join(legacy, " ") != "diff --staged" {
		t.Errorf("no base must keep the old argv, got %v (%v)", legacy, err)
	}
	based, err := gitDiffArgv(false, true, "14eb149d", []string{"src/x.go"})
	if err != nil {
		t.Fatalf("valid base rejected: %v", err)
	}
	if strings.Join(based, " ") != "diff --stat --end-of-options 14eb149d -- src/x.go" {
		t.Errorf("argv = %v, want rev behind --end-of-options and paths behind --", based)
	}
	for _, bad := range []string{"-b", "--force", "a b", ""} {
		if bad == "" {
			continue
		}
		if _, err := gitDiffArgv(false, false, bad, nil); err == nil {
			t.Errorf("base %q must be refused", bad)
		}
	}
}

func TestBuildReviewPrompt(t *testing.T) {
	soft := buildReviewPrompt("hello diff", false)
	if !strings.Contains(soft, "```diff\nhello diff\n```") {
		t.Error("the diff must be embedded")
	}
	if !strings.Contains(soft, "read_file") {
		t.Error("the prompt must tell the reviewer to open files anyway")
	}
	if strings.Contains(soft, "previous pass") {
		t.Error("only the hard mandate mentions the failed pass")
	}
	hard := buildReviewPrompt("d", true)
	if !strings.Contains(hard, "MUST open the changed files") {
		t.Error("the hard prompt must mandate inspection")
	}
}

func TestReviewChangesNeedsSubagentRunner(t *testing.T) {
	env := testEnv(t)
	env.RunSubagent = nil
	result, err := (&reviewChangesTool{}).Run(context.Background(), env, map[string]any{})
	if err != nil || !result.IsError {
		t.Fatalf("without a subagent runner the review must fail loudly, got %+v err %v", result, err)
	}
}

func TestReviewRoleDiscardsTwoEmptyPasses(t *testing.T) {
	// One scripted step only: the first pass answers "Looks good." having
	// opened nothing, the mandated retry gets an exhausted fake and also
	// opens nothing. Two empty passes must be an error, never an approval.
	client := &fakeClient{steps: [][]provider.StreamEvent{
		{textChunk("Looks good.")},
	}}
	parent := testEnv(t)
	parent.Config = config.Default()
	_, _, err := RunSubagent(context.Background(), parent, client, provider.Model{ID: "test-model"}, string(SubagentCodeReview), "review it", 4)
	if err == nil || !strings.Contains(err.Error(), "without a single tool call") {
		t.Fatalf("want the empty-pass discard error, got %v", err)
	}
	// Two attempts minimum; the provider loop may re-ask an empty stream
	// inside one attempt, so the count asserts the retry happened, not how
	// many provider calls each attempt spent.
	if client.calls < 2 {
		t.Errorf("the judge must retry, Stream calls = %d", client.calls)
	}
}

func TestReviewRoleAcceptsTheInspectedRetry(t *testing.T) {
	client := &fakeClient{steps: [][]provider.StreamEvent{
		{textChunk("Looks good.")},
		{callChunk("c1", "list_directory", `{"path":"."}`), textChunk("[MUST] - judge untested -> add the test")},
	}}
	parent := testEnv(t)
	parent.Config = config.Default()
	answer, _, err := RunSubagent(context.Background(), parent, client, provider.Model{ID: "test-model"}, string(SubagentCodeReview), "review it", 4)
	if err != nil {
		t.Fatalf("a retry that inspects must be accepted: %v", err)
	}
	if !strings.Contains(answer, "[MUST]") {
		t.Errorf("answer = %q, want the second pass report", answer)
	}
}

func TestWorkerRoleIsNotJudgedForEmptyPasses(t *testing.T) {
	// A general task can be legitimately answered from the prompt alone;
	// the judge guards read-tier roles, not every delegation.
	client := &fakeClient{steps: [][]provider.StreamEvent{
		{textChunk("The answer is 42.")},
	}}
	parent := testEnv(t)
	parent.Config = config.Default()
	answer, _, err := RunSubagent(context.Background(), parent, client, provider.Model{ID: "test-model"}, string(SubagentGeneral), "compute", 4)
	if err != nil || !strings.Contains(answer, "42") {
		t.Fatalf("worker role must pass through: answer=%q err=%v", answer, err)
	}
	if client.calls != 1 {
		t.Errorf("no retry for a worker, Stream calls = %d", client.calls)
	}
}

func TestPentestAndVisionRolesResolve(t *testing.T) {
	for _, name := range []string{"pentest", "pentest-recon", "pentest-web", "pentest-network", "vision"} {
		def := LookupSubagent(name)
		if string(def.Type) != name {
			t.Errorf("LookupSubagent(%q) fell back to %q", name, def.Type)
		}
		if def.SystemPrompt == "" {
			t.Errorf("%s has no system prompt", name)
		}
	}
	if !SubagentIsReadOnly("pentest-recon") {
		t.Error("recon is enumeration-only, it must stay read-tier")
	}
	if SubagentIsReadOnly("pentest") {
		t.Error("pentest runs scanners through the approval policy, it is not read-tier")
	}
	// The read-tier registry must actually carry the network tools recon needs.
	registry := subagentRegistry("pentest-recon", 0)
	for _, name := range []string{"web_fetch", "probe_url", "web_search"} {
		if _, ok := registry.Lookup(name); !ok {
			t.Errorf("read-tier registry lacks %s, recon cannot map a surface without it", name)
		}
	}
	if _, ok := registry.Lookup("run_command"); ok {
		t.Error("a read-tier role must not reach run_command")
	}
}
