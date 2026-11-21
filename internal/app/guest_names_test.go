package app

import (
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
)

// Every guest-denied name must be a real tool name or alias. Without names
// the deny-list silently stopped covering run_logs, run_wait, run_list,
// run_kill and run_subagent after those tools were renamed.
func TestGuestDeniedNamesAreRealTools(t *testing.T) {
	known := map[string]bool{}
	for _, tool := range agent.DefaultRegistry().Tools() {
		known[strings.ToLower(tool.Name())] = true
		for _, alias := range tool.Aliases() {
			known[strings.ToLower(alias)] = true
		}
	}
	for _, name := range guestDeniedTools {
		if !known[name] {
			t.Errorf("guest deny-list names %q, which is no tool", name)
		}
	}
}

func TestGuestCannotDelegateOrTouchProcesses(t *testing.T) {
	application := newTestApp(t)
	offered := map[string]bool{}
	for _, tool := range application.toolsFor("", true).Tools() {
		offered[tool.Name()] = true
	}
	for _, name := range []string{"run_subagent", "send_subagent", "run_logs", "run_kill", "run_monitor", "schedule_task", "ssh_run_command", "dev_server"} {
		if offered[name] {
			t.Errorf("a guest is offered %s", name)
		}
	}
	if !offered["read_file"] {
		t.Errorf("a guest keeps the read tools")
	}
}
