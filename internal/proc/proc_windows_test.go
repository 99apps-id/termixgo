//go:build windows

package proc

import (
	"os/exec"
	"testing"
)

func TestDetachGivesTheChildItsOwnHiddenConsole(t *testing.T) {
	cmd := exec.Command("cmd", "/c", "exit")
	Detach(cmd)
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&createNoWindow == 0 || !cmd.SysProcAttr.HideWindow {
		t.Fatalf("SysProcAttr = %+v, want CREATE_NO_WINDOW and HideWindow", cmd.SysProcAttr)
	}
}
