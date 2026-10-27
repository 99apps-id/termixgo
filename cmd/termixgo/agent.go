package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/99apps-id/termixgo/internal/agenthome"
	"github.com/99apps-id/termixgo/internal/config"
)

// runAgent manages the named agents: their persona files, workspace and the
// conversation directory each one keeps.
func runAgent(args []string, stdout io.Writer) error {
	if len(args) == 0 || strings.EqualFold(strings.TrimSpace(args[0]), "list") {
		return listAgents(stdout)
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "add":
		return addAgent(args[1:], stdout)
	case "rm", "remove", "delete":
		return removeAgent(args[1:], stdout)
	case "use", "switch":
		return useAgent(args[1:], stdout)
	case "show":
		return showAgent(args[1:], stdout)
	case "soul":
		return soulAgent(args[1:], stdout)
	case "set":
		return setAgent(args[1:], stdout)
	default:
		return fmt.Errorf("usage: termixgo agent [list|add <name>|rm <name>|use <name>|show <name>|soul <name>|set <name>]")
	}
}

func listAgents(stdout io.Writer) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	names := make([]string, 0, len(cfg.Agents))
	for name := range cfg.Agents {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		entry := cfg.Agent(name)
		marker := " "
		if name == cfg.ActiveAgent {
			marker = "*"
		}
		paths, resolveErr := agenthome.Resolve(name, entry.Workspace)
		workspace := entry.Workspace
		switch {
		case name == config.FinalAgentName && entry.Workspace == "":
			// The default agent works where Termixgo is launched; naming the
			// home's workspace directory here would be a lie.
			workspace = "(launch directory)"
		case resolveErr == nil:
			workspace = paths.Workspace
		}
		model := entry.Model
		if model == "" {
			model = "(default model)"
		}
		fmt.Fprintf(stdout, "%s %-16s %-14s %s\n", marker, name, model, workspace)
	}
	return nil
}

func addAgent(args []string, stdout io.Writer) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	positionals, template, workspace, err := parseAgentFlags(args)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("usage: termixgo agent add <name> [--template %s] [--workspace <path>]", strings.Join(agenthome.Templates, "|"))
	}
	name := strings.TrimSpace(positionals[0])
	if !config.ValidAgentName(name) {
		return fmt.Errorf("invalid agent name %q: use lowercase letters, digits, underscore or dash (max 32)", name)
	}
	if cfg.HasAgent(name) {
		return fmt.Errorf("agent %q already exists; edit it with 'termixgo agent soul %s --edit'", name, name)
	}
	paths, err := agenthome.Ensure(name, workspace, template)
	if err != nil {
		return err
	}
	cfg = cfg.Trust(paths.Workspace)
	cfg = cfg.WithAgent(name, config.Agent{Workspace: workspace})
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "agent %q created\n", name)
	fmt.Fprintf(stdout, "  home:      %s\n", paths.Home)
	fmt.Fprintf(stdout, "  workspace: %s (trusted)\n", paths.Workspace)
	fmt.Fprintf(stdout, "  persona:   %s\n", paths.Soul)
	fmt.Fprintf(stdout, "Edit the persona with: termixgo agent soul %s --edit\n", name)
	return nil
}

func removeAgent(args []string, stdout io.Writer) error {
	purge := false
	var positionals []string
	for _, arg := range args {
		switch arg {
		case "--purge":
			purge = true
		default:
			if strings.HasPrefix(arg, "-") {
				return fmt.Errorf("unknown agent rm option %q", arg)
			}
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) != 1 {
		return fmt.Errorf("usage: termixgo agent rm <name> [--purge]")
	}
	name := strings.TrimSpace(positionals[0])
	if name == config.FinalAgentName {
		return fmt.Errorf("the default agent cannot be removed")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	paths, resolveErr := agenthome.Resolve(name, cfg.Agent(name).Workspace)
	updated, ok := cfg.WithoutAgent(name)
	if !ok {
		return fmt.Errorf("no agent named %q", name)
	}
	if err := config.Save(updated); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "agent %q removed from the configuration\n", name)
	if resolveErr != nil {
		return nil
	}
	if purge {
		if err := os.RemoveAll(paths.Home); err != nil {
			return fmt.Errorf("remove agent home: %w", err)
		}
		fmt.Fprintf(stdout, "  home deleted: %s\n", paths.Home)
		return nil
	}
	fmt.Fprintf(stdout, "  files kept:   %s (delete with --purge)\n", paths.Home)
	return nil
}

func useAgent(args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: termixgo agent use <name>")
	}
	name := strings.TrimSpace(args[0])
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if !cfg.HasAgent(name) {
		return fmt.Errorf("unknown agent %q; create it with 'termixgo agent add %s'", name, name)
	}
	cfg.ActiveAgent = name
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "active agent is now %q\n", name)
	return nil
}

func showAgent(args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: termixgo agent show <name>")
	}
	name := strings.TrimSpace(args[0])
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	entry, ok := cfg.Agents[name]
	if !ok {
		return fmt.Errorf("unknown agent %q", name)
	}
	paths, err := agenthome.Resolve(name, entry.Workspace)
	if err != nil {
		return err
	}
	workspace := paths.Workspace
	if name == config.FinalAgentName && entry.Workspace == "" {
		workspace = "(launch directory)"
	}
	sessions, sessionsErr := agenthome.SessionsDirFor(name)
	active := "no"
	if cfg.ActiveAgent == name {
		active = "yes"
	}
	model := entry.Model
	if model == "" {
		model = "(default model)"
	}
	fmt.Fprintf(stdout, "name:      %s\n", name)
	fmt.Fprintf(stdout, "active:    %s\n", active)
	fmt.Fprintf(stdout, "model:     %s\n", model)
	fmt.Fprintf(stdout, "home:      %s\n", paths.Home)
	fmt.Fprintf(stdout, "workspace: %s\n", workspace)
	fmt.Fprintf(stdout, "persona:   %s\n", paths.Soul)
	if sessionsErr == nil {
		fmt.Fprintf(stdout, "sessions:  %s\n", sessions)
	}
	return nil
}

func soulAgent(args []string, stdout io.Writer) error {
	edit := false
	var positionals []string
	for _, arg := range args {
		switch arg {
		case "--edit":
			edit = true
		default:
			if strings.HasPrefix(arg, "-") {
				return fmt.Errorf("unknown agent soul option %q", arg)
			}
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) != 1 {
		return fmt.Errorf("usage: termixgo agent soul <name> [--edit]")
	}
	name := strings.TrimSpace(positionals[0])
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	paths, err := agenthome.Resolve(name, cfg.Agent(name).Workspace)
	if err != nil {
		return err
	}
	if edit {
		editor := strings.TrimSpace(os.Getenv("EDITOR"))
		if editor == "" {
			return fmt.Errorf("set $EDITOR first, then run: termixgo agent soul %s --edit", name)
		}
		cmd := exec.Command(editor, paths.Soul)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("open editor: %w", err)
		}
		return nil
	}
	fmt.Fprintf(stdout, "%s\n", paths.Soul)
	data, err := os.ReadFile(paths.Soul)
	if err != nil {
		fmt.Fprintf(stdout, "(no persona file yet; run 'termixgo agent add %s' or create it)\n", name)
		return nil
	}
	preview := strings.TrimRight(string(data), "\n")
	lines := strings.Split(preview, "\n")
	if len(lines) > 24 {
		lines = append(lines[:24], "...")
	}
	fmt.Fprintln(stdout, strings.Join(lines, "\n"))
	return nil
}

func setAgent(args []string, stdout io.Writer) error {
	var name, model, workspace, prompt string
	rest := args
	for index := 0; index < len(rest); index++ {
		arg := rest[index]
		value := func(flag string) (string, error) {
			if index+1 >= len(rest) {
				return "", fmt.Errorf("%s needs a value", flag)
			}
			index++
			return rest[index], nil
		}
		var err error
		switch arg {
		case "--model":
			model, err = value(arg)
		case "--workspace":
			workspace, err = value(arg)
		case "--prompt":
			prompt, err = value(arg)
		default:
			if strings.HasPrefix(arg, "-") {
				return fmt.Errorf("unknown agent set option %q", arg)
			}
			if name != "" {
				return fmt.Errorf("usage: termixgo agent set <name> [--model <id>] [--workspace <path>] [--prompt <text|@file>]")
			}
			name = arg
		}
		if err != nil {
			return err
		}
	}
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("usage: termixgo agent set <name> [--model <id>] [--workspace <path>] [--prompt <text|@file>]")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	entry, ok := cfg.Agents[name]
	if !ok {
		return fmt.Errorf("unknown agent %q", name)
	}
	if model != "" {
		entry.Model = strings.TrimSpace(model)
	}
	if workspace != "" {
		cleaned := config.CleanFolder(workspace)
		if err := os.MkdirAll(cleaned, 0o700); err != nil {
			return fmt.Errorf("create workspace: %w", err)
		}
		entry.Workspace = cleaned
		cfg = cfg.Trust(cleaned)
	}
	if prompt != "" {
		content := prompt
		if strings.HasPrefix(prompt, "@") {
			data, err := os.ReadFile(strings.TrimPrefix(prompt, "@"))
			if err != nil {
				return fmt.Errorf("read prompt file: %w", err)
			}
			content = string(data)
		}
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		paths, err := agenthome.Resolve(name, entry.Workspace)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(paths.Soul), 0o700); err != nil {
			return fmt.Errorf("create agent home: %w", err)
		}
		if err := os.WriteFile(paths.Soul, []byte(content), 0o600); err != nil {
			return fmt.Errorf("write persona: %w", err)
		}
	}
	cfg = cfg.WithAgent(name, entry)
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "agent %q updated\n", name)
	return nil
}

// parseAgentFlags reads the shared add/set flags off an argument list.
func parseAgentFlags(args []string) (positionals []string, template, workspace string, err error) {
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--template":
			if index+1 >= len(args) {
				return nil, "", "", fmt.Errorf("--template needs a value")
			}
			index++
			template = args[index]
		case "--workspace":
			if index+1 >= len(args) {
				return nil, "", "", fmt.Errorf("--workspace needs a value")
			}
			index++
			workspace = args[index]
		default:
			if strings.HasPrefix(args[index], "-") {
				return nil, "", "", fmt.Errorf("unknown agent option %q", args[index])
			}
			positionals = append(positionals, args[index])
		}
	}
	return positionals, template, workspace, nil
}
