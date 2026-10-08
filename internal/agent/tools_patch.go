package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// patchOp is one file operation inside a patch document.
type patchOp struct {
	action string // "update", "add" or "delete"
	path   string
	hunks  []patchHunk
}

// patchHunk is one @@ section: context lines plus removals and additions in
// order. A nil keep distinguishes a blank context line from a missing one.
type patchHunk struct {
	lines []patchLine
}

type patchLine struct {
	kind string // " ", "-" or "+"
	text string
}

// applyPatchTool applies a multi-file patch in one call, Codex style. One
// patch replaces a whole sequence of edits, which keeps a refactor to a
// single approval instead of one per file.
type applyPatchTool struct{}

func (t *applyPatchTool) Name() string      { return "apply_patch" }
func (t *applyPatchTool) Aliases() []string { return []string{"patch"} }
func (t *applyPatchTool) Mutating() bool    { return true }
func (t *applyPatchTool) Risk() Risk        { return RiskEdit }
func (t *applyPatchTool) Label(a map[string]any) string {
	return "Applying a patch"
}
func (t *applyPatchTool) DoneLabel(a map[string]any) string {
	return "Applied a patch"
}
func (t *applyPatchTool) Description() string {
	return "Apply a patch to several files at once. Format:\n*** Begin Patch\n*** Update File: path\n@@\n context\n-removed\n+added\n*** Add File: path\n*** Delete File: path\n*** End Patch\nHunk lines need the leading space, minus or plus. Context and removals must match the file byte for byte. Prefer it over many edits for a refactor."
}
func (t *applyPatchTool) Schema() map[string]any {
	return object(map[string]any{
		"patch": strProp("The full patch document."),
	}, "patch")
}

func (t *applyPatchTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	document := argString(args, "patch")
	ops, err := parsePatch(document)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	var applied []string
	for _, op := range ops {
		path := resolvePath(env, op.path)
		if err := checkWorkspacePath(env, path); err != nil {
			return Result{Output: err.Error(), IsError: true}, nil
		}
		switch op.action {
		case "add":
			if err := applyPatchAdd(env, path, op); err != nil {
				return Result{Output: err.Error(), IsError: true}, nil
			}
		case "delete":
			if err := applyPatchDelete(env, path); err != nil {
				return Result{Output: err.Error(), IsError: true}, nil
			}
		default:
			if err := applyPatchUpdate(env, path, op); err != nil {
				return Result{Output: err.Error(), IsError: true}, nil
			}
		}
		applied = append(applied, op.action+" "+op.path)
	}
	return Result{Output: "Applied:\n  " + strings.Join(applied, "\n  ")}, nil
}

func parsePatch(document string) ([]patchOp, error) {
	trimmed := strings.TrimSpace(document)
	if !strings.Contains(trimmed, "*** Begin Patch") {
		return nil, fmt.Errorf("the patch must start with *** Begin Patch")
	}
	var ops []patchOp
	var current *patchOp
	var hunk *patchHunk
	flush := func() {
		if hunk != nil && current != nil {
			current.hunks = append(current.hunks, *hunk)
			hunk = nil
		}
	}
	for _, raw := range strings.Split(trimmed, "\n") {
		line := strings.TrimRight(raw, "\r")
		// Only a line that starts at column zero can be a directive: hunk
		// content always carries its kind prefix, so a context line whose
		// text happens to begin with stars must stay content. Matching
		// ignores trailing whitespace, which model output often adds after
		// a marker, so that alone never reads as a misspelled header.
		if strings.HasPrefix(line, "***") {
			directive := strings.TrimSpace(line)
			switch {
			case directive == "*** Begin Patch" || directive == "*** End Patch":
				flush()
			case strings.HasPrefix(directive, "*** Update File:"):
				flush()
				ops = append(ops, patchOp{action: "update", path: strings.TrimSpace(strings.TrimPrefix(directive, "*** Update File:"))})
				current = &ops[len(ops)-1]
			case strings.HasPrefix(directive, "*** Add File:"):
				flush()
				ops = append(ops, patchOp{action: "add", path: strings.TrimSpace(strings.TrimPrefix(directive, "*** Add File:"))})
				current = &ops[len(ops)-1]
			case strings.HasPrefix(directive, "*** Delete File:"):
				flush()
				ops = append(ops, patchOp{action: "delete", path: strings.TrimSpace(strings.TrimPrefix(directive, "*** Delete File:"))})
				current = &ops[len(ops)-1]
			default:
				// A misspelled header used to fall through silently and
				// leave its hunks attached to the previous file, or
				// orphaned. Refuse it so the model sees the typo instead
				// of editing the wrong file.
				return nil, fmt.Errorf("unknown patch directive %q; use *** Update File:, *** Add File: or *** Delete File", directive)
			}
			continue
		}
		switch {
		case strings.HasPrefix(line, "@@"):
			flush()
			if current == nil {
				return nil, fmt.Errorf("a @@ hunk arrived before any file header")
			}
			hunk = &patchHunk{}
		case current != nil && hunk != nil && line != "":
			kind := line[:1]
			if kind != " " && kind != "-" && kind != "+" {
				return nil, fmt.Errorf("hunk lines need a leading space, - or +, got %q", line)
			}
			hunk.lines = append(hunk.lines, patchLine{kind: kind, text: line[1:]})
		case current != nil && hunk != nil && line == "":
			hunk.lines = append(hunk.lines, patchLine{kind: " ", text: ""})
		}
	}
	flush()
	if len(ops) == 0 {
		return nil, fmt.Errorf("the patch holds no file operations")
	}
	for _, op := range ops {
		if strings.TrimSpace(op.path) == "" {
			return nil, fmt.Errorf("a file operation is missing its path")
		}
		if (op.action == "update" || op.action == "add") && len(op.hunks) == 0 {
			return nil, fmt.Errorf("%s %s holds no hunks", op.action, op.path)
		}
	}
	return ops, nil
}

func applyPatchAdd(env *Env, path string, op patchOp) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists; update it instead of adding", op.path)
	}
	// A new file leaves an absent marker, so undoing its addition removes
	// it again.
	backupFile(env, path)
	var builder strings.Builder
	for _, hunk := range op.hunks {
		for _, line := range hunk.lines {
			if line.kind == "-" {
				return fmt.Errorf("an add hunk cannot remove lines in %s", op.path)
			}
			builder.WriteString(line.text)
			builder.WriteByte('\n')
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(builder.String()), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", op.path, err)
	}
	return nil
}

func applyPatchDelete(env *Env, path string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("cannot delete %s: file not found", filepath.Base(path))
	}
	// The content is kept before the removal, so undo_edit restores it.
	backupFile(env, path)
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	return nil
}

func applyPatchUpdate(env *Env, path string, op patchOp) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", op.path, err)
	}
	// Matching runs on the LF view, but the file is written back with the
	// ending it already favours: normalising and writing LF rewrote every
	// line of a CRLF file, so a one-hunk patch showed up as a whole-file
	// diff. Added lines take the same ending as the file around them.
	ending := dominantEnding(string(data))
	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(content, "\n")
	// A trailing newline splits into a final empty element; drop it so line
	// numbers stay honest, and restore it when writing back.
	trailing := false
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
		trailing = true
	}
	cursor := 0
	for _, hunk := range op.hunks {
		at, ok := findHunk(lines, cursor, hunk)
		if !ok {
			return fmt.Errorf("a hunk in %s matches nowhere; copy the context byte for byte", op.path)
		}
		lines = spliceHunk(lines, at, hunk)
		cursor = at + newHunkLength(hunk)
	}
	output := strings.Join(lines, "\n")
	if trailing {
		output += "\n"
	}
	if ending == "\r\n" {
		output = strings.ReplaceAll(output, "\n", "\r\n")
	}
	// Every hunk matched, so the write below will happen: keep the previous
	// content first. A hunk that matches nowhere returns above, which is why
	// a refused patch leaves no backup behind.
	backupFile(env, path)
	if err := os.WriteFile(path, []byte(output), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", op.path, err)
	}
	return nil
}

// findHunk locates the hunk's old lines (context plus removals) at or after
// the cursor. Searching forward keeps multi-hunk patches ordered instead of
// matching every hunk against the first occurrence.
func findHunk(lines []string, cursor int, hunk patchHunk) (int, bool) {
	var want []string
	for _, line := range hunk.lines {
		if line.kind != "+" {
			want = append(want, line.text)
		}
	}
	if len(want) == 0 {
		return cursor, true
	}
	for at := cursor; at+len(want) <= len(lines); at++ {
		match := true
		for i, line := range want {
			if lines[at+i] != line {
				match = false
				break
			}
		}
		if match {
			return at, true
		}
	}
	return 0, false
}

func spliceHunk(lines []string, at int, hunk patchHunk) []string {
	var replacement []string
	for _, line := range hunk.lines {
		if line.kind != "-" {
			replacement = append(replacement, line.text)
		}
	}
	var kept []string
	for _, line := range hunk.lines {
		if line.kind != "+" {
			kept = append(kept, line.text)
		}
	}
	next := append([]string{}, lines[:at]...)
	next = append(next, replacement...)
	return append(next, lines[at+len(kept):]...)
}

func newHunkLength(hunk patchHunk) int {
	length := 0
	for _, line := range hunk.lines {
		if line.kind != "-" {
			length++
		}
	}
	return length
}
