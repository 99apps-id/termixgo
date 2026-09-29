// Package command discovers user-defined slash commands.
//
// A command is one Markdown file: optional YAML frontmatter (description)
// followed by the prompt body. Project commands live in
// <workspace>/.termixgo/commands/<name>.md, user commands in
// <termixgo-home>/commands/<name>.md. The file name is the command name, so
// review.md becomes /review. A project command shadows a user command of the
// same name.
//
// The body may contain $ARGUMENTS where the typed arguments go; without the
// placeholder the arguments are appended at the end.
package command

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/99apps-id/termixgo/internal/config"
)

// Command is one discovered slash command.
type Command struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body,omitempty"`
	Path        string `json:"path"`
	Scope       string `json:"scope"`
}

// Frontmatter is the optional YAML header of a command file.
type Frontmatter struct {
	Description string `yaml:"description"`
}

const delimiter = "---"

// ProjectDir is the commands root inside a workspace.
func ProjectDir(workspace string) string {
	return filepath.Join(workspace, ".termixgo", "commands")
}

// UserDir is the commands root inside the state directory.
func UserDir() string {
	home, err := config.Home()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "commands")
}

// Discover returns project and user commands, sorted by name. Missing
// folders are not an error; a project command shadows a user command of the
// same name.
func Discover(workspace string) ([]Command, error) {
	seen := map[string]int{}
	commands := make([]Command, 0, 8)
	for _, source := range []struct{ root, scope string }{
		{ProjectDir(workspace), "project"},
		{UserDir(), "user"},
	} {
		if strings.TrimSpace(source.root) == "" {
			continue
		}
		found, err := fromDir(source.root, source.scope)
		if err != nil {
			return nil, err
		}
		for _, item := range found {
			key := strings.ToLower(item.Name)
			if index, ok := seen[key]; ok {
				if commands[index].Scope == "project" {
					continue
				}
				commands[index] = item
				continue
			}
			seen[key] = len(commands)
			commands = append(commands, item)
		}
	}
	sort.SliceStable(commands, func(i, j int) bool {
		return strings.ToLower(commands[i].Name) < strings.ToLower(commands[j].Name)
	})
	return commands, nil
}

// Load returns one command by name, case-insensitively.
func Load(workspace, name string) (Command, error) {
	commands, err := Discover(workspace)
	if err != nil {
		return Command{}, err
	}
	for _, candidate := range commands {
		if strings.EqualFold(candidate.Name, name) {
			return candidate, nil
		}
	}
	return Command{}, fmt.Errorf("command %q not found", name)
}

// Expand renders the prompt for a run: $ARGUMENTS is replaced with the typed
// arguments, or the arguments are appended when the body has no placeholder.
func (c Command) Expand(args string) string {
	body := strings.TrimSpace(c.Body)
	args = strings.TrimSpace(args)
	if args == "" {
		return strings.ReplaceAll(body, "$ARGUMENTS", "")
	}
	if strings.Contains(body, "$ARGUMENTS") {
		return strings.ReplaceAll(body, "$ARGUMENTS", args)
	}
	if body == "" {
		return args
	}
	return body + "\n\n" + args
}

func fromDir(root, scope string) ([]Command, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var commands []Command
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".md") {
			continue
		}
		command, err := fromFile(filepath.Join(root, name), scope)
		if err != nil {
			continue
		}
		commands = append(commands, command)
	}
	return commands, nil
}

func fromFile(path, scope string) (Command, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Command{}, err
	}
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	name = NormalizeName(name)
	if name == "" {
		return Command{}, fmt.Errorf("invalid command file name %q", path)
	}
	description, body := parse(string(data))
	if strings.TrimSpace(description) == "" {
		description = "Custom command " + name
	}
	return Command{Name: name, Description: description, Body: body, Path: path, Scope: scope}, nil
}

// NormalizeName keeps lowercase letters, digits and dashes: the characters a
// slash trigger can carry.
func NormalizeName(name string) string {
	var builder strings.Builder
	for _, char := range strings.ToLower(strings.TrimSpace(name)) {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' || char == '_' {
			builder.WriteRune(char)
		}
	}
	return builder.String()
}

func parse(text string) (description, body string) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, delimiter) {
		return "", trimmed
	}
	rest := strings.TrimSpace(trimmed[len(delimiter):])
	end := strings.Index(rest, delimiter)
	if end < 0 {
		return "", trimmed
	}
	var front Frontmatter
	if err := yaml.Unmarshal([]byte(rest[:end]), &front); err != nil {
		return "", strings.TrimSpace(rest[end+len(delimiter):])
	}
	return strings.TrimSpace(front.Description), strings.TrimSpace(rest[end+len(delimiter):])
}
