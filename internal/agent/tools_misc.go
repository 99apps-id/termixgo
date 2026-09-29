package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/skill"
)

// searchMemoryTool searches the agent memory and workspace using FTS5.
type searchMemoryTool struct{}

func (t *searchMemoryTool) Name() string      { return "search_memory" }
func (t *searchMemoryTool) Aliases() []string { return []string{"fts_search", "memory_search"} }
func (t *searchMemoryTool) Mutating() bool    { return false }
func (t *searchMemoryTool) Risk() Risk        { return RiskEdit }
func (t *searchMemoryTool) Label(a map[string]any) string {
	return "Searching memory for " + Shorten(argString(a, "query"), 40)
}
func (t *searchMemoryTool) DoneLabel(a map[string]any) string {
	return "Searched memory"
}
func (t *searchMemoryTool) Description() string {
	return "Search the agent's learned memory, error journal, and indexed workspace files using full-text search."
}
func (t *searchMemoryTool) Schema() map[string]any {
	return object(map[string]any{
		"query": strProp("Full-text search query."),
		"scope": strProp("Optional scope filter: memory, journal, workspace, or all."),
	}, "query")
}

func (t *searchMemoryTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	query := strings.TrimSpace(argString(args, "query"))
	if query == "" {
		return Result{Output: "query is required", IsError: true}, nil
	}
	scope := strings.ToLower(strings.TrimSpace(argString(args, "scope")))
	if scope == "" {
		scope = "all"
	}

	// Try the FTS5 search store if available. The scope goes into the query
	// rather than filtering the results afterwards: the store returns the best
	// twenty rows overall, so a scope whose matches all ranked lower would have
	// looked like "no matches" even though the index held them.
	if env.Search != nil {
		results, err := env.Search.SearchScope(query, scope, 20)
		if err == nil && len(results) > 0 {
			var b strings.Builder
			b.WriteString(fmt.Sprintf("FTS5 results for %q:\n\n", query))
			for _, r := range results {
				b.WriteString(fmt.Sprintf("[%s] %s (%s)\n%s\n\n", r.Scope, r.Title, r.Path, r.Snippet))
			}
			return Result{Output: strings.TrimRight(b.String(), "\n")}, nil
		}
	}

	// Fallback: plain text search through memory and journal files.
	var matches []string
	mem := NewMemory(env.Workspace)
	block := mem.PromptBlock()
	if block != "" && strings.Contains(strings.ToLower(block), strings.ToLower(query)) {
		matches = append(matches, "[memory] "+block)
	}
	if env.Journal != nil {
		for _, p := range env.Journal.Patterns() {
			if strings.Contains(strings.ToLower(p.Error), strings.ToLower(query)) ||
				strings.Contains(strings.ToLower(p.Tool), strings.ToLower(query)) {
				matches = append(matches, fmt.Sprintf("[journal] %s x%d: %s (hint: %s)", p.Tool, p.Count, p.Error, p.Hint))
			}
		}
	}
	if len(matches) == 0 {
		return Result{Output: fmt.Sprintf("No matches for %q in memory or journal.", query)}, nil
	}
	return Result{Output: strings.Join(matches, "\n\n")}, nil
}

// installSkillTool installs a skill from a git repository or local path.
type installSkillTool struct{}

func (t *installSkillTool) Name() string      { return "install_skill" }
func (t *installSkillTool) Aliases() []string { return []string{"add_skill"} }

// Mutating is true even though the tool only writes outside the workspace: it
// copies files onto disk, so it must sit behind the approval policy like any
// other write. Reporting false let it run unnoticed in plan mode and in an
// untrusted folder.
func (t *installSkillTool) Mutating() bool { return true }
func (t *installSkillTool) Risk() Risk     { return RiskEdit }
func (t *installSkillTool) Label(a map[string]any) string {
	return "Installing skill " + Shorten(argString(a, "source"), 40)
}
func (t *installSkillTool) DoneLabel(a map[string]any) string {
	return "Installed skill " + Shorten(argString(a, "source"), 40)
}
func (t *installSkillTool) Description() string {
	return "Install a skill from a git repository URL or local filesystem path. The source must contain a SKILL.md file."
}
func (t *installSkillTool) Schema() map[string]any {
	return object(map[string]any{
		"source": strProp("Git repository URL or local path to a skill folder."),
		"scope":  strProp("Installation scope: 'user' (default) or 'project'."),
		"name":   strProp("Optional skill name override."),
	}, "source")
}

func (t *installSkillTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	source := strings.TrimSpace(argString(args, "source"))
	if source == "" {
		return Result{Output: "source is required", IsError: true}, nil
	}
	scope := strings.ToLower(strings.TrimSpace(argString(args, "scope")))
	if scope != "project" {
		scope = "user"
	}
	nameOverride := strings.TrimSpace(argString(args, "name"))

	workdir := ""
	if isGitURL(source) {
		repoDir, err := cloneGitRepo(ctx, source)
		if err != nil {
			return Result{Output: fmt.Sprintf("failed to clone %s: %v", source, err), IsError: true}, nil
		}
		defer os.RemoveAll(repoDir)
		workdir = repoDir
	} else {
		abs, err := filepath.Abs(source)
		if err != nil {
			return Result{Output: fmt.Sprintf("invalid source path: %v", err), IsError: true}, nil
		}
		info, err := os.Stat(abs)
		if err != nil || !info.IsDir() {
			return Result{Output: fmt.Sprintf("source is not a directory: %s", abs), IsError: true}, nil
		}
		workdir = abs
	}

	skillPath, err := findSkillMarkdown(workdir)
	if err != nil {
		return Result{Output: fmt.Sprintf("no SKILL.md found in %s: %v", workdir, err), IsError: true}, nil
	}

	frontmatter, _, err := parseSkillFile(skillPath)
	if err != nil {
		return Result{Output: fmt.Sprintf("invalid SKILL.md: %v", err), IsError: true}, nil
	}

	name := nameOverride
	if name == "" {
		name = frontmatter.Name
	}
	if name == "" {
		name = skill.NormalizeName(filepath.Base(workdir))
	}

	installDir, err := skillInstallDir(env, scope)
	if err != nil {
		return Result{Output: fmt.Sprintf("cannot resolve skill directory: %v", err), IsError: true}, nil
	}

	targetDir := filepath.Join(installDir, name)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return Result{Output: fmt.Sprintf("cannot create skill directory: %v", err), IsError: true}, nil
	}

	targetPath := filepath.Join(targetDir, "SKILL.md")
	if err := copyFile(skillPath, targetPath); err != nil {
		return Result{Output: fmt.Sprintf("cannot install skill: %v", err), IsError: true}, nil
	}

	helperFiles := make([]string, 0)
	sourceBase := filepath.Dir(skillPath)
	entries, _ := os.ReadDir(sourceBase)
	for _, entry := range entries {
		if entry.Name() == "SKILL.md" || entry.IsDir() {
			continue
		}
		src := filepath.Join(sourceBase, entry.Name())
		dst := filepath.Join(targetDir, entry.Name())
		if err := copyFile(src, dst); err != nil {
			return Result{Output: fmt.Sprintf("cannot copy helper file %s: %v", entry.Name(), err), IsError: true}, nil
		}
		helperFiles = append(helperFiles, entry.Name())
	}

	description := frontmatter.Description
	if description == "" {
		description = "(no description)"
	}
	return Result{Output: fmt.Sprintf("Installed skill %q to %s (scope %s). Description: %s. Files: %s", name, targetDir, scope, description, strings.Join(helperFiles, ", "))}, nil
}

// isGitURL reports whether the source looks like a git URL.
func isGitURL(source string) bool {
	source = strings.ToLower(source)
	return strings.HasPrefix(source, "https://") ||
		strings.HasPrefix(source, "http://") ||
		strings.HasPrefix(source, "git@") ||
		strings.HasPrefix(source, "ssh://") ||
		strings.HasSuffix(source, ".git")
}

// cloneGitRepo clones a git repository to a temporary directory and returns
// the path. The caller is responsible for removing the directory.
func cloneGitRepo(ctx context.Context, source string) (string, error) {
	tmp, err := os.MkdirTemp("", "skill-clone-*")
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth=1", "--no-checkout", source, tmp)
	cmd.Dir = os.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		os.RemoveAll(tmp)
		return "", fmt.Errorf("git clone failed: %s: %v", strings.TrimSpace(string(output)), err)
	}
	return tmp, nil
}

// findSkillMarkdown walks a directory and returns the path to the first
// SKILL.md file it finds.
func findSkillMarkdown(root string) (string, error) {
	var found string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d == nil || d.IsDir() {
			return nil
		}
		if strings.EqualFold(filepath.Base(path), "SKILL.md") {
			found = path
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("no SKILL.md found in %s", root)
	}
	return found, nil
}

// parseSkillFile reads a SKILL.md and returns its frontmatter and body.
func parseSkillFile(path string) (skill.Frontmatter, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return skill.Frontmatter{}, "", err
	}
	frontmatter, body, err := skill.Parse(data)
	if err != nil {
		return skill.Frontmatter{}, "", err
	}
	_ = body
	return frontmatter, body, nil
}

// skillInstallDir returns the directory where skills of the given scope are
// installed.
func skillInstallDir(env *Env, scope string) (string, error) {
	if scope == "project" {
		if strings.TrimSpace(env.Workspace) == "" {
			return "", fmt.Errorf("no workspace configured for project skills")
		}
		return skill.ProjectDir(env.Workspace), nil
	}
	home, err := config.Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "skills"), nil
}

// copyFile copies a file from src to dst. The output is closed explicitly so a
// write failure is reported rather than lost, which a deferred close would hide.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// todoWriteTool replaces the plan.
type todoWriteTool struct{}

func (t *todoWriteTool) Name() string      { return "todo_write" }
func (t *todoWriteTool) Aliases() []string { return []string{"update_plan", "write_todos"} }
func (t *todoWriteTool) Mutating() bool    { return false }
func (t *todoWriteTool) Risk() Risk        { return RiskEdit }
func (t *todoWriteTool) Label(a map[string]any) string {
	return "Updating plan"
}
func (t *todoWriteTool) DoneLabel(a map[string]any) string { return "Updated plan" }
func (t *todoWriteTool) Description() string {
	return "Replace the task plan. Keep exactly one item in_progress. Use this for any task with three or more steps so progress stays visible."
}
func (t *todoWriteTool) Schema() map[string]any {
	return object(map[string]any{
		"todos": arrayProp("The full plan, in order.", object(map[string]any{
			"id":     strProp("Short stable id, optional."),
			"title":  strProp("What the step does, in the imperative."),
			"status": map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "completed"}},
		}, "title", "status")),
	}, "todos")
}

func (t *todoWriteTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	items, err := parseTodos(args["todos"])
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	if err := env.Todos.Write(items); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	plan := env.Todos.Items()
	if env.Emit != nil {
		env.Emit(Event{Kind: EventPlan, Plan: plan})
	}
	done, total := env.Todos.Progress()
	return Result{
		Output: fmt.Sprintf("Plan updated: %d of %d complete.", done, total),
		Plan:   plan,
	}, nil
}

// todoReadTool lists the plan.
type todoReadTool struct{}

func (t *todoReadTool) Name() string      { return "todo_read" }
func (t *todoReadTool) Aliases() []string { return []string{"read_plan"} }
func (t *todoReadTool) Mutating() bool    { return false }
func (t *todoReadTool) Risk() Risk        { return RiskEdit }
func (t *todoReadTool) Label(a map[string]any) string {
	return "Reading plan"
}
func (t *todoReadTool) DoneLabel(a map[string]any) string { return "Read plan" }
func (t *todoReadTool) Description() string {
	return "Read the current task plan."
}
func (t *todoReadTool) Schema() map[string]any { return object(map[string]any{}) }

func (t *todoReadTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	items := env.Todos.Items()
	if len(items) == 0 {
		return Result{Output: "The plan is empty."}, nil
	}
	var builder strings.Builder
	for _, item := range items {
		fmt.Fprintf(&builder, "- [%s] %s\n", item.Status, item.Title)
	}
	return Result{Output: strings.TrimRight(builder.String(), "\n")}, nil
}

// rememberTool appends a learned fact.
type rememberTool struct{}

func (t *rememberTool) Name() string      { return "remember" }
func (t *rememberTool) Aliases() []string { return []string{"learn"} }
func (t *rememberTool) Mutating() bool    { return true }
func (t *rememberTool) Risk() Risk        { return RiskEdit }
func (t *rememberTool) Label(a map[string]any) string {
	return "Remembering: " + Shorten(argString(a, "fact"), 50)
}
func (t *rememberTool) DoneLabel(a map[string]any) string {
	return "Remembered: " + Shorten(argString(a, "fact"), 50)
}
func (t *rememberTool) Description() string {
	return "Store a durable fact or convention so later sessions know it. Use project scope for this codebase, global for cross-project preferences."
}
func (t *rememberTool) Schema() map[string]any {
	return object(map[string]any{
		"fact":  strProp("The fact to remember, one sentence."),
		"scope": map[string]any{"type": "string", "enum": []string{"project", "global"}, "description": "Defaults to project."},
	}, "fact")
}

func (t *rememberTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	fact := argString(args, "fact")
	if fact == "" {
		return Result{Output: "fact is required", IsError: true}, nil
	}
	if env.Memory == nil {
		return Result{Output: "memory is not available", IsError: true}, nil
	}
	scope := strings.ToLower(strings.TrimSpace(argString(args, "scope")))
	if scope != "global" {
		scope = "project"
	}
	if err := env.Memory.Remember(fact, scope); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	// Keep the full-text index in step with the file that just changed, so a
	// later search_memory finds this fact through the index rather than only
	// through the substring fallback.
	IndexLearnedContent(env.Search, env.Memory, env.Journal)
	return Result{Output: fmt.Sprintf("Remembered (%s): %s", scope, Shorten(fact, 120))}, nil
}

// useSkillTool loads one skill body.
type useSkillTool struct{}

func (t *useSkillTool) Name() string      { return "use_skill" }
func (t *useSkillTool) Aliases() []string { return []string{"load_skill"} }
func (t *useSkillTool) Mutating() bool    { return false }
func (t *useSkillTool) Risk() Risk        { return RiskEdit }
func (t *useSkillTool) Label(a map[string]any) string {
	return "Loading skill " + argString(a, "name")
}
func (t *useSkillTool) DoneLabel(a map[string]any) string {
	return "Loaded skill " + argString(a, "name")
}
func (t *useSkillTool) Description() string {
	return "Load a skill's full instructions by name. The SKILLS section of the system prompt lists what is available."
}
func (t *useSkillTool) Schema() map[string]any {
	return object(map[string]any{"name": strProp("Skill name.")}, "name")
}

func (t *useSkillTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	name := argString(args, "name")
	if name == "" {
		return Result{Output: "name is required", IsError: true}, nil
	}
	for _, item := range env.Skills {
		if strings.EqualFold(item.Name, name) {
			var builder strings.Builder
			fmt.Fprintf(&builder, "SKILL %s (scope %s)\n\n%s", item.Name, item.Scope, item.Body)
			if len(item.Files) > 0 {
				fmt.Fprintf(&builder, "\n\nHelper files in %s: %s", item.Path, strings.Join(item.Files, ", "))
			}
			return Result{Output: builder.String()}, nil
		}
	}
	return Result{Output: fmt.Sprintf("No skill named %q. Available: %s", name, skillNames(env.Skills)), IsError: true}, nil
}

// findSkillTool searches skills.
type findSkillTool struct{}

func (t *findSkillTool) Name() string      { return "find_skill" }
func (t *findSkillTool) Aliases() []string { return []string{"search_skills"} }
func (t *findSkillTool) Mutating() bool    { return false }
func (t *findSkillTool) Risk() Risk        { return RiskEdit }
func (t *findSkillTool) Label(a map[string]any) string {
	return "Finding skill " + Shorten(argString(a, "query"), 30)
}
func (t *findSkillTool) DoneLabel(a map[string]any) string {
	return "Found skill " + Shorten(argString(a, "query"), 30)
}
func (t *findSkillTool) Description() string {
	return "Search the available skills by keyword when the right one is not obvious from the SKILLS list."
}
func (t *findSkillTool) Schema() map[string]any {
	return object(map[string]any{"query": strProp("Keyword to search for.")}, "query")
}

func (t *findSkillTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	query := strings.ToLower(strings.TrimSpace(argString(args, "query")))
	if len(query) < 2 {
		return Result{Output: "query must be at least two characters", IsError: true}, nil
	}
	var matches []string
	for _, item := range env.Skills {
		if strings.Contains(strings.ToLower(item.Name+" "+item.Description), query) {
			matches = append(matches, fmt.Sprintf("- %s: %s", item.Name, item.Description))
		}
	}
	if len(matches) == 0 {
		return Result{Output: fmt.Sprintf("No skill matches %q. Available: %s", query, skillNames(env.Skills))}, nil
	}
	return Result{Output: strings.Join(matches, "\n")}, nil
}

func skillNames(skills []skill.Skill) string {
	if len(skills) == 0 {
		return "(none)"
	}
	names := make([]string, 0, len(skills))
	for _, item := range skills {
		names = append(names, item.Name)
	}
	return strings.Join(names, ", ")
}

// askUserTool asks the operator a question mid-run.
type askUserTool struct{}

func (t *askUserTool) Name() string      { return "ask_user" }
func (t *askUserTool) Aliases() []string { return []string{"ask", "elicitation"} }
func (t *askUserTool) Mutating() bool    { return false }
func (t *askUserTool) Risk() Risk        { return RiskEdit }
func (t *askUserTool) Label(a map[string]any) string {
	return "Asking: " + Shorten(argString(a, "question"), 50)
}
func (t *askUserTool) DoneLabel(a map[string]any) string {
	return "Asked: " + Shorten(argString(a, "question"), 50)
}
func (t *askUserTool) Description() string {
	return "Ask the operator a question when a decision cannot be made from the code. Provide two to five short options when the answer is a choice."
}
func (t *askUserTool) Schema() map[string]any {
	return object(map[string]any{
		"question": strProp("The question to ask."),
		"options":  arrayProp("Optional short choices, two to five.", strProp("One choice.")),
	}, "question")
}

func (t *askUserTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	question := argString(args, "question")
	if question == "" {
		return Result{Output: "question is required", IsError: true}, nil
	}
	if env.Ask == nil {
		return Result{Output: "The operator is not available for questions in this mode.", IsError: true}, nil
	}
	options := argList(args, "options")
	answer, err := env.Ask(question, options)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	return Result{Output: fmt.Sprintf("The operator answered: %s", answer)}, nil
}

// thinkTool records internal reasoning.
type thinkTool struct{}

func (t *thinkTool) Name() string      { return "think" }
func (t *thinkTool) Aliases() []string { return []string{"reason"} }
func (t *thinkTool) Mutating() bool    { return false }
func (t *thinkTool) Risk() Risk        { return RiskEdit }
func (t *thinkTool) Label(a map[string]any) string {
	return "Thinking"
}
func (t *thinkTool) DoneLabel(a map[string]any) string { return "Thought" }
func (t *thinkTool) Description() string {
	return "Record a short line of reasoning before acting, so the operator can follow the plan. Does not change anything."
}
func (t *thinkTool) Schema() map[string]any {
	return object(map[string]any{"thoughts": strProp("The reasoning to record.")}, "thoughts")
}

func (t *thinkTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	thoughts := argString(args, "thoughts", "thought")
	if thoughts == "" {
		return Result{Output: "thoughts is required", IsError: true}, nil
	}
	if env.Emit != nil {
		env.Emit(Event{Kind: EventThinking, Text: thoughts})
		env.Emit(Event{Kind: EventReasoned, Text: thoughts})
	}
	return Result{Output: "Noted."}, nil
}

// subagentTool delegates a self-contained task to a nested Termigo-style
// worker with the full toolset and no sandbox. Review types are read-only
// by design; every other type can read, edit and run commands as needed.
type subagentTool struct{}

func (t *subagentTool) Name() string      { return "run_subagent" }
func (t *subagentTool) Aliases() []string { return []string{"task", "delegate"} }
func (t *subagentTool) Mutating() bool    { return false }
func (t *subagentTool) Risk() Risk        { return RiskEdit }
func (t *subagentTool) Label(a map[string]any) string {
	return "Delegating (" + LookupSubagent(argString(a, "type")).Label + "): " + Shorten(argString(a, "prompt", "description"), 50)
}
func (t *subagentTool) DoneLabel(a map[string]any) string {
	return "Delegated (" + LookupSubagent(argString(a, "type")).Label + "): " + Shorten(argString(a, "prompt", "description"), 50)
}
func (t *subagentTool) Description() string {
	return "Spawn a subagent worker (explore, general, builder, code-review, security) with the full toolset and no sandbox. Use it for a self-contained task so the main context stays lean. Review types only read."
}
func (t *subagentTool) Schema() map[string]any {
	return object(map[string]any{
		"prompt":      strProp("The task to carry out, stated precisely."),
		"description": strProp("Three to five words naming the task."),
		"type":        strProp("Worker type: explore, general, builder, code-review or security. Defaults to general."),
	}, "prompt")
}

func (t *subagentTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	prompt := argString(args, "prompt")
	if prompt == "" {
		return Result{Output: "prompt is required", IsError: true}, nil
	}
	if env.RunSubagent == nil {
		return Result{Output: "Subagents are not available in this session.", IsError: true}, nil
	}
	if env.Depth >= MaxSubagentDepth {
		return Result{Output: "Subagents cannot nest more than three deep.", IsError: true}, nil
	}
	subType := strings.TrimSpace(argString(args, "type"))
	if subType == "" {
		subType = string(SubagentGeneral)
	}
	report, err := env.RunSubagent(ctx, subType, prompt)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	return Result{Output: report}, nil
}

// webFetchTool retrieves a URL as text.
type webFetchTool struct{}

func (t *webFetchTool) Name() string      { return "web_fetch" }
func (t *webFetchTool) Aliases() []string { return []string{"fetch_url", "http_get"} }
func (t *webFetchTool) Mutating() bool    { return false }
func (t *webFetchTool) Risk() Risk        { return RiskNetwork }
func (t *webFetchTool) Label(a map[string]any) string {
	return "Fetching " + Shorten(argString(a, "url"), 50)
}
func (t *webFetchTool) DoneLabel(a map[string]any) string {
	return "Fetched " + Shorten(argString(a, "url"), 50)
}
func (t *webFetchTool) Description() string {
	return "Fetch an http or https URL and return its readable text. HTML tags are stripped. Use it to read documentation."
}
func (t *webFetchTool) Schema() map[string]any {
	return object(map[string]any{"url": strProp("Absolute http or https URL.")}, "url")
}

// RE2 has no backreferences, so each container tag is spelled out.
var (
	scriptBlock = regexp.MustCompile(`(?is)<script[^>]*>.*?</script\s*>|<style[^>]*>.*?</style\s*>|<noscript[^>]*>.*?</noscript\s*>`)
	tagPattern  = regexp.MustCompile(`(?s)<[^>]*>`)
	spaceRun    = regexp.MustCompile(`[ \t\r\f\v]+`)
	blankRun    = regexp.MustCompile(`\n{3,}`)
)

func (t *webFetchTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	raw := strings.TrimSpace(argString(args, "url"))
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return Result{Output: "url must be an absolute http or https URL", IsError: true}, nil
	}
	if isBlockedHost(parsed.Hostname()) {
		return Result{Output: "That host is a link-local or cloud metadata address, which the agent does not fetch.", IsError: true}, nil
	}
	requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, raw, nil)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	request.Header.Set("User-Agent", "Termixgo/0.1 (+https://github.com/99apps-id/termixgo)")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return Result{Output: fmt.Sprintf("fetch failed: %v", err), IsError: true}, nil
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Result{Output: fmt.Sprintf("fetch returned %s", response.Status), IsError: true}, nil
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024))
	if err != nil {
		return Result{Output: fmt.Sprintf("read body: %v", err), IsError: true}, nil
	}
	text := string(body)
	if strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "html") {
		text = htmlToText(text)
	}
	if len(text) > 20000 {
		text = clipBytes(text, 20000) + "\n... [truncated]"
	}
	return Result{Output: text}, nil
}

// isBlockedHost refuses the cloud metadata addresses and the link-local range,
// which are never the documentation the model meant to read.
//
// Loopback and the private ranges are deliberately allowed: a documentation
// server running on the operator's own machine is a legitimate target, and the
// project's own tests fetch from one. That does leave the tool able to reach a
// service on the local network, which is recorded in the audit rather than
// enforced here.
func isBlockedHost(host string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(host))
	if trimmed == "169.254.169.254" || trimmed == "metadata.google.internal" {
		return true
	}
	if ip := net.ParseIP(trimmed); ip != nil && ip.IsLinkLocalUnicast() {
		return true
	}
	return false
}

func htmlToText(html string) string {
	stripped := scriptBlock.ReplaceAllString(html, " ")
	stripped = tagPattern.ReplaceAllString(stripped, "")
	stripped = strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", "\"", "&#39;", "'").Replace(stripped)
	stripped = spaceRun.ReplaceAllString(stripped, " ")
	stripped = blankRun.ReplaceAllString(stripped, "\n\n")
	return strings.TrimSpace(stripped)
}

func parseTodos(value any) ([]Todo, error) {
	if value == nil {
		return nil, fmt.Errorf("todos is required")
	}
	if text, ok := value.(string); ok {
		var decoded []Todo
		if err := json.Unmarshal([]byte(text), &decoded); err != nil {
			return nil, fmt.Errorf("todos string is not valid JSON: %v", err)
		}
		return decoded, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("todos must be an array")
	}
	todos := make([]Todo, 0, len(items))
	for index, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("todo %d must be an object", index+1)
		}
		todos = append(todos, Todo{
			ID:     argString(entry, "id"),
			Title:  argString(entry, "title", "description", "text"),
			Status: argString(entry, "status"),
			Parent: argString(entry, "parent"),
		})
	}
	return todos, nil
}
