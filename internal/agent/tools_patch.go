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

// patchPlan is one file's planned outcome. Every operation is planned before
// any of them is written, so a patch whose later operation cannot apply leaves
// the tree exactly as it was. Applying as it went used to leave the earlier
// files changed and report only an error, which is a half-patch the model
// cannot see.
type patchPlan struct {
	// path is the resolved absolute path; display is it as the patch wrote it.
	path    string
	display string
	// action is the first operation that touched the file, for the summary.
	action  string
	content []byte
	// remove marks a file this patch deletes.
	remove bool
}

func (t *applyPatchTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	document := argString(args, "patch")
	ops, err := parsePatch(document)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	plans, order, err := planPatch(env, ops)
	if err != nil {
		return Result{Output: fmt.Sprintf("%v (nothing was written)", err), IsError: true}, nil
	}
	applied := make([]string, 0, len(order))
	for _, path := range order {
		plan := plans[path]
		if err := applyPatchPlan(env, plan); err != nil {
			if len(applied) == 0 {
				return Result{Output: fmt.Sprintf("%v (no file was changed before it)", err), IsError: true}, nil
			}
			return Result{Output: fmt.Sprintf("%v; already applied: %s", err, strings.Join(applied, ", ")), IsError: true}, nil
		}
		applied = append(applied, plan.action+" "+plan.display)
	}
	return Result{Output: "Applied:\n  " + strings.Join(applied, "\n  ")}, nil
}

// planPatch works out the result of every operation without writing anything.
//
// Two operations on one file are chained: the second reads the first one's
// result rather than the bytes on disk, so one file is written once and a patch
// that updates the same file twice still lands the way the patch reads.
func planPatch(env *Env, ops []patchOp) (map[string]*patchPlan, []string, error) {
	plans := make(map[string]*patchPlan, len(ops))
	order := make([]string, 0, len(ops))
	for _, op := range ops {
		path := resolvePath(env, op.path)
		if err := checkWorkspacePath(env, path); err != nil {
			return nil, nil, err
		}
		plan, seen := plans[path]
		if !seen {
			plan = &patchPlan{path: path, display: op.path}
			plans[path] = plan
			order = append(order, path)
		}
		if plan.remove {
			return nil, nil, fmt.Errorf("%s is deleted by an earlier operation in this patch, so a later one cannot change it", op.path)
		}
		switch op.action {
		case "add":
			if plan.action != "" {
				return nil, nil, fmt.Errorf("%s is already changed by this patch, so it cannot also be added", op.path)
			}
			if _, err := os.Stat(path); err == nil {
				return nil, nil, fmt.Errorf("%s already exists; update it instead of adding", op.path)
			}
			content, err := addedContent(op)
			if err != nil {
				return nil, nil, err
			}
			plan.action, plan.content = "add", content
		case "delete":
			if plan.action == "" {
				if _, err := os.Stat(path); err != nil {
					return nil, nil, fmt.Errorf("cannot delete %s: file not found", filepath.Base(path))
				}
				plan.action = "delete"
			}
			plan.remove = true
		default:
			// The base is what this patch has already planned for the file, or
			// the file on disk the first time it is touched.
			base := plan.content
			if plan.action == "" {
				data, err := os.ReadFile(path)
				if err != nil {
					return nil, nil, fmt.Errorf("read %s: %w", op.path, err)
				}
				base = data
			}
			content, err := patchedContent(op, base)
			if err != nil {
				return nil, nil, err
			}
			if plan.action == "" {
				plan.action = "update"
			}
			plan.content = content
		}
	}
	return plans, order, nil
}

// addedContent renders the body of an add operation.
func addedContent(op patchOp) ([]byte, error) {
	var builder strings.Builder
	for _, hunk := range op.hunks {
		for _, line := range hunk.lines {
			if line.kind == "-" {
				return nil, fmt.Errorf("an add hunk cannot remove lines in %s", op.path)
			}
			builder.WriteString(line.text)
			builder.WriteByte('\n')
		}
	}
	return []byte(builder.String()), nil
}

// patchedContent applies an update operation to bytes already in hand. Matching
// runs on the LF view, but the result keeps the ending the file already favours:
// normalising and writing LF rewrote every line of a CRLF file, so a one-hunk
// patch showed up as a whole-file diff. Added lines take the same ending as the
// file around them.
func patchedContent(op patchOp, data []byte) ([]byte, error) {
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
			return nil, fmt.Errorf("a hunk in %s matches nowhere; copy the context byte for byte", op.path)
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
	return []byte(output), nil
}

// applyPatchPlan performs one planned outcome. The previous content is kept
// before it is replaced, so undo_edit can bring it back; a file that does not
// exist yet leaves an absent marker instead, so undoing its creation removes it
// again.
func applyPatchPlan(env *Env, plan *patchPlan) error {
	backupFile(env, plan.path)
	if plan.remove {
		if err := os.Remove(plan.path); err != nil {
			return fmt.Errorf("delete %s: %w", plan.display, err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(plan.path), 0o755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}
	if err := os.WriteFile(plan.path, plan.content, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", plan.display, err)
	}
	return nil
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
