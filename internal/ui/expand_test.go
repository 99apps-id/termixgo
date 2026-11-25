package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/99apps-id/termixgo/internal/agent"
)

func noLongerThan(t *testing.T, text string, width int) {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if got := len([]rune(stripANSI(line))); got > width {
			t.Fatalf("a row is %d columns, over %d: %q", got, width, stripANSI(line))
		}
	}
}

// A table cell wider than its column wraps inside it: nothing is lost and no row
// outgrows the terminal.
func TestTableCellsWrapInsteadOfBeingCut(t *testing.T) {
	long := "Kolom ini berisi penjelasan yang cukup panjang sehingga tidak muat dalam satu baris"
	out := md(t, "| Nama | Keterangan |\n|---|---|\n| satu | "+long+" |\n| dua | pendek |", 50)
	noLongerThan(t, out, 50)
	flat := strings.Join(strings.Fields(strings.NewReplacer("│", " ", "─", " ", "┄", " ", "┼", " ").Replace(out)), " ")
	for _, word := range strings.Fields(long) {
		if !strings.Contains(flat, word) {
			t.Fatalf("the word %q was lost:\n%s", word, out)
		}
	}
}

// With too many columns for the width the table is stacked as "header: value".
func TestTooWideATableIsStacked(t *testing.T) {
	out := md(t, "| Alpha | Beta | Gamma | Delta | Epsilon | Zeta |\n|---|---|---|---|---|---|\n| aaaaaaaaaa | bbbbbbbbbb | cccccccccc | dddddddddd | eeeeeeeeee | ffffffffff |", 30)
	noLongerThan(t, out, 30)
	for _, want := range []string{"Alpha:", "Zeta:", "aaaaaaaaaa", "ffffffffff"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the stacked table lacks %q:\n%s", want, out)
		}
	}
}

func TestLongHeadingWrapsInsteadOfBeingCut(t *testing.T) {
	heading := "Rencana migrasi basis data produksi beserta seluruh langkah verifikasi dan pemulihan"
	out := md(t, "## "+heading, 30)
	noLongerThan(t, out, 30)
	for _, word := range strings.Fields(heading) {
		if !strings.Contains(out, word) {
			t.Fatalf("the heading word %q was lost:\n%s", word, out)
		}
	}
}

func TestLongPlanItemWrapsUnderItsMarker(t *testing.T) {
	styles := NewStyles(DefaultPalette())
	title := "Perbarui seluruh dokumentasi pengguna dan pastikan setiap contoh perintah masih berjalan"
	out := stripANSI(renderPlanBlock(block{kind: blockPlan, plan: []agent.Todo{{Title: title, Status: "pending"}}}, styles, 40, 0))
	noLongerThan(t, out, 40)
	for _, word := range strings.Fields(title) {
		if !strings.Contains(out, word) {
			t.Fatalf("the plan word %q was lost:\n%s", word, out)
		}
	}
}

// The details view wraps a long output line under its indent.
func TestCommandOutputLongLineWrapsInTheDetailsView(t *testing.T) {
	styles := NewStyles(DefaultPalette())
	line := strings.Repeat("keluaran-panjang ", 12)
	item := block{kind: blockTool, toolLabel: "Ran go build", toolOK: true, toolOutput: line, toolOutputLines: 1}
	details := stripANSI(renderToolBlock(item, styles, 50, true))
	noLongerThan(t, details, 50)
	if strings.Count(details, "keluaran-panjang") != 12 {
		t.Fatalf("the whole output line should be shown:\n%s", details)
	}
	compact := stripANSI(renderToolBlock(item, styles, 50, false))
	if !strings.Contains(compact, "expand") {
		t.Fatalf("a compact line that was cut should say how to expand:\n%s", compact)
	}
}

func clickAt(t *testing.T, model *Model, line int) {
	t.Helper()
	y := model.transcriptTop() + line - model.viewport.YOffset
	for _, action := range []tea.MouseAction{tea.MouseActionPress, tea.MouseActionRelease} {
		model.handleMouse(tea.MouseMsg{X: 2, Y: y, Button: tea.MouseButtonLeft, Action: action})
	}
}

func rowContaining(model *Model, text string) int {
	for index, line := range model.contentLines {
		if strings.Contains(stripANSI(line), text) {
			return index
		}
	}
	return -1
}

// A click on a tool line opens it, a second click closes it, and the global
// details toggle is untouched throughout.
func TestClickOpensAndClosesAToolBlock(t *testing.T) {
	model := chatModel(t)
	model.inline = false
	model.width, model.height = 70, 30
	model.Update(tea.WindowSizeMsg{Width: 70, Height: 30})
	command := "Ran go test ./internal/... -run TestSomethingVeryLong -count=1 -v -timeout 10m -tags integration,e2e"
	model.blocks = []block{
		{kind: blockUser, text: "jalankan tes"},
		{kind: blockTool, toolName: "run_command", toolLabel: command, toolOK: true},
		{kind: blockAssistant, text: "selesai"},
	}
	model.refresh()
	row := rowContaining(model, "Ran go test")
	if row < 0 {
		t.Fatalf("the tool line is not on screen:\n%s", strings.Join(model.contentLines, "\n"))
	}
	if strings.Contains(strings.Join(model.contentLines, "\n"), "e2e") {
		t.Fatalf("the compact line should have cut the end of the command")
	}

	clickAt(t, model, row)
	if !model.blocks[1].expanded {
		t.Fatalf("a click on the tool line should open it")
	}
	if model.showDetails {
		t.Fatalf("a click must not flip the global details toggle")
	}
	if !strings.Contains(stripANSI(strings.Join(model.contentLines, "\n")), "e2e") {
		t.Fatalf("the opened block should show the whole command:\n%s", strings.Join(model.contentLines, "\n"))
	}

	clickAt(t, model, rowContaining(model, "Ran go test"))
	if model.blocks[1].expanded {
		t.Fatalf("a second click should close it again")
	}
}

// A click on text that is not a tool or thinking block opens nothing.
func TestClickOnAnAnswerOpensNothing(t *testing.T) {
	model := chatModel(t)
	model.inline = false
	model.Update(tea.WindowSizeMsg{Width: 70, Height: 30})
	model.blocks = []block{{kind: blockUser, text: "halo"}, {kind: blockAssistant, text: "jawaban"}}
	model.refresh()
	clickAt(t, model, rowContaining(model, "jawaban"))
	for _, item := range model.blocks {
		if item.expanded {
			t.Fatalf("a click on an answer must not expand anything")
		}
	}
}

// Consecutive read tools collapse into one summary line; a click opens the
// group into its own lines.
func TestClickOpensAGroupOfReadTools(t *testing.T) {
	model := chatModel(t)
	model.inline = false
	model.Update(tea.WindowSizeMsg{Width: 70, Height: 30})
	model.blocks = []block{
		{kind: blockTool, toolName: "read_file", toolLabel: "Read a.go", toolOK: true},
		{kind: blockTool, toolName: "read_file", toolLabel: "Read b.go", toolOK: true},
		{kind: blockAssistant, text: "ok"},
	}
	model.refresh()
	row := rowContaining(model, "Read 2 files")
	if row < 0 {
		t.Fatalf("the group line is missing:\n%s", strings.Join(model.contentLines, "\n"))
	}
	clickAt(t, model, row)
	joined := stripANSI(strings.Join(model.contentLines, "\n"))
	if !strings.Contains(joined, "Read a.go") || !strings.Contains(joined, "Read b.go") {
		t.Fatalf("the opened group should list its tools:\n%s", joined)
	}
}

// The approval dialog shows the whole command being approved: a cut command
// hides the part the operator most needs to read before saying yes.
func TestApprovalDialogShowsTheWholeCommand(t *testing.T) {
	model := chatModel(t)
	model.width, model.height = 70, 40
	command := "run_command: cd /srv/app && tar -czf backup.tar.gz data/ && scp backup.tar.gz deploy@example.org:/backups/ && rm -rf /srv/app/data/old"
	model.pendingApproval = &agent.ApprovalRequest{Tool: "run_command", Risk: "command", Detail: command}
	view := stripANSI(model.viewApproval())
	for _, word := range strings.Fields(command) {
		if !strings.Contains(view, word) {
			t.Fatalf("the word %q of the command is not shown:\n%s", word, view)
		}
	}
	noLongerThan(t, view, 70)
}

// On a short terminal the detail is clipped with an ellipsis and the choices
// still fit.
func TestApprovalDetailIsClippedOnlyWhenRoomRunsOut(t *testing.T) {
	model := chatModel(t)
	model.width, model.height = 50, 20
	command := strings.Repeat("segmen-perintah-sangat-panjang ", 40)
	model.pendingApproval = &agent.ApprovalRequest{Tool: "run_command", Risk: "command", Detail: command}
	view := stripANSI(model.viewApproval())
	if !strings.Contains(view, "...") {
		t.Fatalf("a clipped detail must end in an ellipsis:\n%s", view)
	}
	if got := strings.Count(view, "\n") + 1; got > model.modalRowBudget() {
		t.Fatalf("the dialog is %d rows, over the budget of %d:\n%s", got, model.modalRowBudget(), view)
	}
	for _, option := range approvalOptions {
		if !strings.Contains(view, option.label) {
			t.Fatalf("the choice %q must stay visible:\n%s", option.label, view)
		}
	}
}
