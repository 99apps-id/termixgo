package sandbox

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
)

func baseConfig() config.Sandbox {
	return config.Sandbox{Enabled: true, Image: "golang:1.26"}
}

func TestCommandBuildsAContainedInvocation(t *testing.T) {
	workspace := t.TempDir()
	engineName := "podman"
	if runtime.GOOS == "windows" {
		engineName = "podman.exe"
	}
	engine := filepath.Join(t.TempDir(), engineName)
	if err := os.WriteFile(engine, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Skipf("cannot create a fake engine here: %v", err)
	}
	settings := baseConfig()
	settings.Engine = engine

	binary, args, err := Command(settings, workspace, workspace, "go test ./...")
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if binary != engine {
		t.Fatalf("binary = %q", binary)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"run --rm --init",
		"--cap-drop ALL",
		"--security-opt no-new-privileges",
		"--network none",
		"--pids-limit 512",
		"golang:1.26 /bin/sh -c go test ./...",
		"--env HOME=/tmp",
		"--tmpfs /tmp:rw,exec,size=512m,mode=1777",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args %q missing %q", joined, want)
		}
	}
	// The workspace is mounted at the same path on POSIX, so the paths a
	// command prints are the ones the agent uses.
	if runtime.GOOS != "windows" {
		if !strings.Contains(joined, workspace+":"+workspace+":rw") {
			t.Fatalf("mount missing from %q", joined)
		}
		if !strings.Contains(joined, "--user ") {
			t.Fatalf("a POSIX build should match the operator's uid: %q", joined)
		}
	}
}

func TestCommandRespectsNetworkAndMountMode(t *testing.T) {
	engineName := "docker"
	if runtime.GOOS == "windows" {
		engineName = "docker.exe"
	}
	engine := filepath.Join(t.TempDir(), engineName)
	if err := os.WriteFile(engine, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Skipf("cannot create a fake engine here: %v", err)
	}
	settings := baseConfig()
	settings.Engine = engine
	settings.Network = "bridge"
	settings.Workspace = "ro"

	workspace := t.TempDir()
	_, args, err := Command(settings, workspace, workspace, "ls")
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--network bridge") {
		t.Fatalf("network missing: %q", joined)
	}
	if !strings.Contains(joined, ":ro") {
		t.Fatalf("read-only mount missing: %q", joined)
	}
}

func TestCommandRequiresAnImage(t *testing.T) {
	_, _, err := Command(config.Sandbox{Enabled: true}, t.TempDir(), t.TempDir(), "ls")
	if err == nil || !strings.Contains(err.Error(), "sandbox.image") {
		t.Fatalf("err = %v", err)
	}
}

func TestCommandValidatesSettings(t *testing.T) {
	settings := baseConfig()
	settings.Network = "everywhere"
	if _, _, err := Command(settings, t.TempDir(), t.TempDir(), "ls"); err == nil {
		t.Fatal("an unknown network mode must be refused")
	}
	settings = baseConfig()
	settings.Workspace = "write"
	if _, _, err := Command(settings, t.TempDir(), t.TempDir(), "ls"); err == nil {
		t.Fatal("an unknown mount mode must be refused")
	}
	settings = baseConfig()
	settings.Engine = "definitely-not-a-container-engine"
	if _, _, err := Command(settings, t.TempDir(), t.TempDir(), "ls"); err == nil {
		t.Fatal("an unknown engine must be refused")
	}
}

func TestResolveEngineWithNoCandidates(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := resolveEngine(""); err == nil || !strings.Contains(err.Error(), "docker") {
		t.Fatalf("err = %v", err)
	}
}

func TestDescribe(t *testing.T) {
	if Describe(config.Sandbox{}) != "" {
		t.Fatal("a disabled sandbox has no description")
	}
	line := Describe(config.Sandbox{Enabled: true, Image: "golang:1.26"})
	if !strings.Contains(line, "auto") || !strings.Contains(line, "golang:1.26") || !strings.Contains(line, "network=none") {
		t.Fatalf("line = %q", line)
	}
}
