package config

import (
	"path/filepath"
	"testing"
)

func TestFolderAllowRoundTripAndInheritance(t *testing.T) {
	root := filepath.Join(t.TempDir(), "proj")
	child := filepath.Join(root, "src", "deep")
	var cfg Config
	if cfg.AllowedInFolder(root, "edit") {
		t.Fatal("a fresh config grants nothing")
	}
	cfg.AllowInFolder(root, "edit")
	if !cfg.AllowedInFolder(root, "edit") {
		t.Error("the folder answer must survive")
	}
	if !cfg.AllowedInFolder(child, "edit") {
		t.Error("a parent folder's answer covers nested dirs, like trust does")
	}
	if cfg.AllowedInFolder(child, "run_command") {
		t.Error("grants are per tool")
	}
	cfg.AllowInFolder(root, "edit")
	if got := len(cfg.ToolsAllowedInFolder(root)); got != 1 {
		t.Errorf("repeating the answer must not duplicate: %v", cfg.FolderAllowedTools)
	}
	other := filepath.Join(t.TempDir(), "elsewhere")
	if cfg.AllowedInFolder(other, "edit") {
		t.Error("a folder grant must not leak to unrelated folders")
	}
	cfg.AllowInFolder("  ", "edit")
	cfg.AllowInFolder(root, "  ")
	if len(cfg.FolderAllowedTools) != 1 {
		t.Errorf("blank inputs must be refused, got %v", cfg.FolderAllowedTools)
	}
}
