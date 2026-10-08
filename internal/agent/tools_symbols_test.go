package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func symbolEnv(t *testing.T) *Env {
	t.Helper()
	workspace := t.TempDir()
	return &Env{Workspace: workspace, Todos: NewTodoStore(), Memory: NewMemory(workspace), Trusted: true}
}

func writeSymbolFile(t *testing.T, workspace, name, body string) {
	t.Helper()
	path := filepath.Join(workspace, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runSymbolSearch(t *testing.T, env *Env, query string) string {
	t.Helper()
	result, err := (&symbolSearchTool{}).Run(context.Background(), env, map[string]any{"query": query})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("symbol_search %q: %q", query, result.Output)
	}
	return result.Output
}

// TestSymbolSearchFindsGoSymbols pins the precise Go path: functions,
// methods, structs and interfaces with their real line numbers.
func TestSymbolSearchFindsGoSymbols(t *testing.T) {
	env := symbolEnv(t)
	writeSymbolFile(t, env.Workspace, "server.go", `package main

type Server struct {
	addr string
}

func NewServer(addr string) *Server {
	return &Server{addr: addr}
}

func (s *Server) Start() error {
	return nil
}
`)
	output := runSymbolSearch(t, env, "server")
	for _, want := range []string{
		"server.go:3: struct Server",
		"server.go:7: func NewServer",
		"server.go:11: method Start",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("missing %q in:\n%s", want, output)
		}
	}
	if strings.Contains(output, "addr string") && strings.Contains(output, "var addr") {
		t.Errorf("fields must not be reported as symbols:\n%s", output)
	}
}

// TestSymbolSearchMatchesStructurally covers the line-pattern languages and
// the discipline that a bare call is not a declaration.
func TestSymbolSearchMatchesStructurally(t *testing.T) {
	env := symbolEnv(t)
	writeSymbolFile(t, env.Workspace, "app.py", "class Handler:\n    def handle_request(self):\n        pass\n")
	writeSymbolFile(t, env.Workspace, "web/app.ts", "export class Router {\n  resolve(path: string) {\n    return path;\n  }\n}\nresolve(stray);\n")
	writeSymbolFile(t, env.Workspace, "lib.rs", "pub struct Config {\n    pub port: u16,\n}\n\npub fn load_config() -> Config {\n    Config { port: 8080 }\n}\n")

	output := runSymbolSearch(t, env, "handl")
	if !strings.Contains(output, "app.py:1: class Handler") {
		t.Errorf("missing the Python class in:\n%s", output)
	}
	if !strings.Contains(output, "app.py:2: func handle_request") {
		t.Errorf("missing the Python method in:\n%s", output)
	}

	output = runSymbolSearch(t, env, "resolv")
	if !strings.Contains(output, "web/app.ts:2: method resolve") {
		t.Errorf("missing the indented TypeScript method in:\n%s", output)
	}
	if strings.Count(output, "resolve") > 4 {
		t.Errorf("the bare call must not be reported as a declaration:\n%s", output)
	}

	output = runSymbolSearch(t, env, "config")
	if !strings.Contains(output, "lib.rs:1: struct Config") {
		t.Errorf("missing the Rust struct in:\n%s", output)
	}
	if !strings.Contains(output, "lib.rs:5: func load_config") {
		t.Errorf("missing the Rust function in:\n%s", output)
	}
}

// TestSymbolSearchNeedsAQuery refuses an unbounded listing, which on a large
// tree would be thousands of rows.
func TestSymbolSearchNeedsAQuery(t *testing.T) {
	env := symbolEnv(t)
	result, _ := (&symbolSearchTool{}).Run(context.Background(), env, map[string]any{"query": "  "})
	if !result.IsError {
		t.Errorf("an empty query must be refused")
	}
}

// TestSymbolSearchSkipsStateAndGeneratedDirs keeps the agent's own database
// and vendored trees out of navigation results.
func TestSymbolSearchSkipsStateAndGeneratedDirs(t *testing.T) {
	env := symbolEnv(t)
	writeSymbolFile(t, env.Workspace, "real.go", "package main\n\nfunc VisibleSymbol() {}\n")
	writeSymbolFile(t, env.Workspace, ".termixgo/edit-backups/real.go/1.bak", "package main\n\nfunc VisibleSymbol() {}\n")
	writeSymbolFile(t, env.Workspace, "node_modules/dep/index.js", "function VisibleSymbol() {}\n")
	output := runSymbolSearch(t, env, "visiblesymbol")
	if count := strings.Count(output, "VisibleSymbol"); count != 1 {
		t.Errorf("want exactly the real definition, got %d mentions in:\n%s", count, output)
	}
	if strings.Contains(output, ".termixgo") || strings.Contains(output, "node_modules") {
		t.Errorf("state and generated trees must be skipped:\n%s", output)
	}
}
