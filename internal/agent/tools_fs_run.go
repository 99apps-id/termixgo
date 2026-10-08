package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (t *readFileTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	raw := argString(args, "path", "file")
	if raw == "" {
		return Result{Output: "path is required", IsError: true}, nil
	}
	path := resolvePath(env, raw)
	if err := checkWorkspacePath(env, path); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return Result{Output: openError(err, displayPath(env, path)), IsError: true}, nil
	}
	if info.IsDir() {
		return Result{Output: fmt.Sprintf("%s is a directory; use list_directory", displayPath(env, path)), IsError: true}, nil
	}
	if !isTextFile(path) {
		return Result{Output: fmt.Sprintf("%s looks binary or an image, which this build cannot render in the terminal", displayPath(env, path)), IsError: true}, nil
	}

	// A file larger than the direct cap is streamed below: the window is
	// collected line by line so the process never holds the whole file.
	if info.Size() > maxReadDirectBytes {
		return readFileWindowed(env, path, argInt(args, "offset", 1, 1, 0), argInt(args, "limit", maxReadLines, 1, 10000))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{Output: openError(err, displayPath(env, path)), IsError: true}, nil
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	total := len(lines)

	offset := argInt(args, "offset", 1, 1, 0)
	if offset > total {
		offset = total
	}
	limit := argInt(args, "limit", maxReadLines, 1, 10000)
	return formatReadWindow(env, path, lines, total, offset, limit), nil
}

// formatReadWindow renders one window of lines with the same header the
// direct read produces, so both paths describe the file the same way.
func formatReadWindow(env *Env, path string, lines []string, total, offset, limit int) Result {
	if offset > total {
		offset = total
	}
	if offset < 1 {
		offset = 1
	}
	end := offset - 1 + limit
	if end > total {
		end = total
	}
	window := lines[offset-1 : end]
	body := strings.Join(window, "\n")
	truncatedByBytes := false
	if len(body) > maxReadBytes {
		body = clipBytes(body, maxReadBytes)
		truncatedByBytes = true
	}

	header := fmt.Sprintf("%s (lines %d-%d of %d)", displayPath(env, path), offset, end, total)
	if truncatedByBytes {
		header += " [clipped at 64 KB]"
	}
	if end < total && !truncatedByBytes {
		header += fmt.Sprintf(" [more below; next offset %d]", end+1)
	}
	return Result{Output: header + "\n" + body}
}

// readFileWindowed serves the offset and limit window of a file too large to
// read at once. Lines are scanned in fixed chunks and only the window is
// kept, so memory stays flat no matter how large the file is. Line splitting
// follows strings.Split semantics, including the trailing empty element of a
// file that ends in a newline, so the header counts match the direct read.
func readFileWindowed(env *Env, path string, offset, limit int) (Result, error) {
	display := displayPath(env, path)
	file, err := os.Open(path)
	if err != nil {
		return Result{Output: openError(err, display), IsError: true}, nil
	}
	defer file.Close()

	end := offset - 1 + limit
	var window []string
	keptBytes := 0
	windowFull := false
	newlines := 0
	lineNo := 0
	var current []byte
	lineCapped := false
	flush := func() {
		lineNo++
		if lineNo >= offset && lineNo <= end && !windowFull {
			text := string(current)
			if lineCapped {
				text += "... [line truncated]"
			}
			// The body is clipped to maxReadBytes later anyway; stop
			// keeping lines once the kept window passes twice that, and
			// keep counting so the header stays honest.
			if keptBytes+len(text) > 2*maxReadBytes && len(window) > 0 {
				windowFull = true
			} else {
				window = append(window, text)
				keptBytes += len(text)
			}
		}
		current = current[:0]
		lineCapped = false
	}
	buffer := make([]byte, 64*1024)
	for {
		count, readErr := file.Read(buffer)
		for _, b := range buffer[:count] {
			if b == '\n' {
				newlines++
				// Drop the \r of a CRLF pair while scanning, which is
				// what the direct read's global replace does.
				current = trimCR(current)
				flush()
				continue
			}
			if len(current) < maxReadLineBytes {
				current = append(current, b)
			} else {
				lineCapped = true
			}
		}
		if readErr != nil {
			break
		}
	}
	// The tail after the last newline is a line of its own, and a file that
	// ends in a newline still holds the trailing empty element Split
	// reports. An empty file holds exactly that one empty element.
	if len(current) > 0 || lineCapped {
		// No trim here: the direct read only drops a \r that sits before
		// a \n, so a bare carriage return at the end of file is kept.
		flush()
	} else {
		lineNo++
		if lineNo >= offset && lineNo <= end && !windowFull {
			window = append(window, "")
		}
	}
	total := newlines + 1
	// An offset past the end reads the last line, the same clamp the direct
	// read applies. Only then is a second scan needed, and it terminates:
	// the clamped offset can no longer overshoot.
	if offset > total {
		return readFileWindowed(env, path, total, limit)
	}
	if end > total {
		end = total
	}
	body := strings.Join(window, "\n")
	truncatedByBytes := windowFull
	if len(body) > maxReadBytes {
		body = clipBytes(body, maxReadBytes)
		truncatedByBytes = true
	}

	header := fmt.Sprintf("%s (lines %d-%d of %d)", display, offset, end, total)
	if truncatedByBytes {
		header += " [clipped at 64 KB]"
	}
	if end < total && !truncatedByBytes {
		header += fmt.Sprintf(" [more below; next offset %d]", end+1)
	}
	return Result{Output: header + "\n" + body}, nil
}

// trimCR drops one trailing carriage return in place.
func trimCR(line []byte) []byte {
	if len(line) > 0 && line[len(line)-1] == '\r' {
		return line[:len(line)-1]
	}
	return line
}

func (t *listDirectoryTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	path := resolvePath(env, argString(args, "path", "dir", "directory"))
	// The empty fallback comes first. resolvePath returns "" for an absent
	// argument, and the workspace check rejects "" because it cannot be made
	// relative to anything, so the check used to fail before the fallback could
	// turn the empty path into the workspace root.
	if path == "" {
		path = env.Workspace
	}
	if err := checkWorkspacePath(env, path); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return Result{Output: openError(err, displayPath(env, path)), IsError: true}, nil
	}
	sortEntries(entries)
	const maxEntries = 500
	var builder strings.Builder
	builder.WriteString(displayPath(env, path))
	builder.WriteString("/\n")
	for index, entry := range entries {
		if index == maxEntries {
			fmt.Fprintf(&builder, "... and %d more\n", len(entries)-maxEntries)
			break
		}
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		builder.WriteString(name)
		builder.WriteByte('\n')
	}
	if len(entries) == 0 {
		builder.WriteString("(empty)\n")
	}
	return Result{Output: strings.TrimRight(builder.String(), "\n")}, nil
}

func (t *writeFileTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	raw := argString(args, "path", "file")
	if raw == "" {
		return Result{Output: "path is required", IsError: true}, nil
	}
	content, ok := args["content"]
	if !ok {
		return Result{Output: "content is required", IsError: true}, nil
	}
	text := toString(content)
	path := resolvePath(env, raw)
	if err := checkWorkspacePath(env, path); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	// The previous content is kept before it is replaced, so undo_edit can
	// bring it back. A file that does not exist yet leaves an absent marker
	// instead, so undoing its creation removes it again.
	backupFile(env, path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Result{Output: fmt.Sprintf("create parent directory: %v", err), IsError: true}, nil
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return Result{Output: fmt.Sprintf("write %s: %v", displayPath(env, path), err), IsError: true}, nil
	}
	lineCount := 0
	if text != "" {
		lineCount = strings.Count(text, "\n") + 1
	}
	return Result{Output: fmt.Sprintf("Wrote %s (%d bytes, %d lines)", displayPath(env, path), len(text), lineCount)}, nil
}

func (t *createDirectoryTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	raw := argString(args, "path", "dir", "directory")
	if raw == "" {
		return Result{Output: "path is required", IsError: true}, nil
	}
	path := resolvePath(env, raw)
	if err := checkWorkspacePath(env, path); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	existed := false
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		existed = true
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return Result{Output: fmt.Sprintf("create directory: %v", err), IsError: true}, nil
	}
	if existed {
		return Result{Output: fmt.Sprintf("%s already exists", displayPath(env, path))}, nil
	}
	return Result{Output: "Created " + displayPath(env, path)}, nil
}

// isWorkspaceRoot reports whether a resolved path is the workspace itself.
//
// Removing or renaming it is never what the model means, and one wrong call
// would take the whole project with it: `delete_file` with "." used to remove
// every file under the workspace and report success.
func isWorkspaceRoot(env *Env, path string) bool {
	if strings.TrimSpace(env.Workspace) == "" || strings.TrimSpace(path) == "" {
		return false
	}
	if filepath.Clean(path) == filepath.Clean(env.Workspace) {
		return true
	}
	// Compare the resolved forms too: a symlinked spelling of the root must
	// not slip past the plain-string comparison above.
	resolvedPath, pathErr := filepath.EvalSymlinks(path)
	resolvedRoot, rootErr := filepath.EvalSymlinks(env.Workspace)
	return pathErr == nil && rootErr == nil && filepath.Clean(resolvedPath) == filepath.Clean(resolvedRoot)
}

func (t *deleteFileTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	raw := argString(args, "path", "file")
	if raw == "" {
		return Result{Output: "path is required", IsError: true}, nil
	}
	path := resolvePath(env, raw)
	if err := checkWorkspacePath(env, path); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	if isWorkspaceRoot(env, path) {
		return Result{
			Output:  "Refusing to delete the workspace root itself. Delete the files you mean inside it, or move them elsewhere first.",
			IsError: true,
		}, nil
	}
	if _, err := os.Stat(path); err != nil {
		return Result{Output: openError(err, displayPath(env, path)), IsError: true}, nil
	}
	if err := os.RemoveAll(path); err != nil {
		return Result{Output: fmt.Sprintf("delete %s: %v", displayPath(env, path), err), IsError: true}, nil
	}
	return Result{Output: "Deleted " + displayPath(env, path)}, nil
}

func (t *moveFileTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	from := resolvePath(env, argString(args, "from", "source", "path"))
	to := resolvePath(env, argString(args, "to", "destination", "dest"))
	if from == "" || to == "" {
		return Result{Output: "from and to are required", IsError: true}, nil
	}
	if err := checkWorkspacePath(env, from); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	if err := checkWorkspacePath(env, to); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	// Moving the workspace root would take the session's working directory with
	// it, so every later tool call would fail for a reason the model cannot see.
	if isWorkspaceRoot(env, from) {
		return Result{
			Output:  "Refusing to move the workspace root itself. Move the files you mean inside it instead.",
			IsError: true,
		}, nil
	}
	if _, err := os.Stat(from); err != nil {
		return Result{Output: openError(err, displayPath(env, from)), IsError: true}, nil
	}
	if _, err := os.Stat(to); err == nil {
		return Result{Output: fmt.Sprintf("%s already exists; delete it first", displayPath(env, to)), IsError: true}, nil
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return Result{Output: fmt.Sprintf("create destination directory: %v", err), IsError: true}, nil
	}
	if err := os.Rename(from, to); err != nil {
		return Result{Output: fmt.Sprintf("move: %v", err), IsError: true}, nil
	}
	return Result{Output: fmt.Sprintf("Moved %s to %s", displayPath(env, from), displayPath(env, to))}, nil
}
