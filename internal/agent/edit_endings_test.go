package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// TestApplyEditsKeepsEndingsOfUntouchedLines is the line-ending guard.
//
// CRLF used to be a property of the whole file: if any line ended with \r\n,
// every \n in the result was written back as \r\n. In a file that mixes the two
// that rewrote lines the edit never mentioned, so a one-line change arrived as a
// whole-file diff.
func TestApplyEditsKeepsEndingsOfUntouchedLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mixed.txt")
	if err := os.WriteFile(path, []byte("a\r\nb\r\n\nc\r\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	env := &Env{Workspace: dir}

	if _, err := applyEdits(env, path, []editInstruction{{Old: "b", New: "B"}}); err != nil {
		t.Fatalf("applyEdits: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got := string(data); got != "a\r\nB\r\n\nc\r\n" {
		t.Errorf("untouched line endings were rewritten\n got: %q\nwant: %q", got, "a\r\nB\r\n\nc\r\n")
	}
}

// TestApplyEditsUsesTheMatchedLinesOwnEnding covers a multi-line needle: the
// replacement inherits the terminator of the region it replaced, not whatever the
// rest of the file happens to use.
func TestApplyEditsUsesTheMatchedLinesOwnEnding(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mixed.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\r\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	env := &Env{Workspace: dir}

	if _, err := applyEdits(env, path, []editInstruction{{Old: "one\ntwo", New: "A\nB"}}); err != nil {
		t.Fatalf("applyEdits: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got := string(data); got != "A\nB\nthree\r\n" {
		t.Errorf("the LF region should stay LF\n got: %q\nwant: %q", got, "A\nB\nthree\r\n")
	}
}

// TestApplyEditsMatchesACRLFSpelledNeedle keeps the useful half of the old
// behaviour: a needle copied out of read_file is always LF, and a uniform CRLF
// file must still match it.
func TestApplyEditsMatchesACRLFSpelledNeedle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "crlf.txt")
	if err := os.WriteFile(path, []byte("alpha\r\nbeta\r\ngamma\r\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	env := &Env{Workspace: dir}

	result, err := applyEdits(env, path, []editInstruction{{Old: "beta\r\ngamma", New: "stop\nreturn"}})
	if err != nil {
		t.Fatalf("a CRLF needle should match a CRLF file: %v", err)
	}
	if result == "" {
		t.Errorf("applyEdits returned no summary")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got := string(data); got != "alpha\r\nstop\r\nreturn\r\n" {
		t.Errorf("the replacement should follow the matched region\n got: %q\nwant: %q", got, "alpha\r\nstop\r\nreturn\r\n")
	}
}
