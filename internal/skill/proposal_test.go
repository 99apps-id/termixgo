package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProposeCreatesAndApplies(t *testing.T) {
	workspace := t.TempDir()
	proposal, err := Propose(workspace, "Release Notes", "Write release notes.", "# Release Notes\n\nSummarise the change.", "add a release skill")
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if proposal.Action != ProposalCreate || proposal.Status != "pending" {
		t.Fatalf("proposal = %+v", proposal)
	}
	if !strings.Contains(proposal.Summary, "release skill") {
		t.Errorf("summary = %q", proposal.Summary)
	}

	before, err := ListProposals(workspace)
	if err != nil || len(before) != 1 {
		t.Fatalf("ListProposals = %v, %v", before, err)
	}

	created, err := ApplyProposal(workspace, proposal.ID)
	if err != nil {
		t.Fatalf("ApplyProposal: %v", err)
	}
	if created.Name != "release-notes" {
		t.Errorf("created name = %q", created.Name)
	}
	if _, err := os.Stat(filepath.Join(ProjectDir(workspace), "release-notes", "SKILL.md")); err != nil {
		t.Errorf("the skill file is missing: %v", err)
	}
	if after, _ := ListProposals(workspace); len(after) != 0 {
		t.Errorf("an applied proposal should leave the queue, got %v", after)
	}
}

func TestProposeUpdateDetectsAChangedTarget(t *testing.T) {
	workspace := t.TempDir()
	if _, err := Create(workspace, "demo", "first"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	proposal, err := Propose(workspace, "demo", "second", "a better body", "")
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if proposal.Action != ProposalUpdate {
		t.Fatalf("action = %q, want update", proposal.Action)
	}

	// The target changes after the proposal, so applying it would clobber.
	document := filepath.Join(ProjectDir(workspace), "demo", "SKILL.md")
	if err := os.WriteFile(document, []byte("---\nname: demo\ndescription: moved on\n---\n\nnewer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyProposal(workspace, proposal.ID); err == nil {
		t.Fatalf("applying a stale proposal should fail")
	}
	stale, err := GetProposal(workspace, proposal.ID)
	if err != nil {
		t.Fatalf("GetProposal: %v", err)
	}
	if stale.Status != ProposalStatusStale {
		t.Errorf("status = %q, want stale", stale.Status)
	}
}

func TestProposeRejectsBadInput(t *testing.T) {
	workspace := t.TempDir()
	cases := []struct {
		name, description, body string
	}{
		{"", "d", "body"},
		{"demo", "d", ""},
		{"demo", "d", strings.Repeat("x", maxProposalBytes+1)},
		{"demo", "d", "bad\x00byte"},
	}
	for _, testCase := range cases {
		if _, err := Propose(workspace, testCase.name, testCase.description, testCase.body, ""); err == nil {
			t.Errorf("Propose(%q, len=%d) should fail", testCase.name, len(testCase.body))
		}
	}
}

func TestRejectProposalDropsIt(t *testing.T) {
	workspace := t.TempDir()
	proposal, err := Propose(workspace, "temp", "d", "body", "")
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if err := RejectProposal(workspace, proposal.ID); err != nil {
		t.Fatalf("RejectProposal: %v", err)
	}
	if proposals, _ := ListProposals(workspace); len(proposals) != 0 {
		t.Errorf("the queue should be empty, got %v", proposals)
	}
}
