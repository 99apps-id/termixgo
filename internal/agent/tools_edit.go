package agent

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// editTool replaces an exact string in one file.
type editTool struct{}

func (t *editTool) Name() string      { return "edit" }
func (t *editTool) Aliases() []string { return []string{"replace"} }
func (t *editTool) Mutating() bool    { return true }
func (t *editTool) Risk() Risk        { return RiskEdit }
func (t *editTool) Label(a map[string]any) string {
	return "Editing " + displayName(a)
}
func (t *editTool) DoneLabel(a map[string]any) string {
	return "Edited " + displayName(a)
}
func (t *editTool) Description() string {
	return "Replace an exact string in a file. old_string must match the file byte for byte, including indentation, and must be unique unless replace_all is true. Read the file first. Do not include line-number prefixes."
}
func (t *editTool) Schema() map[string]any {
	return object(map[string]any{
		"path":        strProp("File to edit."),
		"old_string":  strProp("Exact existing text to replace. Copy it verbatim from the file."),
		"new_string":  strProp("Replacement text. Use an empty string to delete."),
		"replace_all": boolProp("Replace every occurrence instead of requiring uniqueness."),
	}, "path", "old_string", "new_string")
}

// editInstruction is one replacement inside multi_edit.
type editInstruction struct {
	Old        string
	New        string
	ReplaceAll bool
}

func (t *editTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	raw := argString(args, "path", "file")
	if raw == "" {
		return Result{Output: "path is required", IsError: true}, nil
	}
	instructions := []editInstruction{{
		Old:        argString(args, "old_string", "old"),
		New:        argString(args, "new_string", "new"),
		ReplaceAll: argBool(args, "replace_all", false),
	}}
	if _, present := args["old_string"]; !present && args["old"] == nil {
		return Result{Output: "old_string is required", IsError: true}, nil
	}
	path := resolvePath(env, raw)
	if err := checkWorkspacePath(env, path); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	result, err := applyEdits(env, path, instructions)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	return Result{Output: result}, nil
}

// multiEditTool applies several replacements atomically.
type multiEditTool struct{}

func (t *multiEditTool) Name() string      { return "multi_edit" }
func (t *multiEditTool) Aliases() []string { return []string{"multi_replace"} }
func (t *multiEditTool) Mutating() bool    { return true }
func (t *multiEditTool) Risk() Risk        { return RiskEdit }
func (t *multiEditTool) Label(a map[string]any) string {
	return "Editing " + displayName(a)
}
func (t *multiEditTool) DoneLabel(a map[string]any) string {
	return "Edited " + displayName(a)
}
func (t *multiEditTool) Description() string {
	return "Apply several exact-string replacements to one file atomically. Any missing or ambiguous old_string aborts the whole batch, so nothing is written unless every edit applies."
}
func (t *multiEditTool) Schema() map[string]any {
	return object(map[string]any{
		"path": strProp("File to edit."),
		"edits": arrayProp("Replacements, applied in order.", object(map[string]any{
			"old_string":  strProp("Exact existing text."),
			"new_string":  strProp("Replacement text."),
			"replace_all": boolProp("Replace every occurrence."),
		}, "old_string", "new_string")),
	}, "path", "edits")
}

func (t *multiEditTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	raw := argString(args, "path", "file")
	if raw == "" {
		return Result{Output: "path is required", IsError: true}, nil
	}
	instructions, err := parseEdits(args["edits"])
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	if len(instructions) == 0 {
		return Result{Output: "edits must contain at least one replacement", IsError: true}, nil
	}
	path := resolvePath(env, raw)
	if err := checkWorkspacePath(env, path); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	result, applyErr := applyEdits(env, path, instructions)
	if applyErr != nil {
		return Result{Output: applyErr.Error(), IsError: true}, nil
	}
	return Result{Output: result}, nil
}

func parseEdits(value any) ([]editInstruction, error) {
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("edits must be an array")
	}
	instructions := make([]editInstruction, 0, len(items))
	for index, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("edit %d must be an object", index+1)
		}
		old := argString(entry, "old_string", "old")
		new := argString(entry, "new_string", "new")
		instructions = append(instructions, editInstruction{
			Old:        old,
			New:        new,
			ReplaceAll: argBool(entry, "replace_all", false),
		})
	}
	return instructions, nil
}

// applyEdits runs the replacements against the file on disk.
//
// Matching is done against the file's own bytes rather than a normalised copy,
// so a line the edit never mentions keeps the terminator it had. Deciding CRLF
// from "this file contains a \r\n somewhere" and then writing every \n back as
// CRLF rewrote every line of a mixed-ending file, so a one-line fix showed up as
// a whole-file diff and buried the change in it.
func applyEdits(env *Env, path string, instructions []editInstruction) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%s", openError(err, displayPath(env, path)))
	}
	text := string(data)
	dominant := dominantEnding(text)

	totalApplied := 0
	for index, instruction := range instructions {
		// What the caller sent is still normalised: a model copying from
		// read_file always sees LF, whatever the file holds.
		old := strings.ReplaceAll(instruction.Old, "\r\n", "\n")
		replacement := strings.ReplaceAll(instruction.New, "\r\n", "\n")
		if old == "" {
			return "", fmt.Errorf("edit %d: old_string must not be empty", index+1)
		}
		if old == replacement {
			return "", fmt.Errorf("edit %d: old_string and new_string are identical", index+1)
		}
		found := presentSpellings(text, old)
		if len(found) == 0 {
			// Diagnosed against a normalised view, because the caller's needle is
			// one: comparing it to raw CRLF bytes would blame the caller's
			// spacing for what is a line-ending difference.
			return "", fmt.Errorf("edit %d: old_string was not found in %s. %s", index+1, displayPath(env, path), diagnose(strings.ReplaceAll(text, "\r\n", "\n"), old))
		}
		count := 0
		for _, spelling := range found {
			count += strings.Count(text, spelling)
		}
		if count > 1 && !instruction.ReplaceAll {
			return "", fmt.Errorf("edit %d: old_string appears %d times (lines %s); add more context or set replace_all", index+1, count, strings.Join(ambiguousLines(text, found), ", "))
		}
		for _, spelling := range found {
			body := replacement
			if inheritedEnding(spelling, dominant) == "\r\n" {
				body = strings.ReplaceAll(body, "\n", "\r\n")
			}
			occurrences := strings.Count(text, spelling)
			if instruction.ReplaceAll {
				text = strings.ReplaceAll(text, spelling, body)
			} else {
				text = strings.Replace(text, spelling, body, 1)
			}
			totalApplied += occurrences
		}
	}

	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %v", displayPath(env, path), err)
	}
	return fmt.Sprintf("Applied %d replacement(s) in %s", totalApplied, displayPath(env, path)), nil
}

// spellings are the byte forms an old_string can take in a file. A needle that
// spans lines and is written with \n matches an LF region and not a CRLF one,
// so both have to be looked for; a single-line needle is identical either way.
func spellings(old string) []string {
	if !strings.Contains(old, "\n") {
		return []string{old}
	}
	crlf := strings.ReplaceAll(old, "\n", "\r\n")
	if crlf == old {
		return []string{old}
	}
	// CRLF first: a block copied out of a mixed file most likely carries its
	// own terminators, and the search for the two forms cannot overlap, since
	// the LF spelling demands a bare \n exactly where the CRLF one has \r.
	return []string{crlf, old}
}

// presentSpellings keeps only the forms the text actually holds.
func presentSpellings(text, old string) []string {
	var found []string
	for _, spelling := range spellings(old) {
		if strings.Count(text, spelling) > 0 {
			found = append(found, spelling)
		}
	}
	return found
}

// dominantEnding is what a single-line match inherits. The matched text holds no
// line break to copy, so the convention the file already favours wins, which is
// what the whole-file rewrite used to do for the uniform case it was written for.
func dominantEnding(text string) string {
	crlf := strings.Count(text, "\r\n")
	if crlf > strings.Count(text, "\n")-crlf {
		return "\r\n"
	}
	return "\n"
}

// inheritedEnding is the terminator the replacement is written with: a match
// that spans lines carries its own answer, one that does not takes the file's.
func inheritedEnding(matched, dominant string) string {
	if strings.Contains(matched, "\r\n") {
		return "\r\n"
	}
	if strings.Contains(matched, "\n") {
		return "\n"
	}
	return dominant
}

// ambiguousLines names every line a repeated match starts on, across both
// spellings, so the caller can tell an ambiguity from an ending difference.
func ambiguousLines(text string, found []string) []string {
	seen := map[string]bool{}
	lines := make([]string, 0, len(found)*5)
	numbers := make([]int, 0, len(found)*5)
	for _, spelling := range found {
		for _, line := range matchLines(text, spelling) {
			if seen[line] {
				continue
			}
			seen[line] = true
			if parsed, err := strconv.Atoi(line); err == nil {
				numbers = append(numbers, parsed)
			}
		}
	}
	sort.Ints(numbers)
	for _, number := range numbers {
		lines = append(lines, strconv.Itoa(number))
	}
	return lines
}

// matchLines returns the 1-based line numbers where a needle starts.
func matchLines(text, needle string) []string {
	var lines []string
	offset := 0
	for {
		index := strings.Index(text[offset:], needle)
		if index < 0 {
			break
		}
		absolute := offset + index
		line := strings.Count(text[:absolute], "\n") + 1
		lines = append(lines, fmt.Sprintf("%d", line))
		offset = absolute + len(needle)
		if len(lines) >= 5 {
			break
		}
	}
	return lines
}

// diagnose explains the usual reasons an exact match failed.
func diagnose(text, needle string) string {
	switch {
	case strings.Contains(text, strings.TrimRight(needle, " \t")):
		return "Trailing whitespace differs."
	case strings.Contains(strings.ToLower(text), strings.ToLower(needle)):
		return "Case differs from the file."
	case strings.Contains(text, strings.Join(strings.Fields(needle), " ")):
		return "Whitespace inside the line differs."
	}
	return "Copy the text verbatim from read_file, including indentation."
}
