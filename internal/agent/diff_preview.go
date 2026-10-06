package agent

import (
	"fmt"
	"os"
	"strings"
)

// previewLimits bound the approval diff: the dialog is small and the full
// change is one Enter away from running anyway.
const (
	previewContext  = 3
	previewMaxHunks = 4
	previewMaxLines = 40
)

// PreviewToolDiff renders a unified-style preview of what a file tool call
// would change, for the approval dialog. Unknown tools and unreadable files
// yield an empty string: the dialog then shows the one-line detail as before.
func PreviewToolDiff(env *Env, toolName string, args map[string]any) string {
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "edit", "replace":
		return previewEdit(env, args, false)
	case "multi_edit", "multi_replace":
		return previewEdit(env, args, true)
	case "write_file":
		return previewWrite(env, args)
	case "apply_patch", "patch":
		return previewPatch(env, args)
	}
	return ""
}

func previewEdit(env *Env, args map[string]any, multi bool) string {
	path := resolvePath(env, argString(args, "path", "file"))
	if err := checkWorkspacePath(env, path); err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	oldLines := splitPreviewLines(string(data))
	var hunks []previewHunk
	if multi {
		for _, item := range decodeMultiEdits(args) {
			hunk, ok := locatePreview(oldLines, item.oldText)
			if !ok {
				return ""
			}
			hunks = append(hunks, previewHunk{at: hunk, oldText: item.oldText, newText: item.newText})
		}
	} else {
		oldText := argString(args, "old_string", "old")
		hunk, ok := locatePreview(oldLines, oldText)
		if !ok {
			return ""
		}
		hunks = append(hunks, previewHunk{at: hunk, oldText: oldText, newText: argString(args, "new_string", "new")})
	}
	return renderPreviewHunks(displayPath(env, path), oldLines, hunks)
}

// editInstruction mirrors the multi_edit argument shape for the preview.
type multiPreviewEdit struct {
	oldText string
	newText string
}

func decodeMultiEdits(args map[string]any) []multiPreviewEdit {
	raw, ok := args["edits"]
	if !ok {
		raw, _ = args["instructions"]
	}
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	var edits []multiPreviewEdit
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		edits = append(edits, multiPreviewEdit{
			oldText: argString(entry, "old_string", "old"),
			newText: argString(entry, "new_string", "new"),
		})
	}
	return edits
}

type previewHunk struct {
	at      int
	oldText string
	newText string
}

func locatePreview(oldLines []string, oldText string) (int, bool) {
	want := splitPreviewLines(oldText)
	if len(strings.TrimSpace(oldText)) == 0 || len(want) == 0 {
		return 0, false
	}
	for at := 0; at+len(want) <= len(oldLines); at++ {
		match := true
		for i, line := range want {
			if oldLines[at+i] != line {
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

func previewWrite(env *Env, args map[string]any) string {
	path := resolvePath(env, argString(args, "path", "file"))
	if err := checkWorkspacePath(env, path); err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		added := splitPreviewLines(argString(args, "content", "text"))
		return fmt.Sprintf("--- new file: %s\n+++ %d line(s) added", displayPath(env, path), len(added))
	}
	oldLines := splitPreviewLines(string(data))
	newLines := splitPreviewLines(argString(args, "content", "text"))
	var builder strings.Builder
	fmt.Fprintf(&builder, "--- %s\n+++ %s\n", displayPath(env, path), displayPath(env, path))
	writePreviewLines(&builder, '-', oldLines)
	writePreviewLines(&builder, '+', newLines)
	return capPreview(builder.String())
}

func previewPatch(env *Env, args map[string]any) string {
	ops, err := parsePatch(argString(args, "patch"))
	if err != nil {
		return ""
	}
	var builder strings.Builder
	shown := 0
	for _, op := range ops {
		if shown >= previewMaxHunks {
			break
		}
		switch op.action {
		case "add":
			fmt.Fprintf(&builder, "+++ new file: %s\n", op.path)
		case "delete":
			fmt.Fprintf(&builder, "--- deleted: %s\n", op.path)
		default:
			path := resolvePath(env, op.path)
			if err := checkWorkspacePath(env, path); err != nil {
				return ""
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return ""
			}
			oldLines := splitPreviewLines(string(data))
			var hunks []previewHunk
			cursor := 0
			valid := true
			for _, hunk := range op.hunks {
				var oldText, newText []string
				for _, line := range hunk.lines {
					if line.kind != "+" {
						oldText = append(oldText, line.text)
					}
					if line.kind != "-" {
						newText = append(newText, line.text)
					}
				}
				at, ok := locateForward(oldLines, cursor, oldText)
				if !ok {
					valid = false
					break
				}
				hunks = append(hunks, previewHunk{at: at, oldText: strings.Join(oldText, "\n"), newText: strings.Join(newText, "\n")})
				cursor = at + len(oldText)
			}
			if !valid {
				return ""
			}
			builder.WriteString(renderPreviewHunks(op.path, oldLines, hunks))
			builder.WriteString("\n")
		}
		shown++
	}
	return capPreview(strings.TrimRight(builder.String(), "\n"))
}

func locateForward(oldLines []string, cursor int, want []string) (int, bool) {
	if len(want) == 0 {
		return cursor, true
	}
	for at := cursor; at+len(want) <= len(oldLines); at++ {
		match := true
		for i, line := range want {
			if oldLines[at+i] != line {
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

func renderPreviewHunks(path string, oldLines []string, hunks []previewHunk) string {
	if len(hunks) > previewMaxHunks {
		hunks = hunks[:previewMaxHunks]
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "--- %s\n+++ %s\n", path, path)
	for _, hunk := range hunks {
		oldText := splitPreviewLines(hunk.oldText)
		newText := splitPreviewLines(hunk.newText)
		start := hunk.at - previewContext
		if start < 0 {
			start = 0
		}
		writePreviewLines(&builder, ' ', oldLines[start:hunk.at])
		writePreviewLines(&builder, '-', oldText)
		writePreviewLines(&builder, '+', newText)
		end := hunk.at + len(oldText) + previewContext
		if end > len(oldLines) {
			end = len(oldLines)
		}
		writePreviewLines(&builder, ' ', oldLines[hunk.at+len(oldText):end])
	}
	return capPreview(strings.TrimRight(builder.String(), "\n"))
}

// writePreviewLines writes each line with a one-byte diff marker in front.
func writePreviewLines(builder *strings.Builder, marker byte, lines []string) {
	for _, line := range lines {
		builder.WriteByte(marker)
		builder.WriteString(line)
		builder.WriteByte('\n')
	}
}

func splitPreviewLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.Trim(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func capPreview(text string) string {
	lines := strings.Split(text, "\n")
	if len(lines) > previewMaxLines {
		lines = append(lines[:previewMaxLines], fmt.Sprintf("... (%d more lines)", len(lines)-previewMaxLines))
	}
	return strings.Join(lines, "\n")
}
