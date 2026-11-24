//go:build windows

package ui

import (
	"os/exec"
	"testing"
)

// A helper started for a notification must not share the terminal's console.
func TestDetachFromConsoleGivesTheHelperItsOwnHiddenConsole(t *testing.T) {
	cmd := exec.Command("cmd", "/c", "exit")
	detachFromConsole(cmd)
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&createNoWindow == 0 || !cmd.SysProcAttr.HideWindow {
		t.Fatalf("SysProcAttr = %+v, want CREATE_NO_WINDOW and HideWindow", cmd.SysProcAttr)
	}
}
