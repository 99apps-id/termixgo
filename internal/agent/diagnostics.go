package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"go/parser"
	"go/scanner"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// DiagnoseFile validates the syntax of a single file and returns any diagnostic
// error message. If the file is valid or its format is not supported, it returns
// an empty string.
func DiagnoseFile(env *Env, rawPath string) string {
	if env == nil || strings.TrimSpace(rawPath) == "" {
		return ""
	}
	targetPath := resolvePath(env, rawPath)
	info, err := os.Stat(targetPath)
	if err != nil || info.IsDir() {
		return ""
	}

	display := displayPath(env, targetPath)
	ext := strings.ToLower(filepath.Ext(targetPath))

	switch ext {
	case ".go":
		return diagnoseGoFile(targetPath, display)
	case ".json":
		return diagnoseJSONFile(targetPath, display)
	case ".yaml", ".yml":
		return diagnoseYAMLFile(targetPath, display)
	case ".py":
		return diagnosePythonFile(targetPath, display)
	case ".js", ".mjs", ".cjs":
		return diagnoseJSFile(targetPath, display)
	default:
		return ""
	}
}

// diagnoseGoFile uses Go standard library parser to detect syntax errors in-memory.
func diagnoseGoFile(targetPath, display string) string {
	src, err := os.ReadFile(targetPath)
	if err != nil {
		return ""
	}
	fset := token.NewFileSet()
	_, parseErr := parser.ParseFile(fset, targetPath, src, parser.AllErrors)
	if parseErr == nil {
		return ""
	}

	var errorLines []string
	if errList, ok := parseErr.(scanner.ErrorList); ok {
		for i, e := range errList {
			if i >= 5 {
				errorLines = append(errorLines, fmt.Sprintf("... and %d more error(s)", len(errList)-5))
				break
			}
			errorLines = append(errorLines, fmt.Sprintf("line %d:%d: %s", e.Pos.Line, e.Pos.Column, e.Msg))
		}
	} else {
		errorLines = append(errorLines, parseErr.Error())
	}

	if len(errorLines) == 0 {
		return ""
	}
	return fmt.Sprintf("%s: Go syntax error:\n  %s", display, strings.Join(errorLines, "\n  "))
}

// diagnoseJSONFile validates JSON syntax.
func diagnoseJSONFile(targetPath, display string) string {
	src, err := os.ReadFile(targetPath)
	if err != nil {
		return ""
	}
	var dummy any
	if err := json.Unmarshal(src, &dummy); err != nil {
		return fmt.Sprintf("%s: invalid JSON syntax: %v", display, err)
	}
	return ""
}

// diagnoseYAMLFile validates YAML syntax using yaml.v3.
func diagnoseYAMLFile(targetPath, display string) string {
	src, err := os.ReadFile(targetPath)
	if err != nil {
		return ""
	}
	var dummy any
	if err := yaml.Unmarshal(src, &dummy); err != nil {
		return fmt.Sprintf("%s: invalid YAML syntax: %v", display, err)
	}
	return ""
}

// diagnosePythonFile checks Python syntax via py_compile when python is available.
func diagnosePythonFile(targetPath, display string) string {
	bin, err := exec.LookPath("python")
	if err != nil {
		bin, err = exec.LookPath("python3")
		if err != nil {
			return ""
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "-m", "py_compile", targetPath)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return ""
	}

	text := strings.TrimSpace(string(output))
	if text == "" {
		text = err.Error()
	}
	lines := strings.Split(text, "\n")
	var relevant []string
	for _, line := range lines {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "SyntaxError:") || strings.HasPrefix(l, "IndentationError:") || strings.Contains(l, "line ") {
			relevant = append(relevant, l)
		}
	}
	if len(relevant) > 0 {
		text = strings.Join(relevant, "; ")
	}
	return fmt.Sprintf("%s: Python syntax error: %s", display, text)
}

// diagnoseJSFile checks JavaScript syntax via node --check when node is available.
func diagnoseJSFile(targetPath, display string) string {
	bin, err := exec.LookPath("node")
	if err != nil {
		return ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "--check", targetPath)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return ""
	}

	text := strings.TrimSpace(string(output))
	if text == "" {
		text = err.Error()
	}
	return fmt.Sprintf("%s: JavaScript syntax error: %s", display, text)
}

// DiagnosePaths validates syntax for a list of file paths and collects findings.
func DiagnosePaths(env *Env, paths []string) string {
	if env == nil || len(paths) == 0 {
		return ""
	}

	seen := make(map[string]bool, len(paths))
	var issues []string

	for _, p := range paths {
		cleaned := strings.TrimSpace(p)
		if cleaned == "" || seen[cleaned] {
			continue
		}
		seen[cleaned] = true

		if issue := DiagnoseFile(env, cleaned); issue != "" {
			issues = append(issues, "- "+issue)
		}
	}

	if len(issues) == 0 {
		return ""
	}

	return "[Diagnostics: syntax error(s) detected after change:\n" +
		strings.Join(issues, "\n") +
		"\nReview and fix before completing the task.]"
}

// checkDiagnosticsTool allows the agent or operator to explicitly validate syntax.
type checkDiagnosticsTool struct{}

func (t *checkDiagnosticsTool) Name() string { return "check_diagnostics" }
func (t *checkDiagnosticsTool) Aliases() []string {
	return []string{"diagnostics", "syntax_check", "check_syntax"}
}
func (t *checkDiagnosticsTool) Mutating() bool { return false }
func (t *checkDiagnosticsTool) Risk() Risk     { return RiskEdit }
func (t *checkDiagnosticsTool) Label(a map[string]any) string {
	return "Checking syntax diagnostics in " + displayName(a)
}
func (t *checkDiagnosticsTool) DoneLabel(a map[string]any) string {
	return "Checked syntax diagnostics in " + displayName(a)
}
func (t *checkDiagnosticsTool) Description() string {
	return "Fast syntax diagnostic check for Go, JSON, YAML, Python and JavaScript. Validates files in-memory without running slow test suites."
}
func (t *checkDiagnosticsTool) Schema() map[string]any {
	return object(map[string]any{
		"path": strProp("Optional file or directory to check. Defaults to workspace."),
	})
}

func (t *checkDiagnosticsTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	raw := argString(args, "path", "file", "dir")
	target := env.Workspace
	if raw != "" {
		target = resolvePath(env, raw)
	}

	info, err := os.Stat(target)
	if err != nil {
		return Result{Output: fmt.Sprintf("Path not found: %s", displayPath(env, target)), IsError: true}, nil
	}

	if !info.IsDir() {
		diag := DiagnoseFile(env, target)
		if diag == "" {
			return Result{Output: fmt.Sprintf("No syntax errors found in %s", displayPath(env, target))}, nil
		}
		return Result{Output: diag, IsError: true}, nil
	}

	var issues []string
	checked := 0
	err = filepath.Walk(target, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			base := filepath.Base(path)
			if base == ".git" || base == ".termixgo" || base == "node_modules" || base == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".go", ".json", ".yaml", ".yml", ".py", ".js", ".mjs", ".cjs":
			checked++
			if issue := DiagnoseFile(env, path); issue != "" {
				issues = append(issues, "- "+issue)
			}
		}
		if checked >= 300 {
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return Result{Output: fmt.Sprintf("diagnostics walk error: %v", err), IsError: true}, nil
	}

	if len(issues) == 0 {
		return Result{Output: fmt.Sprintf("Checked %d files in %s: no syntax errors found.", checked, displayPath(env, target))}, nil
	}

	return Result{
		Output:  fmt.Sprintf("Checked %d files in %s. Found %d syntax issue(s):\n%s", checked, displayPath(env, target), len(issues), strings.Join(issues, "\n")),
		IsError: true,
	}, nil
}

// CheckDiagnostics runs the diagnostics check on a file or directory.
func CheckDiagnostics(workspace, target string) (output string, isError bool) {
	env := &Env{Workspace: workspace}
	t := &checkDiagnosticsTool{}
	res, err := t.Run(context.Background(), env, map[string]any{"path": target})
	if err != nil {
		return err.Error(), true
	}
	return res.Output, res.IsError
}
