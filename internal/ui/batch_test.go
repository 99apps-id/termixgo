package ui

import (
	"strings"
	"testing"
)

func TestSlashBatchNeedsTasks(t *testing.T) {
	model := chatModel(t)
	next, _ := model.runSlash("batch", "   ")
	started, ok := next.(*Model)
	if !ok {
		t.Fatalf("runSlash returned %T", next)
	}
	last := started.blocks[len(started.blocks)-1]
	if last.kind != blockError || !strings.Contains(last.text, "/batch") {
		t.Errorf("empty batch should show usage, got %+v", last)
	}
}

func TestSlashWorkersShowsNotice(t *testing.T) {
	model := chatModel(t)
	next, _ := model.runSlash("workers", "")
	started, ok := next.(*Model)
	if !ok {
		t.Fatalf("runSlash returned %T", next)
	}
	last := started.blocks[len(started.blocks)-1]
	if last.kind != blockNotice {
		t.Fatalf("last block = %+v, want a notice", last)
	}
	if !strings.Contains(strings.ToLower(last.text), "worktree") {
		t.Errorf("workers view should mention worktrees, got %q", last.text)
	}
}

func TestSlashDiffCatalogueOffersLayouts(t *testing.T) {
	options := SlashOptions("diff")
	found := map[string]bool{}
	for _, option := range options {
		found[option.Value] = true
	}
	if !found["side"] || !found["unified"] {
		t.Errorf("diff options = %+v, want side and unified", options)
	}
}
