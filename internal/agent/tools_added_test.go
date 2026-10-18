package agent

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/search"
)

// TestGitHubToolsValidateArguments keeps a bad call from reaching the network:
// every case below fails before gh is executed.
func TestGitHubToolsValidateArguments(t *testing.T) {
	env := testEnv(t)
	ctx := context.Background()
	cases := []struct {
		name string
		tool Tool
		args map[string]any
	}{
		{"github_create_pr", &githubCreatePRTool{}, map[string]any{}},
		{"github_get_pr", &githubGetPRTool{}, map[string]any{"number": 0}},
		{"github_comment_pr", &githubCommentPRTool{}, map[string]any{"number": 1, "body": "   "}},
		{"github_review_pr", &githubReviewPRTool{}, map[string]any{"number": 1, "state": "maybe", "body": "x"}},
		{"github_merge_pr", &githubMergePRTool{}, map[string]any{"number": 0}},
	}
	for _, tc := range cases {
		result, err := tc.tool.Run(ctx, env, tc.args)
		if err != nil {
			t.Fatalf("%s: unexpected Go error: %v", tc.name, err)
		}
		if !result.IsError {
			t.Errorf("%s should reject %v, got %q", tc.name, tc.args, result.Output)
		}
	}
}

func TestReadImageAttachesTheImage(t *testing.T) {
	env := testEnv(t)
	raw := []byte("PNGDATA-not-a-real-image")
	if err := os.WriteFile(filepath.Join(env.Workspace, "shot.png"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := (&readImageTool{}).Run(context.Background(), env, map[string]any{"path": "shot.png"})
	if err != nil || result.IsError {
		t.Fatalf("Run: err=%v result=%+v", err, result)
	}
	if len(result.Images) != 1 {
		t.Fatalf("want one attached image, got %d", len(result.Images))
	}
	if result.Images[0].MediaType != "image/png" {
		t.Errorf("media type = %q", result.Images[0].MediaType)
	}
	if result.Images[0].Data != base64.StdEncoding.EncodeToString(raw) {
		t.Errorf("image data is not the file bytes")
	}

	if bad, _ := (&readImageTool{}).Run(context.Background(), env, map[string]any{"path": "notes.txt"}); !bad.IsError {
		t.Errorf("a non-image path must be refused")
	}
	if escape, _ := (&readImageTool{}).Run(context.Background(), env, map[string]any{"path": "../escape.png"}); !escape.IsError {
		t.Errorf("a path outside the workspace must be refused")
	}
}

// TestExecuteCancelStopsTheProcessTree is the stuck-/stop guard: killing only
// the shell left a grandchild such as `du` holding the output pipe, so Wait
// never returned and a stopped turn stayed in progress. On POSIX the command is
// grouped and the whole group is killed, so a cancel returns at once.
func TestExecuteCancelStopsTheProcessTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the process-group assertion is POSIX only")
	}
	env := testEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan Result, 1)
	go func() {
		result, _ := execute(ctx, env, "sleep 30 & echo started; wait", env.Workspace, 60*time.Second)
		done <- result
	}()

	time.Sleep(500 * time.Millisecond)
	cancel()

	select {
	case result := <-done:
		if !result.IsError || !strings.Contains(result.Output, "cancelled") {
			t.Errorf("result = %+v, want a cancellation", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("execute did not return after cancel; the process tree was not killed")
	}
}

// TestSearchMemoryPathScopesResults covers the operator ask: a search over a
// large workspace can be limited to the folder being audited.
func TestSearchMemoryPathScopesResults(t *testing.T) {
	store, err := search.Open(t.TempDir())
	if err != nil {
		t.Fatalf("search.Open: %v", err)
	}
	defer store.Close()
	_ = store.Index("workspace", "termixgo/internal/ui/render.go", "render.go", "the widget renders the frame")
	_ = store.Index("workspace", "other/render.go", "render.go", "the widget renders the frame")

	env := testEnv(t)
	env.Search = store

	result, err := (&searchMemoryTool{}).Run(context.Background(), env, map[string]any{"query": "widget", "path": "termixgo"})
	if err != nil || result.IsError {
		t.Fatalf("Run: err=%v result=%+v", err, result)
	}
	if !strings.Contains(result.Output, "termixgo/internal/ui/render.go") {
		t.Errorf("the scoped file is missing:\n%s", result.Output)
	}
	if strings.Contains(result.Output, "other/render.go") {
		t.Errorf("a file outside the path filter was returned:\n%s", result.Output)
	}
}

func TestInterpolatePrompt(t *testing.T) {
	context := map[string]any{
		"a": "hello",
		"b": map[string]any{"output": "world"},
		"n": float64(3),
	}
	got := interpolatePrompt("{{a}} {{b.output}} {{n}} {{missing}}", context)
	if got != "hello world 3 {{missing}}" {
		t.Errorf("interpolate = %q", got)
	}
}

func TestLoadPipelineRejectsUnsafeAndMissing(t *testing.T) {
	env := testEnv(t)
	if _, err := loadPipeline(env.Workspace, "../secret"); err == nil {
		t.Errorf("a traversal id must be refused")
	}
	if _, err := loadPipeline(env.Workspace, "missing"); err == nil {
		t.Errorf("a missing pipeline must be an error")
	}

	dir := filepath.Join(env.Workspace, ".termixgo", "pipelines")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"id":"check","name":"Check","steps":[{"id":"a","prompt":"look around"}]}`
	if err := os.WriteFile(filepath.Join(dir, "check.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	pipeline, err := loadPipeline(env.Workspace, "check")
	if err != nil {
		t.Fatalf("loadPipeline: %v", err)
	}
	if pipeline.Name != "Check" || len(pipeline.Steps) != 1 {
		t.Errorf("pipeline = %+v", pipeline)
	}
	if pipelines := listPipelines(env.Workspace); len(pipelines) != 1 || pipelines[0].ID != "check" {
		t.Errorf("listPipelines = %+v", pipelines)
	}
}

func TestRunPipelineRespectsDependenciesAndFailures(t *testing.T) {
	env := testEnv(t)
	var ran []string
	env.RunSubagent = func(ctx context.Context, subType, prompt string) (string, SubagentSpend, error) {
		ran = append(ran, prompt)
		if strings.Contains(prompt, "boom") {
			return "", SubagentSpend{}, errors.New("step failed")
		}
		return "out:" + prompt, SubagentSpend{}, nil
	}

	pipeline := OrchestrationPipeline{ID: "p", Steps: []OrchestrationStep{
		{ID: "a", Prompt: "first"},
		{ID: "b", Prompt: "uses {{a}}", DependsOn: []string{"a"}},
	}}
	result := runPipeline(context.Background(), env, pipeline, nil)
	if len(result.Completed) != 2 || len(result.Failed) != 0 {
		t.Fatalf("result = %+v", result)
	}
	if result.Results["b"] != "out:uses out:first" {
		t.Errorf("dependency output not interpolated: %v", result.Results["b"])
	}

	// A failed step skips everything that depends on it.
	ran = nil
	failing := OrchestrationPipeline{ID: "p2", Steps: []OrchestrationStep{
		{ID: "x", Prompt: "boom"},
		{ID: "y", Prompt: "after {{x}}", DependsOn: []string{"x"}},
	}}
	failure := runPipeline(context.Background(), env, failing, nil)
	if len(failure.Failed) != 1 || len(failure.Skipped) != 1 || len(failure.Completed) != 0 {
		t.Errorf("failure result = %+v", failure)
	}
	for _, prompt := range ran {
		if strings.Contains(prompt, "after") {
			t.Errorf("a dependent step must not run after its dependency failed")
		}
	}
}

func TestSearchToolIndexAndSuggestions(t *testing.T) {
	index := []ToolIndexEntry{
		{Name: "github_create_pr", Summary: "Create a pull request."},
		{Name: "read_image", Summary: "Read a local image."},
		{Name: "orchestrate", Summary: "Run a pipeline."},
	}
	if got := searchToolIndex(index, "image", 8); len(got) != 1 || got[0].Name != "read_image" {
		t.Errorf("search image = %+v", got)
	}
	if got := searchToolIndex(index, "github", 8); len(got) != 1 || got[0].Name != "github_create_pr" {
		t.Errorf("search github = %+v", got)
	}
	if got := searchToolIndex(index, "zzz", 8); len(got) != 0 {
		t.Errorf("a miss should return nothing, got %+v", got)
	}

	suggestions := suggestToolNames("read_img", []string{"read_file", "read_image", "grep"}, 5)
	if len(suggestions) == 0 || suggestions[0] != "read_image" {
		t.Errorf("suggestions = %v", suggestions)
	}
}

func TestUnknownToolMessageNamesTheFix(t *testing.T) {
	message := unknownToolMessage("view_file", []string{"read_file", "grep", "find_tools"}, true)
	for _, want := range []string{`"view_file"`, "Did you mean", "read_file", "find_tools"} {
		if !strings.Contains(message, want) {
			t.Errorf("message is missing %q:\n%s", want, message)
		}
	}
	withoutSearch := unknownToolMessage("view_file", []string{"read_file"}, false)
	if strings.Contains(withoutSearch, "find_tools") {
		t.Errorf("tool search is off, so find_tools should not be mentioned")
	}
}

func TestFindToolsLoadsMatches(t *testing.T) {
	env := testEnv(t)
	env.ToolIndex = []ToolIndexEntry{
		{Name: "github_create_pr", Summary: "Create a pull request."},
		{Name: "read_image", Summary: "Read a local image."},
	}
	var discovered []string
	env.DiscoverTools = func(names []string) { discovered = append(discovered, names...) }

	result, err := (&findToolsTool{}).Run(context.Background(), env, map[string]any{"query": "github"})
	if err != nil || result.IsError {
		t.Fatalf("Run: err=%v result=%+v", err, result)
	}
	if !strings.Contains(result.Output, "github_create_pr") {
		t.Errorf("output = %q", result.Output)
	}
	if len(discovered) != 1 || discovered[0] != "github_create_pr" {
		t.Errorf("discovered = %v", discovered)
	}

	if empty, _ := (&findToolsTool{}).Run(context.Background(), env, map[string]any{}); !empty.IsError {
		t.Errorf("an empty query must be an error")
	}
}

func TestStepToolsLoadsOnDemand(t *testing.T) {
	full := DefaultRegistry()
	on := &Runner{Tools: full, ToolSearch: true}

	visible := on.stepTools(nil)
	if _, ok := visible.Lookup("read_file"); !ok {
		t.Errorf("the core loop must stay visible")
	}
	if _, ok := visible.Lookup("find_tools"); !ok {
		t.Errorf("find_tools must stay visible")
	}
	if _, ok := visible.Lookup("github_create_pr"); ok {
		t.Errorf("an ecosystem tool must be deferred until discovered")
	}
	if _, ok := on.stepTools(map[string]bool{"github_create_pr": true}).Lookup("github_create_pr"); !ok {
		t.Errorf("a discovered tool must become visible")
	}

	off := &Runner{Tools: full}
	if _, ok := off.stepTools(nil).Lookup("github_create_pr"); !ok {
		t.Errorf("with search off every tool is visible")
	}
}
