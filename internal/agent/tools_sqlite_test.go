package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestSQLiteQueryToolCreateInsertSelect(t *testing.T) {
	workspace := t.TempDir()
	env := &Env{Workspace: workspace, Trusted: true}
	tool := &sqliteQueryTool{}

	dbPath := "test.db"

	// 1. Create table
	res, err := tool.Run(context.Background(), env, map[string]any{
		"path":  dbPath,
		"query": "CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT, email TEXT);",
	})
	if err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if res.IsError {
		t.Fatalf("CREATE TABLE error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "Statement executed successfully") {
		t.Errorf("expected success message, got: %s", res.Output)
	}

	// 2. Insert rows
	res, err = tool.Run(context.Background(), env, map[string]any{
		"path":  dbPath,
		"query": "INSERT INTO users (name, email) VALUES ('Alice', 'alice@example.com'), ('Bob', 'bob@example.com');",
	})
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	if res.IsError {
		t.Fatalf("INSERT error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "Rows affected: 2") {
		t.Errorf("expected 2 rows affected, got: %s", res.Output)
	}

	// 3. SELECT rows
	res, err = tool.Run(context.Background(), env, map[string]any{
		"path":  dbPath,
		"query": "SELECT id, name, email FROM users ORDER BY id;",
	})
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if res.IsError {
		t.Fatalf("SELECT error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "| id | name | email |") {
		t.Errorf("expected table header, got: %s", res.Output)
	}
	if !strings.Contains(res.Output, "| 1 | Alice | alice@example.com |") {
		t.Errorf("expected Alice in output, got: %s", res.Output)
	}
	if !strings.Contains(res.Output, "| 2 | Bob | bob@example.com |") {
		t.Errorf("expected Bob in output, got: %s", res.Output)
	}
}

func TestSQLiteQueryToolReadOnlyEnforced(t *testing.T) {
	workspace := t.TempDir()
	env := &Env{Workspace: workspace, Trusted: true}
	tool := &sqliteQueryTool{}

	dbPath := "readonly.db"

	// Create table first
	_, err := tool.Run(context.Background(), env, map[string]any{
		"path":  dbPath,
		"query": "CREATE TABLE items (id INT);",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Try inserting with readonly=true
	res, err := tool.Run(context.Background(), env, map[string]any{
		"path":     dbPath,
		"query":    "INSERT INTO items VALUES (1);",
		"readonly": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Errorf("expected readonly mutation to fail, got output: %s", res.Output)
	}
}

func TestSQLiteQueryToolPathEscape(t *testing.T) {
	workspace := t.TempDir()
	env := &Env{Workspace: workspace, Trusted: true}
	tool := &sqliteQueryTool{}

	res, err := tool.Run(context.Background(), env, map[string]any{
		"path":  filepath.Join("..", "outside.db"),
		"query": "SELECT 1;",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Errorf("expected path escape to be rejected, got: %s", res.Output)
	}
}

func TestSQLiteQueryToolMaxRowsTruncation(t *testing.T) {
	workspace := t.TempDir()
	env := &Env{Workspace: workspace, Trusted: true}
	tool := &sqliteQueryTool{}

	dbPath := "trunc.db"
	_, _ = tool.Run(context.Background(), env, map[string]any{
		"path":  dbPath,
		"query": "CREATE TABLE numbers (n INT);",
	})
	_, _ = tool.Run(context.Background(), env, map[string]any{
		"path":  dbPath,
		"query": "INSERT INTO numbers VALUES (1), (2), (3), (4), (5);",
	})

	res, err := tool.Run(context.Background(), env, map[string]any{
		"path":     dbPath,
		"query":    "SELECT n FROM numbers ORDER BY n;",
		"max_rows": 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "truncated at 3 rows") {
		t.Errorf("expected truncation note, got: %s", res.Output)
	}
}
