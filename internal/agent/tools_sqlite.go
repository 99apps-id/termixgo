package agent

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// sqliteTimeout bounds a single database query.
const sqliteTimeout = 30 * time.Second

type sqliteQueryTool struct{}

func (t *sqliteQueryTool) Name() string      { return "sqlite_query" }
func (t *sqliteQueryTool) Aliases() []string { return []string{"sqlite", "sql_query"} }
func (t *sqliteQueryTool) Mutating() bool    { return true }
func (t *sqliteQueryTool) Risk() Risk        { return RiskEdit }
func (t *sqliteQueryTool) Label(a map[string]any) string {
	return "Querying SQLite " + Shorten(argString(a, "path", "db"), 40)
}
func (t *sqliteQueryTool) DoneLabel(a map[string]any) string {
	return "Queried SQLite " + Shorten(argString(a, "path", "db"), 40)
}
func (t *sqliteQueryTool) Description() string {
	return "Execute a SQL query against a local SQLite database file using the bundled pure-Go SQLite engine. Formats query results as a markdown table."
}
func (t *sqliteQueryTool) Schema() map[string]any {
	return object(map[string]any{
		"path":     strProp("Path to the SQLite database file (.db, .sqlite, .sqlite3)."),
		"query":    strProp("SQL statement to execute (e.g. SELECT, PRAGMA, CREATE, INSERT)."),
		"max_rows": intProp("Maximum rows to return, 1 to 500. Defaults to 50."),
		"readonly": boolProp("Enforce read-only execution with PRAGMA query_only."),
	}, "path", "query")
}

func (t *sqliteQueryTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	path := argString(args, "path", "db", "file")
	if strings.TrimSpace(path) == "" {
		return Result{Output: "Path is required.", IsError: true}, nil
	}
	query := strings.TrimSpace(argString(args, "query", "sql"))
	if query == "" {
		return Result{Output: "Query is required.", IsError: true}, nil
	}

	resolved := resolvePath(env, path)
	if err := checkWorkspacePath(env, resolved); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}

	readonly := argBool(args, "readonly", false)
	_, statErr := os.Stat(resolved)
	if os.IsNotExist(statErr) && readonly {
		return Result{Output: fmt.Sprintf("database file does not exist: %s", displayPath(env, resolved)), IsError: true}, nil
	}

	dbCtx, cancel := context.WithTimeout(ctx, sqliteTimeout)
	defer cancel()

	db, err := sql.Open("sqlite", resolved)
	if err != nil {
		return Result{Output: fmt.Sprintf("open database: %v", err), IsError: true}, nil
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if readonly {
		if _, err := db.ExecContext(dbCtx, "PRAGMA query_only = ON;"); err != nil {
			return Result{Output: fmt.Sprintf("set readonly pragma: %v", err), IsError: true}, nil
		}
	}

	firstWord := strings.ToUpper(strings.Fields(query)[0])
	isQuery := firstWord == "SELECT" || firstWord == "PRAGMA" || firstWord == "EXPLAIN" || firstWord == "WITH"

	if !isQuery {
		res, err := db.ExecContext(dbCtx, query)
		if err != nil {
			return Result{Output: fmt.Sprintf("execute query: %v", err), IsError: true}, nil
		}
		affected, _ := res.RowsAffected()
		lastID, _ := res.LastInsertId()
		var msg string
		if lastID > 0 {
			msg = fmt.Sprintf("Statement executed successfully. Rows affected: %d, Last insert ID: %d.", affected, lastID)
		} else {
			msg = fmt.Sprintf("Statement executed successfully. Rows affected: %d.", affected)
		}
		return Result{Output: msg}, nil
	}

	rows, err := db.QueryContext(dbCtx, query)
	if err != nil {
		return Result{Output: fmt.Sprintf("execute query: %v", err), IsError: true}, nil
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return Result{Output: fmt.Sprintf("read columns: %v", err), IsError: true}, nil
	}
	if len(cols) == 0 {
		return Result{Output: "No columns returned."}, nil
	}

	maxRows := argInt(args, "max_rows", 50, 1, 500)

	var sb strings.Builder
	// Header row
	sb.WriteString("| ")
	sb.WriteString(strings.Join(cols, " | "))
	sb.WriteString(" |\n|")
	for range cols {
		sb.WriteString(" --- |")
	}
	sb.WriteString("\n")

	count := 0
	values := make([]any, len(cols))
	scanArgs := make([]any, len(cols))
	for i := range values {
		scanArgs[i] = &values[i]
	}

	truncated := false
	for rows.Next() {
		if count >= maxRows {
			truncated = true
			break
		}
		if err := rows.Scan(scanArgs...); err != nil {
			return Result{Output: fmt.Sprintf("scan row %d: %v", count+1, err), IsError: true}, nil
		}
		sb.WriteString("| ")
		for i, val := range values {
			if i > 0 {
				sb.WriteString(" | ")
			}
			if val == nil {
				sb.WriteString("NULL")
			} else {
				switch v := val.(type) {
				case []byte:
					sb.WriteString(strings.ReplaceAll(string(v), "\n", " "))
				default:
					sb.WriteString(strings.ReplaceAll(fmt.Sprintf("%v", v), "\n", " "))
				}
			}
		}
		sb.WriteString(" |\n")
		count++
	}

	if err := rows.Err(); err != nil {
		return Result{Output: fmt.Sprintf("iterate rows: %v", err), IsError: true}, nil
	}

	if count == 0 {
		return Result{Output: "No rows returned."}, nil
	}

	output := strings.TrimRight(sb.String(), "\n")
	if truncated {
		output += fmt.Sprintf("\n\n... (truncated at %d rows)", maxRows)
	}
	return Result{Output: output}, nil
}
