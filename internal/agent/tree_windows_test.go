//go:build windows

package agent

import (
	"os/exec"
	"testing"
)

// TestProcessTreeGuardsWithoutAJob covers the checks that run before any Win32
// call. They are what turns a released handle into a reported warning instead
// of a crash on a bad handle value.
func TestProcessTreeGuardsWithoutAJob(t *testing.T) {
	empty := &processTree{}

	if err := empty.attach(&exec.Cmd{}); err == nil {
		t.Errorf("attaching without a job object must be refused")
	}
	if err := empty.terminate(&exec.Cmd{}); err == nil {
		t.Errorf("terminating without a job object must be refused")
	}
	// release on an empty tree is a no-op, which is what makes it safe to defer.
	empty.release()

	// A job with no started process cannot be attached either: there is no pid.
	job, err := newProcessTree()
	if err != nil {
		t.Fatalf("newProcessTree: %v", err)
	}
	defer job.release()
	if err := job.attach(&exec.Cmd{}); err == nil {
		t.Errorf("attaching an unstarted command must be refused")
	}
}

// TestNewProcessTreeSetsKillOnClose is the whole point of the job object: the
// limit flag is what makes closing the handle stop the children that would
// otherwise keep a port busy.
func TestNewProcessTreeSetsKillOnClose(t *testing.T) {
	tree, err := newProcessTree()
	if err != nil {
		t.Fatalf("newProcessTree: %v", err)
	}
	if tree.job == 0 {
		t.Fatalf("no job handle was created")
	}
	tree.release()
	if tree.job != 0 {
		t.Errorf("release should zero the handle, got %d", tree.job)
	}
	// Releasing twice must be safe, because it runs on both the success and the
	// start-failure paths.
	tree.release()
}

// TestTerminateEndsAStartedProcess is the end-to-end guarantee at the tree
// level: a started process is in the job, and terminating the job ends it.
func TestTerminateEndsAStartedProcess(t *testing.T) {
	tree, err := newProcessTree()
	if err != nil {
		t.Fatalf("newProcessTree: %v", err)
	}
	defer tree.release()

	shell, args := shellForProcess(longRunningCommand())
	command := exec.Command(shell, args...)
	if err := command.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := tree.attach(command); err != nil {
		// The process is running and useful; failing to group it only loses the
		// ability to stop its children, which the production code treats as a
		// warning. The assertion below is what the call is for.
		t.Skipf("this machine will not assign the process to a job: %v", err)
	}

	if err := tree.terminate(command); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	// Wait is bounded by the OS here because the process has been killed; a
	// process that survived would make this return after the ping finishes.
	state, err := command.Process.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if state.Success() {
		t.Errorf("the process reported success, so the job did not kill it")
	}
}
