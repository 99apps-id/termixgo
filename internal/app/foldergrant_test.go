package app

import (
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/config"
)

// TestFolderGrantsReachTheNextTurn is the cross-turn half of the fix: the
// trust-gate answer must still be readable when the next turn builds a fresh
// Env, and the persisted half must survive a config reload.
func TestFolderGrantsReachTheNextTurn(t *testing.T) {
	application := newTestApp(t)
	application.noteFolderDecision("edit", agent.DecisionAllowSession)
	if !application.folderGrants()["edit"] {
		t.Error(`the "session" answer must reach the next turn's Env`)
	}
	application.noteFolderDecision("write_file", agent.DecisionAllowAlways)
	grants := application.folderGrants()
	if !grants["write_file"] {
		t.Error(`the "always" answer must reach the gate`)
	}
	if grants["run_command"] {
		t.Error("grants stay per tool")
	}
	reloaded, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reloaded.AllowedInFolder(application.Workspace(), "write_file") {
		t.Error(`the "always" answer must persist under this folder`)
	}
	if reloaded.AllowedInFolder(application.Workspace(), "edit") {
		t.Error(`a "session" answer must NOT persist to disk`)
	}
}

// TestFolderGrantsIgnoreDeny keeps the recording honest: refusing a call is
// not a grant, and a folder where every call was denied must stay quiet in
// the grant list.
func TestFolderGrantsIgnoreDeny(t *testing.T) {
	application := newTestApp(t)
	application.noteFolderDecision("delete_file", agent.DecisionDeny)
	if application.folderGrants()["delete_file"] {
		t.Error("a denial recorded a grant")
	}
}
