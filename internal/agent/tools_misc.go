package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/99apps-id/termixgo/internal/skill"
)

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
		return Result{Output: "That host is not reachable from the agent (link-local or loopback metadata address).", IsError: true}, nil
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
		text = text[:20000] + "\n... [truncated]"
	}
	return Result{Output: text}, nil
}

// isBlockedHost refuses the cloud metadata addresses, which are never the
// documentation the model meant to read.
func isBlockedHost(host string) bool {
	if host == "169.254.169.254" || host == "metadata.google.internal" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLinkLocalUnicast() {
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
