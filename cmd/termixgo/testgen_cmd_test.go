package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunTestGenCommand(t *testing.T) {
	dir := t.TempDir()
	originalWd, _ := os.Getwd()
	_ = os.Chdir(dir)
	defer func() { _ = os.Chdir(originalWd) }()

	codeFile := filepath.Join(dir, "greeter.go")
	_ = os.WriteFile(codeFile, []byte("package greeter\n\nfunc Greet(name string) string { return \"Hello \" + name }\n"), 0o644)

	var stdout bytes.Buffer
	err := runTestGen([]string{"--dry-run", "greeter.go"}, &stdout)
	if err != nil {
		t.Fatalf("runTestGen dry run error: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "TestGreet(t *testing.T)") {
		t.Errorf("expected TestGreet in dry run output, got: %s", out)
	}

	// Missing args error
	err = runTestGen([]string{}, &stdout)
	if err == nil {
		t.Errorf("expected error when no file specified")
	}
}
