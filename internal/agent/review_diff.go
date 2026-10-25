package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DiffIssueKind defines the class of issue caught in a diff.
type DiffIssueKind string

const (
	DiffIssueConflict DiffIssueKind = "conflict"
	DiffIssueSecret   DiffIssueKind = "secret"
	DiffIssueDebug    DiffIssueKind = "debug"
	DiffIssueSyntax   DiffIssueKind = "syntax"
)

// DiffIssue represents one issue detected in git diff additions.
type DiffIssue struct {
	Kind        DiffIssueKind `json:"kind"`
	Severity    Severity      `json:"severity"`
	File        string        `json:"file"`
	Line        int           `json:"line"`
	Description string        `json:"description"`
	Snippet     string        `json:"snippet,omitempty"`
}

// DiffReviewReport holds static and LLM review findings for a git diff.
type DiffReviewReport struct {
	Workspace    string      `json:"workspace"`
	FilesChanged []string    `json:"files_changed"`
	Additions    int         `json:"additions"`
	Deletions    int         `json:"deletions"`
	StaticIssues []DiffIssue `json:"static_issues"`
	LLMReview    string      `json:"llm_review,omitempty"`
	DurationMS   int64       `json:"duration_ms"`
}

// DiffReviewOptions configures the diff review run.
type DiffReviewOptions struct {
	Staged     bool
	Base       string
	Scope      string
	StaticOnly bool
}

var debugPatterns = []struct {
	Pattern     *regexp.Regexp
	Exts        []string
	Description string
}{
	{
		Pattern:     regexp.MustCompile(`\bfmt\.Print(ln|f)?\(`),
		Exts:        []string{".go"},
		Description: "Leftover fmt.Print debugging call in Go code",
	},
	{
		Pattern:     regexp.MustCompile(`\bconsole\.(log|debug)\(`),
		Exts:        []string{".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs"},
		Description: "Leftover console.log debugging statement",
	},
	{
		Pattern:     regexp.MustCompile(`\bdebugger\b`),
		Exts:        []string{".js", ".jsx", ".ts", ".tsx"},
		Description: "Leftover JavaScript debugger statement",
	},
	{
		Pattern:     regexp.MustCompile(`\b(breakpoint\(\)|pdb\.set_trace\(\)|import\s+pdb)`),
		Exts:        []string{".py"},
		Description: "Leftover Python debugger or breakpoint statement",
	},
	{
		Pattern:     regexp.MustCompile(`\bbinding\.pry\b`),
		Exts:        []string{".rb"},
		Description: "Leftover Ruby binding.pry breakpoint",
	},
}

var hunkHeaderPattern = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// ReviewDiff performs comprehensive static and agentic review of git diffs.
func ReviewDiff(ctx context.Context, env *Env, opts DiffReviewOptions) (*DiffReviewReport, error) {
	start := time.Now()

	var paths []string
	if scope := strings.TrimSpace(opts.Scope); scope != "" {
		paths = []string{scope}
	}

	argv, err := gitDiffArgv(opts.Staged, false, strings.TrimSpace(opts.Base), paths)
	if err != nil {
		return nil, fmt.Errorf("git diff args: %w", err)
	}

	diffText, gitErr := runGit(ctx, env, argv...)
	if gitErr != nil {
		msg := strings.TrimSpace(diffText)
		if msg == "" {
			msg = gitErr.Error()
		}
		return nil, fmt.Errorf("git diff failed: %s", msg)
	}

	report := &DiffReviewReport{
		Workspace: env.Workspace,
	}

	if strings.TrimSpace(diffText) == "" {
		report.DurationMS = time.Since(start).Milliseconds()
		return report, nil
	}

	// Parse diff to extract files, additions, deletions and static issues
	filesMap := make(map[string]bool)
	var currentFile string
	var currentExt string
	var newLineNum int

	scanner := bufio.NewScanner(strings.NewReader(diffText))
	for scanner.Scan() {
		line := scanner.Text()

		switch {
		case strings.HasPrefix(line, "diff --git "):
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				bPath := parts[3]
				currentFile = strings.TrimPrefix(bPath, "b/")
				currentExt = strings.ToLower(filepath.Ext(currentFile))
				filesMap[currentFile] = true
			}
		case strings.HasPrefix(line, "+++ b/"):
			currentFile = strings.TrimPrefix(line, "+++ b/")
			currentExt = strings.ToLower(filepath.Ext(currentFile))
			filesMap[currentFile] = true
		case strings.HasPrefix(line, "@@ "):
			if matches := hunkHeaderPattern.FindStringSubmatch(line); len(matches) > 1 {
				if n, err := strconv.Atoi(matches[1]); err == nil {
					newLineNum = n - 1
				}
			}
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			newLineNum++
			report.Additions++
			addedContent := strings.TrimPrefix(line, "+")
			trimmed := strings.TrimSpace(addedContent)

			// 1. Check for merge conflict markers
			if strings.HasPrefix(trimmed, "<<<<<<<") || strings.HasPrefix(trimmed, "=======") || strings.HasPrefix(trimmed, ">>>>>>>") {
				report.StaticIssues = append(report.StaticIssues, DiffIssue{
					Kind:        DiffIssueConflict,
					Severity:    SeverityCritical,
					File:        currentFile,
					Line:        newLineNum,
					Description: "Unresolved git merge conflict marker in added line",
					Snippet:     Shorten(trimmed, 80),
				})
			}

			// 2. Check for secrets
			for _, rule := range defaultAuditRules {
				if rule.Category != CategorySecrets {
					continue
				}
				if rule.Pattern.MatchString(addedContent) {
					report.StaticIssues = append(report.StaticIssues, DiffIssue{
						Kind:        DiffIssueSecret,
						Severity:    rule.Severity,
						File:        currentFile,
						Line:        newLineNum,
						Description: rule.Description,
						Snippet:     Shorten(trimmed, 80),
					})
				}
			}

			// 3. Check for leftover debug statements
			for _, debugRule := range debugPatterns {
				extMatch := false
				for _, ext := range debugRule.Exts {
					if currentExt == ext {
						extMatch = true
						break
					}
				}
				if extMatch && debugRule.Pattern.MatchString(addedContent) {
					// Skip if within a test file
					if !strings.Contains(currentFile, "_test.") && !strings.Contains(currentFile, ".test.") && !strings.Contains(currentFile, "test_") {
						report.StaticIssues = append(report.StaticIssues, DiffIssue{
							Kind:        DiffIssueDebug,
							Severity:    SeverityMedium,
							File:        currentFile,
							Line:        newLineNum,
							Description: debugRule.Description,
							Snippet:     Shorten(trimmed, 80),
						})
					}
				}
			}

		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			report.Deletions++
		case !strings.HasPrefix(line, "-"):
			newLineNum++
		}
	}

	for f := range filesMap {
		report.FilesChanged = append(report.FilesChanged, f)
	}
	sort.Strings(report.FilesChanged)

	// 4. Run syntax diagnostics on changed files
	for _, f := range report.FilesChanged {
		fullPath := resolvePath(env, f)
		if _, err := os.Stat(fullPath); err == nil {
			if diag := DiagnoseFile(env, fullPath); diag != "" {
				report.StaticIssues = append(report.StaticIssues, DiffIssue{
					Kind:        DiffIssueSyntax,
					Severity:    SeverityCritical,
					File:        f,
					Line:        1,
					Description: fmt.Sprintf("Syntax error: %s", strings.ReplaceAll(diag, "\n", "; ")),
				})
			}
		}
	}

	// 5. Run LLM review if requested and available
	if !opts.StaticOnly && env.RunSubagent != nil {
		llmOut, _, runErr := env.RunSubagent(ctx, string(SubagentCodeReview), buildReviewPrompt(diffText, false))
		if runErr == nil {
			report.LLMReview = llmOut
		} else {
			report.LLMReview = fmt.Sprintf("AI review failed: %v", runErr)
		}
	}

	report.DurationMS = time.Since(start).Milliseconds()
	return report, nil
}

// FormatText formats the report into human-readable terminal text.
func (r *DiffReviewReport) FormatText() string {
	if len(r.FilesChanged) == 0 {
		return "No changes detected to review."
	}

	var sb strings.Builder
	sb.WriteString("=== Git Diff Review Report ===\n")
	fmt.Fprintf(&sb, "Changes: %d file(s) (+%d, -%d lines) [%d ms]\n",
		len(r.FilesChanged), r.Additions, r.Deletions, r.DurationMS)
	for _, f := range r.FilesChanged {
		fmt.Fprintf(&sb, "  - %s\n", f)
	}

	sb.WriteString("\nStatic Inspections:\n")
	if len(r.StaticIssues) == 0 {
		sb.WriteString("  PASS (no conflict markers, hardcoded secrets, debug statements or syntax errors)\n")
	} else {
		for _, issue := range r.StaticIssues {
			lineInfo := ""
			if issue.Line > 0 {
				lineInfo = fmt.Sprintf(":%d", issue.Line)
			}
			fmt.Fprintf(&sb, "  [%s] %s%s: %s\n", issue.Severity, issue.File, lineInfo, issue.Description)
			if issue.Snippet != "" {
				fmt.Fprintf(&sb, "    snippet: %s\n", issue.Snippet)
			}
		}
	}

	if r.LLMReview != "" {
		sb.WriteString("\nAI Code Review:\n")
		sb.WriteString(strings.TrimSpace(r.LLMReview))
		sb.WriteString("\n")
	}

	return strings.TrimRight(sb.String(), "\n")
}

// FormatJSON serializes the review report to indented JSON.
func (r *DiffReviewReport) FormatJSON() (string, error) {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}
