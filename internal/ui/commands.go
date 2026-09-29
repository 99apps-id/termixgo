package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/config"
)

// mcpReloadTimeout bounds a /mcp reload, so a server that hangs on its
// handshake does not freeze the interface. It is longer than the app's own
// startup budget because a reload also lists every server's tools.
const mcpReloadTimeout = 30 * time.Second

// initPrompt asks the model to write the project memory file.
const initPrompt = "Analyse this repository and write a TERMIXGO.md at the workspace root. " +
	"Include: the project's purpose in one line, the stack and package manager, " +
	"the commands to build, test, lint and typecheck, the main module map, and the conventions " +
	"a new contributor must follow. Read real files first; do not guess. Keep it under 120 lines."

// runSlash dispatches a slash command.
func (m *Model) runSlash(name, args string) (tea.Model, tea.Cmd) {
	switch name {
	case "model":
		return m.slashModel(args)
	case "setup":
		m.startSetup()
		return m, nil
	case "help", "?":
		m.current = modeHelp
		return m, nil
	case "exit", "quit":
		return m, tea.Quit
	case "new":
		m.app.NewSession()
		m.blocks = append(m.blocks, block{kind: blockNotice, text: "Started a new session."})
		m.refresh()
		return m, nil
	case "sessions":
		return m.slashSessions()
	case "stop":
		m.app.Stop()
		m.blocks = append(m.blocks, block{kind: blockNotice, text: "Stopping the running turn."})
		m.refresh()
		return m, nil
	case "status":
		m.blocks = append(m.blocks, block{kind: blockNotice, text: m.app.Status()})
		m.refresh()
		return m, nil
	case "cost":
		usage := m.app.Usage()
		howMuch, known := m.app.Cost()
		spend := "cost unknown for this model"
		if known {
			spend = fmt.Sprintf("about $%.4f", howMuch)
		}
		budget := "no budget set"
		limit := m.appConfig().CostBudgetUSD
		if limit > 0 {
			budget = fmt.Sprintf("budget $%.2f", limit)
			// A cap with no price behind it can never fire, and saying so here
			// is the difference between a measured spend and a false sense of
			// protection.
			if !known {
				budget += ", which cannot be enforced until this model has a price"
			}
		}
		m.blocks = append(m.blocks, block{kind: blockNotice, text: fmt.Sprintf(
			"Tokens: %d in, %d out, %d total.\nEstimated spend: %s (%s).\nPrices are published list values, an estimate not a bill. Set a cap with costBudgetUsd in config, and override a price with modelPricing.",
			usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens, spend, budget)})
		m.refresh()
		return m, nil
	case "ps":
		return m.slashProcesses(args)
	case "trust":
		return m.slashTrust(args)
	case "approval":
		return m.slashApproval(args)
	case "harness":
		return m.slashHarness(args)
	case "plan":
		m.blocks = append(m.blocks, block{kind: blockPlan, plan: m.app.Todos().Items()})
		m.refresh()
		return m, nil
	case "tools":
		return m.slashTools()
	case "mcp":
		return m.slashMCP(args)
	case "skills":
		return m.slashSkills(args)
	case "memory":
		return m.slashMemory()
	case "telegram":
		return m.slashTelegram(args)
	case "init":
		if !m.app.HasModel() {
			m.blocks = append(m.blocks, block{kind: blockError, text: "Pick a model first with /setup."})
			m.refresh()
			return m, nil
		}
		return m.startRun(initPrompt)
	default:
		m.blocks = append(m.blocks, block{kind: blockError, text: fmt.Sprintf("Unknown command /%s. Try /help.", name)})
		m.refresh()
		return m, nil
	}
}

func (m *Model) slashModel(args string) (tea.Model, tea.Cmd) {
	if strings.TrimSpace(args) == "" {
		items := make([]pickerItem, 0, len(m.providerModels()))
		for _, model := range m.providerModels() {
			extra := ""
			if model.Provider == m.app.CurrentModel().Provider {
				extra = "current provider"
			}
			items = append(items, pickerItem{ID: model.ID, Label: model.Label, Detail: model.Description, Extra: extra})
		}
		m.openPicker("Choose a model", "model", items)
		return m, nil
	}
	model, err := m.app.SetModelByQuery(args)
	if err != nil {
		m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
		m.refresh()
		return m, nil
	}
	m.blocks = append(m.blocks, block{kind: blockNotice, text: "Model is now " + model.Label})
	m.refresh()
	return m, nil
}

func (m *Model) slashSessions() (tea.Model, tea.Cmd) {
	sessions, err := agent.ListSessions()
	if err != nil {
		m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
		m.refresh()
		return m, nil
	}
	if len(sessions) == 0 {
		m.blocks = append(m.blocks, block{kind: blockNotice, text: "No saved sessions yet."})
		m.refresh()
		return m, nil
	}
	items := make([]pickerItem, 0, len(sessions))
	for _, session := range sessions {
		title := session.Title
		if strings.TrimSpace(title) == "" {
			title = "(untitled)"
		}
		items = append(items, pickerItem{
			ID:     session.ID,
			Label:  title,
			Detail: fmt.Sprintf("%d turns", session.Turns),
			Extra:  session.UpdatedAt.Format("2006-01-02 15:04"),
		})
	}
	m.openPicker("Resume a session", "session", items)
	return m, nil
}

// slashProcesses lists the background processes, or stops one when asked.
//
// The operator needs this independently of the agent: a dev server the model
// started is still their machine's process, and they should be able to see and
// stop it without asking.
func (m *Model) slashProcesses(args string) (tea.Model, tea.Cmd) {
	manager := m.app.Processes()
	if manager == nil {
		m.blocks = append(m.blocks, block{kind: blockNotice, text: "Background processes are not available."})
		m.refresh()
		return m, nil
	}

	fields := strings.Fields(args)
	if len(fields) > 0 && strings.EqualFold(fields[0], "kill") {
		if len(fields) < 2 {
			m.blocks = append(m.blocks, block{kind: blockError, text: "Usage: /ps kill <handle>"})
			m.refresh()
			return m, nil
		}
		process, err := manager.Kill(fields[1])
		switch {
		case err != nil:
			m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
		case process.Exited():
			m.blocks = append(m.blocks, block{kind: blockNotice, text: fmt.Sprintf("%s had already exited with code %d.", process.ID, process.ExitCode())})
		default:
			m.blocks = append(m.blocks, block{kind: blockNotice, text: fmt.Sprintf("Stopping %s.", process.ID)})
		}
		m.refresh()
		return m, nil
	}

	processes := manager.List()
	if len(processes) == 0 {
		m.blocks = append(m.blocks, block{kind: blockNotice, text: "No background processes."})
		m.refresh()
		return m, nil
	}
	lines := make([]string, 0, len(processes)+2)
	lines = append(lines, fmt.Sprintf("%d background process(es):", len(processes)))
	for _, process := range processes {
		lines = append(lines, "  "+process.Summary())
	}
	lines = append(lines, "", "Stop one with /ps kill <handle>.")
	m.blocks = append(m.blocks, block{kind: blockNotice, text: strings.Join(lines, "\n")})
	m.refresh()
	return m, nil
}

func (m *Model) slashTrust(args string) (tea.Model, tea.Cmd) {
	switch strings.ToLower(strings.TrimSpace(args)) {
	case "":
		state := "untrusted"
		if m.app.Trusted() {
			state = "trusted"
		}
		m.blocks = append(m.blocks, block{kind: blockNotice, text: fmt.Sprintf("%s is %s.", m.app.Workspace(), state)})
	case "on", "yes", "true":
		if err := m.app.SetTrust(true); err != nil {
			m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
		} else {
			m.blocks = append(m.blocks, block{kind: blockNotice, text: "This folder is now trusted. Writes and commands run without asking."})
		}
	case "off", "no", "false":
		if err := m.app.SetTrust(false); err != nil {
			m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
		} else {
			m.blocks = append(m.blocks, block{kind: blockNotice, text: "This folder is now untrusted."})
		}
	default:
		m.blocks = append(m.blocks, block{kind: blockError, text: "Usage: /trust [on|off]"})
	}
	m.refresh()
	return m, nil
}

func (m *Model) slashApproval(args string) (tea.Model, tea.Cmd) {
	trimmed := strings.ToLower(strings.TrimSpace(args))
	if trimmed == "" {
		m.blocks = append(m.blocks, block{kind: blockNotice, text: "Approval mode is " + string(m.appConfig().ApprovalMode) + "."})
		m.refresh()
		return m, nil
	}
	mode, err := config.ParseApprovalMode(trimmed)
	if err != nil {
		m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
		m.refresh()
		return m, nil
	}
	if err := m.app.SetApprovalMode(mode); err != nil {
		m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
		m.refresh()
		return m, nil
	}
	note := "Approval mode is now " + string(mode) + "."
	if mode == config.ApprovalAll {
		note += " Nothing waits for confirmation."
	}
	m.blocks = append(m.blocks, block{kind: blockNotice, text: note})
	m.refresh()
	return m, nil
}

// slashHarness shows or selects the agent harness profile.
//
// The profile is who the agent is for the next turn: how much it plans, how
// hard it verifies and how many steps it may take. Naming the candidates here
// means the operator does not have to read the config file to choose.
func (m *Model) slashHarness(args string) (tea.Model, tea.Cmd) {
	trimmed := strings.TrimSpace(args)
	if trimmed == "" {
		active := m.app.HarnessProfile()
		lines := []string{"Harness: " + active.Label + " (" + active.ID + ")"}
		for _, profile := range agent.HarnessProfiles() {
			marker := "  "
			if profile.ID == active.ID {
				marker = "> "
			}
			lines = append(lines, fmt.Sprintf("%s%-14s %s", marker, profile.ID, profile.Description))
		}
		m.blocks = append(m.blocks, block{kind: blockNotice, text: strings.Join(lines, "\n")})
		m.refresh()
		return m, nil
	}
	profile, err := m.app.SetHarnessProfile(trimmed)
	if err != nil {
		m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
		m.refresh()
		return m, nil
	}
	m.blocks = append(m.blocks, block{kind: blockNotice, text: "Harness is now " + profile.Label + ". It applies from the next turn."})
	m.refresh()
	return m, nil
}

func (m *Model) slashTools() (tea.Model, tea.Cmd) {
	var lines []string
	for _, entry := range m.app.Tools().Catalog() {
		marker := "read"
		if entry.Mutating {
			marker = "write"
		}
		lines = append(lines, fmt.Sprintf("  %-16s %-6s %s", entry.Name, marker, entry.Description))
	}
	m.blocks = append(m.blocks, block{kind: blockNotice, text: "Tools:\n" + strings.Join(lines, "\n")})
	m.refresh()
	return m, nil
}

// slashMCP reports the configured MCP servers and, on reload, reconnects them.
//
// The listing names a server that failed and the command it was given, because
// that is what an operator needs to reproduce the failure by hand. A server
// that is disabled is shown too, so a typo in the config does not look like
// nothing at all.
func (m *Model) slashMCP(args string) (tea.Model, tea.Cmd) {
	trimmed := strings.TrimSpace(args)
	if strings.EqualFold(trimmed, "reload") {
		ctx, cancel := context.WithTimeout(context.Background(), mcpReloadTimeout)
		defer cancel()
		m.app.ReloadMCP(ctx)
	}
	if !strings.EqualFold(trimmed, "reload") && trimmed != "" {
		m.blocks = append(m.blocks, block{kind: blockError, text: "Usage: /mcp [reload]"})
		m.refresh()
		return m, nil
	}

	status := m.app.MCPStatus()
	if len(status) == 0 {
		m.blocks = append(m.blocks, block{kind: blockNotice, text: strings.Join([]string{
			"No MCP servers are configured.",
			"Add one to mcpServers in the settings file, for example:",
			`  {"name": "files", "command": "npx", "args": ["-y", "@modelcontextprotocol/server-filesystem", "."]}`,
			"Then run /mcp reload.",
		}, "\n")})
		m.refresh()
		return m, nil
	}

	var lines []string
	for _, entry := range status {
		switch {
		case entry.Disabled:
			lines = append(lines, fmt.Sprintf("  %-14s off       %s", entry.Name, entry.Command))
		case entry.Err != nil:
			lines = append(lines, fmt.Sprintf("  %-14s failed    %s", entry.Name, entry.Command))
			lines = append(lines, "                 "+firstLine(entry.Err.Error()))
			if entry.Stderr != "" {
				lines = append(lines, "                 stderr: "+firstLine(entry.Stderr))
			}
		default:
			lines = append(lines, fmt.Sprintf("  %-14s %-8s %d tool(s)  (%s)",
				entry.Name, "ready", entry.ToolCount, entry.Command))
		}
	}
	header := fmt.Sprintf("MCP servers (%d):", len(status))
	m.blocks = append(m.blocks, block{kind: blockNotice, text: header + "\n" + strings.Join(lines, "\n")})
	m.refresh()
	return m, nil
}

// firstLine clips an error to one readable transcript line.
func firstLine(text string) string {
	collapsed := strings.Join(strings.Fields(text), " ")
	if len(collapsed) <= 160 {
		return collapsed
	}
	return collapsed[:160] + "..."
}

func (m *Model) slashSkills(args string) (tea.Model, tea.Cmd) {
	if strings.EqualFold(strings.TrimSpace(args), "reload") {
		m.app.ReloadSkills()
	}
	skills := m.app.Skills()
	if len(skills) == 0 {
		m.blocks = append(m.blocks, block{kind: blockNotice, text: "No skills found. Add one at .termixgo/skills/<name>/SKILL.md."})
		m.refresh()
		return m, nil
	}
	var lines []string
	for _, item := range skills {
		lines = append(lines, fmt.Sprintf("  %-20s %-8s %s", item.Name, item.Scope, item.Description))
	}
	m.blocks = append(m.blocks, block{kind: blockNotice, text: fmt.Sprintf("Skills (%d):\n%s", len(skills), strings.Join(lines, "\n"))})
	m.refresh()
	return m, nil
}

func (m *Model) slashMemory() (tea.Model, tea.Cmd) {
	project, global := agent.NewMemory(m.app.Workspace()).Read()
	if len(project) == 0 && len(global) == 0 {
		m.blocks = append(m.blocks, block{kind: blockNotice, text: "Nothing learned yet. The agent writes facts with the remember tool."})
		m.refresh()
		return m, nil
	}
	var lines []string
	for _, fact := range global {
		lines = append(lines, "  [global] "+fact)
	}
	for _, fact := range project {
		lines = append(lines, "  [project] "+fact)
	}
	m.blocks = append(m.blocks, block{kind: blockNotice, text: "Learned memory:\n" + strings.Join(lines, "\n")})
	m.refresh()
	return m, nil
}

func (m *Model) slashTelegram(args string) (tea.Model, tea.Cmd) {
	sub := strings.ToLower(strings.TrimSpace(args))
	switch sub {
	case "", "status":
		m.blocks = append(m.blocks, block{kind: blockNotice, text: "Telegram: " + m.app.TelegramStatus()})
	case "on":
		if err := m.app.SetTelegramEnabled(true); err != nil {
			m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
		} else {
			m.blocks = append(m.blocks, block{kind: blockNotice, text: "Telegram bot is running."})
		}
	case "off":
		if err := m.app.SetTelegramEnabled(false); err != nil {
			m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
		} else {
			m.blocks = append(m.blocks, block{kind: blockNotice, text: "Telegram bot stopped."})
		}
	case "setup":
		m.setup = setupState{step: setupTelegramToken, message: "Connect the Telegram companion bot."}
		m.current = modeSetup
		m.input.SetValue("")
		m.input.Placeholder = "123456:ABC-DEF..."
		m.input.EchoMode = textinput.EchoPassword
		m.input.Focus()
		m.refresh()
		return m, textareaBlink()
	case "pair":
		code, err := m.app.EnsurePairingCode()
		if err != nil {
			m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
			break
		}
		_ = m.app.StartTelegram()
		m.blocks = append(m.blocks, block{kind: blockNotice, text: fmt.Sprintf("Pairing code: %s. Send /pair %s in Telegram.", code, code)})
	default:
		m.blocks = append(m.blocks, block{kind: blockError, text: "Usage: /telegram [status|on|off|setup|pair]"})
	}
	m.refresh()
	return m, nil
}
