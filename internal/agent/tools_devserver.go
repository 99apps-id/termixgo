package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// devServerTool starts a dev server and waits for it to become ready.
type devServerTool struct{}

func (t *devServerTool) Name() string      { return "dev_server" }
func (t *devServerTool) Aliases() []string { return []string{"start_dev", "serve"} }
func (t *devServerTool) Mutating() bool    { return true }
func (t *devServerTool) Risk() Risk        { return RiskCommand }
func (t *devServerTool) Description() string {
	return "Start a development server in the workspace and wait until it is ready. Detects common frameworks and package.json scripts automatically."
}
func (t *devServerTool) Schema() map[string]any {
	return object(map[string]any{
		"command": strProp("Override the dev command. If omitted, the tool detects it from package.json or common patterns."),
		"dir":     strProp("Working directory (defaults to workspace root)."),
		"url":     strProp("Expected URL pattern to probe, e.g., http://localhost:3000."),
		"timeout": intProp("Max wait in milliseconds (default 15000)."),
	})
}
func (t *devServerTool) Label(a map[string]any) string {
	return "Starting dev server " + Shorten(argString(a, "command"), 40)
}
func (t *devServerTool) DoneLabel(a map[string]any) string {
	return "Started dev server " + Shorten(argString(a, "command"), 40)
}

func (t *devServerTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	dir := argString(args, "dir")
	if dir == "" {
		dir = env.Workspace
	}
	err := checkWorkspacePath(env, dir)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}

	command := argString(args, "command")
	if command == "" {
		command = detectDevCommand(dir)
	}
	if command == "" {
		return Result{Output: "no dev command detected; pass command explicitly", IsError: true}, nil
	}

	timeout := time.Duration(argInt(args, "timeout", 15000, 5000, 120000)) * time.Millisecond

	// Start the process in background.
	handle, err := env.Processes.Start(ctx, command, dir)
	if err != nil {
		return Result{Output: fmt.Sprintf("start failed: %v", err), IsError: true}, nil
	}

	// Wait for URL in output.
	urlPattern := argString(args, "url")
	if urlPattern == "" {
		urlPattern = `http://localhost:\d+`
	}
	urlRE := regexp.MustCompile(urlPattern)

	deadline := time.Now().Add(timeout)
	var lastLog string
	for time.Now().Before(deadline) {
		logs := handle.LogsAll()
		lastLog = logs
		if matches := urlRE.FindAllString(logs, -1); len(matches) > 0 {
			candidate := matches[len(matches)-1]
			if probeURL(candidate, 2*time.Second) {
				return Result{Output: fmt.Sprintf("Dev server ready at %s (handle=%s)", candidate, handle.ID)}, nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Timeout or process exited.
	if handle.Exited() {
		return Result{Output: fmt.Sprintf("dev server exited early:\n%s", truncateOutput(lastLog, 4000)), IsError: true}, nil
	}
	return Result{Output: fmt.Sprintf("dev server did not become ready within %s.\nLast logs:\n%s", timeout, truncateOutput(lastLog, 4000)), IsError: true}, nil
}

// detectDevCommand inspects package.json or returns common defaults.
func detectDevCommand(dir string) string {
	pkg := filepath.Join(dir, "package.json")
	data, err := os.ReadFile(pkg)
	if err == nil {
		var script map[string]any
		if json.Unmarshal(data, &script) == nil {
			if scripts, ok := script["scripts"].(map[string]any); ok {
				if _, ok := scripts["dev"]; ok {
					return "npm run dev"
				}
				if _, ok := scripts["start"]; ok {
					return "npm start"
				}
			}
		}
	}
	return ""
}

// probeURL tries to GET the URL and returns true on any HTTP response.
func probeURL(raw string, timeout time.Duration) bool {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(raw)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return true
}
