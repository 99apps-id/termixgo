//go:build windows

package ui

import (
	"os/exec"
	"syscall"
)

// createNoWindow is CREATE_NO_WINDOW: the child gets a console of its own that
// is never shown.
const createNoWindow = 0x08000000

// detachFromConsole gives a helper process its own hidden console. Without it
// the child shares this program's console, and a helper that hides or minimizes
// "its" window (powershell -WindowStyle Hidden does) acts on the terminal the
// operator is typing in: Windows Terminal minimizes, which is what happened at
// the end of every turn long enough to send a notification.
func detachFromConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}
