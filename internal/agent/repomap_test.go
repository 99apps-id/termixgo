package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateRepoMap(t *testing.T) {
	dir := t.TempDir()

	pkgDir := filepath.Join(dir, "pkg", "calc")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	goFile := filepath.Join(pkgDir, "calc.go")
	goSrc := `package calc

type Calculator struct {
	Base int
}

type Operations interface {
	Compute() int
}

func (c *Calculator) Add(v int) int {
	return c.Base + v
}

func NewCalculator() *Calculator {
	return &Calculator{Base: 0}
}
`
	if err := os.WriteFile(goFile, []byte(goSrc), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	pyFile := filepath.Join(dir, "script.py")
	pySrc := `
class DataPipeline:
    pass

def process_records(limit=100):
    pass
`
	if err := os.WriteFile(pyFile, []byte(pySrc), 0o644); err != nil {
		t.Fatalf("write py: %v", err)
	}

	res, err := GenerateRepoMap(dir, RepoMapOptions{ExportedOnly: false})
	if err != nil {
		t.Fatalf("GenerateRepoMap error: %v", err)
	}

	if !strings.Contains(res, "pkg/calc/calc.go:") {
		t.Errorf("repo map should include pkg/calc/calc.go, got:\n%s", res)
	}
	if !strings.Contains(res, "type Calculator struct") {
		t.Errorf("repo map should list Calculator struct, got:\n%s", res)
	}
	if !strings.Contains(res, "type Operations interface") {
		t.Errorf("repo map should list Operations interface, got:\n%s", res)
	}
	if !strings.Contains(res, "func (*Calculator) Add(v int) int") {
		t.Errorf("repo map should list Add method, got:\n%s", res)
	}
	if !strings.Contains(res, "script.py:") || !strings.Contains(res, "class DataPipeline") {
		t.Errorf("repo map should include python class, got:\n%s", res)
	}
}

func TestRepoMapTool(t *testing.T) {
	dir := t.TempDir()
	env := &Env{Workspace: dir}

	_ = os.WriteFile(filepath.Join(dir, "math.go"), []byte("package math\n\nfunc Square(x int) int { return x * x }\n"), 0o644)

	tool := &repoMapTool{}
	result, err := tool.Run(context.Background(), env, map[string]any{"max_tokens": 1000})
	if err != nil {
		t.Fatalf("run error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected ok result, got error: %s", result.Output)
	}
	if !strings.Contains(result.Output, "math.go:") || !strings.Contains(result.Output, "func Square") {
		t.Errorf("expected Square in repo map output, got: %s", result.Output)
	}
}
