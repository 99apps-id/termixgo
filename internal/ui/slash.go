package ui

import (
	"sort"
	"strings"
)

// SlashCommand is one command the composer understands.
type SlashCommand struct {
	Trigger string
	Args    string
	Summary string
}

// slashCommands is the catalogue, in display order.
var slashCommands = []SlashCommand{
	{Trigger: "/model", Args: "[id]", Summary: "Show or switch the model"},
	{Trigger: "/setup", Args: "", Summary: "Onboarding: provider key, skills, Telegram"},
	{Trigger: "/help", Args: "", Summary: "List every command and key binding"},
	{Trigger: "/new", Args: "", Summary: "Start a new session"},
	{Trigger: "/sessions", Args: "", Summary: "List and resume recent sessions"},
	{Trigger: "/stop", Args: "", Summary: "Stop the running turn"},
	{Trigger: "/status", Args: "", Summary: "Show workspace, model and token status"},
	{Trigger: "/trust", Args: "[on|off]", Summary: "Show or change folder trust"},
	{Trigger: "/approval", Args: "[ask|edits|all]", Summary: "Show or change the approval policy"},
	{Trigger: "/harness", Args: "[id]", Summary: "Show or change the agent harness"},
	{Trigger: "/plan", Args: "", Summary: "Show the current task plan"},
	{Trigger: "/tools", Args: "", Summary: "List the tools the agent can call"},
	{Trigger: "/skills", Args: "[reload]", Summary: "List loaded skills"},
	{Trigger: "/memory", Args: "", Summary: "Show what the agent has learned"},
	{Trigger: "/telegram", Args: "[setup|on|off|status|pair]", Summary: "Manage the Telegram companion"},
	{Trigger: "/init", Args: "", Summary: "Generate a TERMIXGO.md for this project"},
	{Trigger: "/cost", Args: "", Summary: "Show token usage for this session"},
	{Trigger: "/ps", Args: "[kill <handle>]", Summary: "List background processes, or stop one"},
	{Trigger: "/exit", Args: "", Summary: "Quit Termixgo"},
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

// MatchSlash returns the commands whose trigger starts with the typed prefix,
// used by the composer's inline menu.
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
