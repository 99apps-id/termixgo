//go:build windows

package agent

import (
	"os/exec"
	"slices"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// consoleProcesses lists the processes attached to this process's console.
func consoleProcesses() []uint32 {
	list := make([]uint32, 256)
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleProcessList")
	count, _, _ := proc.Call(uintptr(unsafe.Pointer(&list[0])), uintptr(len(list)))
	if int(count) > len(list) {
		return nil
	}
	return list[:count]
}

// A tool process that shares the operator's console can read its keystrokes or
// change its input mode, which left Enter dead in the composer during a turn.
func TestToolProcessesDoNotShareTheConsole(t *testing.T) {
	if len(consoleProcesses()) == 0 {
		t.Skip("this test run has no console")
	}
	cmd := exec.Command("cmd.exe", "/c", "ping -n 10 127.0.0.1 >NUL")
	tree, err := newProcessTree()
	if err != nil {
		t.Fatal(err)
	}
	defer tree.release()
	tree.prepare(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Wait() }()
	defer func() { _ = cmd.Process.Kill() }()
	if err := tree.attach(cmd); err != nil {
		t.Fatal(err)
	}

	time.Sleep(500 * time.Millisecond)
	if slices.Contains(consoleProcesses(), uint32(cmd.Process.Pid)) {
		t.Fatal("the tool process is attached to the operator's console")
	}
}
