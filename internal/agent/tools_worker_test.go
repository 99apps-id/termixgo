package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// echoArgv is a trivial worker that needs no external coding CLI.
func echoArgv(text string) []string {
	if runtime.GOOS == "windows" {
		return []string{"cmd", "/c", "echo", text}
	}
	return []string{"sh", "-c", "echo " + text}
}

func TestBuildWorkerArgv(t *testing.T) {
	cases := map[string]string{
		"":         "claude",
		"claude":   "claude",
		"codex":    "codex",
		"opencode": "opencode",
	}
	for kind, want := range cases {
		argv, err := buildWorkerArgv(kind, "do it")
		if err != nil {
			t.Fatalf("buildWorkerArgv(%q): %v", kind, err)
		}
		if argv[0] != want {
			t.Errorf("buildWorkerArgv(%q) = %v, want binary %q", kind, argv, want)
		}
		if argv[len(argv)-1] != "do it" {
			t.Errorf("the task should be the last argument, got %v", argv)
		}
	}
	if _, err := buildWorkerArgv("bogus", "x"); err == nil {
		t.Errorf("an unknown worker should fail")
	}
	if _, err := buildWorkerArgv("claude", "  "); err == nil {
		t.Errorf("an empty task should fail")
	}

	// The native worker runs this same binary; the path depends on the build,
	// so assert the shape rather than the exact file.
	native, err := buildWorkerArgv("termixgo", "do it")
	if err != nil {
		t.Fatalf("buildWorkerArgv(termixgo): %v", err)
	}
	if len(native) != 3 || native[1] != "run" || native[2] != "do it" {
		t.Errorf("native worker argv = %v, want [<exe> run <task>]", native)
	}
}

func TestCodeWorkerRefusesToNest(t *testing.T) {
	workspace := worktreeRepo(t)
	t.Setenv(workerDepthEnv, "1")
	env := &Env{Workspace: workspace, Todos: NewTodoStore(), Memory: NewMemory(workspace), Trusted: true, Processes: NewProcessManager()}
	result, _ := (&codeWorkerTool{}).Run(context.Background(), env, map[string]any{"task": "x", "worker": "termixgo"})
	if !result.IsError {
		t.Errorf("a nested worker must be refused")
	}
}

func TestStartWorkerAnnouncesCompletion(t *testing.T) {
	manager := NewProcessManager()
	events := make(chan Event, 16)
	manager.SetEmitter(func(event Event) { events <- event })

	process, err := manager.StartWorker(context.Background(), t.TempDir(), echoArgv("done"), "test worker", true)
	if err != nil {
		t.Fatalf("StartWorker: %v", err)
	}
	if !process.Wait(10 * time.Second) {
		t.Fatalf("the worker did not finish")
	}

	deadline := time.After(5 * time.Second)
	for {
		select {
		case event := <-events:
			if event.Kind != EventProcessEnd {
				continue
			}
			if event.ToolName != process.ID {
				t.Errorf("event handle = %q, want %q", event.ToolName, process.ID)
			}
			if !event.ToolOK || !strings.Contains(event.Text, "test worker") {
				t.Errorf("completion event = %+v", event)
			}
			return
		case <-deadline:
			t.Fatal("no completion event arrived")
		}
	}
}

func TestCodeWorkerStartsInAWorktree(t *testing.T) {
	workspace := worktreeRepo(t)
	manager := NewProcessManager()
	events := make(chan Event, 32)
	manager.SetEmitter(func(event Event) { events <- event })
	env := &Env{Workspace: workspace, Todos: NewTodoStore(), Memory: NewMemory(workspace), Trusted: true, Processes: manager}

	original := codeWorkerArgv
	codeWorkerArgv = func(_, _ string) ([]string, error) { return echoArgv("worked"), nil }
	defer func() { codeWorkerArgv = original }()

	result, err := (&codeWorkerTool{}).Run(context.Background(), env, map[string]any{
		"task": "do the thing", "worker": "claude", "name": "demo",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.IsError {
		t.Fatalf("the worker is an error: %q", result.Output)
	}
	dir := filepath.Join(filepath.Dir(workspace), "demo")
	if _, err := os.Stat(filepath.Join(dir, "base.txt")); err != nil {
		t.Errorf("the worker's worktree is missing: %v", err)
	}

	deadline := time.After(10 * time.Second)
	for {
		select {
		case event := <-events:
			if event.Kind == EventProcessEnd {
				return
			}
		case <-deadline:
			t.Fatal("the worker never announced completion")
		}
	}
}

func TestCodeWorkerValidatesInput(t *testing.T) {
	workspace := worktreeRepo(t)
	manager := NewProcessManager()
	env := &Env{Workspace: workspace, Todos: NewTodoStore(), Memory: NewMemory(workspace), Trusted: true, Processes: manager}
	tool := &codeWorkerTool{}

	if result, _ := tool.Run(context.Background(), env, map[string]any{}); !result.IsError {
		t.Errorf("an empty task must fail")
	}
	if result, _ := tool.Run(context.Background(), env, map[string]any{"task": "x", "worker": "bogus"}); !result.IsError {
		t.Errorf("an unknown worker must fail")
	}
}
