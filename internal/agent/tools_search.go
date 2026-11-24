package agent

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// maxSearchFileBytes caps the size of a file a search will read.
const maxSearchFileBytes = 2 * 1024 * 1024

// grepTool searches file contents.
type grepTool struct{}

func (t *grepTool) Name() string      { return "grep" }
func (t *grepTool) Aliases() []string { return []string{"search", "search_files"} }
func (t *grepTool) Mutating() bool    { return false }
func (t *grepTool) Risk() Risk        { return RiskEdit }
func (t *grepTool) Label(a map[string]any) string {
	return "Searching " + Shorten(argString(a, "pattern", "query", "search", "regex", "keyword", "text"), 40)
}
func (t *grepTool) DoneLabel(a map[string]any) string {
	return "Searched " + Shorten(argString(a, "pattern", "query", "search", "regex", "keyword", "text"), 40)
}
func (t *grepTool) Description() string {
	return "Search file contents with a regular expression (RE2 syntax). Honours .gitignore and skips dependency trees, lockfiles and minified bundles. output_mode=files lists only the matching files and count gives matches per file: use them first to map where something is, then content (the default, grouped by file) on the few files that matter. context adds lines around each match so a follow-up read is often unnecessary."
}
func (t *grepTool) Schema() map[string]any {
	return object(map[string]any{
		"pattern":          strProp("Regular expression to search for."),
		"path":             strProp("Directory or file to search. Defaults to the workspace."),
		"glob":             strProp("Only search files matching this glob, for example **/*.go or *.ts."),
		"case_insensitive": boolProp("Match without regard to case."),
		"output_mode":      strProp("content (matching lines, default), files (matching file paths only) or count (matches per file)."),
		"context":          intProp("Lines of context around each match in content mode, 0 to 5. Defaults to 0."),
		"include_noise":    boolProp("Also search lockfiles, minified bundles and source maps. Defaults to false."),
		"max_results":      intProp("Maximum matches (content) or files (files, count) to return, 1 to 500. Defaults to 30 matches or 100 files."),
	}, "pattern")
}

func (t *grepTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	pattern := argString(args, "pattern", "query", "search", "regex", "keyword", "text")
	if strings.TrimSpace(pattern) == "" {
		return Result{Output: `pattern is required (received empty pattern; pass a JSON object such as {"pattern": "search_regex"})`, IsError: true}, nil
	}
	expression := pattern
	if argBool(args, "case_insensitive", false) {
		expression = "(?i)" + expression
	}
	compiled, err := regexp.Compile(expression)
	if err != nil {
		return Result{Output: fmt.Sprintf("invalid regular expression: %v", err), IsError: true}, nil
	}
	root := resolvePath(env, argString(args, "path", "root"))
	if root == "" {
		root = env.Workspace
	}
	if err := checkWorkspacePath(env, root); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	var matcher *globMatcher
	if glob := strings.TrimSpace(argString(args, "glob")); glob != "" {
		if matcher, err = newGlobMatcher(glob); err != nil {
			return Result{Output: fmt.Sprintf("invalid glob: %v", err), IsError: true}, nil
		}
	}
	mode := strings.ToLower(strings.TrimSpace(argString(args, "output_mode", "mode")))
	switch mode {
	case "", "content", "lines":
		mode = "content"
	case "files", "files_with_matches", "names":
		mode = "files"
	case "count", "counts":
		mode = "count"
	default:
		return Result{Output: "output_mode must be content, files or count", IsError: true}, nil
	}
	defaultMax := 30
	if mode != "content" {
		defaultMax = 100
	}
	maxResults := argInt(args, "max_results", defaultMax, 1, 500)
	contextLines := argInt(args, "context", 0, 0, 5)
	includeNoise := argBool(args, "include_noise", false)

	var out grepOutput
	out.mode = mode
	out.limit = maxResults
	skippedNoise := 0
	for _, path := range listSearchFiles(ctx, root) {
		if ctx.Err() != nil || out.full() {
			break
		}
		relative := relativeSlash(env, path)
		if env.Guest && guestSensitive(relative) {
			continue
		}
		if matcher != nil && !matcher.match(relative) {
			continue
		}
		if !isTextFile(path) {
			continue
		}
		if !includeNoise && isNoiseFile(path) {
			skippedNoise++
			continue
		}
		info, statErr := os.Stat(path)
		if statErr != nil || info.IsDir() || info.Size() > maxSearchFileBytes {
			continue
		}
		grepFile(path, relative, compiled, contextLines, &out)
	}

	if out.matches == 0 {
		text := "No matches."
		if skippedNoise > 0 {
			text += fmt.Sprintf(" (%d lockfile/minified file(s) skipped; include_noise=true searches them)", skippedNoise)
		}
		return Result{Output: text}, nil
	}
	return Result{Output: out.render()}, nil
}

// grepOutput collects results per file. Content mode prints each path once
// above its lines instead of on every line, which is a large share of a
// result's tokens when one file has many matches.
type grepOutput struct {
	mode      string
	limit     int
	files     int
	matches   int
	truncated bool
	body      strings.Builder
}

func (o *grepOutput) full() bool {
	if o.mode == "content" {
		return o.matches >= o.limit
	}
	return o.files >= o.limit
}

func (o *grepOutput) render() string {
	var header string
	switch o.mode {
	case "files":
		header = fmt.Sprintf("%d file(s) match", o.files)
	case "count":
		header = fmt.Sprintf("%d match(es) in %d file(s)", o.matches, o.files)
	default:
		header = fmt.Sprintf("%d match(es) in %d file(s)", o.matches, o.files)
	}
	if o.truncated || o.full() {
		header += " (limit reached; narrow with path or glob)"
	}
	return header + "\n" + strings.TrimRight(o.body.String(), "\n")
}

// grepFile searches one file and appends its results.
func grepFile(path, relative string, compiled *regexp.Regexp, contextLines int, out *grepOutput) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 512*1024)

	// lines keeps the whole file only when context is asked for; hits are the
	// 1-based numbers of the matching lines and texts their content.
	var lines []string
	var hits []int
	var texts []string
	number := 0
	for scanner.Scan() {
		number++
		line := scanner.Text()
		if contextLines > 0 {
			lines = append(lines, line)
		}
		if compiled.MatchString(line) {
			hits = append(hits, number)
			if contextLines == 0 {
				texts = append(texts, line)
			}
		}
	}
	// A line longer than the scanner buffer (a minified bundle) ends the scan
	// of this file only; the search carries on with the next one.
	if len(hits) == 0 {
		return
	}
	out.files++
	switch out.mode {
	case "files":
		out.matches += len(hits)
		fmt.Fprintf(&out.body, "%s\n", relative)
		return
	case "count":
		out.matches += len(hits)
		fmt.Fprintf(&out.body, "%s: %d\n", relative, len(hits))
		return
	}

	fmt.Fprintf(&out.body, "%s\n", relative)
	if contextLines == 0 {
		for index, line := range texts {
			if out.matches >= out.limit {
				out.truncated = true
				return
			}
			out.matches++
			fmt.Fprintf(&out.body, "  %d: %s\n", hits[index], Shorten(line, 200))
		}
		return
	}
	isHit := make(map[int]bool, len(hits))
	for _, hit := range hits {
		isHit[hit] = true
	}
	printed := 0
	for _, hit := range hits {
		if out.matches >= out.limit {
			out.truncated = true
			return
		}
		out.matches++
		start := max(hit-contextLines, printed+1)
		end := min(hit+contextLines, len(lines))
		if printed > 0 && start > printed+1 {
			out.body.WriteString("  --\n")
		}
		for number := start; number <= end; number++ {
			marker := "-"
			if isHit[number] {
				marker = ":"
			}
			fmt.Fprintf(&out.body, "  %d%s %s\n", number, marker, Shorten(lines[number-1], 200))
		}
		printed = end
	}
}

// globTool finds files by path pattern.
type globTool struct{}

func (t *globTool) Name() string      { return "glob" }
func (t *globTool) Aliases() []string { return []string{"find_files", "ls_files"} }
func (t *globTool) Mutating() bool    { return false }
func (t *globTool) Risk() Risk        { return RiskEdit }
func (t *globTool) Label(a map[string]any) string {
	return "Globbing " + Shorten(argString(a, "pattern", "query", "glob", "file_pattern", "filter"), 40)
}
func (t *globTool) DoneLabel(a map[string]any) string {
	return "Globbed " + Shorten(argString(a, "pattern", "query", "glob", "file_pattern", "filter"), 40)
}
func (t *globTool) Description() string {
	return "Find files by glob pattern, for example **/*.go, src/**/*.ts or *.json. Supports ** for any depth. Skips generated trees."
}
func (t *globTool) Schema() map[string]any {
	return object(map[string]any{
		"pattern":     strProp("Glob pattern, matched against the workspace-relative path."),
		"path":        strProp("Directory to search. Defaults to the workspace."),
		"max_results": intProp("Maximum files to return, 1 to 2000. Defaults to 200."),
	}, "pattern")
}

func (t *globTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	pattern := argString(args, "pattern", "query", "glob", "file_pattern", "filter")
	if strings.TrimSpace(pattern) == "" {
		return Result{Output: `pattern is required (received empty pattern; pass a JSON object such as {"pattern": "**/*.go"})`, IsError: true}, nil
	}
	root := resolvePath(env, argString(args, "path", "root"))
	if root == "" {
		root = env.Workspace
	}
	if err := checkWorkspacePath(env, root); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	maxResults := argInt(args, "max_results", 200, 1, 2000)
	matcher, err := newGlobMatcher(pattern)
	if err != nil {
		return Result{Output: fmt.Sprintf("invalid glob: %v", err), IsError: true}, nil
	}

	// The same file list as grep: .gitignore is honoured inside a repository.
	matches := make([]string, 0, maxResults)
	truncated := false
	for _, path := range listSearchFiles(ctx, root) {
		relative := relativeSlash(env, path)
		if env.Guest && guestSensitive(relative) {
			continue
		}
		if !matcher.match(relative) {
			continue
		}
		if len(matches) >= maxResults {
			truncated = true
			break
		}
		matches = append(matches, relative)
	}
	if len(matches) == 0 {
		return Result{Output: "No files matched."}, nil
	}
	header := fmt.Sprintf("%d file(s)", len(matches))
	if truncated {
		header += " (limit reached)"
	}
	return Result{Output: header + "\n" + strings.Join(matches, "\n")}, nil
}

func relativeSlash(env *Env, path string) string {
	if strings.TrimSpace(env.Workspace) == "" {
		return filepath.ToSlash(path)
	}
	relative, err := filepath.Rel(env.Workspace, path)
	if err != nil || strings.HasPrefix(relative, "..") {
		if runtime.GOOS == "windows" {
			relLower, errLower := filepath.Rel(strings.ToLower(env.Workspace), strings.ToLower(path))
			if errLower == nil && !strings.HasPrefix(relLower, "..") && !filepath.IsAbs(relLower) {
				return filepath.ToSlash(relLower)
			}
		}
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(relative)
}

// matchGlob matches a slash-separated path against a glob, where ** spans
// directory boundaries and * does not.
func matchGlob(pattern, path string) bool {
	expression := globToRegex(pattern)
	compiled, err := regexp.Compile("^" + expression + "$")
	if err != nil {
		return false
	}
	if compiled.MatchString(path) {
		return true
	}
	// A bare pattern like *.go should also match at any depth.
	if !strings.Contains(pattern, "/") {
		return compiled.MatchString(filepath.Base(path))
	}
	return false
}

// globToRegex converts a glob to a regular expression.
func globToRegex(pattern string) string {
	var builder strings.Builder
	runes := []rune(strings.TrimPrefix(filepath.ToSlash(pattern), "./"))
	for index := 0; index < len(runes); index++ {
		r := runes[index]
		switch r {
		case '*':
			if index+1 < len(runes) && runes[index+1] == '*' {
				index++
				if index+1 < len(runes) && runes[index+1] == '/' {
					index++
					builder.WriteString("(?:.*/)?")
					continue
				}
				builder.WriteString(".*")
				continue
			}
			builder.WriteString("[^/]*")
		case '?':
			builder.WriteString("[^/]")
		case '.', '+', '(', ')', '|', '^', '$', '{', '}', '[', ']', '\\':
			builder.WriteByte('\\')
			builder.WriteRune(r)
		default:
			builder.WriteRune(r)
		}
	}
	return builder.String()
}
