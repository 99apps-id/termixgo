package agent

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
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
	return "Run one read-only SQL statement against a local SQLite database inside the workspace. Writes, ATTACH and multiple statements are refused. Results come back as a markdown table."
}
func (t *sqliteQueryTool) Schema() map[string]any {
	return object(map[string]any{
		"path":     strProp("Path to the SQLite database file (.db, .sqlite, .sqlite3), inside the workspace."),
		"query":    strProp("One read-only SQL statement (SELECT, PRAGMA, EXPLAIN, or WITH)."),
		"max_rows": intProp("Maximum rows to return, 1 to 500. Defaults to 50."),
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
	if err := sqliteReadOnlyQuery(query); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}

	resolved := resolvePath(env, path)
	if err := checkWorkspacePath(env, resolved); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	info, statErr := os.Stat(resolved)
	if statErr != nil {
		return Result{Output: openError(statErr, displayPath(env, resolved)), IsError: true}, nil
	}
	if info.IsDir() {
		return Result{Output: fmt.Sprintf("%s is a directory, not a database file", displayPath(env, resolved)), IsError: true}, nil
	}

	dbCtx, cancel := context.WithTimeout(ctx, sqliteTimeout)
	defer cancel()

	// mode=ro is only honoured on a file: URI, and it cannot be turned off by
	// the statement the way PRAGMA query_only can. A missing file then fails
	// instead of being created.
	db, err := sql.Open("sqlite", sqliteReadOnlyDSN(resolved))
	if err != nil {
		return Result{Output: fmt.Sprintf("open database: %v", err), IsError: true}, nil
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

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

// sqliteReadOnlyDSN opens one database file and nothing else.
//
// modernc.org/sqlite only honours mode=ro on a name that starts with "file:",
// and a plain path plus "?mode=ro" opens read-write and creates a missing file.
// The URI form is also what stops a query from attaching or writing a second
// database: PRAGMA query_only can be turned off again by the same statement
// that writes, so it is not a sandbox.
func sqliteReadOnlyDSN(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		absolute = path
	}
	// Three slashes, not two. url.URL treats file://C:/... as host "C:", which
	// SQLite rejects as an invalid URI authority on Windows.
	return (&url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(absolute), RawQuery: "mode=ro&_query_only=1"}).String()
}

// sqliteReadOnlyQuery refuses anything that is not one read statement.
//
// The first word is not enough: "WITH x AS (SELECT 1) INSERT" writes, and a
// second statement after a semicolon runs too. Stripping comments and quoted
// text first keeps a keyword hiding in a string literal from looking like one.
func sqliteReadOnlyQuery(query string) error {
	stripped := strings.TrimSpace(sqliteStripLiterals(query))
	// A trailing semicolon ends the statement; one in the middle starts another.
	stripped = strings.TrimRight(stripped, ";")
	if strings.Contains(strings.TrimSpace(stripped), ";") {
		return fmt.Errorf("only one statement is allowed")
	}
	fields := strings.Fields(stripped)
	if len(fields) == 0 {
		return fmt.Errorf("query is empty")
	}
	switch strings.ToUpper(strings.TrimRight(fields[0], ";")) {
	case "SELECT", "PRAGMA", "EXPLAIN", "WITH":
	default:
		return fmt.Errorf("only a read-only statement is allowed (SELECT, PRAGMA, EXPLAIN, WITH); %s writes", fields[0])
	}
	for _, word := range fields {
		switch strings.ToUpper(word) {
		case "INSERT", "UPDATE", "DELETE", "REPLACE", "ATTACH", "DETACH",
			"CREATE", "DROP", "ALTER", "REINDEX", "VACUUM", "BEGIN", "COMMIT", "ROLLBACK":
			return fmt.Errorf("statement contains %s, which is not read-only", word)
		}
	}
	return nil
}

// sqliteStripLiterals blanks comments and quoted text so a keyword scan sees
// only the statement structure.
func sqliteStripLiterals(query string) string {
	var builder strings.Builder
	inSingle, inDouble, inLine, inBlock := false, false, false, false
	for index := 0; index < len(query); index++ {
		char := query[index]
		next := byte(0)
		if index+1 < len(query) {
			next = query[index+1]
		}
		switch {
		case inLine:
			if char == '\n' {
				inLine = false
				builder.WriteByte('\n')
			} else {
				builder.WriteByte(' ')
			}
		case inBlock:
			if char == '*' && next == '/' {
				inBlock = false
				builder.WriteString("  ")
				index++
			} else if char == '\n' {
				builder.WriteByte('\n')
			} else {
				builder.WriteByte(' ')
			}
		case inSingle:
			if char == '\'' && next == '\'' {
				builder.WriteString("  ")
				index++
			} else if char == '\'' {
				inSingle = false
				builder.WriteByte(' ')
			} else if char == '\n' {
				builder.WriteByte('\n')
			} else {
				builder.WriteByte(' ')
			}
		case inDouble:
			if char == '"' && next == '"' {
				builder.WriteString("  ")
				index++
			} else if char == '"' {
				inDouble = false
				builder.WriteByte(' ')
			} else if char == '\n' {
				builder.WriteByte('\n')
			} else {
				builder.WriteByte(' ')
			}
		case char == '-' && next == '-':
			inLine = true
			builder.WriteString("  ")
			index++
		case char == '/' && next == '*':
			inBlock = true
			builder.WriteString("  ")
			index++
		case char == '\'':
			inSingle = true
			builder.WriteByte(' ')
		case char == '"':
			inDouble = true
			builder.WriteByte(' ')
		default:
			builder.WriteByte(char)
		}
	}
	return builder.String()
}
