package agent

import "testing"

// TestFolderGateHonoursTheOperatorsOwnAnswer is the bug the gate had: it
// re-asked every mutating call in an untrusted folder, so "allow session"
// and "always" answered at that very prompt meant nothing and the operator
// saw the same dialog every turn. The answer must silence the gate for the
// tool it covered - and only for that tool.
func TestFolderGateHonoursTheOperatorsOwnAnswer(t *testing.T) {
	runner, env, _ := gateEnv(t, false)
	writeTool, ok := DefaultRegistry().Lookup("write_file")
	if !ok {
		t.Fatal("write_file missing from the registry")
	}
	deleteTool, _ := DefaultRegistry().Lookup("delete_file")

	if !runner.needsApprovalFor(writeTool) {
		t.Fatal("an untrusted folder must ask the first time")
	}
	env.SessionAllowed = map[string]bool{"write_file": true}
	if runner.needsApprovalFor(writeTool) {
		t.Error(`the operator answered this gate with "session"; the re-prompt must stop`)
	}
	if !runner.needsApprovalFor(deleteTool) {
		t.Error("the grant covers write_file only; delete_file must still ask")
	}
}

// TestFolderGateStillIgnoresToolLevelPolicy is the half the security design
// keeps: a tool-level allowance from another context does not unlock writes
// in a folder nobody vouched for. Only the folder's own prompt can.
func TestFolderGateStillIgnoresToolLevelPolicy(t *testing.T) {
	policy := &ApprovalPolicy{
		Mode:           ApprovalAll,
		AlwaysAllowed:  map[string]bool{"write_file": true},
		SessionAllowed: map[string]bool{"edit": true},
	}
	runner, env, _ := newTestRunner(t, &fakeClient{}, policy, nil)
	env.Trusted = false
	for _, name := range []string{"write_file", "edit"} {
		tool, ok := DefaultRegistry().Lookup(name)
		if !ok {
			t.Fatalf("%s missing", name)
		}
		if !runner.needsApprovalFor(tool) {
			t.Errorf("%s: a tool-level allowance must not clear the folder gate", name)
		}
	}
}
