package agent

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/99apps-id/termixgo/internal/search"
)

var identifierPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// refactorRenameTool safely renames identifiers across workspace files.
type refactorRenameTool struct{}

func (t *refactorRenameTool) Name() string      { return "refactor_rename" }
func (t *refactorRenameTool) Aliases() []string { return []string{"rename_symbol", "rename"} }
func (t *refactorRenameTool) Mutating() bool    { return true }
func (t *refactorRenameTool) Risk() Risk        { return RiskEdit }
func (t *refactorRenameTool) Label(a map[string]any) string {
	return fmt.Sprintf("Renaming %s to %s", argString(a, "symbol"), argString(a, "new_name"))
}
func (t *refactorRenameTool) DoneLabel(a map[string]any) string {
	return fmt.Sprintf("Renamed %s to %s", argString(a, "symbol"), argString(a, "new_name"))
}
func (t *refactorRenameTool) Description() string {
	return "Safely rename a function, method, type, variable, or class identifier across files in the workspace. Replaces exact word occurrences across definitions and call sites atomically, creating backups before writing and running syntax diagnostics."
}
func (t *refactorRenameTool) Schema() map[string]any {
	return object(map[string]any{
		"symbol":   strProp("Current identifier name to rename."),
		"new_name": strProp("New identifier name to replace it with."),
		"path":     strProp("Optional directory or file path to limit scope. Defaults to workspace."),
		"preview":  boolProp("If true, list planned changes without modifying any files."),
	}, "symbol", "new_name")
}

type renameMatch struct {
	path     string
	disp     string
	count    int
	lineNums []int
	newBytes []byte
}

func (t *refactorRenameTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	symbol := strings.TrimSpace(argString(args, "symbol", "old_name"))
	newName := strings.TrimSpace(argString(args, "new_name", "new"))
	preview := argBool(args, "preview", false)

	if symbol == "" || newName == "" {
		return Result{Output: "symbol and new_name are both required", IsError: true}, nil
	}
	if symbol == newName {
		return Result{Output: "symbol and new_name are identical", IsError: true}, nil
	}
	if !identifierPattern.MatchString(symbol) {
		return Result{Output: fmt.Sprintf("%q is not a valid identifier name", symbol), IsError: true}, nil
	}
	if !identifierPattern.MatchString(newName) {
		return Result{Output: fmt.Sprintf("%q is not a valid identifier name", newName), IsError: true}, nil
	}

	scope := env.Workspace
	if rawScope := argString(args, "path", "scope"); rawScope != "" {
		scope = resolvePath(env, rawScope)
		if err := checkWorkspacePath(env, scope); err != nil {
			return Result{Output: err.Error(), IsError: true}, nil
		}
	}

	scopeInfo, err := os.Stat(scope)
	if err != nil {
		return Result{Output: fmt.Sprintf("scope path not found: %v", err), IsError: true}, nil
	}

	wordRegex, err := regexp.Compile(`\b` + regexp.QuoteMeta(symbol) + `\b`)
	if err != nil {
		return Result{Output: fmt.Sprintf("invalid regex pattern: %v", err), IsError: true}, nil
	}

	var matches []renameMatch
	totalOccurrences := 0

	scanFile := func(filePath string) error {
		ext := strings.ToLower(filepath.Ext(filePath))
		if !isAuditableExt(ext) {
			return nil
		}
		data, err := os.ReadFile(filePath)
		if err != nil {
			return nil
		}
		if bytes.IndexByte(data, 0) != -1 {
			return nil // Skip binary files
		}

		if !wordRegex.Match(data) {
			return nil
		}

		var lineNums []int
		lines := strings.Split(string(data), "\n")
		count := 0
		for i, line := range lines {
			c := len(wordRegex.FindAllStringIndex(line, -1))
			if c > 0 {
				count += c
				lineNums = append(lineNums, i+1)
			}
		}

		if count > 0 {
			replaced := wordRegex.ReplaceAll(data, []byte(newName))
			disp := displayPath(env, filePath)
			matches = append(matches, renameMatch{
				path:     filePath,
				disp:     disp,
				count:    count,
				lineNums: lineNums,
				newBytes: replaced,
			})
			totalOccurrences += count
		}
		return nil
	}

	if !scopeInfo.IsDir() {
		_ = scanFile(scope)
	} else {
		_ = filepath.WalkDir(scope, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if d.IsDir() {
				name := d.Name()
				if p != scope && (search.IsSkippedDir(name) || name == ".agents") {
					return fs.SkipDir
				}
				return nil
			}
			return scanFile(p)
		})
	}

	if len(matches) == 0 {
		return Result{Output: fmt.Sprintf("No occurrences of symbol %q found in %s", symbol, displayPath(env, scope))}, nil
	}

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].disp < matches[j].disp
	})

	if preview {
		var sb strings.Builder
		fmt.Fprintf(&sb, "Preview: renaming %q to %q would update %d occurrence(s) across %d file(s):\n",
			symbol, newName, totalOccurrences, len(matches))
		for _, m := range matches {
			fmt.Fprintf(&sb, "  - %s (%d occurrence(s) on lines: %v)\n", m.disp, m.count, m.lineNums)
		}
		return Result{Output: strings.TrimRight(sb.String(), "\n")}, nil
	}

	// Apply renames atomically with backups
	for _, m := range matches {
		backupFile(env, m.path)
		if err := os.WriteFile(m.path, m.newBytes, 0o644); err != nil {
			return Result{Output: fmt.Sprintf("failed writing %s: %v", m.disp, err), IsError: true}, nil
		}
	}

	var changedPaths []string
	var sb strings.Builder
	fmt.Fprintf(&sb, "Renamed %q to %q across %d occurrence(s) in %d file(s):\n",
		symbol, newName, totalOccurrences, len(matches))
	for _, m := range matches {
		fmt.Fprintf(&sb, "  - %s (%d replacement(s))\n", m.disp, m.count)
		changedPaths = append(changedPaths, m.path)
	}

	out := strings.TrimRight(sb.String(), "\n")
	if diag := DiagnosePaths(env, changedPaths); diag != "" {
		out += "\n\n" + diag
	}

	return Result{Output: out}, nil
}
