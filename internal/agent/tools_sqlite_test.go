package agent

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestSQLiteQueryToolSelectsAnExistingDatabase(t *testing.T) {
	workspace := t.TempDir()
	env := &Env{Workspace: workspace, Trusted: true}
	tool := &sqliteQueryTool{}

	dbPath := "test.db"
	seed, err := sql.Open("sqlite", filepath.Join(workspace, dbPath))
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	if _, err := seed.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT, email TEXT);
		INSERT INTO users (name, email) VALUES ('Alice', 'alice@example.com'), ('Bob', 'bob@example.com');`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	seed.Close()

	res, err := tool.Run(context.Background(), env, map[string]any{
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

func TestSQLiteQueryToolRefusesWrites(t *testing.T) {
	workspace := t.TempDir()
	env := &Env{Workspace: workspace, Trusted: true}
	tool := &sqliteQueryTool{}

	dbPath := filepath.Join(workspace, "readonly.db")
	seed, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec("CREATE TABLE items (id INT)"); err != nil {
		t.Fatal(err)
	}
	seed.Close()

	for _, query := range []string{
		"INSERT INTO items VALUES (1)",
		"PRAGMA query_only = OFF; INSERT INTO items VALUES (2)",
		"WITH x AS (SELECT 1) INSERT INTO items VALUES (3)",
		"ATTACH DATABASE 'other.db' AS other",
		"SELECT 1; DROP TABLE items",
	} {
		res, err := tool.Run(context.Background(), env, map[string]any{
			"path":  "readonly.db",
			"query": query,
		})
		if err != nil {
			t.Fatalf("%q: %v", query, err)
		}
		if !res.IsError {
			t.Errorf("%q should be refused, got: %s", query, res.Output)
		}
	}

	// A refused statement must not have written, and must not have created a
	// second database next to the one it was pointed at.
	check, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var count int
	if err := check.QueryRow("SELECT COUNT(*) FROM items").Scan(&count); err != nil {
		t.Fatalf("table should still exist: %v", err)
	}
	if count != 0 {
		t.Errorf("refused statements wrote %d row(s)", count)
	}
	if _, err := os.Stat(filepath.Join(workspace, "other.db")); !os.IsNotExist(err) {
		t.Errorf("ATTACH created a second database: %v", err)
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

	dbPath := filepath.Join(workspace, "trunc.db")
	seed, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec("CREATE TABLE numbers (n INT); INSERT INTO numbers VALUES (1), (2), (3), (4), (5);"); err != nil {
		t.Fatal(err)
	}
	seed.Close()

	res, err := tool.Run(context.Background(), env, map[string]any{
		"path":     "trunc.db",
		"query":    "SELECT n FROM numbers ORDER BY n",
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

func TestSQLiteQueryToolDoesNotCreateAMissingFile(t *testing.T) {
	workspace := t.TempDir()
	env := &Env{Workspace: workspace, Trusted: true}

	res, err := (&sqliteQueryTool{}).Run(context.Background(), env, map[string]any{
		"path":  "missing.db",
		"query": "SELECT 1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("a missing database should be an error, got: %s", res.Output)
	}
	if _, err := os.Stat(filepath.Join(workspace, "missing.db")); !os.IsNotExist(err) {
		t.Errorf("the query created a database file: %v", err)
	}
}
