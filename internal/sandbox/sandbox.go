// Package sandbox runs a shell command inside a container instead of on the
// host. It is opt-in, and it is deliberately narrow: the container gets the
// workspace and a temporary /tmp, no network unless the operator asked for it,
// no inherited environment, no Linux capabilities, and a non-root user.
//
// It is not a jail for the whole agent. File tools, git, MCP servers, desktop
// control and background processes do not go through it, and the workspace is
// mounted read-write by default because a coding agent must be able to edit the
// tree it is building. What it does contain is arbitrary shell execution: a
// command that the model invented cannot read ~/.termixgo/secrets.json, reach
// the network, or touch anything outside the mounted folder.
package sandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/99apps-id/termixgo/internal/config"
)

// containerWorkdir is where the workspace is mounted on Windows, where host
// paths are not valid container paths. On POSIX the same absolute path is used
// on both sides, so the paths a command prints are the paths the agent knows.
const containerWorkdir = "/workspace"

// resolveEngine picks the container engine. An explicit value must exist; a
// path is accepted, so an engine outside PATH can be named; auto tries Docker
// first, then Podman.
func resolveEngine(name string) (string, error) {
	raw := strings.TrimSpace(name)
	switch strings.ToLower(raw) {
	case "", "auto":
		for _, candidate := range []string{"docker", "podman"} {
			if path, err := exec.LookPath(candidate); err == nil {
				return path, nil
			}
		}
		return "", errors.New("no container engine found: install docker or podman, or set sandbox.engine")
	default:
		path, err := exec.LookPath(raw)
		if err != nil {
			return "", fmt.Errorf("sandbox.engine %q is not an executable on PATH", raw)
		}
		return path, nil
	}
}

// networkMode validates the network setting. The default is none: a sandboxed
// build usually should not reach the internet, and an operator who needs it
// says so.
func networkMode(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "none":
		return "none", nil
	case "bridge", "host":
		return strings.ToLower(strings.TrimSpace(value)), nil
	default:
		return "", fmt.Errorf("sandbox.network must be none, bridge or host, not %q", value)
	}
}

// workspaceMode validates the mount mode.
func workspaceMode(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "rw":
		return "rw", nil
	case "ro":
		return "ro", nil
	default:
		return "", fmt.Errorf("sandbox.workspace must be rw or ro, not %q", value)
	}
}

// Command builds the container invocation for one shell command. It returns the
// engine binary and its argv; the caller starts it like any other process.
func Command(settings config.Sandbox, workspace, dir, command string) (string, []string, error) {
	if !settings.Enabled {
		return "", nil, errors.New("the sandbox is not enabled")
	}
	image := strings.TrimSpace(settings.Image)
	if image == "" {
		return "", nil, errors.New("sandbox.image is required: choose a Linux image with the project's toolchain")
	}
	engine, err := resolveEngine(settings.Engine)
	if err != nil {
		return "", nil, err
	}
	network, err := networkMode(settings.Network)
	if err != nil {
		return "", nil, err
	}
	mode, err := workspaceMode(settings.Workspace)
	if err != nil {
		return "", nil, err
	}
	workspace = filepath.Clean(workspace)
	if workspace == "" || workspace == "." {
		return "", nil, errors.New("the sandbox needs a workspace to mount")
	}

	mountTarget, workdir, err := containerPaths(workspace, dir)
	if err != nil {
		return "", nil, err
	}

	args := []string{
		"run", "--rm", "--init",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--pids-limit", "512",
		"--network", network,
		"--volume", mountTarget + ":" + mountTarget + ":" + mode,
		"--workdir", workdir,
		"--tmpfs", "/tmp:rw,exec,size=512m,mode=1777",
		"--env", "HOME=/tmp",
		"--env", "TMPDIR=/tmp",
		"--env", "LANG=C.UTF-8",
	}
	if runtime.GOOS != "windows" {
		// Building as root leaves root-owned files in the workspace, which then
		// break the next host-side edit. Match the operator's own ids.
		args = append(args, "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()))
	}
	args = append(args, image, "/bin/sh", "-c", command)
	return engine, args, nil
}

// containerPaths maps the host workspace and working directory onto the
// container. On POSIX the same path is used on both sides; on Windows the
// workspace is mounted at a fixed path and the working directory is expressed
// relative to it.
func containerPaths(workspace, dir string) (string, string, error) {
	workspace = filepath.Clean(workspace)
	if runtime.GOOS != "windows" {
		workdir := filepath.Clean(dir)
		if workdir == "" || workdir == "." {
			workdir = workspace
		}
		return workspace, workdir, nil
	}
	if strings.TrimSpace(dir) == "" {
		return containerWorkdir, containerWorkdir, nil
	}
	relative, err := filepath.Rel(workspace, filepath.Clean(dir))
	if err != nil {
		return "", "", fmt.Errorf("cannot map %s into the container: %w", dir, err)
	}
	if relative == "." {
		return containerWorkdir, containerWorkdir, nil
	}
	if strings.HasPrefix(relative, "..") {
		return "", "", fmt.Errorf("%s is outside the workspace and cannot be mounted", dir)
	}
	return containerWorkdir, containerWorkdir + "/" + filepath.ToSlash(relative), nil
}

// EngineAvailable reports whether the configured container engine can be
// reached, which is what doctor shows before an operator relies on it.
func EngineAvailable(settings config.Sandbox) error {
	_, err := resolveEngine(settings.Engine)
	return err
}

// Describe summarizes the configuration for /status and doctor. An empty string
// means the sandbox is off.
func Describe(settings config.Sandbox) string {
	if !settings.Enabled {
		return ""
	}
	engine := strings.TrimSpace(settings.Engine)
	if engine == "" {
		engine = "auto"
	}
	image := strings.TrimSpace(settings.Image)
	if image == "" {
		image = "(no image set)"
	}
	network, _ := networkMode(settings.Network)
	return fmt.Sprintf("%s image=%s network=%s", engine, image, network)
}
