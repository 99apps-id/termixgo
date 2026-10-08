package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadFileWindowedMatchesDirectRead pins the two read paths together: a
// file served through the streaming scan must describe itself exactly the way
// the whole-file read does, or the model learns two dialects for one tool.
func TestReadFileWindowedMatchesDirectRead(t *testing.T) {
	env := testEnv(t)
	body := "package main\n\n// caf\u00e9 na\u00efve \u65e5\u672c\u8a9e\nfunc main() {}\n"
	path := filepath.Join(env.Workspace, "sample.go")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	direct, err := (&readFileTool{}).Run(context.Background(), env, map[string]any{"path": "sample.go"})
	if err != nil || direct.IsError {
		t.Fatalf("direct read: err=%v result=%+v", err, direct)
	}
	streamed, err := readFileWindowed(env, filepath.Join(env.Workspace, "sample.go"), 1, maxReadLines)
	if err != nil || streamed.IsError {
		t.Fatalf("streamed read: err=%v result=%+v", err, streamed)
	}
	if direct.Output != streamed.Output {
		t.Errorf("streamed output differs:\ndirect:   %q\nstreamed: %q", direct.Output, streamed.Output)
	}

	crlf := "one\r\ntwo\r\nthree\r\n"
	crlfPath := filepath.Join(env.Workspace, "crlf.txt")
	if err := os.WriteFile(crlfPath, []byte(crlf), 0o644); err != nil {
		t.Fatal(err)
	}
	direct, _ = (&readFileTool{}).Run(context.Background(), env, map[string]any{"path": "crlf.txt"})
	streamed, _ = readFileWindowed(env, crlfPath, 1, maxReadLines)
	if direct.Output != streamed.Output {
		t.Errorf("CRLF streamed output differs:\ndirect:   %q\nstreamed: %q", direct.Output, streamed.Output)
	}
}

// TestReadFileStreamsLargeFiles proves a file past the direct cap is still
// readable by window instead of being loaded whole or refused.
func TestReadFileStreamsLargeFiles(t *testing.T) {
	env := testEnv(t)
	const lines = 200000
	var builder strings.Builder
	builder.Grow(lines * 12)
	for index := 1; index <= lines; index++ {
		fmt.Fprintf(&builder, "line %06d\n", index)
	}
	path := filepath.Join(env.Workspace, "big.log")
	if err := os.WriteFile(path, []byte(builder.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(path); info.Size() <= maxReadDirectBytes {
		t.Fatalf("the fixture must exceed the direct cap, got %d bytes", info.Size())
	}

	result, err := (&readFileTool{}).Run(context.Background(), env, map[string]any{
		"path": "big.log", "offset": 199999, "limit": 10,
	})
	if err != nil || result.IsError {
		t.Fatalf("windowed read: err=%v result=%+v", err, result)
	}
	// The trailing newline is the extra empty element Split reports.
	if !strings.Contains(result.Output, fmt.Sprintf("lines 199999-200001 of %d", lines+1)) {
		t.Errorf("header counts the wrong window: %q", headerLine(result.Output))
	}
	if !strings.Contains(result.Output, "line 199999\nline 200000\n") {
		t.Errorf("window content is wrong: %q", result.Output)
	}

	pastEnd, err := (&readFileTool{}).Run(context.Background(), env, map[string]any{
		"path": "big.log", "offset": 999999999, "limit": 10,
	})
	if err != nil || pastEnd.IsError {
		t.Fatalf("past-end read: err=%v result=%+v", err, pastEnd)
	}
	if !strings.Contains(pastEnd.Output, fmt.Sprintf("lines %d-%d of %d", lines+1, lines+1, lines+1)) {
		t.Errorf("a past-end offset must clamp to the last line: %q", headerLine(pastEnd.Output))
	}
}

// TestReadFileStreamsAGiantLine bounds the worst case: one line carrying
// megabytes must not blow up the window or the output.
func TestReadFileStreamsAGiantLine(t *testing.T) {
	env := testEnv(t)
	path := filepath.Join(env.Workspace, "wide.log")
	giant := strings.Repeat("x", maxReadDirectBytes+1024)
	if err := os.WriteFile(path, []byte(giant), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := (&readFileTool{}).Run(context.Background(), env, map[string]any{"path": "wide.log"})
	if err != nil || result.IsError {
		t.Fatalf("giant line read: err=%v result=%+v", err, result)
	}
	if !strings.Contains(result.Output, "[clipped at 64 KB]") {
		t.Errorf("a giant line must be clipped visibly: %q", headerLine(result.Output))
	}
	if len(result.Output) > maxReadBytes+512 {
		t.Errorf("output length = %d, want it near the 64 KB cap", len(result.Output))
	}
}

func headerLine(text string) string {
	if index := strings.Index(text, "\n"); index >= 0 {
		return text[:index]
	}
	return text
}
