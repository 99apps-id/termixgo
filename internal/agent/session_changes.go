package agent

import (
	"encoding/json"
	"strings"

	"github.com/99apps-id/termixgo/internal/provider"
)

// SessionChanges is what a session did to files, read back from the edit tool
// calls it made. It is an estimate: the session keeps the calls, not a git
// snapshot, so the line counts say how much the model asked to change, not
// what the working tree looks like now.
type SessionChanges struct {
	Files   int `json:"files"`
	Added   int `json:"added"`
	Removed int `json:"removed"`
}

// SummarizeChanges counts the files and lines the successful edit-family tool
// calls of a conversation touched. A call whose result reads as a failure is
// skipped: an edit that was refused changed nothing.
func SummarizeChanges(messages []provider.Message) SessionChanges {
	results := map[string]string{}
	for _, message := range messages {
		if message.Role == provider.RoleTool && message.ToolID != "" {
			results[message.ToolID] = message.Content
		}
	}
	files := map[string]bool{}
	var changes SessionChanges
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			if output, ok := results[call.ID]; ok && LooksLikeToolFailure(call.Name, output) {
				continue
			}
			var args map[string]any
			if json.Unmarshal([]byte(call.Arguments), &args) != nil {
				continue
			}
			countCall(call.Name, args, files, &changes)
		}
	}
	delete(files, "")
	changes.Files = len(files)
	return changes
}

func countCall(name string, args map[string]any, files map[string]bool, changes *SessionChanges) {
	text := func(keys ...string) string {
		for _, key := range keys {
			if value, ok := args[key].(string); ok && value != "" {
				return value
			}
		}
		return ""
	}
	switch name {
	case "edit":
		files[text("path", "file")] = true
		added, removed := lineDelta(text("old_string", "old"), text("new_string", "new"))
		changes.Added += added
		changes.Removed += removed
	case "multi_edit":
		files[text("path", "file")] = true
		if edits, ok := args["edits"].([]any); ok {
			for _, raw := range edits {
				edit, _ := raw.(map[string]any)
				oldText, _ := edit["old_string"].(string)
				newText, _ := edit["new_string"].(string)
				added, removed := lineDelta(oldText, newText)
				changes.Added += added
				changes.Removed += removed
			}
		}
	case "write_file":
		files[text("path", "file", "file_path", "filepath", "filename", "target", "target_file")] = true
		changes.Added += lineCount(text("content"))
	case "delete_file":
		files[text("path", "file")] = true
	case "move_file":
		files[text("to", "destination", "dest")] = true
	case "apply_patch":
		countPatch(text("patch"), files, changes)
	}
}

// countPatch reads an apply_patch document: the file directives name the
// files, and the plus and minus lines are the change.
func countPatch(document string, files map[string]bool, changes *SessionChanges) {
	for _, line := range strings.Split(document, "\n") {
		switch {
		case strings.HasPrefix(line, "*** Update File:"):
			files[strings.TrimSpace(strings.TrimPrefix(line, "*** Update File:"))] = true
		case strings.HasPrefix(line, "*** Add File:"):
			files[strings.TrimSpace(strings.TrimPrefix(line, "*** Add File:"))] = true
		case strings.HasPrefix(line, "*** Delete File:"):
			files[strings.TrimSpace(strings.TrimPrefix(line, "*** Delete File:"))] = true
		case strings.HasPrefix(line, "+"):
			changes.Added++
		case strings.HasPrefix(line, "-"):
			changes.Removed++
		}
	}
}

// lineDelta is the added and removed lines of replacing old with new, after
// the unchanged lines at both ends are set aside.
func lineDelta(oldText, newText string) (added, removed int) {
	before := splitLines(oldText)
	after := splitLines(newText)
	for len(before) > 0 && len(after) > 0 && before[0] == after[0] {
		before, after = before[1:], after[1:]
	}
	for len(before) > 0 && len(after) > 0 && before[len(before)-1] == after[len(after)-1] {
		before, after = before[:len(before)-1], after[:len(after)-1]
	}
	return len(after), len(before)
}

func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

func lineCount(text string) int { return len(splitLines(text)) }
