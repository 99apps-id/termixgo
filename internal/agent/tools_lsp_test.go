package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The tools are exercised against a real gopls when it is installed; the
// protocol pieces have their own unit tests in internal/lsp.
func goplsWorkspace(t *testing.T) *Env {
	t.Helper()
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls is not installed")
	}
	if testing.Short() {
		t.Skip("starts a language server")
	}
	dir := t.TempDir()
	files := map[string]string{
		"go.mod":  "module example.com/demo\n\ngo 1.21\n",
		"main.go": "package main\n\nfunc greet(name string) string {\n\treturn \"hi \" + name\n}\n\nfunc main() {\n\t_ = greet(\"a\")\n\t_ = greet(\"b\")\n}\n",
		"bad.go":  "package main\n\nfunc broken() int {\n\treturn \"text\"\n}\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(CloseLanguageServers)
	return &Env{Workspace: dir}
}

func TestLSPToolsAgainstGopls(t *testing.T) {
	env := goplsWorkspace(t)
	ctx := context.Background()

	definition, _ := (&lspDefinitionTool{}).Run(ctx, env, map[string]any{"path": "main.go", "line": 8, "symbol": "greet"})
	if definition.IsError || !strings.Contains(definition.Output, "main.go:3:") {
		t.Fatalf("definition = %+v", definition)
	}

	references, _ := (&lspReferencesTool{}).Run(ctx, env, map[string]any{"path": "main.go", "line": 3, "symbol": "greet"})
	if references.IsError || !strings.Contains(references.Output, "3 reference(s)") {
		t.Fatalf("references = %+v", references)
	}

	hover, _ := (&lspHoverTool{}).Run(ctx, env, map[string]any{"path": "main.go", "line": 8, "symbol": "greet"})
	if hover.IsError || !strings.Contains(hover.Output, "func greet(name string) string") {
		t.Fatalf("hover = %+v", hover)
	}

	symbols, _ := (&lspSymbolsTool{}).Run(ctx, env, map[string]any{"path": "main.go"})
	if symbols.IsError || !strings.Contains(symbols.Output, "function greet") {
		t.Fatalf("symbols = %+v", symbols)
	}

	diagnostics, _ := (&lspDiagnosticsTool{}).Run(ctx, env, map[string]any{"path": "bad.go"})
	if diagnostics.IsError || !strings.Contains(diagnostics.Output, "bad.go:4:") || !strings.Contains(diagnostics.Output, "error") {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}

	renamed, _ := (&lspRenameTool{}).Run(ctx, env, map[string]any{"path": "main.go", "line": 3, "symbol": "greet", "new_name": "welcome"})
	if renamed.IsError || !strings.Contains(renamed.Output, "3 edit(s)") {
		t.Fatalf("rename = %+v", renamed)
	}
	data, _ := os.ReadFile(filepath.Join(env.Workspace, "main.go"))
	if strings.Contains(string(data), "greet") || strings.Count(string(data), "welcome") != 3 {
		t.Fatalf("file after rename:\n%s", data)
	}
}

func TestLSPToolsExplainAMissingServer(t *testing.T) {
	env := &Env{Workspace: t.TempDir()}
	path := filepath.Join(env.Workspace, "a.unknownext")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, _ := (&lspSymbolsTool{}).Run(context.Background(), env, map[string]any{"path": "a.unknownext"})
	if !result.IsError || !strings.Contains(result.Output, "no language server") {
		t.Fatalf("result = %+v", result)
	}
}

func TestFindIdentifierPrefersWholeWords(t *testing.T) {
	if got := findIdentifier("x := ab + a", "a"); got != 10 {
		t.Fatalf("got %d, want the standalone a at 10", got)
	}
	if got := findIdentifier("greeting", "greet"); got != 0 {
		t.Fatalf("got %d, want the fallback substring", got)
	}
}
