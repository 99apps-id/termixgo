package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/app"
)

// Task is one self-contained coding or auditing benchmark scenario.
type Task struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Category    string            `json:"category,omitempty"` // bugfix, refactor, audit, reasoning
	Prompt      string            `json:"prompt"`
	Files       map[string]string `json:"files,omitempty"` // relative path -> file content
	VerifyCmd   string            `json:"verify_cmd"`
	ExpectedOut string            `json:"expected_out,omitempty"`
	MaxSteps    int               `json:"max_steps,omitempty"`
	TimeoutSecs int               `json:"timeout_secs,omitempty"`
}

// Suite is a collection of benchmark tasks.
type Suite struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Tasks       []Task `json:"tasks"`
}

// TaskResult records the outcome of evaluating one task.
type TaskResult struct {
	TaskID       string   `json:"task_id"`
	Name         string   `json:"name"`
	Category     string   `json:"category"`
	Passed       bool     `json:"passed"`
	DurationMS   int64    `json:"duration_ms"`
	Steps        int      `json:"steps"`
	Tokens       int      `json:"tokens"`
	CostUSD      float64  `json:"cost_usd"`
	ToolsUsed    []string `json:"tools_used"`
	VerifyOutput string   `json:"verify_output,omitempty"`
	Error        string   `json:"error,omitempty"`
}

// SuiteResult aggregates the benchmark outcomes for an evaluation run.
type SuiteResult struct {
	SuiteName    string       `json:"suite_name"`
	Model        string       `json:"model"`
	StartedAt    time.Time    `json:"started_at"`
	DurationMS   int64        `json:"duration_ms"`
	TotalTasks   int          `json:"total_tasks"`
	PassedTasks  int          `json:"passed_tasks"`
	PassRate     float64      `json:"pass_rate"`
	TotalTokens  int          `json:"total_tokens"`
	TotalCostUSD float64      `json:"total_cost_usd"`
	Results      []TaskResult `json:"results"`
}

// LoadSuite reads an evaluation suite from a JSON file.
func LoadSuite(path string) (Suite, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Suite{}, fmt.Errorf("read suite %s: %w", path, err)
	}
	var suite Suite
	if err := json.Unmarshal(data, &suite); err != nil {
		return Suite{}, fmt.Errorf("parse suite %s: %w", path, err)
	}
	return suite, nil
}

// BuiltinSuite returns the default benchmark suite shipped with Termixgo.
func BuiltinSuite() Suite {
	return Suite{
		Name:        "termixgo-core-benchmarks",
		Description: "Standard coding, bugfixing, and refactoring agent benchmarks",
		Tasks: []Task{
			{
				ID:          "go-nil-guard",
				Name:        "Fix nil pointer dereference in Go",
				Category:    "bugfix",
				Description: "Add nil-guard check in CalculateTotal to prevent panic when input slice is nil",
				Files: map[string]string{
					"go.mod": "module example.com/calc\n\ngo 1.22\n",
					"calc.go": `package calc

type Item struct {
	Price int
}

func CalculateTotal(items []*Item) int {
	total := 0
	for _, item := range items {
		total += item.Price
	}
	return total
}
`,
					"calc_test.go": `package calc

import "testing"

func TestCalculateTotal(t *testing.T) {
	if got := CalculateTotal([]*Item{{Price: 10}, {Price: 20}}); got != 30 {
		t.Errorf("got %d, want 30", got)
	}
	if got := CalculateTotal([]*Item{{Price: 5}, nil, {Price: 15}}); got != 20 {
		t.Errorf("got %d, want 20", got)
	}
	if got := CalculateTotal(nil); got != 0 {
		t.Errorf("got %d, want 0", got)
	}
}
`,
				},
				Prompt:      "Run tests in calc_test.go, observe the failure, and fix calc.go so that nil items in the slice or a nil slice are safely skipped without panicking.",
				VerifyCmd:   "go test -v ./...",
				ExpectedOut: "PASS",
				MaxSteps:    10,
				TimeoutSecs: 90,
			},
			{
				ID:          "go-string-reverse",
				Name:        "Implement UTF-8 string reverse in Go",
				Category:    "refactor",
				Description: "Fix ASCII-only reverse to correctly handle multi-byte UTF-8 runes",
				Files: map[string]string{
					"go.mod": "module example.com/str\n\ngo 1.22\n",
					"reverse.go": `package str

// Reverse reverses a string.
func Reverse(s string) string {
	b := []byte(s)
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return string(b)
}
`,
					"reverse_test.go": `package str

import "testing"

func TestReverse(t *testing.T) {
	tests := []struct{ in, want string }{
		{"hello", "olleh"},
		{"café", "éfac"},
		{"日本語", "語本日"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := Reverse(tc.in); got != tc.want {
			t.Errorf("Reverse(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
`,
				},
				Prompt:      "The current Reverse function in reverse.go only works on ASCII and breaks UTF-8 characters like café and 日本語. Refactor Reverse to support UTF-8 runes properly.",
				VerifyCmd:   "go test -v ./...",
				ExpectedOut: "PASS",
				MaxSteps:    10,
				TimeoutSecs: 90,
			},
		},
	}
}

// ProgressFunc reports when a task begins and ends.
type ProgressFunc func(task Task, result *TaskResult)

// Runner coordinates execution of an evaluation suite.
type Runner struct {
	Suite      Suite
	AppFactory func(workspace string) (*app.App, error)
	OnProgress ProgressFunc
}

// Run executes the evaluation suite and aggregates the benchmark results.
func (r *Runner) Run(ctx context.Context) (SuiteResult, error) {
	started := time.Now()
	suiteResult := SuiteResult{
		SuiteName: r.Suite.Name,
		StartedAt: started,
	}

	for _, task := range r.Suite.Tasks {
		if ctx.Err() != nil {
			break
		}
		if r.OnProgress != nil {
			r.OnProgress(task, nil)
		}
		taskResult := r.runTask(ctx, task)
		if taskResult.Passed {
			suiteResult.PassedTasks++
		}
		suiteResult.TotalTasks++
		suiteResult.TotalTokens += taskResult.Tokens
		suiteResult.TotalCostUSD += taskResult.CostUSD
		suiteResult.Results = append(suiteResult.Results, taskResult)
		if r.OnProgress != nil {
			r.OnProgress(task, &taskResult)
		}
	}

	suiteResult.DurationMS = time.Since(started).Milliseconds()
	if suiteResult.TotalTasks > 0 {
		suiteResult.PassRate = float64(suiteResult.PassedTasks) / float64(suiteResult.TotalTasks) * 100.0
	}
	return suiteResult, nil
}

// runTask runs a single evaluation benchmark in an isolated workspace.
func (r *Runner) runTask(ctx context.Context, task Task) TaskResult {
	start := time.Now()
	res := TaskResult{
		TaskID:   task.ID,
		Name:     task.Name,
		Category: task.Category,
	}

	tempDir, err := os.MkdirTemp("", "termixgo-eval-*")
	if err != nil {
		res.Error = fmt.Sprintf("create temp workspace: %v", err)
		res.DurationMS = time.Since(start).Milliseconds()
		return res
	}
	defer os.RemoveAll(tempDir)

	// Write fixture files.
	for relPath, content := range task.Files {
		fullPath := filepath.Join(tempDir, relPath)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			res.Error = fmt.Sprintf("mkdir %s: %v", relPath, err)
			res.DurationMS = time.Since(start).Milliseconds()
			return res
		}
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			res.Error = fmt.Sprintf("write %s: %v", relPath, err)
			res.DurationMS = time.Since(start).Milliseconds()
			return res
		}
	}

	// Initialize git repo so git tools function normally.
	initGitRepo(tempDir)

	application, err := r.AppFactory(tempDir)
	if err != nil {
		res.Error = fmt.Sprintf("initialize app: %v", err)
		res.DurationMS = time.Since(start).Milliseconds()
		return res
	}
	defer application.Shutdown()

	application.SetEphemeral(true)
	application.SetTrust(true)

	// Collect tool events and usage.
	var toolsUsed []string
	seenTools := map[string]bool{}
	claim := application.SetObserver(func(event agent.Event) {
		if event.Kind == agent.EventToolStart {
			name := strings.TrimSpace(event.ToolName)
			if name != "" && !seenTools[name] {
				seenTools[name] = true
				toolsUsed = append(toolsUsed, name)
			}
		}
	})
	defer application.ClearObserver(claim)

	timeout := time.Duration(task.TimeoutSecs) * time.Second
	if timeout <= 0 {
		timeout = 180 * time.Second
	}
	taskCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	runErr := application.RunTurn(taskCtx, task.Prompt)
	if runErr != nil && !errorsIsCanceled(runErr) {
		res.Error = fmt.Sprintf("agent turn error: %v", runErr)
	}

	session := application.Session()
	if session != nil {
		res.Steps = session.Turns()
	}
	usage := application.Usage()
	res.Tokens = usage.TotalTokens
	cost, _ := application.Cost()
	res.CostUSD = cost
	res.ToolsUsed = toolsUsed

	// Run verification command.
	verifyOut, verifyErr := runVerifyCommand(taskCtx, tempDir, task.VerifyCmd)
	res.VerifyOutput = verifyOut
	if verifyErr == nil {
		if task.ExpectedOut == "" || strings.Contains(verifyOut, task.ExpectedOut) {
			res.Passed = true
		} else {
			res.Error = fmt.Sprintf("verification output missing expected substring %q", task.ExpectedOut)
		}
	} else {
		res.Error = fmt.Sprintf("verification command failed: %v", verifyErr)
	}

	res.DurationMS = time.Since(start).Milliseconds()
	return res
}

func initGitRepo(dir string) {
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	_ = cmd.Run()
	addCmd := exec.Command("git", "add", ".")
	addCmd.Dir = dir
	_ = addCmd.Run()
	commitCmd := exec.Command("git", "-c", "user.name=eval", "-c", "user.email=eval@termixgo.internal", "commit", "-q", "-m", "initial benchmark commit", "--allow-empty")
	commitCmd.Dir = dir
	_ = commitCmd.Run()
}

func runVerifyCommand(ctx context.Context, dir, command string) (string, error) {
	if strings.TrimSpace(command) == "" {
		return "", nil
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func errorsIsCanceled(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "canceled") || strings.Contains(err.Error(), "context deadline")
}

// FormatTable renders the benchmark summary in a clean tabular format.
func FormatTable(result SuiteResult) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("=== Evaluation Suite: %s ===\n", result.SuiteName))
	if result.Model != "" {
		b.WriteString(fmt.Sprintf("Model: %s\n", result.Model))
	}
	b.WriteString(fmt.Sprintf("Date:  %s\n\n", result.StartedAt.Format("2006-01-02 15:04:05")))

	b.WriteString(fmt.Sprintf("%-20s %-12s %-8s %-6s %-10s %-8s %-8s\n", "TASK ID", "CATEGORY", "STATUS", "STEPS", "DURATION", "TOKENS", "COST"))
	b.WriteString(strings.Repeat("-", 76) + "\n")

	for _, tr := range result.Results {
		status := "PASS"
		if !tr.Passed {
			status = "FAIL"
		}
		durationStr := fmt.Sprintf("%.2fs", float64(tr.DurationMS)/1000.0)
		costStr := fmt.Sprintf("$%.4f", tr.CostUSD)
		b.WriteString(fmt.Sprintf("%-20s %-12s %-8s %-6d %-10s %-8d %-8s\n",
			truncate(tr.TaskID, 20),
			truncate(tr.Category, 12),
			status,
			tr.Steps,
			durationStr,
			tr.Tokens,
			costStr,
		))
	}

	b.WriteString(strings.Repeat("-", 76) + "\n")
	durationTotal := fmt.Sprintf("%.2fs", float64(result.DurationMS)/1000.0)
	b.WriteString(fmt.Sprintf("Summary: %d/%d passed (%.1f%%) | Duration: %s | Tokens: %d | Cost: $%.4f\n",
		result.PassedTasks,
		result.TotalTasks,
		result.PassRate,
		durationTotal,
		result.TotalTokens,
		result.TotalCostUSD,
	))

	// Print error highlights for failed tasks.
	var failed []TaskResult
	for _, tr := range result.Results {
		if !tr.Passed {
			failed = append(failed, tr)
		}
	}
	if len(failed) > 0 {
		b.WriteString("\nFailures:\n")
		for _, f := range failed {
			b.WriteString(fmt.Sprintf("- [%s] %s\n", f.TaskID, f.Name))
			if f.Error != "" {
				b.WriteString(fmt.Sprintf("  Error:  %s\n", f.Error))
			}
			if f.VerifyOutput != "" {
				trimmed := strings.TrimSpace(f.VerifyOutput)
				if len(trimmed) > 300 {
					trimmed = trimmed[:300] + "..."
				}
				b.WriteString(fmt.Sprintf("  Output: %s\n", trimmed))
			}
		}
	}

	return b.String()
}

// FormatJSON serializes SuiteResult as indented JSON.
func FormatJSON(result SuiteResult) (string, error) {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}
