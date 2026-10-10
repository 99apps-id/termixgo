package agent

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

// hypothesisVerifyTool structures scientific hypothesis verification.
type hypothesisVerifyTool struct{}

func (t *hypothesisVerifyTool) Name() string { return "hypothesis_verify" }
func (t *hypothesisVerifyTool) Aliases() []string {
	return []string{"verify_hypothesis", "test_hypothesis"}
}
func (t *hypothesisVerifyTool) Mutating() bool { return true }
func (t *hypothesisVerifyTool) Risk() Risk     { return RiskCommand }
func (t *hypothesisVerifyTool) Label(a map[string]any) string {
	return "Verifying hypothesis: " + Shorten(argString(a, "hypothesis"), 40)
}
func (t *hypothesisVerifyTool) DoneLabel(a map[string]any) string {
	return "Verified hypothesis: " + Shorten(argString(a, "hypothesis"), 40)
}
func (t *hypothesisVerifyTool) Description() string {
	return "Formulate an explicit diagnostic hypothesis and evaluate it with concrete code inspection or a test probe. Returns an evidence-backed verification verdict (CONFIRMED, REFUTED, or INCONCLUSIVE)."
}
func (t *hypothesisVerifyTool) Schema() map[string]any {
	return object(map[string]any{
		"hypothesis":       strProp("The concrete assumption or root cause statement to evaluate."),
		"command":          strProp("Optional shell or test probe command to run."),
		"expected_outcome": strProp("Expected output or outcome if the hypothesis is true."),
		"file_path":        strProp("Optional file path in workspace to inspect for evidence."),
		"pattern":          strProp("Optional regex or substring pattern to look for in the file."),
		"timeout_secs":     intProp("Probe timeout in seconds, 1 to 120 (defaults to 30)."),
	}, "hypothesis")
}

func (t *hypothesisVerifyTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	hypothesis := strings.TrimSpace(argString(args, "hypothesis"))
	if hypothesis == "" {
		return Result{Output: "hypothesis is required", IsError: true}, nil
	}

	command := strings.TrimSpace(argString(args, "command"))
	expectedOutcome := strings.TrimSpace(argString(args, "expected_outcome"))
	filePath := strings.TrimSpace(argString(args, "file_path", "path"))
	pattern := strings.TrimSpace(argString(args, "pattern"))

	if command == "" && filePath == "" {
		return Result{
			Output:  "Provide at least a command or a file_path to test the hypothesis against real evidence.",
			IsError: true,
		}, nil
	}

	var sb strings.Builder
	sb.WriteString("=== HYPOTHESIS VERIFICATION REPORT ===\n")
	fmt.Fprintf(&sb, "Hypothesis:        %s\n", hypothesis)

	verdict := "INCONCLUSIVE"
	var evidence []string

	// 1. Evaluate file evidence if specified
	if filePath != "" {
		resolved := resolvePath(env, filePath)
		if err := checkWorkspacePath(env, resolved); err != nil {
			return Result{Output: err.Error(), IsError: true}, nil
		}
		data, err := os.ReadFile(resolved)
		if err != nil {
			evidence = append(evidence, fmt.Sprintf("File inspection failed: %v", err))
		} else {
			content := string(data)
			if pattern != "" {
				re, reErr := regexp.Compile("(?i)" + pattern)
				if reErr == nil && re.MatchString(content) {
					evidence = append(evidence, fmt.Sprintf("Pattern %q found in %s", pattern, displayPath(env, resolved)))
					if command == "" {
						verdict = "CONFIRMED"
					}
				} else if reErr != nil && strings.Contains(strings.ToLower(content), strings.ToLower(pattern)) {
					evidence = append(evidence, fmt.Sprintf("Substring %q found in %s", pattern, displayPath(env, resolved)))
					if command == "" {
						verdict = "CONFIRMED"
					}
				} else {
					evidence = append(evidence, fmt.Sprintf("Pattern %q NOT found in %s", pattern, displayPath(env, resolved)))
					if command == "" {
						verdict = "REFUTED"
					}
				}
			} else {
				evidence = append(evidence, fmt.Sprintf("File %s exists (%d bytes)", displayPath(env, resolved), len(data)))
			}
		}
	}

	// 2. Evaluate command probe if specified
	if command != "" {
		timeoutSec := argInt(args, "timeout_secs", 30, 1, 120)
		res, execErr := execute(ctx, env, command, env.Workspace, time.Duration(timeoutSec)*time.Second)
		cmdOutput := strings.TrimSpace(res.Output)
		if execErr != nil {
			evidence = append(evidence, fmt.Sprintf("Command execution error: %v", execErr))
		} else {
			evidence = append(evidence, fmt.Sprintf("Command output: %s", Shorten(cmdOutput, 200)))
		}

		if expectedOutcome != "" {
			expectedLower := strings.ToLower(expectedOutcome)
			outputLower := strings.ToLower(cmdOutput)
			if strings.Contains(outputLower, expectedLower) {
				verdict = "CONFIRMED"
				evidence = append(evidence, fmt.Sprintf("Observed output matched expected outcome: %q", expectedOutcome))
			} else {
				if verdict != "CONFIRMED" {
					verdict = "REFUTED"
				}
				evidence = append(evidence, fmt.Sprintf("Observed output did NOT match expected outcome: %q", expectedOutcome))
			}
		} else if execErr == nil && !res.IsError && verdict != "REFUTED" {
			verdict = "CONFIRMED"
		}
	}

	fmt.Fprintf(&sb, "Verdict:           %s\n", verdict)
	sb.WriteString("\nObserved Evidence:\n")
	for _, ev := range evidence {
		fmt.Fprintf(&sb, "  - %s\n", ev)
	}

	sb.WriteString("\nNext Step:\n")
	switch verdict {
	case "CONFIRMED":
		sb.WriteString("  The hypothesis is supported by evidence. Proceed with the targeted fix based on this root cause.\n")
	case "REFUTED":
		sb.WriteString("  The hypothesis is refuted by evidence. Re-examine assumptions and inspect alternative causes.\n")
	default:
		sb.WriteString("  Evidence is inconclusive. Gather more observations or formulate a narrower hypothesis.\n")
	}

	return Result{Output: strings.TrimRight(sb.String(), "\n")}, nil
}
