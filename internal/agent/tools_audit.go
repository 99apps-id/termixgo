package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Severity indicates finding impact.
type Severity string

const (
	SeverityCritical Severity = "CRITICAL"
	SeverityHigh     Severity = "HIGH"
	SeverityMedium   Severity = "MEDIUM"
	SeverityLow      Severity = "LOW"
)

// AuditCategory categorizes the finding type.
type AuditCategory string

const (
	CategorySecurity    AuditCategory = "security"
	CategorySecrets     AuditCategory = "secrets"
	CategoryConcurrency AuditCategory = "concurrency"
	CategoryQuality     AuditCategory = "quality"
)

// AuditFinding represents one detected code or security issue.
type AuditFinding struct {
	Category    AuditCategory `json:"category"`
	Severity    Severity      `json:"severity"`
	File        string        `json:"file"`
	Line        int           `json:"line"`
	RuleID      string        `json:"rule_id"`
	Description string        `json:"description"`
	Snippet     string        `json:"snippet,omitempty"`
	Remedy      string        `json:"remedy,omitempty"`
}

// AuditReport holds the aggregated results of a code audit.
type AuditReport struct {
	Workspace    string         `json:"workspace"`
	FilesScanned int            `json:"files_scanned"`
	Findings     []AuditFinding `json:"findings"`
	DurationMS   int64          `json:"duration_ms"`
}

// AuditOptions controls the scope and filters of an audit scan.
type AuditOptions struct {
	Category    string
	MinSeverity string
	MaxResults  int
}

type auditRule struct {
	RuleID      string
	Category    AuditCategory
	Severity    Severity
	Description string
	Remedy      string
	Pattern     *regexp.Regexp
	SkipTests   bool
	FileExts    []string
}

var defaultAuditRules = []auditRule{
	// Secrets & Credentials
	{
		RuleID:      "SEC001",
		Category:    CategorySecrets,
		Severity:    SeverityCritical,
		Description: "Private cryptographic key header detected in source",
		Remedy:      "Remove private key from repository; use environment variables or secret manager",
		Pattern:     regexp.MustCompile(`-----BEGIN (RSA|OPENSSH|EC|DSA|PGP|PRIVATE KEY)`),
		SkipTests:   true,
	},
	{
		RuleID:      "SEC002",
		Category:    CategorySecrets,
		Severity:    SeverityCritical,
		Description: "AWS Access Key identifier detected",
		Remedy:      "Remove AWS key and rotate compromised credentials",
		Pattern:     regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
		SkipTests:   true,
	},
	{
		RuleID:      "SEC003",
		Category:    CategorySecrets,
		Severity:    SeverityCritical,
		Description: "GitHub Personal Access or OAuth Token detected",
		Remedy:      "Revoke and remove hardcoded GitHub token",
		Pattern:     regexp.MustCompile(`\bgh[pousr]_[0-9A-Za-z]{36,}\b`),
		SkipTests:   true,
	},
	{
		RuleID:      "SEC004",
		Category:    CategorySecrets,
		Severity:    SeverityCritical,
		Description: "AI provider secret key detected",
		Remedy:      "Store API keys in ~/.termixgo/secrets.json or environment variables",
		Pattern:     regexp.MustCompile(`\b(sk-[a-zA-Z0-9_-]{24,}|sk-ant-[a-zA-Z0-9_-]{24,}|AIza[0-9A-Za-z\-_]{35})\b`),
		SkipTests:   true,
	},
	{
		RuleID:      "SEC005",
		Category:    CategorySecrets,
		Severity:    SeverityHigh,
		Description: "Hardcoded database connection string with password",
		Remedy:      "Use secret store or environment configuration for database connection strings",
		Pattern:     regexp.MustCompile(`\b(postgres|postgresql|mysql|mongodb|redis)://[^:\s]+:[^@\s]+@`),
		SkipTests:   true,
	},

	// Security Vulnerabilities
	{
		RuleID:      "VUL001",
		Category:    CategorySecurity,
		Severity:    SeverityHigh,
		Description: "Insecure TLS certificate verification (InsecureSkipVerify enabled)",
		Remedy:      "Enable proper TLS certificate validation to prevent man-in-the-middle attacks",
		Pattern:     regexp.MustCompile(`InsecureSkipVerify\s*:\s*true`),
		SkipTests:   true,
		FileExts:    []string{".go"},
	},
	{
		RuleID:      "VUL002",
		Category:    CategorySecurity,
		Severity:    SeverityCritical,
		Description: "Shell command execution with formatted string arguments",
		Remedy:      "Pass arguments as separate array elements instead of interpolating into shell string",
		Pattern:     regexp.MustCompile(`exec\.Command\s*\(\s*["'](sh|bash|cmd|powershell)["'],\s*["'](-c|/c)["'],\s*fmt\.Sprintf`),
		SkipTests:   false,
		FileExts:    []string{".go"},
	},
	{
		RuleID:      "VUL003",
		Category:    CategorySecurity,
		Severity:    SeverityCritical,
		Description: "Potential SQL injection via string formatting",
		Remedy:      "Use parameterized queries with placeholders ($1, ?, etc.) instead of fmt.Sprintf",
		Pattern:     regexp.MustCompile(`(?i)(db\.(Query|Exec|QueryRow)\s*\(\s*fmt\.Sprintf|\.Query\s*\([^,)]*(SELECT|INSERT|UPDATE|DELETE)[^,)]*%s)`),
		SkipTests:   false,
		FileExts:    []string{".go", ".py", ".js", ".ts"},
	},
	{
		RuleID:      "VUL004",
		Category:    CategorySecurity,
		Severity:    SeverityMedium,
		Description: "Weak cryptographic hash function (MD5/SHA1)",
		Remedy:      "Use modern collision-resistant hash algorithms (e.g., SHA-256 or SHA-512)",
		Pattern:     regexp.MustCompile(`\b(md5\.New\(\)|sha1\.New\(\)|crypto\.MD5|crypto\.SHA1)\b`),
		SkipTests:   true,
		FileExts:    []string{".go"},
	},

	// Concurrency & Resource Management
	{
		RuleID:      "CON001",
		Category:    CategoryConcurrency,
		Severity:    SeverityMedium,
		Description: "Unclosed HTTP response body (missing defer resp.Body.Close())",
		Remedy:      "Add 'defer resp.Body.Close()' immediately following err == nil check",
		Pattern:     regexp.MustCompile(`resp,\s*err\s*:?=\s*(http\.(Get|Post|DefaultClient\.Do)|\w+\.Do)\(`),
		SkipTests:   true,
		FileExts:    []string{".go"},
	},

	// Code Quality
	{
		RuleID:      "QLT001",
		Category:    CategoryQuality,
		Severity:    SeverityMedium,
		Description: "Silently swallowed error block",
		Remedy:      "Handle, propagate, or log the error rather than discarding it",
		Pattern:     regexp.MustCompile(`if\s+err\s*!=\s*nil\s*\{\s*\}`),
		SkipTests:   false,
		FileExts:    []string{".go"},
	},
}

func severityRank(s Severity) int {
	switch s {
	case SeverityCritical:
		return 4
	case SeverityHigh:
		return 3
	case SeverityMedium:
		return 2
	case SeverityLow:
		return 1
	default:
		return 0
	}
}

// AuditWorkspace scans files in targetPath and generates an AuditReport.
func AuditWorkspace(workspace, targetPath string, opts AuditOptions) (*AuditReport, error) {
	started := time.Now()
	if strings.TrimSpace(workspace) == "" {
		workspace = "."
	}
	if strings.TrimSpace(targetPath) == "" {
		targetPath = workspace
	}

	cleanWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		cleanWorkspace = workspace
	}
	cleanTarget, err := filepath.Abs(targetPath)
	if err != nil {
		cleanTarget = targetPath
	}

	info, err := os.Stat(cleanTarget)
	if err != nil {
		return nil, fmt.Errorf("target path not found: %w", err)
	}

	categoryFilter := strings.ToLower(strings.TrimSpace(opts.Category))
	minSeverity := Severity(strings.ToUpper(strings.TrimSpace(opts.MinSeverity)))
	minRank := severityRank(minSeverity)
	maxResults := opts.MaxResults
	if maxResults <= 0 {
		maxResults = 100
	}

	var findings []AuditFinding
	scannedCount := 0

	scanFile := func(path string) error {
		ext := strings.ToLower(filepath.Ext(path))
		if !isAuditableExt(ext) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if bytes.IndexByte(data, 0) != -1 {
			return nil // Skip binary files
		}
		scannedCount++

		relPath, err := filepath.Rel(cleanWorkspace, path)
		if err != nil {
			relPath = path
		}
		relPath = filepath.ToSlash(relPath)
		isTest := strings.HasSuffix(strings.ToLower(relPath), "_test.go") ||
			strings.Contains(strings.ToLower(relPath), "/test/") ||
			strings.Contains(strings.ToLower(relPath), "/tests/") ||
			strings.HasPrefix(strings.ToLower(filepath.Base(relPath)), "test_")

		scanner := bufio.NewScanner(bytes.NewReader(data))
		lineNum := 0
		lines := strings.Split(string(data), "\n")

		for scanner.Scan() {
			lineNum++
			lineText := scanner.Text()

			for _, rule := range defaultAuditRules {
				if categoryFilter != "" && categoryFilter != "all" && string(rule.Category) != categoryFilter {
					continue
				}
				if minRank > 0 && severityRank(rule.Severity) < minRank {
					continue
				}
				if rule.SkipTests && isTest {
					continue
				}
				if len(rule.FileExts) > 0 {
					matched := false
					for _, allowed := range rule.FileExts {
						if ext == allowed {
							matched = true
							break
						}
					}
					if !matched {
						continue
					}
				}

				if rule.Pattern.MatchString(lineText) {
					// Extra check for CON001 (body close): verify body is not closed within 5 lines
					if rule.RuleID == "CON001" {
						closed := false
						for checkIdx := lineNum; checkIdx < lineNum+6 && checkIdx < len(lines); checkIdx++ {
							if strings.Contains(lines[checkIdx], ".Body.Close()") {
								closed = true
								break
							}
						}
						if closed {
							continue
						}
					}

					snippet := strings.TrimSpace(lineText)
					if len(snippet) > 120 {
						snippet = snippet[:117] + "..."
					}

					findings = append(findings, AuditFinding{
						Category:    rule.Category,
						Severity:    rule.Severity,
						File:        relPath,
						Line:        lineNum,
						RuleID:      rule.RuleID,
						Description: rule.Description,
						Snippet:     snippet,
						Remedy:      rule.Remedy,
					})

					if len(findings) >= maxResults {
						return nil
					}
				}
			}
		}
		return nil
	}

	if !info.IsDir() {
		_ = scanFile(cleanTarget)
	} else {
		_ = filepath.Walk(cleanTarget, func(path string, f os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if f.IsDir() {
				base := filepath.Base(path)
				if base == ".git" || base == ".termixgo" || base == "node_modules" || base == "vendor" || base == ".agents" {
					return filepath.SkipDir
				}
				return nil
			}
			if len(findings) >= maxResults {
				return filepath.SkipAll
			}
			return scanFile(path)
		})
	}

	sort.SliceStable(findings, func(i, j int) bool {
		rI := severityRank(findings[i].Severity)
		rJ := severityRank(findings[j].Severity)
		if rI != rJ {
			return rI > rJ
		}
		return findings[i].File < findings[j].File
	})

	return &AuditReport{
		Workspace:    filepath.ToSlash(cleanWorkspace),
		FilesScanned: scannedCount,
		Findings:     findings,
		DurationMS:   time.Since(started).Milliseconds(),
	}, nil
}

func isAuditableExt(ext string) bool {
	switch ext {
	case ".go", ".py", ".js", ".ts", ".jsx", ".tsx", ".sh", ".bash", ".ps1", ".json", ".yaml", ".yml", ".env", ".toml", ".sql":
		return true
	default:
		return false
	}
}

// FormatAuditTable formats an AuditReport as an easy-to-read text report.
func FormatAuditTable(report *AuditReport) string {
	if report == nil {
		return "No audit report generated."
	}

	var sb strings.Builder
	crit, high, med, low := 0, 0, 0, 0
	for _, f := range report.Findings {
		switch f.Severity {
		case SeverityCritical:
			crit++
		case SeverityHigh:
			high++
		case SeverityMedium:
			med++
		case SeverityLow:
			low++
		}
	}

	sb.WriteString("Code & Security Audit Report\n")
	sb.WriteString(fmt.Sprintf("Workspace: %s\n", report.Workspace))
	sb.WriteString(fmt.Sprintf("Files scanned: %d (%dms)\n", report.FilesScanned, report.DurationMS))
	sb.WriteString(fmt.Sprintf("Findings: %d (%d Critical, %d High, %d Medium, %d Low)\n\n",
		len(report.Findings), crit, high, med, low))

	if len(report.Findings) == 0 {
		sb.WriteString("No security vulnerabilities or code smells detected.\n")
		return sb.String()
	}

	for _, f := range report.Findings {
		sb.WriteString(fmt.Sprintf("[%s] %s (%s) - %s:%d\n", f.Severity, f.RuleID, f.Category, f.File, f.Line))
		sb.WriteString(fmt.Sprintf("  Issue:  %s\n", f.Description))
		if f.Snippet != "" {
			sb.WriteString(fmt.Sprintf("  Code:   %s\n", f.Snippet))
		}
		if f.Remedy != "" {
			sb.WriteString(fmt.Sprintf("  Remedy: %s\n", f.Remedy))
		}
		sb.WriteString("\n")
	}

	return strings.TrimRight(sb.String(), "\n")
}

// FormatAuditJSON serializes an AuditReport into formatted JSON.
func FormatAuditJSON(report *AuditReport) (string, error) {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// auditCodeTool exposes static code and security auditing to the agent.
type auditCodeTool struct{}

func (t *auditCodeTool) Name() string { return "audit_code" }
func (t *auditCodeTool) Aliases() []string {
	return []string{"security_audit", "code_audit", "audit_workspace"}
}
func (t *auditCodeTool) Mutating() bool { return false }
func (t *auditCodeTool) Risk() Risk     { return RiskEdit }
func (t *auditCodeTool) Label(a map[string]any) string {
	return "Auditing code in " + displayName(a)
}
func (t *auditCodeTool) DoneLabel(a map[string]any) string {
	return "Audited code in " + displayName(a)
}
func (t *auditCodeTool) Description() string {
	return "Perform a static code and security audit across the workspace. Scans for security vulnerabilities (injection, insecure TLS, weak crypto), hardcoded secrets, concurrency leaks, and bad error handling."
}
func (t *auditCodeTool) Schema() map[string]any {
	return object(map[string]any{
		"path":         strProp("Optional file or directory to scan. Defaults to workspace root."),
		"category":     map[string]any{"type": "string", "enum": []string{"all", "security", "secrets", "concurrency", "quality"}, "description": "Category to audit. Defaults to all."},
		"min_severity": map[string]any{"type": "string", "enum": []string{"low", "medium", "high", "critical"}, "description": "Minimum severity to include. Defaults to low."},
		"max_results":  intProp("Maximum number of findings to return, 1 to 200 (defaults to 50)."),
	})
}

func (t *auditCodeTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	raw := argString(args, "path", "file", "dir")
	target := env.Workspace
	if raw != "" {
		target = resolvePath(env, raw)
		if err := checkWorkspacePath(env, target); err != nil {
			return Result{Output: err.Error(), IsError: true}, nil
		}
	}

	opts := AuditOptions{
		Category:    argString(args, "category"),
		MinSeverity: argString(args, "min_severity"),
		MaxResults:  argInt(args, "max_results", 50, 1, 200),
	}

	report, err := AuditWorkspace(env.Workspace, target, opts)
	if err != nil {
		return Result{Output: fmt.Sprintf("audit failed: %v", err), IsError: true}, nil
	}

	return Result{Output: FormatAuditTable(report)}, nil
}
