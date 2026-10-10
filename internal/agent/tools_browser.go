package agent

import (
	"context"
	"errors"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// browserOpenTool opens a web URL or local file path in the default system browser.
type browserOpenTool struct{}

func (t *browserOpenTool) Name() string { return "browser_open" }
func (t *browserOpenTool) Aliases() []string {
	return []string{"open_browser", "open_preview", "open_url"}
}
func (t *browserOpenTool) Mutating() bool { return false }
func (t *browserOpenTool) Risk() Risk     { return RiskNetwork }
func (t *browserOpenTool) Label(a map[string]any) string {
	return "Opening browser " + Shorten(argString(a, "url"), 40)
}
func (t *browserOpenTool) DoneLabel(a map[string]any) string {
	return "Opened in browser " + Shorten(argString(a, "url"), 40)
}
func (t *browserOpenTool) Description() string {
	return "Open a web URL (http/https) or a local file (HTML, image, markdown) in the system's default web browser."
}
func (t *browserOpenTool) Schema() map[string]any {
	return object(map[string]any{
		"url": strProp("Web URL or local file path to open in the system browser."),
	}, "url")
}

func (t *browserOpenTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	raw := strings.TrimSpace(argString(args, "url"))
	if raw == "" {
		return Result{Output: "url is required.", IsError: true}, nil
	}

	target := raw
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") && !strings.HasPrefix(raw, "file://") {
		// Treat as local file path
		resolved := resolvePath(env, raw)
		if err := checkWorkspacePath(env, resolved); err != nil {
			return Result{Output: fmt.Sprintf("cannot open file outside workspace: %v", err), IsError: true}, nil
		}
		if _, err := os.Stat(resolved); err != nil {
			return Result{Output: fmt.Sprintf("target is neither a valid URL nor an existing local file: %s", raw), IsError: true}, nil
		}
		ext := strings.ToLower(filepath.Ext(resolved))
		switch ext {
		case ".exe", ".bat", ".cmd", ".ps1", ".vbs", ".js", ".msi", ".com", ".scr", ".sh", ".bash":
			return Result{Output: fmt.Sprintf("cannot open executable script or program %q in browser", ext), IsError: true}, nil
		}
		target = resolved
	}

	if err := launchDefaultBrowser(target); err != nil {
		return Result{Output: fmt.Sprintf("Failed to launch default browser: %v", err), IsError: true}, nil
	}

	return Result{Output: fmt.Sprintf("Successfully requested system browser to open: %s", target)}, nil
}

// renderMermaidTool generates an interactive dark-themed HTML file for Mermaid diagrams and displays it in the browser.
type renderMermaidTool struct{}

func (t *renderMermaidTool) Name() string      { return "render_mermaid" }
func (t *renderMermaidTool) Aliases() []string { return []string{"mermaid_render", "preview_diagram"} }
func (t *renderMermaidTool) Mutating() bool    { return true }
func (t *renderMermaidTool) Risk() Risk        { return RiskEdit }
func (t *renderMermaidTool) Label(a map[string]any) string {
	title := argString(a, "title")
	if title != "" {
		return "Rendering diagram " + Shorten(title, 30)
	}
	return "Rendering Mermaid diagram"
}
func (t *renderMermaidTool) DoneLabel(a map[string]any) string {
	return "Rendered Mermaid diagram"
}
func (t *renderMermaidTool) Description() string {
	return "Render a Mermaid diagram into a standalone dark-themed HTML file and open it in the default web browser."
}
func (t *renderMermaidTool) Schema() map[string]any {
	return object(map[string]any{
		"diagram":      strProp("Raw Mermaid diagram syntax (e.g. 'graph TD\\n  A --> B')."),
		"title":        strProp("Optional title for the diagram page (defaults to 'Mermaid Diagram')."),
		"open_browser": boolProp("Whether to launch the browser immediately (default true)."),
		"output_path":  strProp("Optional custom path for the HTML file (defaults to '.termixgo/diagrams/<name>.html')."),
	}, "diagram")
}

func (t *renderMermaidTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	diagram := strings.TrimSpace(argString(args, "diagram"))
	if diagram == "" {
		return Result{Output: "diagram content is required.", IsError: true}, nil
	}

	// Strip code block fence if model enclosed it in ```mermaid ... ```
	diagram = strings.TrimPrefix(diagram, "```mermaid")
	diagram = strings.TrimPrefix(diagram, "```")
	diagram = strings.TrimSuffix(diagram, "```")
	diagram = strings.TrimSpace(diagram)

	title := strings.TrimSpace(argString(args, "title"))
	if title == "" {
		title = "Mermaid Diagram"
	}

	outPathRaw := strings.TrimSpace(argString(args, "output_path"))
	var outPath string
	if outPathRaw != "" {
		outPath = resolvePath(env, outPathRaw)
		if err := checkWorkspacePath(env, outPath); err != nil {
			return Result{Output: fmt.Sprintf("cannot write diagram outside workspace: %v", err), IsError: true}, nil
		}
	} else {
		dir := filepath.Join(env.Workspace, ".termixgo", "diagrams")
		_ = os.MkdirAll(dir, 0o755)
		filename := fmt.Sprintf("diagram-%d.html", time.Now().Unix())
		outPath = filepath.Join(dir, filename)
	}

	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return Result{Output: fmt.Sprintf("Failed to create directory: %v", err), IsError: true}, nil
	}

	htmlContent := buildMermaidHTML(title, diagram)
	if err := os.WriteFile(outPath, []byte(htmlContent), 0o644); err != nil {
		return Result{Output: fmt.Sprintf("Failed to write HTML file: %v", err), IsError: true}, nil
	}

	shouldOpen := true
	if v, ok := args["open_browser"].(bool); ok {
		shouldOpen = v
	}

	relPath := displayPath(env, outPath)
	if shouldOpen {
		_ = launchDefaultBrowser(outPath)
		return Result{
			Output: fmt.Sprintf("Mermaid diagram generated and opened in browser.\nFile: %s", relPath),
		}, nil
	}

	return Result{
		Output: fmt.Sprintf("Mermaid diagram HTML generated at: %s", relPath),
	}, nil
}

// browserScreenshotTool captures a headless screenshot of a webpage using the local browser.
type browserScreenshotTool struct{}

func (t *browserScreenshotTool) Name() string      { return "browser_screenshot" }
func (t *browserScreenshotTool) Aliases() []string { return []string{"screenshot", "web_screenshot"} }
func (t *browserScreenshotTool) Mutating() bool    { return true }
func (t *browserScreenshotTool) Risk() Risk        { return RiskEdit }
func (t *browserScreenshotTool) Label(a map[string]any) string {
	return "Capturing screenshot of " + Shorten(argString(a, "url"), 40)
}
func (t *browserScreenshotTool) DoneLabel(a map[string]any) string {
	return "Captured screenshot of " + Shorten(argString(a, "url"), 40)
}
func (t *browserScreenshotTool) Description() string {
	return "Capture a headless screenshot of a webpage or local server URL using the installed browser (Chrome/Edge), saving a PNG image."
}
func (t *browserScreenshotTool) Schema() map[string]any {
	return object(map[string]any{
		"url":           strProp("Target webpage or dev server URL (e.g. http://localhost:3000 or https://example.com)."),
		"output_path":   strProp("Optional output path for the PNG image (defaults to '.termixgo/screenshots/<timestamp>.png')."),
		"width":         intProp("Viewport width in pixels, 320 to 3840 (defaults to 1280)."),
		"height":        intProp("Viewport height in pixels, 240 to 2160 (defaults to 800)."),
		"delay_seconds": intProp("Delay in seconds to allow JavaScript/SPA to render, 0 to 10 (defaults to 1)."),
	}, "url")
}

func (t *browserScreenshotTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	targetURL := strings.TrimSpace(argString(args, "url"))
	if targetURL == "" {
		return Result{Output: "url is required.", IsError: true}, nil
	}

	if strings.HasPrefix(strings.ToLower(targetURL), "file://") {
		filePath := strings.TrimPrefix(targetURL, "file://")
		if runtime.GOOS == "windows" {
			filePath = strings.TrimPrefix(filePath, "/")
		}
		if err := checkWorkspacePath(env, filePath); err != nil {
			return Result{Output: fmt.Sprintf("cannot access local file outside workspace: %v", err), IsError: true}, nil
		}
	}

	browserBin, err := findBrowserExecutable()
	if err != nil {
		return Result{Output: fmt.Sprintf("Browser screenshot failed: %v", err), IsError: true}, nil
	}

	width := argInt(args, "width", 1280, 320, 3840)
	height := argInt(args, "height", 800, 240, 2160)
	delaySec := argInt(args, "delay_seconds", 1, 0, 10)

	outPathRaw := strings.TrimSpace(argString(args, "output_path"))
	var outPath string
	if outPathRaw != "" {
		outPath = resolvePath(env, outPathRaw)
		if err := checkWorkspacePath(env, outPath); err != nil {
			return Result{Output: fmt.Sprintf("cannot write screenshot outside workspace: %v", err), IsError: true}, nil
		}
	} else {
		dir := filepath.Join(env.Workspace, ".termixgo", "screenshots")
		_ = os.MkdirAll(dir, 0o755)
		outPath = filepath.Join(dir, fmt.Sprintf("shot-%d.png", time.Now().Unix()))
	}

	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return Result{Output: fmt.Sprintf("Failed to create directory: %v", err), IsError: true}, nil
	}

	// Use context with timeout for headless browser execution
	execCtx, cancel := context.WithTimeout(ctx, time.Duration(15+delaySec)*time.Second)
	defer cancel()

	cmdArgs := []string{
		"--headless",
		"--disable-gpu",
		"--hide-scrollbars",
		fmt.Sprintf("--window-size=%d,%d", width, height),
		fmt.Sprintf("--screenshot=%s", outPath),
	}
	if delaySec > 0 {
		cmdArgs = append(cmdArgs, fmt.Sprintf("--virtual-time-budget=%d", delaySec*1000))
	}
	cmdArgs = append(cmdArgs, targetURL)

	cmd := exec.CommandContext(execCtx, browserBin, cmdArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil && !fileExists(outPath) {
		return Result{
			Output:  fmt.Sprintf("Failed to capture screenshot with %s: %v\nOutput: %s", filepath.Base(browserBin), err, string(output)),
			IsError: true,
		}, nil
	}

	fi, err := os.Stat(outPath)
	if err != nil {
		return Result{Output: "Screenshot command ran but output file was not created.", IsError: true}, nil
	}

	relPath := displayPath(env, outPath)
	return Result{
		Output: fmt.Sprintf("Screenshot captured successfully!\nPath: %s\nSize: %d bytes (%dx%d px)\nBrowser: %s\nUse tool 'read_image' with path %q to inspect the visual rendering.",
			relPath, fi.Size(), width, height, filepath.Base(browserBin), relPath),
	}, nil
}

// browserDumpDOMTool renders client-side JavaScript via a headless browser and dumps the hydrated DOM.
type browserDumpDOMTool struct{}

func (t *browserDumpDOMTool) Name() string      { return "browser_dump_dom" }
func (t *browserDumpDOMTool) Aliases() []string { return []string{"browser_render", "render_dom"} }
func (t *browserDumpDOMTool) Mutating() bool    { return false }
func (t *browserDumpDOMTool) Risk() Risk        { return RiskNetwork }
func (t *browserDumpDOMTool) Label(a map[string]any) string {
	return "Dumping DOM of " + Shorten(argString(a, "url"), 40)
}
func (t *browserDumpDOMTool) DoneLabel(a map[string]any) string {
	return "Dumped DOM of " + Shorten(argString(a, "url"), 40)
}
func (t *browserDumpDOMTool) Description() string {
	return "Fetch and render a JavaScript-heavy web application (SPA) using a headless browser, returning the hydrated DOM after client-side scripts execute."
}
func (t *browserDumpDOMTool) Schema() map[string]any {
	return object(map[string]any{
		"url":             strProp("Target webpage or dev server URL."),
		"timeout_seconds": intProp("Browser render timeout in seconds, 1 to 30 (defaults to 15)."),
	}, "url")
}

func (t *browserDumpDOMTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	targetURL := strings.TrimSpace(argString(args, "url"))
	if targetURL == "" {
		return Result{Output: "url is required.", IsError: true}, nil
	}

	if strings.HasPrefix(strings.ToLower(targetURL), "file://") {
		filePath := strings.TrimPrefix(targetURL, "file://")
		if runtime.GOOS == "windows" {
			filePath = strings.TrimPrefix(filePath, "/")
		}
		if err := checkWorkspacePath(env, filePath); err != nil {
			return Result{Output: fmt.Sprintf("cannot access local file outside workspace: %v", err), IsError: true}, nil
		}
	}

	browserBin, err := findBrowserExecutable()
	if err != nil {
		return Result{Output: fmt.Sprintf("Headless browser execution failed: %v", err), IsError: true}, nil
	}

	timeoutSec := argInt(args, "timeout_seconds", 15, 1, 30)
	execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	cmdArgs := []string{
		"--headless",
		"--disable-gpu",
		"--dump-dom",
		"--virtual-time-budget=2000",
		targetURL,
	}

	cmd := exec.CommandContext(execCtx, browserBin, cmdArgs...)
	domOutput, err := cmd.Output()
	if err != nil {
		return Result{Output: fmt.Sprintf("Failed to dump DOM: %v", err), IsError: true}, nil
	}

	rendered := string(domOutput)
	const maxDOMBytes = 64 * 1024
	if len(rendered) > maxDOMBytes {
		rendered = clipBytes(rendered, maxDOMBytes) + "\n... [DOM output truncated at 64KB]"
	}

	return Result{Output: rendered}, nil
}

// launchDefaultBrowser opens a file or URL with the platform's default browser handler.
func launchDefaultBrowser(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	case "darwin":
		cmd = exec.Command("open", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	return cmd.Start()
}

// findBrowserExecutable locates an installed Chromium-based browser on the host.
func findBrowserExecutable() (string, error) {
	if envBrowser := os.Getenv("TERMIXGO_BROWSER_BIN"); envBrowser != "" {
		if _, err := os.Stat(envBrowser); err == nil {
			return envBrowser, nil
		}
	}

	switch runtime.GOOS {
	case "windows":
		candidates := []string{
			`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
			`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files\BraveSoftware\Brave-Browser\Application\brave.exe`,
		}
		if localApp := os.Getenv("LOCALAPPDATA"); localApp != "" {
			candidates = append(candidates,
				filepath.Join(localApp, `Microsoft\Edge\Application\msedge.exe`),
				filepath.Join(localApp, `Google\Chrome\Application\chrome.exe`),
				filepath.Join(localApp, `BraveSoftware\Brave-Browser\Application\brave.exe`),
			)
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				return c, nil
			}
		}
		for _, name := range []string{"msedge.exe", "chrome.exe", "brave.exe"} {
			if path, err := exec.LookPath(name); err == nil {
				return path, nil
			}
		}

	case "darwin":
		candidates := []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				return c, nil
			}
		}
		for _, name := range []string{"google-chrome", "chromium", "microsoft-edge"} {
			if path, err := exec.LookPath(name); err == nil {
				return path, nil
			}
		}

	default: // Linux / BSD
		for _, name := range []string{"google-chrome-stable", "google-chrome", "chromium-browser", "chromium", "microsoft-edge", "brave-browser"} {
			if path, err := exec.LookPath(name); err == nil {
				return path, nil
			}
		}
	}

	return "", errors.New("no compatible browser executable (Edge, Chrome, Chromium, or Brave) found on system")
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func buildMermaidHTML(title, diagram string) string {
	escapedTitle := html.EscapeString(title)
	escapedDiagram := html.EscapeString(diagram)

	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>%s</title>
<style>
  * { box-sizing: border-box; }
  body {
    margin: 0;
    padding: 24px;
    background: #0f141c;
    color: #e2e8f0;
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
    display: flex;
    flex-direction: column;
    align-items: center;
    min-height: 100vh;
  }
  .header {
    width: 100%%;
    max-width: 1280px;
    margin-bottom: 20px;
    display: flex;
    justify-content: space-between;
    align-items: center;
    border-bottom: 1px solid #1e293b;
    padding-bottom: 12px;
  }
  h1 { font-size: 1.25rem; font-weight: 600; margin: 0; color: #38bdf8; }
  .badge { font-size: 0.75rem; background: #1e293b; color: #94a3b8; padding: 4px 10px; border-radius: 9999px; }
  .container {
    width: 100%%;
    max-width: 1280px;
    background: #151d2a;
    border: 1px solid #1e293b;
    border-radius: 8px;
    padding: 32px;
    box-shadow: 0 4px 6px -1px rgba(0, 0, 0, 0.4);
    overflow: auto;
    display: flex;
    justify-content: center;
  }
  .mermaid { text-align: center; }
</style>
<script type="module">
  import mermaid from 'https://cdn.jsdelivr.net/npm/mermaid@10/dist/mermaid.esm.min.mjs';
  mermaid.initialize({
    startOnLoad: true,
    theme: 'dark',
    themeVariables: {
      darkMode: true,
      background: '#151d2a',
      primaryColor: '#1e3a5f',
      primaryTextColor: '#f8fafc',
      primaryBorderColor: '#38bdf8',
      lineColor: '#94a3b8',
      secondaryColor: '#1e293b',
      tertiaryColor: '#0f172a'
    }
  });
</script>
</head>
<body>
<div class="header">
  <h1>%s</h1>
  <span class="badge">Generated by Termixgo</span>
</div>
<div class="container">
  <pre class="mermaid">
%s
  </pre>
</div>
</body>
</html>`, escapedTitle, escapedTitle, escapedDiagram)
}
