//go:build windows

// Package proc holds how child processes are started.
package proc

import (
	"os/exec"
	"syscall"
)

// createNoWindow is CREATE_NO_WINDOW: the child gets a console of its own that
// is never shown.
const createNoWindow = 0x08000000

// Detach gives a child process its own hidden console. A child that shares this
// program's console can read the operator's keystrokes, change the input mode
// (Enter then stops working in the composer) or act on the window itself:
// powershell -WindowStyle Hidden minimizes Windows Terminal.
func Detach(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
