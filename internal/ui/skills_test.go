package ui

import (
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/skill"
)

// TestSkillProposalReviewFlow drives the operator side of the workshop: a
// staged proposal is listed, applied, and a rejected one leaves the queue.
func TestSkillProposalReviewFlow(t *testing.T) {
	model := chatModel(t)
	proposal, err := skill.Propose(model.app.Workspace(), "demo", "A demo skill.", "Do the thing.", "because it repeats")
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}

	if _, out := runSlash(t, model, "/skills proposals"); !strings.Contains(out, proposal.ID) || !strings.Contains(out, "demo") {
		t.Fatalf("proposals output = %q", out)
	}
	if _, out := runSlash(t, model, "/skills apply "+proposal.ID); !strings.Contains(out, "Applied skill demo") {
		t.Fatalf("apply output = %q", out)
	}
	if proposals, _ := model.app.SkillProposals(); len(proposals) != 0 {
		t.Errorf("the applied proposal should leave the queue, got %v", proposals)
	}

	second, err := skill.Propose(model.app.Workspace(), "temp", "d", "body", "")
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if _, out := runSlash(t, model, "/skills reject "+second.ID); !strings.Contains(out, "Rejected") {
		t.Fatalf("reject output = %q", out)
	}
	if proposals, _ := model.app.SkillProposals(); len(proposals) != 0 {
		t.Errorf("a rejected proposal should leave the queue, got %v", proposals)
	}
}
