package agent

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// sqlRunTool runs a read-only SQL query against a database via CLI.
type sqlRunTool struct{}

func (t *sqlRunTool) Name() string      { return "run_sql" }
func (t *sqlRunTool) Aliases() []string { return []string{"sql_query", "query_db"} }
func (t *sqlRunTool) Mutating() bool    { return true }
func (t *sqlRunTool) Risk() Risk        { return RiskEdit }
func (t *sqlRunTool) Description() string {
	return "Run one read-only SQL statement against a database via CLI (sqlite, postgres, mysql, mariadb, duckdb)."
}
func (t *sqlRunTool) Schema() map[string]any {
	return object(map[string]any{
		"connection": strProp("Connection string or saved connection name."),
		"query":      strProp("Single read-only SQL statement."),
		"engine":     strProp("Database engine: sqlite, postgres, mysql, mariadb, duckdb."),
	})
}
func (t *sqlRunTool) Label(a map[string]any) string {
	return "SQL " + Shorten(argString(a, "engine"), 10) + " " + Shorten(argString(a, "query"), 30)
}
func (t *sqlRunTool) DoneLabel(a map[string]any) string {
	return "SQL " + Shorten(argString(a, "engine"), 10) + " " + Shorten(argString(a, "query"), 30)
}

func (t *sqlRunTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	engine := strings.ToLower(argString(args, "engine"))
	query := argString(args, "query")
	connection := argString(args, "connection")
	if engine == "" || query == "" || connection == "" {
		return Result{Output: "engine, query, and connection are required", IsError: true}, nil
	}

	var cmd *exec.Cmd
	switch engine {
	case "sqlite", "sqlite3":
		cmd = exec.CommandContext(ctx, "sqlite3", "-readonly", connection, query)
	case "postgres", "psql":
		cmd = exec.CommandContext(ctx, "psql", "-At", "-c", query, connection)
	case "mysql":
		cmd = exec.CommandContext(ctx, "mysql", "-e", query, connection)
	case "mariadb":
		cmd = exec.CommandContext(ctx, "mariadb", "-e", query, connection)
	case "duckdb":
		cmd = exec.CommandContext(ctx, "duckdb", "-readonly", connection, "-c", query)
	default:
		return Result{Output: fmt.Sprintf("unsupported engine %q", engine), IsError: true}, nil
	}

	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd = exec.CommandContext(runCtx, cmd.Path, cmd.Args[1:]...)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return Result{Output: fmt.Sprintf("error: %v\n%s", err, truncate(string(out), 4000)), IsError: true}, nil
	}
	return Result{Output: truncate(string(out), 16000)}, nil
}

// sqlSchemaTool inspects database schema.
type sqlSchemaTool struct{}

func (t *sqlSchemaTool) Name() string      { return "sql_schema" }
func (t *sqlSchemaTool) Aliases() []string { return []string{"db_schema", "describe_db"} }
func (t *sqlSchemaTool) Mutating() bool    { return false }
func (t *sqlSchemaTool) Risk() Risk        { return RiskCommand }
func (t *sqlSchemaTool) Description() string {
	return "Inspect database schema (tables and columns)."
}
func (t *sqlSchemaTool) Schema() map[string]any {
	return object(map[string]any{
		"connection": strProp("Connection string or saved connection name."),
		"engine":     strProp("Database engine."),
		"table":      strProp("Optional table name to filter."),
	})
}
func (t *sqlSchemaTool) Label(a map[string]any) string {
	return "SQL schema " + Shorten(argString(a, "engine"), 10)
}
func (t *sqlSchemaTool) DoneLabel(a map[string]any) string {
	return "SQL schema " + Shorten(argString(a, "engine"), 10)
}

func (t *sqlSchemaTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	engine := strings.ToLower(argString(args, "engine"))
	connection := argString(args, "connection")
	table := argString(args, "table")
	if engine == "" || connection == "" {
		return Result{Output: "engine and connection are required", IsError: true}, nil
	}

	var query string
	switch engine {
	case "sqlite", "sqlite3":
		if table != "" {
			query = fmt.Sprintf("PRAGMA table_info(%s)", table)
		} else {
			query = "SELECT name FROM sqlite_master WHERE type='table' ORDER BY name"
		}
	case "postgres", "psql":
		if table != "" {
			query = fmt.Sprintf("SELECT column_name, data_type FROM information_schema.columns WHERE table_name = '%s' ORDER BY ordinal_position", table)
		} else {
			query = "SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' ORDER BY table_name"
		}
	case "mysql", "mariadb":
		if table != "" {
			query = fmt.Sprintf("SELECT column_name, column_type FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = '%s' ORDER BY ordinal_position", table)
		} else {
			query = "SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE() ORDER BY table_name"
		}
	case "duckdb":
		if table != "" {
			query = fmt.Sprintf("DESCRIBE %s", table)
		} else {
			query = "SHOW TABLES"
		}
	default:
		return Result{Output: fmt.Sprintf("unsupported engine %q", engine), IsError: true}, nil
	}

	// Reuse run_sql logic by calling it indirectly.
	tool := &sqlRunTool{}
	return tool.Run(ctx, env, map[string]any{
		"connection": connection,
		"query":      query,
		"engine":     engine,
	})
}

// sqlExplainTool explains a query plan.
type sqlExplainTool struct{}

func (t *sqlExplainTool) Name() string      { return "sql_explain" }
func (t *sqlExplainTool) Aliases() []string { return []string{"explain_query"} }
func (t *sqlExplainTool) Mutating() bool    { return false }
func (t *sqlExplainTool) Risk() Risk        { return RiskCommand }
func (t *sqlExplainTool) Description() string {
	return "Explain a SQL query plan."
}
func (t *sqlExplainTool) Schema() map[string]any {
	return object(map[string]any{
		"connection": strProp("Connection string or saved connection name."),
		"query":      strProp("SQL statement to explain."),
		"engine":     strProp("Database engine."),
	})
}
func (t *sqlExplainTool) Label(a map[string]any) string {
	return "SQL explain " + Shorten(argString(a, "engine"), 10)
}
func (t *sqlExplainTool) DoneLabel(a map[string]any) string {
	return "SQL explain " + Shorten(argString(a, "engine"), 10)
}

func (t *sqlExplainTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	engine := strings.ToLower(argString(args, "engine"))
	query := argString(args, "query")
	connection := argString(args, "connection")
	if engine == "" || query == "" || connection == "" {
		return Result{Output: "engine, query, and connection are required", IsError: true}, nil
	}
	explainQuery := "EXPLAIN " + query
	tool := &sqlRunTool{}
	return tool.Run(ctx, env, map[string]any{
		"connection": connection,
		"query":      explainQuery,
		"engine":     engine,
	})
}

// sqlHealthTool checks database connectivity.
type sqlHealthTool struct{}

func (t *sqlHealthTool) Name() string      { return "sql_health" }
func (t *sqlHealthTool) Aliases() []string { return []string{"db_health", "ping_db"} }
func (t *sqlHealthTool) Mutating() bool    { return false }
func (t *sqlHealthTool) Risk() Risk        { return RiskCommand }
func (t *sqlHealthTool) Description() string {
	return "Check database connectivity with a lightweight query."
}
func (t *sqlHealthTool) Schema() map[string]any {
	return object(map[string]any{
		"connection": strProp("Connection string or saved connection name."),
		"engine":     strProp("Database engine."),
	})
}
func (t *sqlHealthTool) Label(a map[string]any) string {
	return "SQL health " + Shorten(argString(a, "engine"), 10)
}
func (t *sqlHealthTool) DoneLabel(a map[string]any) string {
	return "SQL health " + Shorten(argString(a, "engine"), 10)
}

func (t *sqlHealthTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	engine := strings.ToLower(argString(args, "engine"))
	connection := argString(args, "connection")
	if engine == "" || connection == "" {
		return Result{Output: "engine and connection are required", IsError: true}, nil
	}
	var query string
	switch engine {
	case "sqlite", "sqlite3":
		query = "SELECT 1"
	case "postgres", "psql":
		query = "SELECT 1"
	case "mysql", "mariadb":
		query = "SELECT 1"
	case "duckdb":
		query = "SELECT 1"
	default:
		return Result{Output: fmt.Sprintf("unsupported engine %q", engine), IsError: true}, nil
	}
	tool := &sqlRunTool{}
	return tool.Run(ctx, env, map[string]any{
		"connection": connection,
		"query":      query,
		"engine":     engine,
	})
}
