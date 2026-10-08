package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// findToolsName is the discovery tool. It is referenced by the fallback
// message and by the runner's always-on set.
const findToolsName = "find_tools"

// toolSearchAlwaysOn is the coding loop: the tools every request keeps while
// tool search is on. The ecosystem tools (GitHub, pipelines, images, skills,
// memory, web) are loaded on demand by find_tools.
var toolSearchAlwaysOn = map[string]bool{
	"read_file": true, "list_directory": true, "write_file": true,
	"edit": true, "multi_edit": true, "apply_patch": true, "undo_edit": true,
	"create_directory": true, "delete_file": true, "move_file": true,
	"grep": true, "glob": true, "code_outline": true, "symbol_search": true,
	"run_command": true, "run_checks": true,
	"background": true, "logs": true, "wait": true, "list_processes": true, "kill": true,
	"todo_write": true, "todo_read": true,
	"think": true, "subagent": true,
	"ask_user":   true,
	"git_status": true, "git_diff": true, "git_log": true, "git_show": true,
	"git_blame": true,
	"git_add":   true, "git_commit": true, "git_branch": true, "git_restore": true,
	findToolsName: true,
}

// toolSearchHint tells the model the deferred tools exist. Without it the model
// reports a missing capability instead of asking for it, which is the failure
// that makes lazy loading feel broken.
const toolSearchHint = "Additional tools are loaded on demand and are not visible to you yet. " +
	"Before saying a capability is missing, call \"" + findToolsName + "\" with a keyword to load it. " +
	"The core coding tools (files, shell, search, git, todos) are already available."

// ToolIndexEntry is one searchable tool, reduced to what keyword search needs.
type ToolIndexEntry struct {
	Name    string
	Summary string
}

// buildToolIndex lists every tool a discovery search may return: everything
// except the always-on loop and the discovery tool itself.
func buildToolIndex(tools []Tool, alwaysOn map[string]bool) []ToolIndexEntry {
	index := make([]ToolIndexEntry, 0, len(tools))
	for _, tool := range tools {
		name := strings.ToLower(tool.Name())
		if alwaysOn[name] || name == findToolsName {
			continue
		}
		index = append(index, ToolIndexEntry{Name: tool.Name(), Summary: firstSentence(tool.Description(), 140)})
	}
	sort.Slice(index, func(i, j int) bool { return index[i].Name < index[j].Name })
	return index
}

// firstSentence trims a description to its first sentence for a result list.
func firstSentence(description string, cap int) string {
	text := strings.Join(strings.Fields(strings.TrimSpace(description)), " ")
	if text == "" {
		return ""
	}
	if stop := strings.Index(text, ". "); stop >= 0 {
		text = text[:stop+1]
	}
	if cap > 0 && len(text) > cap {
		text = clipBytes(text, cap-1) + "..."
	}
	return text
}

// searchToolIndex ranks the index against a free-text query. It is pure and
// deterministic so a miss is explainable rather than a tuning mystery.
func searchToolIndex(index []ToolIndexEntry, query string, limit int) []ToolIndexEntry {
	if limit <= 0 {
		limit = 8
	}
	words := make([]string, 0, 4)
	for _, word := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		if len(word) > 1 {
			words = append(words, word)
		}
	}
	if len(words) == 0 {
		return nil
	}
	phrase := strings.Join(words, "")
	needle := strings.TrimSpace(strings.ToLower(query))

	type scored struct {
		entry ToolIndexEntry
		score int
	}
	ranked := make([]scored, 0, len(index))
	for _, entry := range index {
		name := strings.ToLower(entry.Name)
		summary := strings.ToLower(entry.Summary)
		flat := strings.ReplaceAll(name, "_", "")
		score := 0
		if name == needle {
			score += 100
		}
		if flat == phrase {
			score += 60
		}
		if strings.Contains(name, phrase) {
			score += 20
		}
		if strings.Contains(summary, phrase) {
			score += 10
		}
		for _, word := range words {
			switch {
			case name == word:
				score += 30
			case strings.HasPrefix(name, word+"_"):
				score += 12
			case strings.Contains(name, word):
				score += 6
			}
			if strings.Contains(summary, word) {
				score += 2
			}
		}
		if score > 0 {
			ranked = append(ranked, scored{entry: entry, score: score})
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].entry.Name < ranked[j].entry.Name
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	out := make([]ToolIndexEntry, 0, len(ranked))
	for _, item := range ranked {
		out = append(out, item.entry)
	}
	return out
}

// suggestToolNames proposes near matches for a tool the model asked for by a
// wrong name. It is the recovery path for a model trained on another toolset.
func suggestToolNames(requested string, available []string, limit int) []string {
	if limit <= 0 {
		limit = 5
	}
	needle := strings.ToLower(strings.TrimSpace(requested))
	if needle == "" {
		return nil
	}
	type scored struct {
		name  string
		score int
	}
	ranked := make([]scored, 0, len(available))
	for _, name := range available {
		lower := strings.ToLower(name)
		score := 0
		switch {
		case lower == needle:
			score = 100
		case strings.HasPrefix(lower, needle):
			// read_img -> read_image.
			score = 80
		case strings.HasPrefix(needle, lower):
			score = 70
		case strings.Contains(lower, needle):
			score = 60
		case strings.Contains(needle, lower):
			score = 50
		default:
			// A shared head or tail: view_file -> read_file.
			score = 6*commonPrefix(lower, needle) + 6*commonSuffix(lower, needle)
			if score > 50 {
				score = 50
			}
		}
		if score > 0 {
			ranked = append(ranked, scored{name: name, score: score})
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].name < ranked[j].name
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	out := make([]string, 0, len(ranked))
	for _, item := range ranked {
		out = append(out, item.name)
	}
	return out
}

func commonPrefix(a, b string) int {
	limit := len(a)
	if len(b) < limit {
		limit = len(b)
	}
	count := 0
	for count < limit && a[count] == b[count] {
		count++
	}
	return count
}

func commonSuffix(a, b string) int {
	limit := len(a)
	if len(b) < limit {
		limit = len(b)
	}
	count := 0
	for count < limit && a[len(a)-1-count] == b[len(b)-1-count] {
		count++
	}
	return count
}

// unknownToolMessage is the reply for a tool that does not exist. The wording
// is a pure function so it can be asserted in a test.
func unknownToolMessage(requested string, available []string, toolSearch bool) string {
	sorted := append([]string{}, available...)
	sort.Strings(sorted)
	lines := []string{fmt.Sprintf("There is no tool named %q.", requested)}
	if suggestions := suggestToolNames(requested, sorted, 5); len(suggestions) > 0 {
		lines = append(lines, "Did you mean: "+strings.Join(suggestions, ", ")+"?")
	}
	if toolSearch {
		lines = append(lines, fmt.Sprintf("More tools load on demand: call %q with a keyword to find one.", findToolsName))
	}
	lines = append(lines, "Do not repeat this call. Tools available now: "+strings.Join(sorted, ", ")+".")
	return strings.Join(lines, "\n")
}

// findToolsTool loads deferred tools by keyword.
type findToolsTool struct{}

func (t *findToolsTool) Name() string      { return findToolsName }
func (t *findToolsTool) Aliases() []string { return []string{"search_tools"} }
func (t *findToolsTool) Mutating() bool    { return false }
func (t *findToolsTool) Risk() Risk        { return RiskEdit }
func (t *findToolsTool) Label(a map[string]any) string {
	return "Finding tools " + Shorten(argString(a, "query"), 30)
}
func (t *findToolsTool) DoneLabel(a map[string]any) string {
	return "Found tools " + Shorten(argString(a, "query"), 30)
}
func (t *findToolsTool) Description() string {
	return "Find tools by keyword and make the matches available. Call this before saying a capability is missing. One or two words works better than a sentence."
}
func (t *findToolsTool) Schema() map[string]any {
	return object(map[string]any{
		"query": strProp("Keyword or tool name to load."),
		"limit": intProp("Maximum matches to load, 1 to 20. Defaults to 8."),
	}, "query")
}

func (t *findToolsTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	query := strings.TrimSpace(argString(args, "query"))
	if query == "" {
		return Result{Output: "query is required", IsError: true}, nil
	}
	if env == nil || len(env.ToolIndex) == 0 {
		return Result{Output: "Tool discovery is not available in this session; every tool is already loaded."}, nil
	}
	limit := argInt(args, "limit", 8, 1, 20)
	matches := searchToolIndex(env.ToolIndex, query, limit)
	if len(matches) == 0 {
		return Result{Output: fmt.Sprintf("No tool matches %q. Try another keyword or an exact tool name.", query)}, nil
	}
	names := make([]string, 0, len(matches))
	var builder strings.Builder
	builder.WriteString("These tools are now available. Call them directly.\n")
	for _, match := range matches {
		names = append(names, match.Name)
		fmt.Fprintf(&builder, "- %s: %s\n", match.Name, match.Summary)
	}
	if env.DiscoverTools != nil {
		env.DiscoverTools(names)
	}
	return Result{Output: strings.TrimRight(builder.String(), "\n")}, nil
}
