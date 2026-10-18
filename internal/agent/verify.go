package agent

import (
	"path/filepath"
	"regexp"
	"strings"
)

// VerifyLedger tracks whether a turn that edited code has fresh passing
// verification evidence since the last edit. The loop itself never runs
// checks; it only observes tool results, exactly like the Termigo
// verify-on-stop gate.
type VerifyLedger struct {
	ChangedCodePaths      []string
	VerifiedAfterLastEdit bool
}

// NewVerifyLedger starts an empty ledger for one task.
func NewVerifyLedger() VerifyLedger { return VerifyLedger{} }

// MaxVerifyNudges bounds the synthetic follow-ups per task.
const MaxVerifyNudges = 2

// VerifyNudgePrefix marks a continuation as verification, so it keeps the
// todo list instead of starting a fresh task.
const VerifyNudgePrefix = "[System: verify-on-stop]"

// MaxChangedPathsInNudge caps the paths listed in one nudge.
const MaxChangedPathsInNudge = 8

var checkCommandPattern = regexp.MustCompile(`(?i)\b(test|tests|lint|check|checks|build|typecheck|tsc|pytest|vitest|jest|mocha|cargo|clippy|vet|ruff|biome|eslint|prettier|rubocop|rspec|make|gradle|mvn|go test|go vet|go build)\b`)

var verifyClaimPattern = regexp.MustCompile(`(?i)\b(tests?|checks?|lint|build|typecheck|suite)\b[^.\n]{0,40}\b(pass(?:ed|es|ing)?|green|clean|succeed(?:ed|s)?|ok|successful)\b|\b(verified|validated)\b|\bno\b[^.\n]{0,20}\b(errors?|failures?|issues?)\b[^.\n]{0,15}\b(remain|left|found)\b`)

// ClaimsVerification reports whether prose claims a verification result.
func ClaimsVerification(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	if strings.Contains(lower, "should pass") || strings.Contains(lower, "let me run") || strings.Contains(lower, "will verify") {
		return false
	}
	return verifyClaimPattern.MatchString(trimmed)
}

// IsNonCodePath reports whether a path carries no verifiable runtime
// behavior, so editing it alone never demands verification.
func IsNonCodePath(raw string) bool {
	base := strings.ToLower(filepath.Base(strings.ReplaceAll(raw, "\\", "/")))
	if base == "" {
		return true
	}
	nonCodeExt := map[string]bool{
		".md": true, ".markdown": true, ".mdx": true, ".rst": true,
		".txt": true, ".text": true, ".adoc": true, ".org": true,
		".log": true, ".csv": true, ".tsv": true,
	}
	if ext := filepath.Ext(base); ext != "" {
		if ext == base {
			return false
		}
		return nonCodeExt[ext]
	}
	nonCodeNames := map[string]bool{
		"license": true, "licence": true, "notice": true,
		"authors": true, "contributors": true, "changelog": true, "codeowners": true,
	}
	return nonCodeNames[base]
}

// RecordEdit folds a successful file mutation into the ledger.
func (l VerifyLedger) RecordEdit(path string) VerifyLedger {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" || IsNonCodePath(trimmed) {
		return l
	}
	for _, existing := range l.ChangedCodePaths {
		if existing == trimmed {
			l.VerifiedAfterLastEdit = false
			return l
		}
	}
	l.ChangedCodePaths = append(l.ChangedCodePaths, trimmed)
	l.VerifiedAfterLastEdit = false
	return l
}

// RecordVerification marks fresh passing evidence.
func (l VerifyLedger) RecordVerification() VerifyLedger {
	if l.VerifiedAfterLastEdit {
		return l
	}
	l.VerifiedAfterLastEdit = true
	return l
}

// LooksLikeCheckCommand reports whether a shell command is a verification
// command. Listing files proves nothing, so only check-like commands count.
func LooksLikeCheckCommand(command string) bool {
	return checkCommandPattern.MatchString(command)
}

// codeEdited reports whether any tracked path is verifiable code.
func (l VerifyLedger) codeEdited() bool {
	for _, path := range l.ChangedCodePaths {
		if path != "" && !IsNonCodePath(path) {
			return true
		}
	}
	return false
}

// ShouldNudgeVerification fires when the run has no fresh passing evidence
// and either edited code or claimed a verification result in prose.
func (l VerifyLedger) ShouldNudgeVerification(claimedVerification bool) bool {
	if l.VerifiedAfterLastEdit {
		return false
	}
	return l.codeEdited() || claimedVerification
}

// BuildVerifyNudge renders the synthetic follow-up, or empty when the gate
// stays silent.
func (l VerifyLedger) BuildVerifyNudge(attempts int, claimedVerification bool) string {
	if attempts >= MaxVerifyNudges {
		return ""
	}
	paths := make([]string, 0, len(l.ChangedCodePaths))
	for _, path := range l.ChangedCodePaths {
		if path != "" && !IsNonCodePath(path) {
			paths = append(paths, path)
		}
	}
	if len(paths) == 0 {
		if !claimedVerification {
			return ""
		}
		return VerifyNudgePrefix + " Your reply claims the work is verified, but this run recorded no passing verification evidence (no run_checks success, and no test, lint or build command that succeeded). Run the relevant verification now and report the real result, or state plainly what you did NOT verify."
	}
	lines := make([]string, 0, len(paths)+1)
	for _, path := range paths {
		if len(lines) >= MaxChangedPathsInNudge {
			break
		}
		lines = append(lines, "- `"+path+"`")
	}
	if len(paths) > MaxChangedPathsInNudge {
		lines = append(lines, "- ... and more")
	}
	return VerifyNudgePrefix + " You edited code in this task, but there is no fresh passing verification evidence since the last edit.\n\nChanged paths:\n" +
		strings.Join(lines, "\n") +
		"\n\nRun the relevant verification now (the run_checks tool, or the project test, lint or build command), read any failure, repair the code, and summarize what passed. If verification is not possible, explain the concrete blocker instead of claiming the work is fully verified."
}
