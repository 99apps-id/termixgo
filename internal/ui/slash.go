package ui

import (
	"sort"
	"strings"

	"github.com/99apps-id/termixgo/internal/agent"
)

// SlashOption is one argument choice in the composer menu.
//
// A command whose argument is a closed set declares its choices, so picking the
// command opens them as a second-level menu. Without that, choosing "/trust"
// from the menu only printed the current state and the operator had to know the
// argument from memory, which made the menu look read-only.
type SlashOption struct {
	// Value is the argument to pass to the command.
	Value string
	// Detail is a short hint shown beside the value.
	Detail string
	// Complete means the value is only the start of the argument: choosing it
	// puts "/command value " in the composer and waits for the rest, which is
	// what a free-text argument such as a session id needs.
	Complete bool
}

// SlashCommand is one command the composer understands.
type SlashCommand struct {
	Trigger string
	Args    string
	Summary string
	// Options are the arguments the command accepts. An empty list means the
	// command runs as typed and takes free text or nothing.
	Options []SlashOption
}

// slashCommands is the catalogue, in display order.
var slashCommands = []SlashCommand{
	{Trigger: "/model", Args: "[id]", Summary: "Show or switch the model"},
	{Trigger: "/setup", Args: "", Summary: "Onboarding: provider key, skills, Telegram"},
	{Trigger: "/help", Args: "", Summary: "List every command and key binding"},
	{Trigger: "/new", Args: "", Summary: "Start a new session"},
	{Trigger: "/sessions", Args: "[list|search|rename|delete|export]", Summary: "List, find, rename, delete or export sessions", Options: []SlashOption{
		{Value: "list", Detail: "print every saved session with its id"},
		{Value: "search", Detail: "find a session by title or content", Complete: true},
		{Value: "rename", Detail: "retitle a session", Complete: true},
		{Value: "delete", Detail: "remove a session", Complete: true},
		{Value: "export", Detail: "print one session as Markdown", Complete: true},
	}},
	{Trigger: "/stop", Args: "", Summary: "Stop the running turn"},
	{Trigger: "/status", Args: "", Summary: "Show workspace, model and token status"},
	{Trigger: "/trust", Args: "[on|off]", Summary: "Show or change folder trust", Options: []SlashOption{
		{Value: "on", Detail: "writes and commands run without asking"},
		{Value: "off", Detail: "every mutating tool asks first"},
	}},
	{Trigger: "/approval", Args: "[ask|edits|all|plan]", Summary: "Show or change the approval policy", Options: []SlashOption{
		{Value: "ask", Detail: "every write and command waits"},
		{Value: "edits", Detail: "edits run, commands wait"},
		{Value: "all", Detail: "nothing waits"},
		{Value: "plan", Detail: "read only: mutating tools are blocked"},
	}},
	{Trigger: "/harness", Args: "[id]", Summary: "Show or change the agent harness"},
	{Trigger: "/plan", Args: "", Summary: "Show the current task plan"},
	{Trigger: "/tools", Args: "", Summary: "List the tools the agent can call"},
	{Trigger: "/mcp", Args: "[reload]", Summary: "Show the MCP servers and their tools", Options: []SlashOption{
		{Value: "reload", Detail: "reconnect every configured server"},
	}},
	{Trigger: "/skills", Args: "[list|reload|proposals|apply|reject]", Summary: "List or manage skills", Options: []SlashOption{
		{Value: "list", Detail: "show loaded skills"},
		{Value: "reload", Detail: "rescan the skill folders"},
		{Value: "proposals", Detail: "show pending skill proposals"},
		{Value: "apply", Detail: "apply a proposal by id", Complete: true},
		{Value: "reject", Detail: "drop a proposal by id", Complete: true},
	}},
	{Trigger: "/memory", Args: "", Summary: "Show what the agent has learned"},
	{Trigger: "/telegram", Args: "[setup|on|off|status|pair]", Summary: "Manage the Telegram companion", Options: []SlashOption{
		{Value: "status", Detail: "show whether the bot is running"},
		{Value: "on", Detail: "start the bot"},
		{Value: "off", Detail: "stop the bot"},
		{Value: "setup", Detail: "paste the bot token"},
		{Value: "pair", Detail: "show the code to send from Telegram"},
	}},
	{Trigger: "/cron", Args: "[list|add|remove|on|off|run]", Summary: "Schedule assistant jobs", Options: []SlashOption{
		{Value: "list", Detail: "show every scheduled job"},
		{Value: "add", Detail: "add a job, then type: every 30m :: <prompt>", Complete: true},
		{Value: "remove", Detail: "delete a job by id", Complete: true},
		{Value: "on", Detail: "enable a job by id", Complete: true},
		{Value: "off", Detail: "disable a job by id", Complete: true},
		{Value: "run", Detail: "run a job now by id", Complete: true},
	}},
	{Trigger: "/heartbeat", Args: "[on|off|<interval>]", Summary: "Periodic self-check", Options: []SlashOption{
		{Value: "on", Detail: "start a periodic silent heartbeat"},
		{Value: "off", Detail: "stop the heartbeat"},
		{Value: "30m", Detail: "run the heartbeat every 30 minutes"},
		{Value: "2h", Detail: "run the heartbeat every 2 hours"},
	}},
	{Trigger: "/audit", Args: "[count]", Summary: "Show recent audited actions"},
	{Trigger: "/init", Args: "", Summary: "Generate a TERMIXGO.md for this project"},
	{Trigger: "/cost", Args: "", Summary: "Show token usage for this session"},
	{Trigger: "/ps", Args: "[kill <handle>]", Summary: "List background processes, or stop one"},
	{Trigger: "/checkpoint", Args: "[list|<message>]", Summary: "Save or list working-tree checkpoints", Options: []SlashOption{
		{Value: "list", Detail: "show the checkpoints Termixgo saved"},
	}},
	{Trigger: "/rewind", Args: "[ref]", Summary: "Restore the newest checkpoint, undoing later edits"},
	{Trigger: "/worktree", Args: "[list|add|remove|touch|prune]", Summary: "Manage git worktrees for parallel tasks", Options: []SlashOption{
		{Value: "list", Detail: "show every worktree and its idle age"},
		{Value: "add", Detail: "create a parallel checkout", Complete: true},
		{Value: "remove", Detail: "delete a worktree", Complete: true},
		{Value: "touch", Detail: "mark a worktree as just used", Complete: true},
		{Value: "prune", Detail: "reclaim idle clean worktrees, snapshotted first", Complete: true},
	}},
	{Trigger: "/exit", Args: "", Summary: "Quit Termixgo"},
}

// harnessOptions names every profile, so picking /harness from the menu offers
// the same ids the command accepts instead of only listing them. It is built
// from the agent catalogue rather than duplicated here, and from HarnessProfiles
// rather than the map, so the order is the display order the picker and /help
// already use.
func harnessOptions() []SlashOption {
	profiles := agent.HarnessProfiles()
	options := make([]SlashOption, 0, len(profiles))
	for _, profile := range profiles {
		options = append(options, SlashOption{Value: profile.ID, Detail: profile.Description})
	}
	return options
}

// SlashCommands returns the catalogue.
func SlashCommands() []SlashCommand { return slashCommands }

// ParseSlash splits "/name args" into its parts. It returns ok=false when the
// input is not a slash command.
func ParseSlash(input string) (name, args string, ok bool) {
	trimmed := strings.TrimSpace(input)
	if !strings.HasPrefix(trimmed, "/") {
		return "", "", false
	}
	body := strings.TrimPrefix(trimmed, "/")
	if index := strings.IndexAny(body, " \n\t"); index >= 0 {
		return strings.ToLower(body[:index]), strings.TrimSpace(body[index+1:]), true
	}
	return strings.ToLower(body), "", true
}

// MatchSlash returns the commands whose trigger starts with the typed prefix.
// It backs the command palette and on-demand Tab/Enter completion; there is no
// always-on inline menu.
func MatchSlash(input string) []SlashCommand {
	trimmed := strings.TrimSpace(input)
	if !strings.HasPrefix(trimmed, "/") || strings.ContainsAny(trimmed, " \n\t") {
		return nil
	}
	matches := make([]SlashCommand, 0, len(slashCommands))
	for _, command := range slashCommands {
		if strings.HasPrefix(command.Trigger, strings.ToLower(trimmed)) {
			matches = append(matches, command)
		}
	}
	return matches
}

// FindSlash resolves a command name to its definition.
func FindSlash(name string) (SlashCommand, bool) {
	needle := "/" + strings.ToLower(strings.TrimPrefix(strings.TrimSpace(name), "/"))
	for _, command := range slashCommands {
		if command.Trigger == needle {
			return command, true
		}
	}
	return SlashCommand{}, false
}

// SlashHelp renders the command list for /help.
func SlashHelp() []string {
	lines := make([]string, 0, len(slashCommands))
	for _, command := range slashCommands {
		usage := command.Trigger
		if command.Args != "" {
			usage += " " + command.Args
		}
		lines = append(lines, usage+"\t"+command.Summary)
	}
	sort.Strings(lines)
	return lines
}

// SlashOptions returns the argument choices for a command, or nil when the
// command takes free text or nothing.
func SlashOptions(name string) []SlashOption {
	command, ok := FindSlash(name)
	if !ok {
		return nil
	}
	if command.Trigger == "/harness" {
		return harnessOptions()
	}
	return command.Options
}
