//go:build !windows

package ui

import "os/exec"

// detachFromConsole is a Windows concern: elsewhere a helper process has no
// console window to share.
func detachFromConsole(cmd *exec.Cmd) {}
