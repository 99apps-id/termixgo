package eval

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/app"
)

func TestBuiltinSuiteHasValidTasks(t *testing.T) {
	suite := BuiltinSuite()
	if len(suite.Tasks) == 0 {
		t.Fatal("builtin suite must have tasks")
	}
	seenIDs := map[string]bool{}
	for _, task := range suite.Tasks {
		if task.ID == "" {
			t.Error("task ID must not be empty")
		}
		if seenIDs[task.ID] {
			t.Errorf("duplicate task ID: %s", task.ID)
		}
		seenIDs[task.ID] = true
		if task.Name == "" || task.Prompt == "" || task.VerifyCmd == "" {
			t.Errorf("task %s has incomplete fields", task.ID)
		}
		if len(task.Files) == 0 {
			t.Errorf("task %s must have fixture files", task.ID)
		}
	}
}

func TestLoadSuiteRoundTrip(t *testing.T) {
	tempDir := t.TempDir()
	suitePath := filepath.Join(tempDir, "suite.json")
	data := `{
		"name": "custom-suite",
		"description": "custom tests",
		"tasks": [
			{
				"id": "t1",
				"name": "Task One",
				"prompt": "fix it",
				"verify_cmd": "echo ok",
				"files": {"main.go": "package main"}
			}
		]
	}`
	if err := os.WriteFile(suitePath, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadSuite(suitePath)
	if err != nil {
		t.Fatalf("LoadSuite: %v", err)
	}
	if loaded.Name != "custom-suite" || len(loaded.Tasks) != 1 {
		t.Errorf("unexpected loaded suite: %+v", loaded)
	}
	if loaded.Tasks[0].ID != "t1" {
		t.Errorf("unexpected task: %+v", loaded.Tasks[0])
	}
}

func TestFormatTableAndJSON(t *testing.T) {
	result := SuiteResult{
		SuiteName:    "test-suite",
		Model:        "test-model",
		StartedAt:    time.Date(2026, 10, 25, 7, 0, 0, 0, time.UTC),
		DurationMS:   12500,
		TotalTasks:   2,
		PassedTasks:  1,
		PassRate:     50.0,
		TotalTokens:  450,
		TotalCostUSD: 0.0025,
		Results: []TaskResult{
			{
				TaskID:     "t1",
				Name:       "Task One",
				Category:   "bugfix",
				Passed:     true,
				DurationMS: 5000,
				Steps:      3,
				Tokens:     200,
				CostUSD:    0.001,
				ToolsUsed:  []string{"read_file", "edit"},
			},
			{
				TaskID:       "t2",
				Name:         "Task Two",
				Category:     "refactor",
				Passed:       false,
				DurationMS:   7500,
				Steps:        5,
				Tokens:       250,
				CostUSD:      0.0015,
				ToolsUsed:    []string{"read_file"},
				VerifyOutput: "FAIL: test failed\nexit status 1",
				Error:        "verification command failed",
			},
		},
	}

	table := FormatTable(result)
	if !strings.Contains(table, "test-suite") || !strings.Contains(table, "PASS") || !strings.Contains(table, "FAIL") {
		t.Errorf("table missing expected headers or results:\n%s", table)
	}
	if !strings.Contains(table, "50.0%") {
		t.Errorf("table missing pass rate:\n%s", table)
	}
	if !strings.Contains(table, "Failures:") || !strings.Contains(table, "Task Two") {
		t.Errorf("table missing failure section:\n%s", table)
	}

	jsonStr, err := FormatJSON(result)
	if err != nil {
		t.Fatalf("FormatJSON: %v", err)
	}
	if !strings.Contains(jsonStr, `"suite_name": "test-suite"`) || !strings.Contains(jsonStr, `"pass_rate": 50`) {
		t.Errorf("JSON output unexpected: %s", jsonStr)
	}
}

func TestRunVerifyCommand(t *testing.T) {
	tempDir := t.TempDir()
	out, err := runVerifyCommand(context.Background(), tempDir, "echo hello-eval")
	if err != nil {
		t.Fatalf("runVerifyCommand failed: %v", err)
	}
	if !strings.Contains(out, "hello-eval") {
		t.Errorf("expected hello-eval in output, got %q", out)
	}
}

func TestRunnerExecutionWithApp(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("TERMIXGO_HOME", tempDir)

	task := Task{
		ID:          "mock-pass",
		Name:        "Simple Echo Task",
		Category:    "test",
		Prompt:      "Say hello",
		VerifyCmd:   "echo verified-success",
		ExpectedOut: "verified-success",
		TimeoutSecs: 30,
		Files: map[string]string{
			"dummy.txt": "dummy content\n",
		},
	}

	runner := &Runner{
		Suite: Suite{
			Name:  "mock-suite",
			Tasks: []Task{task},
		},
		AppFactory: func(workspace string) (*app.App, error) {
			return app.New(workspace)
		},
	}

	res, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("runner.Run: %v", err)
	}
	if res.TotalTasks != 1 {
		t.Errorf("expected 1 task, got %d", res.TotalTasks)
	}
	if !res.Results[0].Passed {
		t.Errorf("expected mock task to pass, got error: %s (output: %s)", res.Results[0].Error, res.Results[0].VerifyOutput)
	}
}
