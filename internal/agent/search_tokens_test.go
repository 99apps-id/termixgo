package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func searchFixture(t *testing.T) *Env {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"main.go":           "package main\n\nfunc parseConfig() {}\n\nfunc main() {\n\tparseConfig()\n\tparseConfig()\n}\n",
		"util/helper.go":    "package util\n\n// parseConfig is documented here\n",
		"package-lock.json": `{"parseConfig": "noise"}`,
		"web/app.min.js":    "function parseConfig(){}",
		"out/generated.go":  "package out\n\nfunc parseConfig() {}\n",
		".gitignore":        "out/\n",
	}
	for name, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := exec.LookPath("git"); err == nil {
		cmd := exec.Command("git", "init", "-q")
		cmd.Dir = dir
		_ = cmd.Run()
	}
	return &Env{Workspace: dir}
}

func runGrep(t *testing.T, env *Env, args map[string]any) string {
	t.Helper()
	result, _ := (&grepTool{}).Run(context.Background(), env, args)
	if result.IsError {
		t.Fatalf("grep failed: %s", result.Output)
	}
	return result.Output
}

func TestGrepGroupsByFileAndSkipsNoise(t *testing.T) {
	env := searchFixture(t)
	out := runGrep(t, env, map[string]any{"pattern": "parseConfig"})
	if !strings.Contains(out, "main.go\n  3: func parseConfig() {}\n  6: parseConfig()") {
		t.Fatalf("content should be grouped under one path line:\n%s", out)
	}
	if strings.Count(out, "main.go") != 1 {
		t.Fatalf("a path must be printed once:\n%s", out)
	}
	for _, noise := range []string{"package-lock.json", "app.min.js"} {
		if strings.Contains(out, noise) {
			t.Fatalf("%s should be skipped:\n%s", noise, out)
		}
	}
	if withNoise := runGrep(t, env, map[string]any{"pattern": "parseConfig", "include_noise": true}); !strings.Contains(withNoise, "package-lock.json") {
		t.Fatalf("include_noise should search lockfiles:\n%s", withNoise)
	}
}

func TestGrepHonoursGitignore(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	env := searchFixture(t)
	if out := runGrep(t, env, map[string]any{"pattern": "parseConfig"}); strings.Contains(out, "out/generated.go") {
		t.Fatalf("an ignored directory must not be searched:\n%s", out)
	}
	globbed, _ := (&globTool{}).Run(context.Background(), env, map[string]any{"pattern": "**/*.go"})
	if strings.Contains(globbed.Output, "out/generated.go") || !strings.Contains(globbed.Output, "util/helper.go") {
		t.Fatalf("glob should honour .gitignore too:\n%s", globbed.Output)
	}
}

func TestGrepFilesAndCountModes(t *testing.T) {
	env := searchFixture(t)
	files := runGrep(t, env, map[string]any{"pattern": "parseConfig", "output_mode": "files"})
	if !strings.HasPrefix(files, "2 file(s) match") || strings.Contains(files, "func ") {
		t.Fatalf("files mode lists paths only:\n%s", files)
	}
	counts := runGrep(t, env, map[string]any{"pattern": "parseConfig", "output_mode": "count"})
	if !strings.Contains(counts, "main.go: 3") || !strings.Contains(counts, "util/helper.go: 1") {
		t.Fatalf("count mode gives matches per file:\n%s", counts)
	}
}

func TestGrepContextLines(t *testing.T) {
	env := searchFixture(t)
	out := runGrep(t, env, map[string]any{"pattern": "func main", "context": 1})
	if !strings.Contains(out, "  4- \n  5: func main() {\n  6- parseConfig()") {
		t.Fatalf("context should surround the match:\n%s", out)
	}
}

func TestMemoryPromptDropsApprovalLinesAndFitsTheBudget(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("TERMIXGO_HOME", t.TempDir())
	memory := NewMemory(workspace)
	_ = memory.Remember("approval allowed_once run_command", "project")
	_ = memory.Remember("tests run with make check", "project")
	block := memory.PromptBlock()
	if strings.Contains(block, "approval allowed_once") || !strings.Contains(block, "make check") {
		t.Fatalf("approval bookkeeping must stay out of the prompt:\n%s", block)
	}
	for index := 0; index < 100; index++ {
		_ = memory.Remember(strings.Repeat("x", 200)+string(rune('a'+index%26))+strings.Repeat("y", index), "project")
	}
	if size := len(memory.PromptBlock()); size > maxPromptMemoryBytes+400 {
		t.Fatalf("the prompt block is %d bytes, over the budget", size)
	}
}
