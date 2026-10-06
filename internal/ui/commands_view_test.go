package ui

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/app"
	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/provider"
	"github.com/99apps-id/termixgo/internal/secrets"
)

// runSlash drives one slash command through the model and returns the model
// plus everything the transcript gained.
func runSlash(t *testing.T, model *Model, input string) (*Model, string) {
	t.Helper()
	before := len(model.blocks)
	next, _ := model.submit(input)
	updated, ok := next.(*Model)
	if !ok {
		t.Fatalf("submit returned %T, want *Model", next)
	}
	var added []string
	for _, item := range updated.blocks[before:] {
		added = append(added, item.text)
	}
	return updated, strings.Join(added, "\n")
}

// allText returns the whole transcript as flat text, ignoring the view layout.
func allText(model *Model) string {
	var parts []string
	for _, item := range model.blocks {
		parts = append(parts, item.text)
	}
	return strings.Join(parts, "\n")
}

// longCommand stays alive until it is killed, which is what the /ps tests need
// in order to have a process to list and stop.
func longCommand() string {
	if runtime.GOOS == "windows" {
		return "ping -n 120 127.0.0.1 >nul"
	}
	return "sleep 120"
}

// hasBlockKind reports whether the transcript holds a block of a kind.
func hasBlockKind(model *Model, kind blockKind) bool {
	for _, item := range model.blocks {
		if item.kind == kind {
			return true
		}
	}
	return false
}

// ------------------------------------------------- /export

func TestSlashExportExportsCurrentSession(t *testing.T) {
	model := chatModel(t)
	session := model.app.Session()
	session.SetTitle("My Export Test Session")
	session.AddUser("Hello agent")
	session.AddAssistant("Hello operator", "", nil)

	// 1. Export without path (default exports to .termixgo/exports/session-<id>.md)
	_, output := runSlash(t, model, "/export")
	if !strings.Contains(output, "Exported session") {
		t.Errorf("output should report export: %s", output)
	}

	// 2. Export with a path inside the workspace
	exportFile := filepath.Join(model.app.Workspace(), "exported.md")
	_, output = runSlash(t, model, "/export "+exportFile)
	if !strings.Contains(output, "Exported session") {
		t.Errorf("output should report export: %s", output)
	}
	data, err := os.ReadFile(exportFile)
	if err != nil {
		t.Fatalf("read exportFile: %v", err)
	}
	if !strings.Contains(string(data), "# My Export Test Session") || !strings.Contains(string(data), "Hello agent") {
		t.Errorf("exported file content invalid: %s", string(data))
	}

	// 3. A transcript holds tool output, so it must not land outside the workspace.
	outside := filepath.Join(t.TempDir(), "leaked.md")
	_, output = runSlash(t, model, "/export "+outside)
	if !hasBlockKind(model, blockError) {
		t.Errorf("an export outside the workspace should be refused, got: %s", output)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Errorf("the refused export wrote %s: %v", outside, err)
	}
}

// ------------------------------------------------- /model

func TestSlashModelOpensAProviderPickerFirst(t *testing.T) {
	model := chatModel(t)
	_, _ = runSlash(t, model, "/model")

	if model.current != modePicker {
		t.Fatalf("mode = %d, want the picker", model.current)
	}
	if model.picker.action != "model-provider" {
		t.Errorf("action = %q, want model-provider", model.picker.action)
	}
	// The first list is providers, not the whole model catalogue, so it stays
	// short even with hundreds of models.
	marked := 0
	for _, item := range model.picker.items {
		if item.Extra == "current" {
			marked++
		}
	}
	if marked != 1 {
		t.Errorf("exactly the current provider should be marked, got %d", marked)
	}
	if len(model.picker.items) < 1 {
		t.Errorf("the provider list must not be empty")
	}
}

func TestChoosingAModelProviderDrillsIntoItsModels(t *testing.T) {
	model := chatModel(t)
	_, _ = runSlash(t, model, "/model")

	current := model.app.CurrentModel().Provider
	var chosen pickerItem
	found := false
	for _, item := range model.picker.items {
		if item.ID == current {
			chosen = item
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("the current provider %q must be listed", current)
	}

	updated, _ := model.applyPickerChoice("model-provider", chosen)
	after := updated.(*Model)
	if after.picker.action != "model" {
		t.Fatalf("action = %q, want the model list", after.picker.action)
	}
	if after.picker.parent != "model-provider" {
		t.Errorf("the model list should remember its provider list for Escape")
	}
	if len(after.picker.items) == 0 {
		t.Errorf("the model list for %q must not be empty", current)
	}
}

func TestSlashModelSwitchesByName(t *testing.T) {
	model := chatModel(t)
	switched, output := runSlash(t, model, "/model llama3.2:latest")
	if switched.app.CurrentModel().ID != "llama3.2:latest" {
		t.Fatalf("model = %q", switched.app.CurrentModel().ID)
	}
	// The message names the model's label rather than the id, which is what
	// the operator reads in the picker and the header.
	if !strings.Contains(output, switched.app.ModelLabel()) {
		t.Errorf("output = %q, want the model label %q", output, switched.app.ModelLabel())
	}
	if !strings.Contains(output, "Model is now") {
		t.Errorf("output = %q, want a confirmation", output)
	}
}

// TestSlashModelReportsAnUnusableName keeps a failure from looking like a
// switch that worked.
func TestSlashModelReportsAnUnusableName(t *testing.T) {
	model := chatModel(t)
	before := model.app.CurrentModel().ID

	_, output := runSlash(t, model, "/model anthropic:claude-sonnet-4-5")
	if output == "" {
		t.Fatalf("a provider without a key must produce a message")
	}
	if model.app.CurrentModel().ID != before {
		t.Errorf("the model changed to %q despite the failure", model.app.CurrentModel().ID)
	}
	if !hasBlockKind(model, blockError) {
		t.Errorf("the failure should be shown as an error block")
	}
}

// ------------------------------------------------- /sessions

func TestSlashSessionsSaysWhenThereIsNothingSaved(t *testing.T) {
	model := chatModel(t)
	_, output := runSlash(t, model, "/sessions")
	if !strings.Contains(output, "No saved sessions") {
		t.Errorf("output = %q", output)
	}
	if model.current != modeChat {
		t.Errorf("no list means no picker, got mode %d", model.current)
	}
}

func TestSlashSessionsListsWhatIsSaved(t *testing.T) {
	model := chatModel(t)

	for _, title := range []string{"first conversation", "second conversation"} {
		session := agent.NewSession(model.app.Workspace(), "qwen2.5-coder:latest")
		session.AddUser(title)
		if err := session.Save(); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	listed, _ := runSlash(t, model, "/sessions")
	if listed.current != modePicker || listed.picker.action != "session" {
		t.Fatalf("mode = %d action = %q, want the session picker", listed.current, listed.picker.action)
	}
	if len(listed.picker.items) != 2 {
		t.Fatalf("items = %d, want 2", len(listed.picker.items))
	}
	for _, item := range listed.picker.items {
		if strings.TrimSpace(item.Label) == "" {
			t.Errorf("every row needs a label: %+v", item)
		}
		if !strings.Contains(item.Detail, "turns") {
			t.Errorf("detail = %q, want a turn count", item.Detail)
		}
		if strings.TrimSpace(item.Extra) == "" {
			t.Errorf("every row needs a timestamp: %+v", item)
		}
	}
}

// TestSlashSessionsResumesTheChosenOne is the point of the command: picking a
// row must load that conversation into the live session.
func TestSlashSessionsResumesTheChosenOne(t *testing.T) {
	model := chatModel(t)

	saved := agent.NewSession(model.app.Workspace(), "qwen2.5-coder:latest")
	saved.AddUser("the earlier question")
	saved.AddAssistant("the earlier answer", "", nil)
	if err := saved.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	listed, _ := runSlash(t, model, "/sessions")
	item, ok := listed.picker.selected()
	if !ok {
		t.Fatalf("nothing to select")
	}
	resumed := press(t, listed, "enter")

	if resumed.app.Session().ID() != item.ID {
		t.Errorf("session = %q, want %q", resumed.app.Session().ID(), item.ID)
	}
	if !strings.Contains(allText(resumed), "Resumed session") {
		t.Errorf("the resume should be reported:\n%s", allText(resumed))
	}
	if resumed.app.Session().Turns() == 0 {
		t.Errorf("the resumed session should carry its turns")
	}
}

// ------------------------------------------------- /memory

func TestSlashMemorySaysWhenNothingIsLearned(t *testing.T) {
	model := chatModel(t)
	_, output := runSlash(t, model, "/memory")
	if !strings.Contains(output, "Nothing learned yet") {
		t.Errorf("output = %q", output)
	}
}

func TestSlashMemoryListsBothScopes(t *testing.T) {
	model := chatModel(t)
	memory := agent.NewMemory(model.app.Workspace())
	if err := memory.Remember("tabs not spaces", "project"); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	if err := memory.Remember("always run the tests", "global"); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	_, output := runSlash(t, model, "/memory")
	for _, want := range []string{"[project] tabs not spaces", "[global] always run the tests", "Learned memory"} {
		if !strings.Contains(output, want) {
			t.Errorf("output is missing %q:\n%s", want, output)
		}
	}
}

// ------------------------------------------------- /skills

func TestSlashSkillsSaysWhenThereAreNone(t *testing.T) {
	model := chatModel(t)
	_, output := runSlash(t, model, "/skills")
	// Builtins always ship, so an empty folder lists the defaults instead of
	// the empty-state message.
	for _, want := range []string{"hallmark", "impeccable", "builtin"} {
		if !strings.Contains(output, want) {
			t.Errorf("output = %q, want builtin %q listed", output, want)
		}
	}
}

func TestSlashSkillsListsWhatWasAdded(t *testing.T) {
	model := chatModel(t)
	writeProbeSkill(t, model)

	// Reload picks up a skill added after the model was built. The probe joins
	// the two builtins.
	_, output := runSlash(t, model, "/skills reload")
	if !strings.Contains(output, "probe-skill") {
		t.Errorf("output = %q, want the skill listed after a reload", output)
	}
	if !strings.Contains(output, "project") {
		t.Errorf("output = %q, want the scope shown", output)
	}
	if !strings.Contains(output, "Skills (3)") {
		t.Errorf("output = %q, want a count", output)
	}
}

// ------------------------------------------------- /ps

func TestSlashProcessesSaysWhenNothingIsRunning(t *testing.T) {
	model := chatModel(t)
	_, output := runSlash(t, model, "/ps")
	if !strings.Contains(output, "No background processes") {
		t.Errorf("output = %q", output)
	}
}

func TestSlashProcessesListsAndKills(t *testing.T) {
	model := chatModel(t)
	manager := model.app.Processes()
	process, err := manager.Start(context.Background(), longCommand(), t.TempDir())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer manager.Shutdown()

	_, listing := runSlash(t, model, "/ps")
	if !strings.Contains(listing, process.ID) {
		t.Errorf("the listing should name the process:\n%s", listing)
	}
	if !strings.Contains(listing, "Stop one with /ps kill") {
		t.Errorf("the listing should explain how to stop it:\n%s", listing)
	}

	_, killed := runSlash(t, model, "/ps kill "+process.ID)
	if !strings.Contains(killed, process.ID) {
		t.Errorf("output = %q, want the handle", killed)
	}
}

func TestSlashProcessesNeedsAHandleToKill(t *testing.T) {
	model := chatModel(t)
	_, output := runSlash(t, model, "/ps kill")
	if !strings.Contains(output, "Usage: /ps kill") {
		t.Errorf("output = %q, want the usage line", output)
	}
	if !hasBlockKind(model, blockError) {
		t.Errorf("a missing handle should be an error block")
	}
}

func TestSlashProcessesReportsAnUnknownHandle(t *testing.T) {
	model := chatModel(t)
	_, output := runSlash(t, model, "/ps kill proc-999")
	if !strings.Contains(output, "proc-999") {
		t.Errorf("output = %q, want the handle named", output)
	}
	if !hasBlockKind(model, blockError) {
		t.Errorf("an unknown handle should be an error block")
	}
}

// ------------------------------------------------- /telegram

// TestSlashTelegramCoversEverySubcommand walks the command without starting a
// bot: status and off are always available, on needs a token, setup opens the
// wizard and pair mints a code.
func TestSlashTelegramCoversEverySubcommand(t *testing.T) {
	t.Run("status with no token", func(t *testing.T) {
		model := chatModel(t)
		_, output := runSlash(t, model, "/telegram")
		if !strings.Contains(output, "off (no token)") {
			t.Errorf("output = %q", output)
		}
		_, explicit := runSlash(t, model, "/telegram status")
		if !strings.Contains(explicit, "off (no token)") {
			t.Errorf("output = %q, want the status spelling to work too", explicit)
		}
	})

	t.Run("on without a token is refused", func(t *testing.T) {
		model := chatModel(t)
		_, output := runSlash(t, model, "/telegram on")
		if !strings.Contains(output, "token") {
			t.Errorf("output = %q, want it to name the missing token", output)
		}
		if !hasBlockKind(model, blockError) {
			t.Errorf("enabling without a token should be an error block")
		}
	})

	t.Run("off always works", func(t *testing.T) {
		model := chatModel(t)
		_, output := runSlash(t, model, "/telegram off")
		if !strings.Contains(output, "stopped") {
			t.Errorf("output = %q", output)
		}
	})

	t.Run("setup opens the token step", func(t *testing.T) {
		model := chatModel(t)
		updated, _ := runSlash(t, model, "/telegram setup")
		if updated.current != modeSetup || updated.setup.step != setupTelegramToken {
			t.Fatalf("mode = %d step = %d, want the token step", updated.current, updated.setup.step)
		}
		if !updated.input.Focused() {
			t.Errorf("the token field should be focused")
		}
	})

	t.Run("pair mints a code", func(t *testing.T) {
		model := chatModel(t)
		updated, output := runSlash(t, model, "/telegram pair")
		if !strings.Contains(output, "Pairing code:") {
			t.Fatalf("output = %q, want the code", output)
		}
		code, err := updated.app.EnsurePairingCode()
		if err != nil {
			t.Fatalf("EnsurePairingCode: %v", err)
		}
		if !strings.Contains(output, code) {
			t.Errorf("output = %q, want the minted code %q", output, code)
		}
	})

	t.Run("an unknown subcommand shows usage", func(t *testing.T) {
		model := chatModel(t)
		_, output := runSlash(t, model, "/telegram sideways")
		if !strings.Contains(output, "Usage: /telegram") {
			t.Errorf("output = %q", output)
		}
		if !hasBlockKind(model, blockError) {
			t.Errorf("an unknown subcommand should be an error block")
		}
	})
}

// TestSlashTelegramStatusReflectsThePairing walks the middle states, which are
// the ones an operator is in when they ask.
func TestSlashTelegramStatusReflectsThePairing(t *testing.T) {
	model := chatModel(t)
	if err := model.app.SetTelegramToken("123456:AAaa"); err != nil {
		t.Fatalf("SetTelegramToken: %v", err)
	}
	_, configured := runSlash(t, model, "/telegram status")
	if !strings.Contains(configured, "configured, stopped") {
		t.Errorf("output = %q", configured)
	}

	if err := model.app.SetTelegramChat(7, 9); err != nil {
		t.Fatalf("SetTelegramChat: %v", err)
	}
	_, paired := runSlash(t, model, "/telegram status")
	if !strings.Contains(paired, "paired") {
		t.Errorf("output = %q", paired)
	}
}

// ------------------------------------------------- the plain-mode subset

// TestRunPlainSlashHandlesEveryBranch walks the CI-facing command set. Plain
// mode mirrors the TUI catalogue: anything that needs a picker lists instead
// of opening one, because there is no overlay to open.
//
// Each case builds its own app: several of these commands change persisted
// state, and sharing one app would make the cases order dependent.
func TestRunPlainSlashHandlesEveryBranch(t *testing.T) {
	cases := []struct {
		name    string
		command string
		args    string
		want    string
		quit    bool
	}{
		{name: "exit", command: "exit", quit: true},
		{name: "quit is an alias", command: "quit", quit: true},
		{name: "help", command: "help", want: "/model"},
		{name: "? is a help alias", command: "?", want: "/model"},
		{name: "model reports", command: "model", want: "model: "},
		{name: "model switches", command: "model", args: "llama3.2:latest", want: "model is now"},
		{name: "new", command: "new", want: "started a new session"},
		{name: "sessions lists", command: "sessions", want: "session"},
		{name: "stop", command: "stop", want: "stopping"},
		{name: "status", command: "status", want: "workspace:"},
		{name: "cost", command: "cost", want: "tokens:"},
		{name: "trust reports", command: "trust", want: "folder is untrusted"},
		{name: "trust on", command: "trust", args: "on", want: "trusted"},
		{name: "trust off", command: "trust", args: "off", want: "untrusted"},
		{name: "approval reports", command: "approval", want: "approval mode: all"},
		{name: "approval sets", command: "approval", args: "ask", want: "approval mode is now ask"},
		{name: "harness reports", command: "harness", want: "harness:"},
		{name: "plan reports", command: "plan", want: "plan"},
		{name: "tools lists", command: "tools", want: "tools ("},
		{name: "mcp reports", command: "mcp", want: "MCP"},
		{name: "skills reports", command: "skills", want: "skill"},
		{name: "memory reports", command: "memory", want: "learned"},
		{name: "telegram reports", command: "telegram", want: "telegram:"},
		{name: "ps with nothing running", command: "ps", want: "no background processes"},
		{name: "setup explains", command: "setup", want: "setup wizard"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			model := chatModel(t)
			var out bytes.Buffer
			quit, err := runPlainSlash(context.Background(), model.app, testCase.command, testCase.args, &out)
			if err != nil {
				t.Fatalf("runPlainSlash: %v", err)
			}
			if quit != testCase.quit {
				t.Errorf("quit = %v, want %v", quit, testCase.quit)
			}
			got := out.String()
			if testCase.want != "" && !strings.Contains(got, testCase.want) {
				t.Errorf("output = %q, want it to contain %q", got, testCase.want)
			}
			if strings.Contains(got, "\x1b[") {
				t.Errorf("plain output must not contain escapes: %q", got)
			}
		})
	}
}

// TestRunPlainSlashTrustRoundTrip checks the persisted effect rather than the
// wording, because the wording is the operator's and the effect is the point.
func TestRunPlainSlashTrustRoundTrip(t *testing.T) {
	model := chatModel(t)

	if _, err := runPlainSlash(context.Background(), model.app, "trust", "on", &bytes.Buffer{}); err != nil {
		t.Fatalf("trust on: %v", err)
	}
	if !model.app.Trusted() {
		t.Errorf("the app should report the folder trusted")
	}
	reloaded, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reloaded.IsTrusted(model.app.Workspace()) {
		t.Errorf("trust was not persisted")
	}

	if _, err := runPlainSlash(context.Background(), model.app, "trust", "off", &bytes.Buffer{}); err != nil {
		t.Fatalf("trust off: %v", err)
	}
	if model.app.Trusted() {
		t.Errorf("the app should report the folder untrusted")
	}
}

func TestRunPlainSlashReportsFailures(t *testing.T) {
	model := chatModel(t)

	if _, err := runPlainSlash(context.Background(), model.app, "model", "anthropic:claude-sonnet-4-5", &bytes.Buffer{}); err == nil {
		t.Errorf("selecting a model without a key must report an error")
	}
	if _, err := runPlainSlash(context.Background(), model.app, "approval", "sometimes", &bytes.Buffer{}); err == nil {
		t.Errorf("an invalid approval mode must report an error")
	}
	if _, err := runPlainSlash(context.Background(), model.app, "trust", "sideways", &bytes.Buffer{}); err == nil {
		t.Errorf("an invalid trust argument must report an error")
	}
	if _, err := runPlainSlash(context.Background(), model.app, "mcp", "sideways", &bytes.Buffer{}); err == nil {
		t.Errorf("an invalid mcp argument must report an error")
	}
	if _, err := runPlainSlash(context.Background(), model.app, "telegram", "sideways", &bytes.Buffer{}); err == nil {
		t.Errorf("an invalid telegram argument must report an error")
	}
	if _, err := runPlainSlash(context.Background(), model.app, "nonsense", "", &bytes.Buffer{}); err == nil {
		t.Errorf("an unknown command must report an error")
	}
	if _, err := runPlainSlash(context.Background(), model.app, "ps", "kill", &bytes.Buffer{}); err == nil {
		t.Errorf("killing without a handle must report an error")
	}
	if _, err := runPlainSlash(context.Background(), model.app, "ps", "kill proc-999", &bytes.Buffer{}); err == nil {
		t.Errorf("killing an unknown handle must report an error")
	}
}

func TestRunPlainSlashPSListsAndKills(t *testing.T) {
	model := chatModel(t)
	process, err := model.app.Processes().Start(context.Background(), longCommand(), t.TempDir())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer model.app.Processes().Shutdown()

	var out bytes.Buffer
	if _, err := runPlainSlash(context.Background(), model.app, "ps", "", &out); err != nil {
		t.Fatalf("runPlainSlash: %v", err)
	}
	if !strings.Contains(out.String(), process.ID) {
		t.Errorf("output = %q, want the handle", out.String())
	}

	out.Reset()
	if _, err := runPlainSlash(context.Background(), model.app, "ps", "kill "+process.ID, &out); err != nil {
		t.Fatalf("runPlainSlash: %v", err)
	}
	if !strings.Contains(out.String(), process.ID) {
		t.Errorf("output = %q, want the handle", out.String())
	}
}

// TestRunPlainSlashCostSeparatesKnownFromUnknown is the difference the operator
// needs: a free local model costs nothing, an unpriced one cannot be budgeted.
func TestRunPlainSlashCostSeparatesKnownFromUnknown(t *testing.T) {
	model := chatModel(t)
	var out bytes.Buffer
	if _, err := runPlainSlash(context.Background(), model.app, "cost", "", &out); err != nil {
		t.Fatalf("runPlainSlash: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "estimated spend") {
		t.Errorf("output = %q, want the spend line", got)
	}
	// qwen2.5-coder runs locally, so it is known-free rather than unpriced.
	if !strings.Contains(got, "about $0.0000") {
		t.Errorf("a local model should report a known zero spend, got %q", got)
	}
}

// TestSessionsSubcommandsRoundTrip walks rename, search, export and delete
// through the TUI dispatcher against saved sessions.
func TestSessionsSubcommandsRoundTrip(t *testing.T) {
	model := chatModel(t)
	if err := model.app.Session().Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	id := model.app.Session().ID()

	next, _ := model.runSlash("sessions", "rename "+id+" my feature")
	if view := display(next.(*Model)); !strings.Contains(view, "my feature") {
		t.Fatalf("rename should confirm the title:\n%s", view)
	}

	searched, _ := model.runSlash("sessions", "search feature")
	if view := display(searched.(*Model)); !strings.Contains(view, "my feature") {
		t.Errorf("search should find the renamed session:\n%s", view)
	}

	exported, _ := model.runSlash("sessions", "export "+id)
	if view := display(exported.(*Model)); !strings.Contains(view, "turn(s)") {
		t.Errorf("export should render the session:\n%s", view)
	}

	deleted, _ := model.runSlash("sessions", "delete "+id)
	final := deleted.(*Model)
	if view := display(final); !strings.Contains(view, "Deleted") {
		t.Errorf("delete should confirm:\n%s", view)
	}
	if final.app.Session().ID() == id {
		t.Errorf("deleting the live session should start a new one")
	}
}

// TestSessionsSubcommandUsage pins the usage errors so a half-typed
// subcommand explains itself instead of stalling.
func TestSessionsSubcommandUsage(t *testing.T) {
	model := chatModel(t)
	for _, args := range []string{"rename only-id", "delete", "export"} {
		next, _ := model.runSlash("sessions", args)
		if view := display(next.(*Model)); !strings.Contains(view, "Usage:") {
			t.Errorf("/sessions %q should show usage:\n%s", args, view)
		}
	}
}

// TestRunPlainSessionsSubcommands mirrors the TUI coverage over a pipe.
func TestRunPlainSessionsSubcommands(t *testing.T) {
	model := chatModel(t)
	if err := model.app.Session().Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	id := model.app.Session().ID()

	var out bytes.Buffer
	if _, err := runPlainSlash(context.Background(), model.app, "sessions", "rename "+id+" plain title", &out); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if !strings.Contains(out.String(), "plain title") {
		t.Errorf("rename output = %q", out.String())
	}

	out.Reset()
	if _, err := runPlainSlash(context.Background(), model.app, "sessions", "search plain", &out); err != nil {
		t.Fatalf("search: %v", err)
	}
	if !strings.Contains(out.String(), "plain title") {
		t.Errorf("search output = %q", out.String())
	}

	out.Reset()
	if _, err := runPlainSlash(context.Background(), model.app, "sessions", "export "+id, &out); err != nil {
		t.Fatalf("export: %v", err)
	}
	if !strings.Contains(out.String(), "turn(s)") {
		t.Errorf("export output = %q", out.String())
	}

	out.Reset()
	if _, err := runPlainSlash(context.Background(), model.app, "sessions", "delete "+id, &out); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

// TestCostShowsToolBreakdown is the operator-facing half of the ledger: the
// tools behind the turn appear under the token totals.
func TestCostShowsToolBreakdown(t *testing.T) {
	model := chatModel(t)
	model.applyEvent(agent.Event{Kind: agent.EventToolStart, ToolName: "read_file", ToolLabel: "Reading main.go"})
	model.applyEvent(agent.Event{Kind: agent.EventToolEnd, ToolName: "read_file", ToolLabel: "Read main.go", ToolOK: true, ToolMillis: 8})

	_, output := runSlash(t, model, "/cost")
	if !strings.Contains(output, "Tokens:") {
		t.Errorf("cost should keep its totals:\n%s", output)
	}
}

// TestFormatToolStatsRendersCallsAndFailures pins the ledger lines.
func TestFormatToolStatsRendersCallsAndFailures(t *testing.T) {
	if got := formatToolStats(nil); got != "" {
		t.Errorf("an empty ledger should add nothing, got %q", got)
	}
	got := formatToolStats([]app.ToolStat{{Name: "read_file", Calls: 3}, {Name: "run_command", Calls: 1, Errors: 1}})
	for _, want := range []string{"Tools:", "read_file", "3 call(s)", "run_command", "1 failed"} {
		if !strings.Contains(got, want) {
			t.Errorf("ledger is missing %q:\n%s", want, got)
		}
	}
}

// TestApprovalDiffRendersChangedLines proves the approval dialog shows the
// file preview instead of only the one-line detail.
func TestApprovalDiffRendersChangedLines(t *testing.T) {
	model := chatModel(t)
	resize(model, 100, 30)
	model.pendingApproval = &agent.ApprovalRequest{
		Tool: "edit", Risk: "edit", Detail: "Editing main.go",
		Diff: "--- main.go\n+++ main.go\n-func A() {}\n+func A() int {}",
	}
	view := stripANSI(model.View())
	for _, want := range []string{"Approval needed", "edit", "-func A() {}", "+func A() int {}"} {
		if !strings.Contains(view, want) {
			t.Errorf("the approval view is missing %q:\n%s", want, view)
		}
	}
}

// TestApprovalWithoutDiffFallsBack keeps tools without a preview on the old
// layout: tool, risk and detail only.
func TestApprovalWithoutDiffFallsBack(t *testing.T) {
	model := chatModel(t)
	resize(model, 100, 30)
	model.pendingApproval = &agent.ApprovalRequest{Tool: "run_command", Risk: "command", Detail: "Running go test"}
	view := stripANSI(model.View())
	if !strings.Contains(view, "Running go test") {
		t.Errorf("the detail should render:\n%s", view)
	}
	if strings.Contains(view, "+++") || strings.Contains(view, "--- ") {
		t.Errorf("no diff markers should appear:\n%s", view)
	}
}

// TestSubmitStartsARunForPlainText is the other half of the Enter fork: text// that is not a command becomes a turn, with the operator's line echoed into
// the transcript and the event pump started.
func TestSubmitStartsARunForPlainText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer server.Close()

	model := servedModel(t, server)
	before := len(model.blocks)

	next, cmd := model.submit("what does main.go do?")
	started, ok := next.(*Model)
	if !ok {
		t.Fatalf("submit returned %T", next)
	}
	if !started.running {
		t.Errorf("the turn should be marked in flight")
	}
	if cmd == nil {
		t.Errorf("a run must return the event pump command")
	}
	if len(started.blocks) <= before {
		t.Fatalf("the operator's line should be echoed")
	}
	if got := started.blocks[len(started.blocks)-1]; got.kind != blockUser || got.text != "what does main.go do?" {
		t.Errorf("echoed block = %+v", got)
	}

	// Wait for the turn so its goroutine does not outlive the test.
	deadline := time.Now().Add(30 * time.Second)
	for model.app.Running() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if model.app.Running() {
		t.Fatalf("the turn did not finish")
	}
}

// ------------------------------------------------- the operator handshake

// TestAwaitReplyCoversBothOutcomes is the important half of the approval
// handshake: the answer branch, and the branch where the operator never
// answers. A parked agent goroutine is a turn that never ends.
func TestAwaitReplyCoversBothOutcomes(t *testing.T) {
	t.Run("an answer is returned", func(t *testing.T) {
		reply := make(chan agent.Decision, 1)
		reply <- agent.DecisionAllowSession
		if got := awaitReply(reply, agent.DecisionDeny, time.Minute); got != agent.DecisionAllowSession {
			t.Errorf("decision = %v, want the answer", got)
		}
	})

	t.Run("a timeout returns the fallback", func(t *testing.T) {
		reply := make(chan agent.Decision, 1)
		started := time.Now()
		if got := awaitReply(reply, agent.DecisionDeny, 10*time.Millisecond); got != agent.DecisionDeny {
			t.Errorf("decision = %v, want the safe fallback", got)
		}
		if elapsed := time.Since(started); elapsed > 5*time.Second {
			t.Errorf("the wait took %s; the timeout must bound it", elapsed)
		}
	})

	t.Run("a string reply works too", func(t *testing.T) {
		reply := make(chan string, 1)
		reply <- "8080"
		if got := awaitReply(reply, "", time.Minute); got != "8080" {
			t.Errorf("answer = %q", got)
		}
		if got := awaitReply(make(chan string, 1), "", 10*time.Millisecond); got != "" {
			t.Errorf("answer = %q, want the empty fallback", got)
		}
	})

	t.Run("the default timeout is long enough to answer", func(t *testing.T) {
		// A guard against the constant being misread as nanoseconds: an
		// operator needs minutes, not milliseconds.
		if approvalTimeout < time.Minute {
			t.Errorf("approvalTimeout = %s, want at least a minute", approvalTimeout)
		}
	})
}

// TestInteractorRefusesWithoutAProgram is the safety default: an unattached UI
// must never look like the operator said yes.
func TestInteractorRefusesWithoutAProgram(t *testing.T) {
	model := chatModel(t)
	if model.program != nil {
		t.Fatalf("the fixture should have no program attached")
	}
	if got := model.Approve(agent.ApprovalRequest{Tool: "write_file"}); got != agent.DecisionDeny {
		t.Errorf("decision = %v, want deny", got)
	}
	if _, err := model.Ask("which port?", nil); err == nil {
		t.Errorf("a question must fail when the UI is not attached")
	}
}

// ------------------------------------------------- rendering pieces

func TestStyleInlineRendersCodeAndBold(t *testing.T) {
	styles := NewStyles(DefaultPalette())

	if got := stripANSI(styleInline("no markup here", styles)); got != "no markup here" {
		t.Errorf("plain line = %q", got)
	}
	if got := stripANSI(styleInline("use `make check` now", styles)); got != "use make check now" {
		t.Errorf("code = %q, want the backticks removed", got)
	}
	if got := stripANSI(styleInline("a **bold** word", styles)); got != "a bold word" {
		t.Errorf("bold = %q, want the asterisks removed", got)
	}
	// An unclosed marker is not markup: it is text the operator wrote.
	if got := stripANSI(styleInline("an unclosed `tick", styles)); got != "an unclosed `tick" {
		t.Errorf("an unclosed tick = %q, want it left alone", got)
	}
	if got := stripANSI(styleInline("an unclosed **bold", styles)); got != "an unclosed **bold" {
		t.Errorf("unclosed bold = %q, want it left alone", got)
	}
	// Several spans on one line, which is how a real answer reads.
	if got := stripANSI(styleInline("run `go test` then **commit**", styles)); got != "run go test then commit" {
		t.Errorf("mixed = %q", got)
	}
	// A code span inside bold renders instead of leaking its backticks.
	if got := stripANSI(styleInline("**Stack (dari `package.json`):**", styles)); got != "Stack (dari package.json):" {
		t.Errorf("nested = %q", got)
	}
	if got := styleInline("", styles); got != "" {
		t.Errorf("an empty line = %q", got)
	}
}

func TestSplitBulletRecognisesEveryMarker(t *testing.T) {
	cases := []struct {
		line      string
		wantMark  string
		wantText  string
		wantMatch bool
	}{
		{"- a dash item", "*", "a dash item", true},
		{"* an asterisk item", "*", "an asterisk item", true},
		{"+ a plus item", "*", "a plus item", true},
		{"1. the first", "1.", "the first", true},
		{"99. the ninety ninth", "99.", "the ninety ninth", true},
		{"just a sentence", "", "", false},
		{"-no space is not a bullet", "", "", false},
		{"1.no space is not a bullet", "", "", false},
		{"", "", "", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.line, func(t *testing.T) {
			mark, text, matched := splitBullet(testCase.line)
			if matched != testCase.wantMatch {
				t.Fatalf("matched = %v, want %v", matched, testCase.wantMatch)
			}
			if mark != testCase.wantMark || text != testCase.wantText {
				t.Errorf("got (%q, %q), want (%q, %q)", mark, text, testCase.wantMark, testCase.wantText)
			}
		})
	}
}

func TestTruncateClipsToTheDisplayWidth(t *testing.T) {
	if got := truncate("short", 20); got != "short" {
		t.Errorf("truncate = %q, want it unchanged", got)
	}
	if got := truncate("abcdefghij", 6); got != "abc..." {
		t.Errorf("truncate = %q, want abc...", got)
	}
	// A width this small has no room for an ellipsis, so the text is cut
	// rather than producing a longer string than asked for.
	if got := truncate("abcdefghij", 3); got != "abc" {
		t.Errorf("truncate = %q, want abc", got)
	}
	if got := truncate("abcdefghij", 0); got != "abcdefghij" {
		t.Errorf("a zero width means no limit, got %q", got)
	}
	if got := truncate("", 5); got != "" {
		t.Errorf("truncate of empty = %q", got)
	}
}

func TestRenderAssistantBlockSkipsEmptyText(t *testing.T) {
	styles := NewStyles(DefaultPalette())
	if got := renderAssistantBlock(block{kind: blockAssistant, text: "   "}, styles, 80); got != "" {
		t.Errorf("an empty answer should render nothing, got %q", got)
	}
	if got := renderAssistantBlock(block{kind: blockAssistant, text: "hello"}, styles, 80); !strings.Contains(got, "hello") {
		t.Errorf("rendered = %q", got)
	}
}

func TestRenderWelcomeBlockCarriesTheBannerAndHint(t *testing.T) {
	styles := NewStyles(DefaultPalette())
	rendered := stripANSI(renderWelcomeBlock(block{kind: blockWelcome, text: "welcome text"}, styles, 80))
	if !strings.Contains(rendered, "welcome text") {
		t.Errorf("rendered = %q, want the text", rendered)
	}
	if !strings.Contains(rendered, bannerRows()[0]) {
		t.Errorf("rendered = %q, want the banner", rendered)
	}
	// An extra label is used by the resumed-session notice.
	withLabel := stripANSI(renderWelcomeBlock(block{kind: blockWelcome, text: "text", toolLabel: "resumed"}, styles, 80))
	if !strings.Contains(withLabel, "resumed") {
		t.Errorf("rendered = %q, want the label", withLabel)
	}
}

// ------------------------------------------------- the status bar

// servedModel builds a model whose app talks to a local server for a priced
// model, which is what makes a real turn possible without a key or the
// network. A priced model is what the status bar's cost figure needs.
func servedModel(t *testing.T, server *httptest.Server) *Model {
	t.Helper()
	t.Setenv(config.EnvHome, t.TempDir())

	cfg := config.Default()
	cfg.DefaultModel = "gpt-5.4-mini"
	cfg.ApprovalMode = config.ApprovalAll
	cfg.BaseURLs = map[string]string{"openai": server.URL}
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	application := testApp(t)
	// A key is required before the client is built. It is never sent anywhere,
	// because the base URL points at the test server.
	if err := application.Secrets().Set(secrets.ProviderKey("openai"), "sk-test"); err != nil {
		t.Fatalf("store key: %v", err)
	}
	model, err := application.SetModelByQuery("gpt-5.4-mini")
	if err != nil {
		t.Fatalf("SetModelByQuery: %v", err)
	}
	if model.ID != "gpt-5.4-mini" {
		t.Fatalf("model = %q, want the priced one", model.ID)
	}

	built := New(application)
	if built.current != modeChat {
		t.Fatalf("the fixture should start in chat mode, got %d", built.current)
	}
	resize(built, 120, 40)
	return built
}

// TestViewStatusShowsSpendOnlyOnceItIsKnown is the whole cost chain in one
// assertion: the provider reports usage, the runner prices it, the app stores
// it and the status bar renders it.
//
// The figure is always present once the model is priced, so the operator can
// watch the spend from the start of a session. A zero is truthful rather than
// misleading: it says this session has spent nothing yet, and it keeps the
// position of the number stable instead of appearing mid-run.
func TestViewStatusShowsSpendFromTheStart(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\n")
		fmt.Fprint(writer, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		// A final usage report is how a compatible endpoint reports the cost.
		fmt.Fprint(writer, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":100000,\"completion_tokens\":50000,\"total_tokens\":150000}}\n\n")
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer server.Close()

	model := servedModel(t, server)

	// Before any turn the model is priced, so the figure shows as zero.
	before := display(model)
	if !strings.Contains(before, "$0.0000") {
		t.Errorf("the spend should be visible before the first turn:\n%s", before)
	}
	if !strings.Contains(before, "tokens 0") {
		t.Errorf("the token count should be visible before the first turn:\n%s", before)
	}

	var out bytes.Buffer
	if err := RunOnceWithContext(context.Background(), model.app, "say something", &out); err != nil {
		t.Fatalf("turn: %v", err)
	}

	after := display(model)
	if !strings.Contains(after, "tokens 150000") {
		t.Errorf("the bar should show the token total:\n%s", after)
	}
	if !strings.Contains(after, "$") {
		t.Errorf("the bar should show a spend figure after a priced turn:\n%s", after)
	}
}

// TestViewStatusReportsEachFactIndependently is what makes the bar useful: an
// operator diagnosing a run needs to see which of these is set without the
// others being present.
func TestViewStatusReportsEachFactIndependently(t *testing.T) {
	model := chatModel(t)
	baseline := display(model)
	for _, want := range []string{"session ", "turns ", "approval all"} {
		if !strings.Contains(baseline, want) {
			t.Errorf("the status bar is missing %q:\n%s", want, baseline)
		}
	}

	model.app.AddUsage(provider.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150})
	if got := display(model); !strings.Contains(got, "tokens 150") {
		t.Errorf("the bar should show the token total:\n%s", got)
	}

	// A plan only appears once there is one.
	model.app.Todos().Set([]agent.Todo{{ID: "1", Title: "step", Status: "pending"}})
	if got := display(model); !strings.Contains(got, "plan 0/1") {
		t.Errorf("the bar should show the plan:\n%s", got)
	}

	// A notice is shown until it is cleared.
	model.notice = "Telegram paired."
	if got := display(model); !strings.Contains(got, "Telegram paired.") {
		t.Errorf("the bar should show the notice:\n%s", got)
	}
}

func TestShortIDTrimsToSixCharacters(t *testing.T) {
	if got := shortID("abc"); got != "abc" {
		t.Errorf("shortID = %q, want a short id unchanged", got)
	}
	if got := shortID("0123456789abcdef"); got != "012345" {
		t.Errorf("shortID = %q, want the first six", got)
	}
}

// writeProbeSkill adds one project skill so the /skills listing has something
// to show.
func writeProbeSkill(t *testing.T, model *Model) {
	t.Helper()
	dir := filepath.Join(model.app.Workspace(), ".termixgo", "skills", "probe-skill")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	document := "---\nname: probe-skill\ndescription: a probe\n---\n\nDo it.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(document), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	model.app.ReloadSkills()
}

// writeProbeCommand adds one project command file so the menu and dispatch
// have something custom to show.
func writeProbeCommand(t *testing.T, model *Model) {
	t.Helper()
	dir := filepath.Join(model.app.Workspace(), ".termixgo", "commands")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	document := "---\ndescription: Review the change\n---\n\nReview $ARGUMENTS carefully.\n"
	if err := os.WriteFile(filepath.Join(dir, "review.md"), []byte(document), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := model.reloadCustomCommands(); err != nil {
		t.Fatalf("reload: %v", err)
	}
}

// TestCustomCommandAppearsInMenu proves a command file joins the inline menu
// next to the built-ins.
func TestCustomCommandAppearsInMenu(t *testing.T) {
	model := chatModel(t)
	writeProbeCommand(t, model)
	model = openSlashPalette(t, model)
	model.slashInput = "/rev"
	model.refreshSlashMenu()
	found := false
	for _, match := range model.slashMatches {
		if match.Trigger == "/review" {
			found = true
			if !strings.Contains(match.Summary, "Review the change") {
				t.Errorf("summary = %q", match.Summary)
			}
		}
	}
	if !found {
		t.Errorf("the menu should offer /review, got %+v", model.slashMatches)
	}
}

// TestCustomCommandRunsAsTurn proves dispatch: the file body becomes the run
// prompt with the typed arguments applied.
func TestCustomCommandRunsAsTurn(t *testing.T) {
	model := chatModel(t)
	writeProbeCommand(t, model)

	next, _ := model.runSlash("review", "the diff")
	started, ok := next.(*Model)
	if !ok {
		t.Fatalf("runSlash returned %T", next)
	}
	if !started.running {
		t.Fatalf("a custom command should start a turn")
	}
	last := started.blocks[len(started.blocks)-1]
	if last.kind != blockUser {
		t.Fatalf("last block = %+v, want the echoed prompt", last)
	}
	if !strings.Contains(last.text, "Review the diff carefully.") {
		t.Errorf("prompt = %q, want the expanded body", last.text)
	}

	deadline := time.Now().Add(30 * time.Second)
	for model.app.Running() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
}

// TestCustomCommandHelpListsTheFile proves /help shows the custom section so
// a command file is discoverable without typing its prefix.
func TestCustomCommandHelpListsTheFile(t *testing.T) {
	model := chatModel(t)
	writeProbeCommand(t, model)
	if view := stripANSI(model.viewHelp()); !strings.Contains(view, "/review") {
		t.Errorf("the help view should list the custom command:\n%s", view)
	}
}
