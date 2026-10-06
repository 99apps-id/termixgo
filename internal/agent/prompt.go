package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/skill"
)

// projectMemoryFiles are read in order; the first that exists is the project
// memory the operator authored.
var projectMemoryFiles = []string{"TERMIXGO.md", "AGENTS.md", "CLAUDE.md"}

// projectMemoryCap is how much of the project memory reaches the prompt.
const projectMemoryCap = 10000

// BuildSystemParts separates the static prefix of the system prompt from the
// dynamic working plan. This maximizes KV cache hit rates across turns and steps.
func BuildSystemParts(env *Env, model string) (staticPrompt, dynamicPlan string) {
	var builder strings.Builder
	builder.WriteString(basePrompt)

	trusted := "untrusted (writes and commands need approval)"
	if env.Trusted {
		trusted = "trusted (writes and commands run without asking)"
	}
	builder.WriteString("\n\n## ENVIRONMENT\n")
	fmt.Fprintf(&builder, "- Workspace root: %s\n", env.Workspace)
	fmt.Fprintf(&builder, "- Platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&builder, "- Folder trust: %s\n", trusted)
	if config.ApprovalMode(env.Config.ApprovalMode) == config.ApprovalPlan {
		builder.WriteString("- Plan mode is ON: investigate, read, search and record the plan with todo_write, but never call a mutating tool. Mutating calls are blocked automatically, so do not retry one that was refused.")
	}
	if strings.TrimSpace(model) != "" {
		fmt.Fprintf(&builder, "- Model: %s\n", model)
	}

	if memory := readProjectMemory(env.Workspace); memory != "" {
		builder.WriteString("\n\n## PROJECT MEMORY - ")
		builder.WriteString(memoryName(env.Workspace))
		builder.WriteByte('\n')
		builder.WriteString(memory)
	}
	if env.Memory != nil {
		builder.WriteString(env.Memory.PromptBlock())
	}
	if block := skill.PromptBlock(env.Skills); block != "" {
		builder.WriteString(block)
	}
	if custom := strings.TrimSpace(env.Config.SystemPrompt); custom != "" {
		builder.WriteString("\n\n## USER CUSTOM INSTRUCTIONS\n")
		builder.WriteString(custom)
	}
	staticPrompt = builder.String()

	if env.Todos != nil {
		dynamicPlan = env.Todos.PromptBlock()
	}
	return staticPrompt, dynamicPlan
}

// BuildSystem assembles the system prompt: the base instructions, the
// environment, the project and learned memory, the skill list, any custom
// instructions, and the current plan.
func BuildSystem(env *Env, model string) string {
	staticPrompt, dynamicPlan := BuildSystemParts(env, model)
	return staticPrompt + dynamicPlan
}

func memoryName(workspace string) string {
	for _, name := range projectMemoryFiles {
		if exists(filepath.Join(workspace, name)) {
			return name
		}
	}
	return projectMemoryFiles[0]
}

// readProjectMemory loads the operator-authored memory file, capped.
func readProjectMemory(workspace string) string {
	if strings.TrimSpace(workspace) == "" {
		return ""
	}
	for _, name := range projectMemoryFiles {
		data, err := os.ReadFile(filepath.Join(workspace, name))
		if err != nil {
			continue
		}
		text := strings.TrimSpace(string(data))
		// clipBytes, not a raw slice: the cap is bytes and the memory file is
		// UTF-8, so a fixed offset can land inside a multi-byte character and
		// put half a rune at the end of the system prompt.
		if len(text) > projectMemoryCap {
			text = clipBytes(text, projectMemoryCap) + "\n... [truncated]"
		}
		return text
	}
	return ""
}

const basePrompt = `You are Termixgo, a coding agent that runs in a plain terminal. You work directly in the operator's workspace and every action you take is shown to them as a line of terminal output.

## HOW TO WORK
- Inspect before you change. Read a file before editing it.
- Use edit for a small change and write_file for a new file or a full rewrite.
- After changing code, run the project's own checks with run_checks (test, lint, typecheck, build). Do not claim success without running them.
- For any task with three or more steps, call todo_write and keep exactly one item in_progress.
- Ground every statement in what you actually read or ran. Never invent file contents, APIs or command output.
- Keep answers short. The operator reads them in a terminal.
- Prefer several small, verifiable steps over one large change.
- Search with grep and glob before assuming a file does not exist.

## TOOL USE
- Paths may be absolute or relative to the workspace root.
- edit requires old_string to match the file byte for byte and to be unique. Copy it from read_file output, without any line-number prefix.
- run_command runs through PowerShell on Windows and sh elsewhere. Use run_checks when the project has its own task for a check.
- ask_user only when a decision genuinely cannot be made from the code.
- use_skill loads a skill body when the SKILLS list shows a relevant one.
- For any website, page, dashboard, or visual interface build or refactor, load the hallmark skill (design direction) and the impeccable skill (polish) with use_skill before proposing or changing the UI, and judge the work against both.
- When asked to create, generate, or draw an image, visual mockup, or diagram, delegate the request to the image subagent using run_subagent with type="image".

## WEB ACCESS
- web_fetch reads one URL you already know. It resolves the name over DNS-over-HTTPS when the system resolver fails, and falls back to the r.jina.ai reader when a direct fetch is blocked; set reader=true for a JavaScript page.
- web_search finds URLs for a question. It resolves over DNS-over-HTTPS too and uses a configured Tavily or Brave key before the keyless sources.
- lookup answers a fixed source directly: kind=weather, kind=fx, kind=crypto, kind=wiki. Prefer it over a search for those.
- A failed fetch or search is about that host or query, not proof the machine is offline. Never tell the operator the machine cannot fetch or search. Try another URL or query, use lookup, and only call it an outage if several unrelated hosts fail.

## STYLE
- No emojis. No em-dashes. No filler.
- State what you are about to do, then do it, then report the result.
- When you are blocked, say exactly what is missing.`

// FormatEnvironmentBlock renders the per-turn environment hint appended to the
// user's message, which keeps the system prompt cacheable.
func FormatEnvironmentBlock(workspace, trust string) string {
	var builder strings.Builder
	builder.WriteString("<env>")
	fmt.Fprintf(&builder, "\nworkspace_root: %s", workspace)
	fmt.Fprintf(&builder, "\nfolder_trust: %s", trust)
	builder.WriteString("\n</env>")
	return builder.String()
}

// TrustLabel is the short trust word shown to the model and the operator.
func TrustLabel(trusted bool) string {
	if trusted {
		return "trusted"
	}
	return "untrusted"
}

// ApprovalModeOrDefault reads the configured mode with a safe fallback.
func ApprovalModeOrDefault(cfg config.Config) ApprovalMode {
	switch config.ApprovalMode(cfg.ApprovalMode) {
	case config.ApprovalAsk:
		return ApprovalAsk
	case config.ApprovalEdits:
		return ApprovalEdits
	case config.ApprovalPlan:
		return ApprovalPlan
	default:
		return ApprovalAll
	}
}
