package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodeOutlineGo(t *testing.T) {
	workspace := t.TempDir()
	env := &Env{Workspace: workspace, Trusted: true}
	tool := &codeOutlineTool{}

	goSource := `package example

type Config struct {
	Name string
	Port int
}

type Runner interface {
	Run(ctx context.Context) error
}

func NewConfig(name string) *Config {
	return &Config{Name: name}
}

func (c *Config) Validate() error {
	return nil
}
`
	filePath := filepath.Join(workspace, "sample.go")
	if err := os.WriteFile(filePath, []byte(goSource), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := tool.Run(context.Background(), env, map[string]any{"path": "sample.go"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IsError {
		t.Fatalf("error: %s", res.Output)
	}

	expected := []string{
		"package example",
		"type Config struct",
		"type Runner interface",
		"func NewConfig",
		"func (*Config) Validate",
	}
	for _, exp := range expected {
		if !strings.Contains(res.Output, exp) {
			t.Errorf("output missing %q:\n%s", exp, res.Output)
		}
	}
}

func TestCodeOutlineMarkdown(t *testing.T) {
	workspace := t.TempDir()
	env := &Env{Workspace: workspace, Trusted: true}
	tool := &codeOutlineTool{}

	mdSource := `# Title
Some introduction.

## Section 1
Details here.

### Subsection 1.1
More details.

## Section 2
Conclusion.
`
	filePath := filepath.Join(workspace, "doc.md")
	if err := os.WriteFile(filePath, []byte(mdSource), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := tool.Run(context.Background(), env, map[string]any{"path": "doc.md", "depth": 3})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IsError {
		t.Fatalf("error: %s", res.Output)
	}

	expected := []string{
		"# Title",
		"## Section 1",
		"### Subsection 1.1",
		"## Section 2",
	}
	for _, exp := range expected {
		if !strings.Contains(res.Output, exp) {
			t.Errorf("output missing %q:\n%s", exp, res.Output)
		}
	}
}

func TestCodeOutlinePythonAndTS(t *testing.T) {
	workspace := t.TempDir()
	env := &Env{Workspace: workspace, Trusted: true}
	tool := &codeOutlineTool{}

	pySource := `class Agent:
    def __init__(self, name):
        self.name = name

    def run(self):
        pass

def helper():
    return True
`
	if err := os.WriteFile(filepath.Join(workspace, "script.py"), []byte(pySource), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := tool.Run(context.Background(), env, map[string]any{"path": "script.py"})
	if err != nil || res.IsError {
		t.Fatalf("Run python failed: %v, %s", err, res.Output)
	}
	if !strings.Contains(res.Output, "class Agent") || !strings.Contains(res.Output, "def run") {
		t.Errorf("python outline missing symbols: %s", res.Output)
	}

	tsSource := `export interface User {
    id: string;
}

export class Service {
    start() {}
}

export function connect(): void {}
`
	if err := os.WriteFile(filepath.Join(workspace, "app.ts"), []byte(tsSource), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err = tool.Run(context.Background(), env, map[string]any{"path": "app.ts"})
	if err != nil || res.IsError {
		t.Fatalf("Run ts failed: %v, %s", err, res.Output)
	}
	if !strings.Contains(res.Output, "interface User") || !strings.Contains(res.Output, "class Service") || !strings.Contains(res.Output, "function connect") {
		t.Errorf("ts outline missing symbols: %s", res.Output)
	}
}
