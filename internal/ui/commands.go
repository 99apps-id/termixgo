package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/app"
	customcmd "github.com/99apps-id/termixgo/internal/command"
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

// slashArgAction prefixes a picker action that carries the command name, so a
// chosen argument runs the command the menu was opened for.
const slashArgAction = "slash-arg:"

// openSlashArgs shows the argument choices for a command. It is what makes the
// menu usable for a command whose argument is a closed set: choosing /trust
// from the menu offers on and off instead of only printing the state.
func (m *Model) openSlashArgs(name string) (tea.Model, tea.Cmd) {
	options := SlashOptions(name)
	if len(options) == 0 {
		return m, nil
	}
	if m.slashPending == "" {
		m.slashPending = slashTrigger(name)
		m.pickerResume = ""
	}
	m.closeSlashEntry()
	m.composer.SetValue(m.pickerResume)
	m.slashMatches = nil
	m.mentionMatches = nil
	items := make([]pickerItem, 0, len(options)+1)
	// The bare form stays available as a row when the command has one, so
	// "show me the current value" is still one keystroke away. The label
	// states what the row does rather than naming a specific command's state,
	// because one menu serves every command.
	if command, ok := FindSlash(name); ok && command.Args != "" && strings.HasPrefix(command.Args, "[") {
		items = append(items, pickerItem{ID: "", Label: "(no argument)", Detail: command.Summary})
	}
	for _, option := range options {
		detail := option.Detail
		if option.Complete {
			detail += " (then type the rest)"
		}
		items = append(items, pickerItem{ID: option.Value, Label: option.Value, Detail: detail})
	}
	m.openPicker("Arguments for "+slashTrigger(name), slashArgAction+name, items)
	return m, nil
}

// slashTrigger renders the canonical "/name" for a command name.
func slashTrigger(name string) string {
	if command, ok := FindSlash(name); ok {
		return command.Trigger
	}
	return "/" + strings.TrimPrefix(name, "/")
}

// applySlashArg runs one argument chosen from the menu. A choice that only
// starts an argument, such as a session id, completes the composer instead of
// running the command, because the rest still has to be typed.
func (m *Model) applySlashArg(name, value string) (tea.Model, tea.Cmd) {
	for _, option := range SlashOptions(name) {
		if option.Value != value || !option.Complete {
			continue
		}
		m.composer.SetValue(slashTrigger(name) + " " + value + " ")
		m.composer.CursorEnd()
		m.slashPending = ""
		m.pickerResume = ""
		m.closeSlashEntry()
		m.updateSlashMatches()
		m.refresh()
		return m, textareaBlink()
	}
	m.slashPending = ""
	m.pickerResume = ""
	m.closeSlashEntry()
	m.composer.SetValue("")
	m.updateSlashMatches()
	return m.runSlash(strings.TrimPrefix(name, "/"), value)
}

// runSlash dispatches a slash command.
func (m *Model) runSlash(name, args string) (tea.Model, tea.Cmd) {
	switch name {
	case "model":
		return m.slashModel(args)
	case "copy":
		return m.slashCopy(args)
	case "export":
		return m.slashExport(args)
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
		return m.slashSessions(args)
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
		unpriced := m.app.CostUnpriced()
		plan, planBilled := m.app.CurrentModel().Plan()
		spend := "cost unknown for this model"
		switch {
		case planBilled:
			spend = fmt.Sprintf("covered by the %s, billed in %s rather than dollars", plan.Name, plan.CreditUnit)
		case known:
			spend = fmt.Sprintf("about $%.4f", howMuch)
			if unpriced > 0 {
				// The figure is a floor, not the whole spend: some delegated
				// runs cost tokens nobody priced. Name that rather than
				// silently under-reporting.
				spend += fmt.Sprintf(" (a floor: %d delegated run(s) cost tokens with no priced model)", unpriced)
			}
		}
		budget := "no budget set"
		limit := m.appConfig().CostBudgetUSD
		switch {
		case planBilled:
			// A dollar cap cannot see credits, so claiming one applies would be
			// a false sense of protection.
			budget = "a dollar budget does not apply to a plan subscription"
		case limit > 0:
			budget = fmt.Sprintf("budget $%.2f", limit)
			// A cap with no price behind it can never fire, and saying so here
			// is the difference between a measured spend and a false sense of
			// protection.
			if !known {
				budget += ", which cannot be enforced until this model has a price"
			}
		}
		tail := "Prices are published list values, an estimate not a bill. Set a cap with costBudgetUsd in config, and override a price with modelPricing."
		if planBilled {
			tail = fmt.Sprintf("The %s is a prepaid quota: the plan's own console reports the %s used. A costBudgetUsd cap applies only to pay-as-you-go models.", plan.Name, plan.CreditUnit)
		}
		m.blocks = append(m.blocks, block{kind: blockNotice, text: fmt.Sprintf(
			"Tokens: %d in, %d out, %d total.\nEstimated spend: %s (%s).\n%s%s",
			usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens, spend, budget, tail, formatToolStats(m.app.ToolStats()))})
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
	case "checkpoint":
		return m.slashCheckpoint(args)
	case "rewind":
		return m.slashRewind(args)
	case "diff":
		return m.slashDiff(args)
	case "worktree":
		return m.slashWorktree(args)
	case "telegram":
		return m.slashTelegram(args)
	case "cron":
		return m.slashCron(args)
	case "heartbeat":
		return m.slashHeartbeat(args)
	case "audit":
		return m.slashAudit(args)
	case "worker":
		return m.slashWorker(args)
	case "workers":
		return m.slashWorkers()
	case "batch":
		return m.slashBatch(args)
	case "init":
		if !m.app.HasModel() {
			m.blocks = append(m.blocks, block{kind: blockError, text: "Pick a model first with /setup."})
			m.refresh()
			return m, nil
		}
		return m.startRun(initPrompt)
	default:
		return m.runCustom(name, args)
	}
}

// runCustom runs a user-defined slash command from .termixgo/commands. The
// file body becomes the turn prompt with the typed arguments applied, so
// /review <paths> reads like a built-in that the operator wrote themselves.
func (m *Model) runCustom(name, args string) (tea.Model, tea.Cmd) {
	item, err := customcmd.Load(m.app.Workspace(), name)
	if err != nil {
		m.blocks = append(m.blocks, block{kind: blockError, text: fmt.Sprintf("Unknown command /%s. Try /help.", name)})
		m.refresh()
		return m, nil
	}
	if !m.app.HasModel() {
		m.blocks = append(m.blocks, block{kind: blockError, text: "Pick a model first with /setup."})
		m.refresh()
		return m, nil
	}
	return m.startRun(item.Expand(args))
}

// slashCopy copies conversation text to the terminal clipboard with OSC 52, so
// it works over SSH and needs no platform clipboard tool. The default scope is
// the newest answer; "all" copies the whole transcript.
func (m *Model) slashCopy(args string) (tea.Model, tea.Cmd) {
	var text string
	switch strings.ToLower(strings.TrimSpace(args)) {
	case "all", "transcript":
		text = m.transcriptText()
	default:
		text = m.lastAssistantText()
	}
	if strings.TrimSpace(text) == "" {
		m.blocks = append(m.blocks, block{kind: blockError, text: "There is nothing to copy yet."})
		m.refresh()
		return m, nil
	}
	m.blocks = append(m.blocks, block{kind: blockNotice, text: "Copied to the clipboard."})
	m.refresh()
	return m, writeClipboard(text)
}

// slashExport exports the live session to a file and clipboard.
func (m *Model) slashExport(args string) (tea.Model, tea.Cmd) {
	session := m.app.Session()
	if session == nil {
		m.blocks = append(m.blocks, block{kind: blockError, text: "No active session to export."})
		m.refresh()
		return m, nil
	}

	fields := strings.Fields(args)
	format := agent.SessionExportMarkdown
	var targetPath string

	for _, field := range fields {
		switch strings.ToLower(field) {
		case "--jsonl", "-j":
			format = agent.SessionExportJSONL
		case "--markdown", "-m":
			format = agent.SessionExportMarkdown
		default:
			if !strings.HasPrefix(field, "-") && targetPath == "" {
				targetPath = field
			}
		}
	}

	document, err := session.Export(format)
	if err != nil {
		m.blocks = append(m.blocks, block{kind: blockError, text: "Export failed: " + err.Error()})
		m.refresh()
		return m, nil
	}

	ext := "md"
	if format == agent.SessionExportJSONL {
		ext = "jsonl"
	}

	if targetPath == "" {
		exportDir := filepath.Join(m.app.Workspace(), ".termixgo", "exports")
		_ = os.MkdirAll(exportDir, 0o755)
		targetPath = filepath.Join(exportDir, fmt.Sprintf("session-%s.%s", shortID(session.ID()), ext))
	} else if !filepath.IsAbs(targetPath) {
		targetPath = filepath.Join(m.app.Workspace(), targetPath)
	}
	// The transcript holds tool output, so it stays inside the workspace the
	// same way a write_file does. A path the operator typed is not trusted
	// more than one the model chose.
	if err := agent.CheckWorkspacePath(m.app.Workspace(), targetPath); err != nil {
		m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
		m.refresh()
		return m, nil
	}

	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		m.blocks = append(m.blocks, block{kind: blockError, text: fmt.Sprintf("Create dir failed: %v", err)})
		m.refresh()
		return m, nil
	}

	// The transcript can carry tool output, so it is written owner-only, like
	// the session file it came from.
	if err := os.WriteFile(targetPath, []byte(document), 0o600); err != nil {
		m.blocks = append(m.blocks, block{kind: blockError, text: fmt.Sprintf("Write export failed: %v", err)})
		m.refresh()
		return m, nil
	}

	relPath, err := filepath.Rel(m.app.Workspace(), targetPath)
	display := targetPath
	if err == nil && !strings.HasPrefix(relPath, "..") {
		display = filepath.ToSlash(relPath)
	}

	m.blocks = append(m.blocks, block{
		kind: blockNotice,
		text: fmt.Sprintf("Exported session %s to %s (%d bytes) and copied to clipboard.", shortID(session.ID()), display, len(document)),
	})
	m.refresh()
	return m, writeClipboard(document)
}

// lastAssistantText returns the newest non-empty assistant answer.
func (m *Model) lastAssistantText() string {
	for index := len(m.blocks) - 1; index >= 0; index-- {
		if m.blocks[index].kind != blockAssistant {
			continue
		}
		if text := strings.TrimSpace(m.blocks[index].text); text != "" {
			return text
		}
	}
	return ""
}

// transcriptText renders the visible conversation as plain text: the operator
// turns, the answers, and what each tool did.
func (m *Model) transcriptText() string {
	var builder strings.Builder
	write := func(label, text string) {
		if strings.TrimSpace(text) == "" {
			return
		}
		if builder.Len() > 0 {
			builder.WriteString("\n\n")
		}
		if label != "" {
			builder.WriteString(label)
			builder.WriteString("\n")
		}
		builder.WriteString(text)
	}
	for _, entry := range m.blocks {
		switch entry.kind {
		case blockUser:
			write("You:", entry.text)
		case blockAssistant:
			write("Assistant:", entry.text)
		case blockTool:
			detail := entry.toolResult
			if strings.TrimSpace(detail) == "" {
				detail = entry.toolLabel
			}
			write("Tool "+entry.toolName+":", detail)
		case blockNotice:
			write("", entry.text)
		case blockError:
			write("Error:", entry.text)
		}
	}
	return builder.String()
}

// activeProviderItems lists the providers the operator can use right now: the
// current provider, local providers that need no key, and providers with a
// stored key or login. /model drills from here into one provider's models, so
// the menu stays short instead of pouring the whole catalogue into one list.
func (m *Model) activeProviderItems() []pickerItem {
	current := m.app.CurrentModel().Provider
	providers := m.app.ActiveProviders()
	items := make([]pickerItem, 0, len(providers))
	for _, info := range providers {
		extra := "local"
		switch {
		case info.ID == current:
			extra = "current"
		case info.OAuth:
			extra = "login"
		case info.NeedsKey:
			extra = "API key"
		}
		items = append(items, pickerItem{ID: info.ID, Label: info.Label, Detail: info.DefaultBaseURL, Extra: extra})
	}
	return items
}

func (m *Model) slashModel(args string) (tea.Model, tea.Cmd) {
	trimmed := strings.TrimSpace(args)
	if trimmed == "" {
		m.openPicker(brandedTitle("Choose a provider"), "model-provider", m.activeProviderItems())
		return m, nil
	}
	fields := strings.Fields(trimmed)
	if len(fields) > 0 {
		switch strings.ToLower(fields[0]) {
		case "voice":
			if len(fields) == 1 {
				current := m.app.VoiceModel()
				if current == "" {
					current = "none (using default Whisper)"
				}
				m.blocks = append(m.blocks, block{kind: blockNotice, text: "Voice model is " + current})
				m.refresh()
				return m, nil
			}
			val := strings.Join(fields[1:], " ")
			if strings.EqualFold(val, "off") || strings.EqualFold(val, "none") || strings.EqualFold(val, "clear") {
				_ = m.app.SetVoiceModel("")
				m.blocks = append(m.blocks, block{kind: blockNotice, text: "Cleared voice model (using default Whisper)."})
			} else {
				_ = m.app.SetVoiceModel(val)
				m.blocks = append(m.blocks, block{kind: blockNotice, text: "Voice model is now " + val})
			}
			m.refresh()
			return m, nil

		case "image":
			if len(fields) == 1 {
				current := m.app.ImageModel()
				if current == "" {
					current = "none (using active model)"
				}
				m.blocks = append(m.blocks, block{kind: blockNotice, text: "Image model is " + current})
				m.refresh()
				return m, nil
			}
			val := strings.Join(fields[1:], " ")
			if strings.EqualFold(val, "off") || strings.EqualFold(val, "none") || strings.EqualFold(val, "clear") {
				_ = m.app.SetImageModel("")
				m.blocks = append(m.blocks, block{kind: blockNotice, text: "Cleared image model (using active model for subagents)."})
			} else {
				_ = m.app.SetImageModel(val)
				m.blocks = append(m.blocks, block{kind: blockNotice, text: "Image model is now " + val})
			}
			m.refresh()
			return m, nil
		}
	}

	model, err := m.app.SetModelByQuery(args)
	if err != nil {
		m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
		m.refresh()
		return m, nil
	}
	m.refreshWelcome()
	m.blocks = append(m.blocks, block{kind: blockNotice, text: "Model is now " + model.Label})
	m.refresh()
	return m, nil
}

func (m *Model) slashSessions(args string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(args)
	if len(fields) > 0 {
		switch strings.ToLower(fields[0]) {
		case "search":
			return m.slashSessionsSearch(strings.TrimSpace(strings.TrimPrefix(args, fields[0])))
		case "rename":
			return m.slashSessionsRename(fields[1:])
		case "delete", "rm":
			return m.slashSessionsDelete(fields[1:])
		case "export":
			return m.slashSessionsExport(fields[1:])
		case "list":
			return m.slashSessionsList()
		}
	}
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

// slashSessionsList prints the saved sessions as text, for operators who
// want to copy an id without opening the picker.
func (m *Model) slashSessionsList() (tea.Model, tea.Cmd) {
	sessions, err := agent.ListSessions()
	if err != nil {
		m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
		m.refresh()
		return m, nil
	}
	m.blocks = append(m.blocks, block{kind: blockNotice, text: formatSessionList(sessions)})
	m.refresh()
	return m, nil
}

func (m *Model) slashSessionsSearch(query string) (tea.Model, tea.Cmd) {
	sessions, err := agent.SearchSessions(query)
	if err != nil {
		m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
		m.refresh()
		return m, nil
	}
	if len(sessions) == 0 {
		m.blocks = append(m.blocks, block{kind: blockNotice, text: fmt.Sprintf("No sessions match %q.", strings.TrimSpace(query))})
		m.refresh()
		return m, nil
	}
	m.blocks = append(m.blocks, block{kind: blockNotice, text: formatSessionList(sessions)})
	m.refresh()
	return m, nil
}

func (m *Model) slashSessionsRename(fields []string) (tea.Model, tea.Cmd) {
	if len(fields) < 2 {
		m.blocks = append(m.blocks, block{kind: blockError, text: "Usage: /sessions rename <id> <title>"})
		m.refresh()
		return m, nil
	}
	summary, err := agent.RenameSession(fields[0], strings.Join(fields[1:], " "))
	if err != nil {
		m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
		m.refresh()
		return m, nil
	}
	m.blocks = append(m.blocks, block{kind: blockNotice, text: fmt.Sprintf("Renamed session %s to %q.", shortID(summary.ID), summary.Title)})
	m.refresh()
	return m, nil
}

func (m *Model) slashSessionsDelete(fields []string) (tea.Model, tea.Cmd) {
	if len(fields) < 1 {
		m.blocks = append(m.blocks, block{kind: blockError, text: "Usage: /sessions delete <id>"})
		m.refresh()
		return m, nil
	}
	id := fields[0]
	if err := agent.DeleteSession(id); err != nil {
		m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
		m.refresh()
		return m, nil
	}
	// Deleting the live session would leave the next turn saving into a file
	// the operator just removed, so start fresh instead.
	if strings.EqualFold(m.app.Session().ID(), id) {
		m.app.NewSession()
		m.blocks = append(m.blocks, block{kind: blockNotice, text: "Deleted the current session and started a new one."})
	} else {
		m.blocks = append(m.blocks, block{kind: blockNotice, text: fmt.Sprintf("Deleted session %s.", shortID(id))})
	}
	m.refresh()
	return m, nil
}

func (m *Model) slashSessionsExport(fields []string) (tea.Model, tea.Cmd) {
	if len(fields) < 1 {
		m.blocks = append(m.blocks, block{kind: blockError, text: "Usage: /sessions export <id> [--format markdown|jsonl]"})
		m.refresh()
		return m, nil
	}
	var format agent.SessionExportFormat
	for index, field := range fields[1:] {
		if strings.EqualFold(field, "--format") && index+2 < len(fields) {
			format = agent.SessionExportFormat(strings.ToLower(fields[index+2]))
			break
		}
	}
	document, err := agent.ExportSession(fields[0], format)
	if err != nil {
		m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
		m.refresh()
		return m, nil
	}
	m.blocks = append(m.blocks, block{kind: blockNotice, text: document})
	m.refresh()
	return m, nil
}

func formatSessionList(sessions []agent.SessionSummary) string {
	if len(sessions) == 0 {
		return "No saved sessions yet."
	}
	lines := []string{fmt.Sprintf("Sessions (%d):", len(sessions))}
	for _, session := range sessions {
		title := strings.TrimSpace(session.Title)
		if title == "" {
			title = "(untitled)"
		}
		lines = append(lines, fmt.Sprintf("  %s  %s  %d turn(s)  %s", session.ID, title, session.Turns, session.UpdatedAt.Format("2006-01-02 15:04")))
	}
	return strings.Join(lines, "\n")
}

// slashProcesses lists the background processes, or stops one when asked.
//
// The operator needs this independently of the agent: a dev server the model
// started is still their machine's process, and they should be able to see and
// stop it without asking.
// formatToolStats renders the per-tool call ledger for /cost. An empty
// ledger adds nothing: a session with no tool calls yet shows just tokens.
func formatToolStats(stats []app.ToolStat) string {
	if len(stats) == 0 {
		return ""
	}
	var lines []string
	for _, stat := range stats {
		if stat.Errors > 0 {
			lines = append(lines, fmt.Sprintf("  %-16s %d call(s), %d failed", stat.Name, stat.Calls, stat.Errors))
			continue
		}
		lines = append(lines, fmt.Sprintf("  %-16s %d call(s)", stat.Name, stat.Calls))
	}
	return "\nTools:\n" + strings.Join(lines, "\n")
}

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
		// Name the next step: picking /trust from the menu and pressing
		// Enter lands here, and without the tip the operator loops on the
		// status line wondering why nothing changed.
		m.blocks = append(m.blocks, block{kind: blockNotice, text: fmt.Sprintf("%s is %s.\nChange it with /trust on or /trust off.", m.app.Workspace(), state)})
	case "on", "yes", "true":
		if err := m.app.SetTrust(true); err != nil {
			m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
		} else {
			m.refreshWelcome()
			m.blocks = append(m.blocks, block{kind: blockNotice, text: "This folder is now trusted. Writes and commands run without asking."})
		}
	case "off", "no", "false":
		if err := m.app.SetTrust(false); err != nil {
			m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
		} else {
			m.refreshWelcome()
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
	if mode == config.ApprovalPlan {
		note += " Mutating tools are blocked: the agent can only read and plan."
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
	// truncate is ANSI-aware and rune-aware, so a multi-byte character is
	// never cut in half and the terminal is never fed invalid UTF-8.
	return truncate(collapsed, 163)
}

func (m *Model) slashSkills(args string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(strings.TrimSpace(args))
	sub := ""
	if len(fields) > 0 {
		sub = strings.ToLower(fields[0])
	}
	switch sub {
	case "proposals":
		proposals, err := m.app.SkillProposals()
		if err != nil {
			m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
			break
		}
		if len(proposals) == 0 {
			m.blocks = append(m.blocks, block{kind: blockNotice, text: "No pending skill proposals."})
			break
		}
		lines := []string{fmt.Sprintf("%d skill proposal(s):", len(proposals))}
		for _, proposal := range proposals {
			line := fmt.Sprintf("  %s  %-6s %-20s %s", proposal.ID, proposal.Action, proposal.Name, proposal.Summary)
			if proposal.Status != "" && proposal.Status != "pending" {
				line += "  [" + proposal.Status + "]"
			}
			lines = append(lines, line)
		}
		lines = append(lines, "", "Apply with /skills apply <id>, drop with /skills reject <id>.")
		m.blocks = append(m.blocks, block{kind: blockNotice, text: strings.Join(lines, "\n")})
	case "apply":
		if len(fields) < 2 {
			m.blocks = append(m.blocks, block{kind: blockError, text: "Usage: /skills apply <id>"})
			break
		}
		applied, err := m.app.ApplySkillProposal(fields[1])
		if err != nil {
			m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
			break
		}
		m.blocks = append(m.blocks, block{kind: blockNotice, text: "Applied skill " + applied.Name + "."})
	case "reject":
		if len(fields) < 2 {
			m.blocks = append(m.blocks, block{kind: blockError, text: "Usage: /skills reject <id>"})
			break
		}
		if err := m.app.RejectSkillProposal(fields[1]); err != nil {
			m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
			break
		}
		m.blocks = append(m.blocks, block{kind: blockNotice, text: "Rejected proposal " + fields[1] + "."})
	case "", "list", "reload":
		if sub == "reload" {
			m.app.ReloadSkills()
		}
		skills := m.app.Skills()
		if len(skills) == 0 {
			m.blocks = append(m.blocks, block{kind: blockNotice, text: "No skills found. Add one at .termixgo/skills/<name>/SKILL.md."})
			break
		}
		var lines []string
		for _, item := range skills {
			lines = append(lines, fmt.Sprintf("  %-20s %-8s %s", item.Name, item.Scope, item.Description))
		}
		m.blocks = append(m.blocks, block{kind: blockNotice, text: fmt.Sprintf("Skills (%d):\n%s", len(skills), strings.Join(lines, "\n"))})
	default:
		m.blocks = append(m.blocks, block{kind: blockError, text: "Usage: /skills [list|reload|proposals|apply|reject]"})
	}
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

func (m *Model) slashCheckpoint(args string) (tea.Model, tea.Cmd) {
	trimmed := strings.TrimSpace(args)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if strings.EqualFold(trimmed, "list") {
		checkpoints, err := agent.ListCheckpoints(ctx, m.app.Workspace())
		if err != nil {
			m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
		} else if len(checkpoints) == 0 {
			m.blocks = append(m.blocks, block{kind: blockNotice, text: "No checkpoints yet. Run /checkpoint to save one."})
		} else {
			lines := []string{fmt.Sprintf("Checkpoints (%d):", len(checkpoints))}
			for _, item := range checkpoints {
				label := item.Ref
				if strings.TrimSpace(item.Message) != "" {
					label += "  " + item.Message
				}
				lines = append(lines, "  "+label)
			}
			m.blocks = append(m.blocks, block{kind: blockNotice, text: strings.Join(lines, "\n")})
		}
		m.refresh()
		return m, nil
	}
	checkpoint, err := agent.CreateCheckpoint(ctx, m.app.Workspace(), trimmed)
	if err != nil {
		m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
	} else if checkpoint.Ref == "" {
		m.blocks = append(m.blocks, block{kind: blockNotice, text: "Nothing to save: the working tree is clean."})
	} else {
		m.blocks = append(m.blocks, block{kind: blockNotice, text: fmt.Sprintf("Checkpoint %s saved. Restore it with /rewind.", checkpoint.Ref)})
	}
	m.refresh()
	return m, nil
}

// slashDiff renders the working tree diff as a visual side-by-side view.
// /diff [unified|side] [path...] defaults to side-by-side. Narrow terminals
// fall back to unified automatically so columns never wrap and shift the frame.
func (m *Model) slashDiff(args string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(args)
	layout := "side"
	var paths []string
	for _, field := range fields {
		switch strings.ToLower(field) {
		case "side", "side-by-side", "split":
			layout = "side"
		case "unified", "single":
			layout = "unified"
		default:
			paths = append(paths, field)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	tool, ok := m.app.Tools().Lookup("git_diff")
	if !ok {
		m.blocks = append(m.blocks, block{kind: blockError, text: "The diff tool is not available."})
		m.refresh()
		return m, nil
	}
	callArgs := map[string]any{}
	if len(paths) > 0 {
		// The tool reads a list here. A space-joined string would be taken for one
		// path containing a space, match nothing, and report a clean tree.
		callArgs["paths"] = paths
	}
	result, err := tool.Run(ctx, &agent.Env{Workspace: m.app.Workspace()}, callArgs)
	if err != nil {
		m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
		m.refresh()
		return m, nil
	}
	if result.IsError {
		m.blocks = append(m.blocks, block{kind: blockError, text: result.Output})
		m.refresh()
		return m, nil
	}
	raw := strings.TrimSpace(result.Output)
	if diffIsClean(raw) {
		m.blocks = append(m.blocks, block{kind: blockNotice, text: "Working tree is clean."})
		m.refresh()
		return m, nil
	}
	files := sanitizeDiffFiles(parseUnifiedDiff(raw))
	budget := max(10, m.modalRowBudget()*3)
	m.blocks = append(m.blocks, block{kind: blockDiff, diffFiles: files, diffLayout: layout, diffBudget: budget})
	m.refresh()
	return m, nil
}

func (m *Model) slashRewind(args string) (tea.Model, tea.Cmd) {
	trimmed := strings.TrimSpace(args)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	var checkpoint agent.Checkpoint
	var err error
	if trimmed == "" {
		checkpoint, err = agent.RewindToLatest(ctx, m.app.Workspace())
	} else {
		checkpoint, err = agent.RewindToCheckpoint(ctx, m.app.Workspace(), trimmed)
	}
	if err != nil {
		m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
	} else {
		message := strings.TrimSpace(checkpoint.Message)
		if message == "" {
			message = checkpoint.Ref
		}
		m.blocks = append(m.blocks, block{kind: blockNotice, text: fmt.Sprintf("Restored %s (%s). Later edits to tracked files were discarded.", checkpoint.Ref, message)})
	}
	m.refresh()
	return m, nil
}

// slashWorktree runs git worktree operations for the operator: a parallel
// task gets its own checkout instead of colliding in the current tree.
func (m *Model) slashWorktree(args string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(args)
	action := "list"
	if len(fields) > 0 {
		action = strings.ToLower(fields[0])
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	tool, ok := m.app.Tools().Lookup("git_worktree")
	if !ok {
		m.blocks = append(m.blocks, block{kind: blockError, text: "The worktree tool is not available."})
		m.refresh()
		return m, nil
	}
	callArgs := map[string]any{"action": action}
	switch {
	case action == "prune":
		// /worktree prune [max_age]
		if len(fields) > 1 {
			callArgs["max_age"] = fields[1]
		}
	case action == "add" && len(fields) > 2:
		// /worktree add <path> <branch> carries the branch as the tail.
		callArgs["path"] = fields[1]
		callArgs["branch"] = strings.Join(fields[2:], " ")
	case len(fields) > 1:
		callArgs["path"] = strings.Join(fields[1:], " ")
	}
	result, err := tool.Run(ctx, &agent.Env{Workspace: m.app.Workspace()}, callArgs)
	if err != nil {
		m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
	} else if result.IsError {
		m.blocks = append(m.blocks, block{kind: blockError, text: result.Output})
	} else {
		m.blocks = append(m.blocks, block{kind: blockNotice, text: result.Output})
	}
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

// slashCron manages scheduled jobs. The "::" separator keeps a schedule with
// spaces apart from the prompt: /cron add every 30m :: check the build.
func (m *Model) slashCron(args string) (tea.Model, tea.Cmd) {
	trimmed := strings.TrimSpace(args)
	fields := strings.Fields(trimmed)
	if trimmed == "" || (len(fields) > 0 && fields[0] == "list") {
		jobs, err := m.app.CronJobs()
		if err != nil {
			m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
			m.refresh()
			return m, nil
		}
		if len(jobs) == 0 {
			m.blocks = append(m.blocks, block{kind: blockNotice, text: "No scheduled jobs.\nAdd one with /cron add every 30m :: <prompt>"})
			m.refresh()
			return m, nil
		}
		lines := []string{fmt.Sprintf("%d scheduled job(s):", len(jobs))}
		for _, job := range jobs {
			state := "off"
			if job.Enabled {
				state = "on"
			}
			name := job.Name
			if name == "" {
				name = "(unnamed)"
			}
			lines = append(lines, fmt.Sprintf("  %s  %-3s  %-18s %s", job.ID, state, job.Schedule.String(), name))
			if job.Enabled && !job.NextRun.IsZero() {
				lines = append(lines, "       next: "+job.NextRun.Format("2006-01-02 15:04"))
			}
			if job.LastError != "" {
				lines = append(lines, "       error: "+job.LastError)
			}
		}
		lines = append(lines, "", "Manage with /cron add <spec> :: <prompt>, /cron remove|on|off|run <id>.")
		m.blocks = append(m.blocks, block{kind: blockNotice, text: strings.Join(lines, "\n")})
		m.refresh()
		return m, nil
	}
	switch fields[0] {
	case "add":
		rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "add"))
		parts := strings.SplitN(rest, "::", 2)
		if len(parts) != 2 {
			m.blocks = append(m.blocks, block{kind: blockError, text: "Usage: /cron add every 30m :: <prompt>"})
			break
		}
		job, err := m.app.CronAdd("", strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]))
		if err != nil {
			m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
			break
		}
		m.blocks = append(m.blocks, block{kind: blockNotice, text: fmt.Sprintf("Added job %s (%s). Next run %s.", job.ID, job.Schedule.String(), job.NextRun.Format("2006-01-02 15:04"))})
	case "remove":
		if len(fields) < 2 {
			m.blocks = append(m.blocks, block{kind: blockError, text: "Usage: /cron remove <id>"})
			break
		}
		removed, err := m.app.CronRemove(fields[1])
		switch {
		case err != nil:
			m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
		case !removed:
			m.blocks = append(m.blocks, block{kind: blockError, text: "No job with id " + fields[1] + "."})
		default:
			m.blocks = append(m.blocks, block{kind: blockNotice, text: "Removed job " + fields[1] + "."})
		}
	case "on", "off":
		if len(fields) < 2 {
			m.blocks = append(m.blocks, block{kind: blockError, text: "Usage: /cron " + fields[0] + " <id>"})
			break
		}
		_, ok, err := m.app.CronSetEnabled(fields[1], fields[0] == "on")
		switch {
		case err != nil:
			m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
		case !ok:
			m.blocks = append(m.blocks, block{kind: blockError, text: "No job with id " + fields[1] + "."})
		default:
			m.blocks = append(m.blocks, block{kind: blockNotice, text: "Job " + fields[1] + " is now " + fields[0] + "."})
		}
	case "run":
		if len(fields) < 2 {
			m.blocks = append(m.blocks, block{kind: blockError, text: "Usage: /cron run <id>"})
			break
		}
		job, ok, err := m.app.CronJob(fields[1])
		// The failure branches have to return rather than break: a break here
		// leaves the switch and falls into the line below, which reported
		// "Running job ." and started an empty turn for a job that does not
		// exist or could not be read.
		switch {
		case err != nil:
			m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
		case !ok:
			m.blocks = append(m.blocks, block{kind: blockError, text: "No job with id " + fields[1] + "."})
		default:
			m.blocks = append(m.blocks, block{kind: blockNotice, text: "Running job " + job.ID + "."})
			m.refresh()
			return m.startRun(job.Prompt)
		}
	default:
		m.blocks = append(m.blocks, block{kind: blockError, text: "Usage: /cron [list|add|remove|on|off|run]"})
	}
	m.refresh()
	return m, nil
}

// slashHeartbeat shows or changes the periodic self-check.
func (m *Model) slashHeartbeat(args string) (tea.Model, tea.Cmd) {
	trimmed := strings.ToLower(strings.TrimSpace(args))
	switch {
	case trimmed == "" || trimmed == "status":
		enabled := m.app.Config().Heartbeat.Enabled
		state := "off"
		if enabled {
			state = "on"
		}
		text := "Heartbeat is " + state + " (" + m.app.Config().Heartbeat.Interval + ")."
		if !enabled {
			text += "\nTurn it on with /heartbeat on, or set a cadence with /heartbeat 30m."
		}
		m.blocks = append(m.blocks, block{kind: blockNotice, text: text})
	case trimmed == "on":
		if err := m.app.SetHeartbeat(true, ""); err != nil {
			m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
		} else {
			m.blocks = append(m.blocks, block{kind: blockNotice, text: "Heartbeat is on (" + m.app.Config().Heartbeat.Interval + "). It applies the next time 'termixgo serve' starts."})
		}
	case trimmed == "off":
		if err := m.app.SetHeartbeat(false, ""); err != nil {
			m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
		} else {
			m.blocks = append(m.blocks, block{kind: blockNotice, text: "Heartbeat is off."})
		}
	default:
		interval, err := time.ParseDuration(trimmed)
		if err != nil || interval < time.Minute {
			m.blocks = append(m.blocks, block{kind: blockError, text: "Usage: /heartbeat [on|off|30m]"})
			break
		}
		if err := m.app.SetHeartbeat(true, trimmed); err != nil {
			m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
		} else {
			m.blocks = append(m.blocks, block{kind: blockNotice, text: "Heartbeat runs every " + trimmed + "."})
		}
	}
	m.refresh()
	return m, nil
}

// slashAudit prints the newest metadata-only ledger entries.
func (m *Model) slashAudit(args string) (tea.Model, tea.Cmd) {
	limit := 20
	if fields := strings.Fields(args); len(fields) > 0 {
		if value, err := strconv.Atoi(fields[0]); err == nil && value > 0 {
			limit = value
		}
	}
	entries, err := m.app.AuditTail(limit)
	if err != nil {
		m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
		m.refresh()
		return m, nil
	}
	if len(entries) == 0 {
		m.blocks = append(m.blocks, block{kind: blockNotice, text: "No audit entries yet."})
		m.refresh()
		return m, nil
	}
	lines := []string{fmt.Sprintf("Audit (%d):", len(entries))}
	for _, entry := range entries {
		status := "ok"
		if !entry.OK {
			status = "fail"
		}
		detail := entry.StopReason
		if entry.Kind == "tool" && entry.Millis > 0 {
			detail = fmt.Sprintf("%dms", entry.Millis)
		}
		lines = append(lines, fmt.Sprintf("  %s  %-5s %-16s %-5s %s", entry.Time.Format("01-02 15:04:05"), entry.Kind, entry.Name, status, detail))
	}
	m.blocks = append(m.blocks, block{kind: blockNotice, text: strings.Join(lines, "\n")})
	m.refresh()
	return m, nil
}

// slashBatch starts parallel workers from one line: /batch fix login :: add tests.
// Tasks split on "::", each runs in its own worktree and announces completion.
func (m *Model) slashBatch(args string) (tea.Model, tea.Cmd) {
	parts := strings.Split(args, "::")
	var tasks []string
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			tasks = append(tasks, trimmed)
		}
	}
	if len(tasks) == 0 {
		m.blocks = append(m.blocks, block{kind: blockError, text: "Usage: /batch <task> :: <task> [...]"})
		m.refresh()
		return m, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	output, err := m.app.StartParallelBatch(ctx, tasks, "")
	if err != nil {
		m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
	} else {
		m.blocks = append(m.blocks, block{kind: blockNotice, text: output})
	}
	m.refresh()
	return m, nil
}

// slashWorkers shows every tracked parallel worktree with its live worker
// state: running, done, failed or idle. It is the one screen that answers
// which checkout is safe to review without hunting process logs.
func (m *Model) slashWorkers() (tea.Model, tea.Cmd) {
	text := m.app.BatchStatus()
	m.blocks = append(m.blocks, block{kind: blockNotice, text: text})
	m.refresh()
	return m, nil
}

// slashWorker lists the coding workers or starts one.
func (m *Model) slashWorker(args string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(strings.TrimSpace(args))
	if len(fields) == 0 || strings.EqualFold(fields[0], "list") {
		lines := []string{"Coding workers:"}
		for _, info := range m.app.WorkerCatalog() {
			state := "missing"
			if info.Available {
				state = "ready"
			}
			lines = append(lines, fmt.Sprintf("  %-10s %-7s %s", info.Kind, state, info.Detail))
		}
		lines = append(lines, "", "Start one with /worker start <kind> <task>.")
		m.blocks = append(m.blocks, block{kind: blockNotice, text: strings.Join(lines, "\n")})
		m.refresh()
		return m, nil
	}
	if !strings.EqualFold(fields[0], "start") {
		m.blocks = append(m.blocks, block{kind: blockError, text: "Usage: /worker [list|start <kind> <task>]"})
		m.refresh()
		return m, nil
	}
	if len(fields) < 3 {
		m.blocks = append(m.blocks, block{kind: blockError, text: "Usage: /worker start <kind> <task>"})
		m.refresh()
		return m, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := m.app.StartCodeWorker(ctx, fields[1], strings.Join(fields[2:], " "), "")
	if err != nil {
		m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
	} else {
		m.blocks = append(m.blocks, block{kind: blockNotice, text: output})
	}
	m.refresh()
	return m, nil
}
