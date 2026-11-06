package agent

import (
	"context"
	"path/filepath"
	"testing"
)

func TestTodoUpdateTool(t *testing.T) {
	store := NewTodoStore()
	store.Set([]Todo{
		{ID: "a", Title: "first", Status: "pending"},
		{ID: "b", Title: "second", Status: "pending"},
	})
	env := &Env{Todos: store}
	ctx := context.Background()

	res, err := (&todoUpdateTool{}).Run(ctx, env, map[string]any{"id": "a", "status": "completed"})
	if err != nil || res.IsError {
		t.Fatalf("mark complete failed: err=%v out=%q", err, res.Output)
	}
	if got := store.Items()[0].Status; got != "completed" {
		t.Fatalf("status = %q, want completed", got)
	}

	if _, err := (&todoUpdateTool{}).Run(ctx, env, map[string]any{"id": "b", "title": "renamed"}); err != nil {
		t.Fatalf("retitle: %v", err)
	}
	if got := store.Items()[1].Title; got != "renamed" {
		t.Fatalf("title = %q, want renamed", got)
	}

	if res, _ := (&todoUpdateTool{}).Run(ctx, env, map[string]any{"id": "a", "status": "bogus"}); !res.IsError {
		t.Fatal("expected error for invalid status")
	}
	if res, _ := (&todoUpdateTool{}).Run(ctx, env, map[string]any{"id": "zzz", "status": "completed"}); !res.IsError {
		t.Fatal("expected error for unknown id")
	}
	if res, _ := (&todoUpdateTool{}).Run(ctx, &Env{}, map[string]any{"id": "a", "status": "completed"}); !res.IsError {
		t.Fatal("expected error when Todos store is nil")
	}
}

func TestPolicyStoreLoadSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policies.txt")

	s := &policyStore{path: path}
	s.policies = []string{"never commit secrets", "use parameterized queries"}
	if err := s.save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	s2 := &policyStore{path: path}
	if err := s2.load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(s2.policies) != 2 || s2.policies[0] != "never commit secrets" {
		t.Fatalf("loaded policies = %v", s2.policies)
	}

	s3 := &policyStore{path: filepath.Join(dir, "missing.txt")}
	if err := s3.load(); err != nil {
		t.Fatalf("load missing: %v", err)
	}
	if len(s3.policies) != 0 {
		t.Fatalf("expected no policies for missing file, got %v", s3.policies)
	}
}

func TestReadPDFToolRejectsBadInput(t *testing.T) {
	env := &Env{Workspace: t.TempDir()}
	if res, _ := (&readPDFTool{}).Run(context.Background(), env, map[string]any{}); !res.IsError {
		t.Fatal("expected error for missing path")
	}
	if res, _ := (&readPDFTool{}).Run(context.Background(), env, map[string]any{"path": "../outside.pdf"}); !res.IsError {
		t.Fatal("expected error for path escaping the workspace")
	}
}
