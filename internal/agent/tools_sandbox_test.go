package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/config"
)

func TestBackgroundIsRefusedWhileSandboxed(t *testing.T) {
	env := testEnv(t)
	env.Processes = NewProcessManager()
	env.Config = config.Config{Sandbox: config.Sandbox{Enabled: true, Image: "img"}}
	result, err := (&backgroundTool{}).Run(context.Background(), env, map[string]any{"command": "sleep 10"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Output, "Sandbox is on") {
		t.Fatalf("output = %q", result.Output)
	}
}

func TestExecuteRefusesSandboxWithoutAnImage(t *testing.T) {
	env := testEnv(t)
	env.Config = config.Config{Sandbox: config.Sandbox{Enabled: true}}
	result, err := execute(context.Background(), env, "echo hi", env.Workspace, 5*time.Second)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Output, "sandbox.image") {
		t.Fatalf("output = %q", result.Output)
	}
}

func TestExecuteRunsThroughTheSandboxEngine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake engine is a POSIX shell script")
	}
	engine := filepath.Join(t.TempDir(), "fake-engine")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\"\n"
	if err := os.WriteFile(engine, []byte(script), 0o755); err != nil {
		t.Fatalf("write engine: %v", err)
	}

	env := testEnv(t)
	env.Config = config.Config{Sandbox: config.Sandbox{
		Enabled: true,
		Engine:  engine,
		Image:   "golang:1.26",
	}}
	result, err := execute(context.Background(), env, "echo hi", env.Workspace, 10*time.Second)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Output)
	}
	for _, want := range []string{"run", "--rm", "--init", "--network", "none", "golang:1.26", "/bin/sh", "-c", "echo hi"} {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("output %q missing %q", result.Output, want)
		}
	}
}
