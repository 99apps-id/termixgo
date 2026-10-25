package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunRepoMapCommand(t *testing.T) {
	dir := t.TempDir()
	originalWd, _ := os.Getwd()
	_ = os.Chdir(dir)
	defer func() { _ = os.Chdir(originalWd) }()

	codeFile := filepath.Join(dir, "calc.go")
	_ = os.WriteFile(codeFile, []byte("package calc\n\ntype Calc struct{}\nfunc (c *Calc) Add(a, b int) int { return a + b }\n"), 0o644)

	var stdout bytes.Buffer
	err := runRepoMap([]string{"--path", dir}, &stdout)
	if err != nil {
		t.Fatalf("runRepoMap error: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "calc.go:") || !strings.Contains(out, "type Calc struct") {
		t.Errorf("expected calc.go in repomap output, got: %s", out)
	}
}
