package ui

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/provider"
)

// ---------------------------------------------------------------- input history

// historyCap is how many submitted inputs the history keeps.
const historyCap = 500

// promptHistory is the list Up and Down walk through. position is len(entries)
// while editing a fresh draft, and the draft is kept so walking back down
// restores it.
type promptHistory struct {
	entries  []string
	position int
	draft    string
	path     string
}

func historyPath() string {
	home, err := config.Home()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "prompt-history.jsonl")
}

// loadHistory reads the persisted history, one JSON string per line.
func loadHistory(path string) *promptHistory {
	history := &promptHistory{path: path}
	if path == "" {
		return history
	}
	file, err := os.Open(path)
	if err != nil {
		return history
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var entry string
		if json.Unmarshal(scanner.Bytes(), &entry) == nil && strings.TrimSpace(entry) != "" {
			history.entries = append(history.entries, entry)
		}
	}
	if len(history.entries) > historyCap {
		history.entries = history.entries[len(history.entries)-historyCap:]
	}
	history.position = len(history.entries)
	return history
}

// historyWorthy reports whether an input may be kept. A command that can carry
// a key is never written to disk, and a collapsed paste is kept only as typed.
func historyWorthy(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	for _, prefix := range []string{"/endpoint", "/setup", "/telegram setup", "/secret"} {
		if strings.HasPrefix(lower, prefix) {
			return false
		}
	}
	return true
}

// add records a submitted input and resets the walk.
func (h *promptHistory) add(value string) {
	h.draft = ""
	if !historyWorthy(value) {
		h.position = len(h.entries)
		return
	}
	value = strings.TrimSpace(value)
	if n := len(h.entries); n == 0 || h.entries[n-1] != value {
		h.entries = append(h.entries, value)
		if len(h.entries) > historyCap {
			h.entries = h.entries[len(h.entries)-historyCap:]
		}
		h.append(value)
	}
	h.position = len(h.entries)
}

// append writes one entry, rewriting the file when it grew past twice the cap.
func (h *promptHistory) append(value string) {
	if h.path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(h.path), 0o700); err != nil {
		return
	}
	if info, err := os.Stat(h.path); err == nil && info.Size() > 2*historyCap*200 {
		var b strings.Builder
		for _, entry := range h.entries {
			line, _ := json.Marshal(entry)
			b.Write(line)
			b.WriteByte('\n')
		}
		_ = os.WriteFile(h.path, []byte(b.String()), 0o600)
		return
	}
	file, err := os.OpenFile(h.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	line, _ := json.Marshal(value)
	_, _ = file.Write(append(line, '\n'))
}

// previous steps back; it returns false at the oldest entry.
func (h *promptHistory) previous(current string) (string, bool) {
	if h.position == 0 || len(h.entries) == 0 {
		return "", false
	}
	if h.position == len(h.entries) {
		h.draft = current
	}
	h.position--
	return h.entries[h.position], true
}

// next steps forward, ending on the draft that was being typed.
func (h *promptHistory) next() (string, bool) {
	if h.position >= len(h.entries) {
		return "", false
	}
	h.position++
	if h.position == len(h.entries) {
		return h.draft, true
	}
	return h.entries[h.position], true
}

// historyKey walks the history with Up and Down when the cursor is on the
// composer's first (Up) or last (Down) line, so moving inside a multi-line
// draft still works.
func (m *Model) historyKey(key string) bool {
	if m.history == nil {
		return false
	}
	switch key {
	case "up":
		if m.composer.Line() != 0 {
			return false
		}
		entry, ok := m.history.previous(m.composer.Value())
		if !ok {
			return false
		}
		m.composer.SetValue(entry)
		m.composer.CursorEnd()
	case "down":
		if m.composer.Line() < m.composer.LineCount()-1 {
			return false
		}
		entry, ok := m.history.next()
		if !ok {
			return false
		}
		m.composer.SetValue(entry)
		m.composer.CursorEnd()
	default:
		return false
	}
	m.updateSlashMatches()
	m.refresh()
	return true
}

// ---------------------------------------------------------------- reverse history search (Ctrl+R)

func (m *Model) openHistorySearch() (tea.Model, tea.Cmd) {
	m.closeMenus()
	m.historySearchActive = true
	m.historySearchDraft = m.composer.Value()
	m.historySearchQuery = ""
	m.historySearchCursor = 0
	m.refresh()
	return m, nil
}

func (m *Model) historySearchMatches() []string {
	if m.history == nil || len(m.history.entries) == 0 {
		return nil
	}
	needle := strings.ToLower(strings.TrimSpace(m.historySearchQuery))
	seen := make(map[string]bool, len(m.history.entries))
	matches := make([]string, 0, 16)
	for i := len(m.history.entries) - 1; i >= 0; i-- {
		entry := m.history.entries[i]
		if seen[entry] {
			continue
		}
		seen[entry] = true
		if needle == "" || strings.Contains(strings.ToLower(entry), needle) {
			matches = append(matches, entry)
		}
	}
	return matches
}

func (m *Model) handleHistorySearchKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	matches := m.historySearchMatches()
	selected := ""
	if len(matches) > 0 {
		if m.historySearchCursor >= len(matches) || m.historySearchCursor < 0 {
			m.historySearchCursor = 0
		}
		selected = matches[m.historySearchCursor]
	}

	switch key.String() {
	case "esc", "ctrl+c", "ctrl+g":
		m.historySearchActive = false
		m.historySearchQuery = ""
		m.composer.SetValue(m.historySearchDraft)
		m.composer.CursorEnd()
		m.updateSlashMatches()
		m.refresh()
		return m, nil
	case "ctrl+r", "down":
		if len(matches) > 0 {
			m.historySearchCursor = (m.historySearchCursor + 1) % len(matches)
		}
		m.refresh()
		return m, nil
	case "up":
		if len(matches) > 0 {
			m.historySearchCursor = (m.historySearchCursor - 1 + len(matches)) % len(matches)
		}
		m.refresh()
		return m, nil
	case "tab", "right":
		m.historySearchActive = false
		m.historySearchQuery = ""
		if selected != "" {
			m.composer.SetValue(selected)
			m.composer.CursorEnd()
		} else {
			m.composer.SetValue(m.historySearchDraft)
			m.composer.CursorEnd()
		}
		m.updateSlashMatches()
		m.refresh()
		return m, nil
	case "enter":
		m.historySearchActive = false
		m.historySearchQuery = ""
		if selected == "" {
			m.composer.SetValue(m.historySearchDraft)
			m.composer.CursorEnd()
			m.updateSlashMatches()
			m.refresh()
			return m, nil
		}
		return m.submitComposerValue(selected)
	case "backspace":
		if m.historySearchQuery != "" {
			m.historySearchQuery = trimLastRune(m.historySearchQuery)
			m.historySearchCursor = 0
		}
		m.refresh()
		return m, nil
	}
	if key.Type == tea.KeyRunes && len(key.Runes) > 0 {
		m.historySearchQuery += string(key.Runes)
		m.historySearchCursor = 0
		m.refresh()
	}
	return m, nil
}

func (m *Model) viewHistorySearch() string {
	body := m.menuBodyWidth()
	matches := m.historySearchMatches()
	if m.historySearchCursor >= len(matches) || m.historySearchCursor < 0 {
		m.historySearchCursor = 0
	}

	header := fmt.Sprintf("(reverse-i-search) '%s':", m.historySearchQuery)
	if len(matches) == 0 {
		header += " (no match)"
	}
	rows := []string{truncate(m.styles.BoxTitle.Render(header), body)}

	limit := min(4, len(matches))
	start := 0
	if m.historySearchCursor >= limit && limit > 0 {
		start = m.historySearchCursor - limit + 1
	}
	end := min(len(matches), start+limit)
	for idx := start; idx < end; idx++ {
		preview := strings.ReplaceAll(matches[idx], "\n", " ")
		if idx == m.historySearchCursor {
			rows = append(rows, truncate(m.styles.MenuSelected.Render("> "+preview), body))
		} else {
			rows = append(rows, truncate("  "+m.styles.MenuDesc.Render(preview), body))
		}
	}
	if len(matches) > end {
		rows = append(rows, truncate(m.styles.Dim.Render(fmt.Sprintf("  ... %d older matches", len(matches)-end)), body))
	}
	rows = append(rows, truncate(m.styles.Hint.Render("Enter run | Tab edit | Ctrl+R/Down next | Up prev | Esc cancel"), body))
	return m.styles.Menu.Render(strings.Join(rows, "\n"))
}

// ---------------------------------------------------------------- transcript viewer

// openTranscript shows the whole session, details expanded, in a scrollable
// pager. In the inline layout the scrollback already holds the printed
// messages, so this is where their tool output and reasoning can be read.
func (m *Model) openTranscript() (tea.Model, tea.Cmd) {
	m.current = modeTranscript
	m.transcriptSearchOpen = false
	m.transcriptSearchQuery = ""
	m.viewport.Width = m.width
	m.viewport.Height = max(3, m.height-2)
	m.updateTranscriptContent()
	m.viewport.GotoBottom()
	if m.inline {
		return m, tea.EnterAltScreen
	}
	return m, nil
}

func (m *Model) updateTranscriptContent() {
	full := transcript(m.blocks, m.styles, m.width, true)
	needle := strings.ToLower(strings.TrimSpace(m.transcriptSearchQuery))
	if needle == "" {
		m.viewport.SetContent(full)
		return
	}
	lines := strings.Split(full, "\n")
	var matched []string
	for _, line := range lines {
		if strings.Contains(strings.ToLower(stripANSI(line)), needle) {
			matched = append(matched, line)
		}
	}
	if len(matched) == 0 {
		m.viewport.SetContent(m.styles.Dim.Render(fmt.Sprintf("  No transcript lines match %q.", m.transcriptSearchQuery)))
		return
	}
	m.viewport.SetContent(strings.Join(matched, "\n"))
	m.viewport.GotoTop()
}

func (m *Model) closeTranscript() (tea.Model, tea.Cmd) {
	m.current = modeChat
	m.transcriptSearchOpen = false
	m.transcriptSearchQuery = ""
	m.layout()
	if m.inline {
		return m, tea.ExitAltScreen
	}
	return m, nil
}

func (m *Model) handleTranscriptKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.transcriptSearchOpen {
		switch key.String() {
		case "esc":
			m.transcriptSearchOpen = false
			m.transcriptSearchQuery = ""
			m.updateTranscriptContent()
			return m, nil
		case "enter":
			m.transcriptSearchOpen = false
			return m, nil
		case "backspace":
			if m.transcriptSearchQuery != "" {
				m.transcriptSearchQuery = trimLastRune(m.transcriptSearchQuery)
				m.updateTranscriptContent()
			}
			return m, nil
		}
		if key.Type == tea.KeyRunes && len(key.Runes) > 0 {
			m.transcriptSearchQuery += string(key.Runes)
			m.updateTranscriptContent()
		}
		return m, nil
	}

	switch key.String() {
	case "esc":
		if m.transcriptSearchQuery != "" {
			m.transcriptSearchQuery = ""
			m.updateTranscriptContent()
			return m, nil
		}
		return m.closeTranscript()
	case "q", "ctrl+o", "ctrl+c":
		return m.closeTranscript()
	case "/":
		m.transcriptSearchOpen = true
		m.transcriptSearchQuery = ""
		return m, nil
	case "up", "k":
		m.viewport.ScrollUp(1)
	case "down", "j":
		m.viewport.ScrollDown(1)
	case "pgup", "b":
		m.viewport.HalfPageUp()
	case "pgdown", " ", "f":
		m.viewport.HalfPageDown()
	case "home", "g":
		m.viewport.GotoTop()
	case "end", "G":
		m.viewport.GotoBottom()
	}
	return m, nil
}

func (m *Model) viewTranscript() string {
	var header string
	switch {
	case m.transcriptSearchOpen:
		header = m.styles.BoxTitle.Render("Transcript search") + m.styles.MenuDesc.Render(fmt.Sprintf("  /%s_  (Enter apply | Esc clear)", m.transcriptSearchQuery))
	case m.transcriptSearchQuery != "":
		header = m.styles.BoxTitle.Render("Transcript") + m.styles.Dim.Render(fmt.Sprintf("  filter: %q  %d%%  | / search | Esc clear filter | Ctrl+O close", m.transcriptSearchQuery, int(m.viewport.ScrollPercent()*100)))
	default:
		header = m.styles.BoxTitle.Render("Transcript") + m.styles.Dim.Render(fmt.Sprintf("  %d%%  Up/Down PgUp/PgDn Home/End scroll | / search | Esc or Ctrl+O close", int(m.viewport.ScrollPercent()*100)))
	}
	return truncate(header, max(1, m.width)) + "\n" + m.viewport.View()
}

func (m *Model) slashTranscript() (tea.Model, tea.Cmd) {
	return m.openTranscript()
}

// ---------------------------------------------------------------- approval cycling

// approvalCycle is the Shift+Tab order.
var approvalCycle = []config.ApprovalMode{config.ApprovalAsk, config.ApprovalEdits, config.ApprovalAll, config.ApprovalPlan}

// cycleApproval moves to the next approval mode.
func (m *Model) cycleApproval() (tea.Model, tea.Cmd) {
	current := m.appConfig().ApprovalMode
	next := approvalCycle[0]
	for index, mode := range approvalCycle {
		if mode == current {
			next = approvalCycle[(index+1)%len(approvalCycle)]
			break
		}
	}
	if err := m.app.SetApprovalMode(next); err != nil {
		m.notice = err.Error()
	} else {
		m.notice = "Approval: " + string(next) + " (Shift+Tab cycles)"
	}
	m.refresh()
	return m, nil
}

// ---------------------------------------------------------------- turn summary

// turnStats is what a turn summary needs, captured when the turn starts.
type turnStats struct {
	// streamed counts the characters of answer and reasoning streamed so
	// far, which the activity line turns into a live token estimate.
	streamed int
	usage    provider.Usage
	cost     float64
	steps    int
}

func (m *Model) beginTurnStats() {
	spend, _ := m.app.Cost()
	m.turnStats = turnStats{usage: m.app.Usage(), cost: spend}
}

// turnSummary is the one line shown when a turn ends: steps, input with its
// cache share, output, spend and time.
func (m *Model) turnSummary(elapsed time.Duration) string {
	now := m.app.Usage()
	input := now.PromptTokens - m.turnStats.usage.PromptTokens
	output := now.CompletionTokens - m.turnStats.usage.CompletionTokens
	cached := now.CacheReadTokens - m.turnStats.usage.CacheReadTokens
	if input <= 0 && output <= 0 {
		return ""
	}
	parts := []string{}
	if m.turnStats.steps > 0 {
		parts = append(parts, fmt.Sprintf("%d step(s)", m.turnStats.steps))
	}
	in := shortCount(input) + " in"
	if input > 0 && cached > 0 {
		in += fmt.Sprintf(" (%d%% cache)", cached*100/input)
	}
	parts = append(parts, in, shortCount(output)+" out")
	if spend, known := m.app.Cost(); known && spend > m.turnStats.cost {
		parts = append(parts, fmt.Sprintf("$%.4f", spend-m.turnStats.cost))
	}
	parts = append(parts, formatElapsed(elapsed))
	return strings.Join(parts, " · ")
}

func shortCount(value int) string {
	if value < 1000 {
		return fmt.Sprintf("%d", value)
	}
	return fmt.Sprintf("%.1fk", float64(value)/1000)
}

func formatElapsed(elapsed time.Duration) string {
	elapsed = elapsed.Round(time.Second)
	if elapsed < time.Minute {
		return elapsed.String()
	}
	return fmt.Sprintf("%dm%02ds", int(elapsed.Minutes()), int(elapsed.Seconds())%60)
}

// ---------------------------------------------------------------- bell & OS notifications

// bellAfter is how long a turn runs before its end rings the bell and fires
// a desktop notification.
const bellAfter = 20 * time.Second

// bell rings the terminal bell, which most terminals turn into a taskbar
// flash or a notification. TERMIXGO_NO_BELL=1 silences it.
func bell() tea.Cmd {
	if value := strings.TrimSpace(os.Getenv("TERMIXGO_NO_BELL")); value != "" && value != "0" {
		return nil
	}
	return func() tea.Msg {
		fmt.Fprint(os.Stdout, "\a")
		return nil
	}
}

func notifyTurnDone(elapsed time.Duration) tea.Cmd {
	return tea.Batch(bell(), osToastCmd("Turn finished", "Completed in "+formatElapsed(elapsed)))
}

func notifyApproval(request agent.ApprovalRequest) tea.Cmd {
	detail := strings.TrimSpace(request.Tool)
	if detail == "" {
		detail = "tool"
	}
	if summary := strings.TrimSpace(request.Detail); summary != "" {
		detail += ": " + agent.Shorten(summary, 80)
	}
	return tea.Batch(bell(), osToastCmd("Approval needed", detail))
}

func osToastCmd(title, message string) tea.Cmd {
	if testing.Testing() {
		return nil
	}
	if value := strings.TrimSpace(os.Getenv("TERMIXGO_NO_NOTIFY")); value != "" && value != "0" {
		return nil
	}
	return func() tea.Msg {
		go sendOSNotification(title, message)
		return nil
	}
}

func sendOSNotification(title, message string) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	switch runtime.GOOS {
	case "windows":
		safeTitle := strings.ReplaceAll(title, "'", "''")
		safeMsg := strings.ReplaceAll(message, "'", "''")
		script := fmt.Sprintf(`
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null
[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] > $null
$xml = New-Object Windows.Data.Xml.Dom.XmlDocument
$xml.LoadXml('<toast><visual><binding template="ToastGeneric"><text>%s</text><text>%s</text></binding></visual></toast>')
$toast = [Windows.UI.Notifications.ToastNotification]::new($xml)
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('Termixgo').Show($toast)
`, safeTitle, safeMsg)
		_ = exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", script).Run()
	case "darwin":
		safeTitle := strings.ReplaceAll(title, `"`, `\"`)
		safeMsg := strings.ReplaceAll(message, `"`, `\"`)
		_ = exec.CommandContext(ctx, "osascript", "-e", fmt.Sprintf(`display notification "%s" with title "Termixgo" subtitle "%s"`, safeMsg, safeTitle)).Run()
	default:
		_ = exec.CommandContext(ctx, "notify-send", "Termixgo: "+title, message).Run()
	}
}

// ---------------------------------------------------------------- Esc-Esc rewind/edit

func (m *Model) hasRewindableTurn() bool {
	if m.app != nil && m.app.Session() != nil {
		if _, ok := m.app.Session().LastUserPrompt(); ok {
			return true
		}
	}
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind == blockUser {
			return true
		}
	}
	return false
}

func (m *Model) openRewindMenu() (*Model, tea.Cmd) {
	preview := ""
	if m.app != nil && m.app.Session() != nil {
		if prompt, ok := m.app.Session().LastUserPrompt(); ok {
			preview = agent.Shorten(strings.ReplaceAll(prompt, "\n", " "), 52)
		}
	}
	if preview == "" {
		for i := len(m.blocks) - 1; i >= 0; i-- {
			if m.blocks[i].kind == blockUser {
				preview = agent.Shorten(strings.ReplaceAll(m.blocks[i].text, "\n", " "), 52)
				break
			}
		}
	}
	items := []pickerItem{
		{ID: "edit", Label: "Edit & resend last message", Detail: preview},
		{ID: "drop", Label: "Rewind last turn (drop prompt & reply)", Detail: "removes the last turn from conversation"},
	}
	if m.app != nil && strings.TrimSpace(m.app.Workspace()) != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		checkpoints, err := agent.ListCheckpoints(ctx, m.app.Workspace())
		cancel()
		if err == nil && len(checkpoints) > 0 {
			latest := checkpoints[0]
			label := strings.TrimSpace(latest.Message)
			if label == "" {
				label = latest.Ref
			}
			items = append(items,
				pickerItem{ID: "both", Label: "Rewind conversation & restore code", Detail: label},
				pickerItem{ID: "code", Label: "Restore code only (keep conversation)", Detail: label},
			)
		}
	}
	items = append(items, pickerItem{ID: "cancel", Label: "Cancel", Detail: "keep conversation as is"})
	m.openPicker(brandedTitle("Rewind or edit last turn"), "rewind", items)
	return m, nil
}

func (m *Model) applyRewindChoice(choice string) (tea.Model, tea.Cmd) {
	switch choice {
	case "edit", "drop":
		prompt, ok, err := m.app.RewindLastTurn()
		if err != nil {
			m.blocks = append(m.blocks, block{kind: blockError, text: "Save rewound session: " + err.Error()})
		}
		fallbackPrompt := m.dropLastUserBlocks()
		if !ok {
			prompt = fallbackPrompt
		}
		if choice == "edit" && prompt != "" {
			m.composer.SetValue(prompt)
			m.composer.CursorEnd()
			m.notice = "Loaded last message into composer for editing"
		} else {
			m.blocks = append(m.blocks, block{kind: blockNotice, text: "Rewound the last turn."})
		}
	case "both":
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		cp, cpErr := agent.RewindToLatest(ctx, m.app.Workspace())
		cancel()
		if cpErr != nil {
			m.blocks = append(m.blocks, block{kind: blockError, text: cpErr.Error()})
			return m, m.enterChat()
		}
		prompt, ok, err := m.app.RewindLastTurn()
		if err != nil {
			m.blocks = append(m.blocks, block{kind: blockError, text: "Save rewound session: " + err.Error()})
		}
		fallbackPrompt := m.dropLastUserBlocks()
		if !ok {
			prompt = fallbackPrompt
		}
		if prompt != "" {
			m.composer.SetValue(prompt)
			m.composer.CursorEnd()
		}
		msg := strings.TrimSpace(cp.Message)
		if msg == "" {
			msg = cp.Ref
		}
		m.blocks = append(m.blocks, block{kind: blockNotice, text: fmt.Sprintf("Rewound turn and restored %s (%s).", cp.Ref, msg)})
	case "code":
		return m.slashRewind("")
	}
	return m, m.enterChat()
}

func (m *Model) dropLastUserBlocks() string {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind == blockUser {
			text := m.blocks[i].text
			m.blocks = m.blocks[:i]
			if m.printed > len(m.blocks) {
				m.printed = len(m.blocks)
			}
			return text
		}
	}
	return ""
}

// ---------------------------------------------------------------- commands

func (m *Model) slashContext() (tea.Model, tea.Cmd) {
	m.blocks = append(m.blocks, block{kind: blockNotice, text: m.app.ContextReport()})
	m.refresh()
	return m, nil
}

func (m *Model) slashLayout(args string) (tea.Model, tea.Cmd) {
	want := strings.ToLower(strings.TrimSpace(args))
	current := "fullscreen"
	if m.inline {
		current = "inline"
	}
	notice := func(text string) (tea.Model, tea.Cmd) {
		m.blocks = append(m.blocks, block{kind: blockNotice, text: text})
		m.refresh()
		return m, nil
	}
	switch want {
	case "":
		return notice("Layout is " + current + ". /layout inline or /layout fullscreen switches.")
	case current:
		return notice("Layout is already " + current + ".")
	case "inline", "fullscreen":
	default:
		return notice("Use /layout inline or /layout fullscreen.")
	}
	_ = m.app.UpdateConfig(func(c *config.Config) { c.TUILayout = want })
	m.blocks = append(m.blocks, block{kind: blockNotice, text: "Layout is now " + want + "."})
	if want == "inline" {
		m.inline = true
		// The scrollback before the switch belongs to the terminal; the
		// transcript is printed into it once more from the start.
		m.printed = 0
		m.layout()
		return m, tea.Batch(tea.ExitAltScreen, tea.DisableMouse)
	}
	m.inline = false
	m.pendingPrint = nil
	m.layout()
	return m, tea.Batch(tea.EnterAltScreen, tea.EnableMouseCellMotion)
}

func (m *Model) slashPermissions(args string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(args)
	notice := func(text string) (tea.Model, tea.Cmd) {
		m.blocks = append(m.blocks, block{kind: blockNotice, text: text})
		m.refresh()
		return m, nil
	}
	if len(fields) > 0 {
		switch strings.ToLower(fields[0]) {
		case "revoke", "remove", "rm":
			if len(fields) < 2 {
				return notice("Usage: /permissions revoke <tool> | /permissions clear")
			}
			if err := m.app.RevokeTool(fields[1]); err != nil {
				return notice("Permissions: " + err.Error())
			}
			return notice(fields[1] + " asks again before its next call.")
		case "clear", "reset":
			if err := m.app.RevokeAllTools(); err != nil {
				return notice("Permissions: " + err.Error())
			}
			return notice("Every tool allowance was dropped; tools ask again per the approval mode.")
		default:
			return notice("Usage: /permissions [revoke <tool> | clear]")
		}
	}
	perms := m.app.Permissions()
	list := func(values []string) string {
		if len(values) == 0 {
			return "none"
		}
		return strings.Join(values, ", ")
	}
	return notice(fmt.Sprintf("Approval mode: %s (Shift+Tab cycles)\nAlways allowed: %s\nAllowed in this folder: %s\nAllowed this session: %s\n/permissions revoke <tool> asks again; /permissions clear drops them all.",
		m.appConfig().ApprovalMode, list(perms.Always), list(perms.Folder), list(perms.Session)))
}

func (m *Model) slashRename(args string) (tea.Model, tea.Cmd) {
	title := strings.TrimSpace(args)
	session := m.app.Session()
	if title == "" {
		current := session.Title()
		if current == "" {
			current = "(untitled)"
		}
		m.blocks = append(m.blocks, block{kind: blockNotice, text: "Session " + shortID(session.ID()) + " is " + current + ". /rename <title> changes it."})
		m.refresh()
		return m, nil
	}
	session.SetTitle(title)
	text := "Renamed this session to " + title + "."
	if err := session.Save(); err != nil {
		text += " It could not be saved yet: " + err.Error()
	}
	m.blocks = append(m.blocks, block{kind: blockNotice, text: text})
	m.refresh()
	return m, tea.SetWindowTitle(m.windowTitle())
}
