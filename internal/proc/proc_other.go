//go:build !windows

// Package proc holds how child processes are started.
package proc

import "os/exec"

// Detach is a Windows concern: elsewhere a child has no console to share.
func Detach(cmd *exec.Cmd) {}
