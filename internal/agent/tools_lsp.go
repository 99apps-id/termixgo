package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/99apps-id/termixgo/internal/lsp"
)

// Bounds for language server calls. The first request on a large Go or
// TypeScript project waits for the index, so the cap is generous; every later
// call answers in milliseconds.
const (
	lspRequestTimeout  = 60 * time.Second
	lspDiagnosticsWait = 15 * time.Second
	lspMaxLocations    = 200
)

// lspManager is the manager the tools use; tests replace it.
var lspManager = lsp.Shared

// CloseLanguageServers stops every language server, for shutdown.
func CloseLanguageServers() { lspManager().CloseAll() }

// lspTarget is a resolved request position.
type lspTarget struct {
	client *lsp.Client
	server lsp.Server
	path   string
	text   string
	pos    lsp.Position
}

// lspOpen starts the server for a file and syncs the file to it.
func lspOpen(ctx context.Context, env *Env, rawPath string) (*lsp.Client, lsp.Server, string, string, bool, error) {
	path := resolvePath(env, rawPath)
	if path == "" {
		return nil, lsp.Server{}, "", "", false, fmt.Errorf("path is required")
	}
	if err := checkWorkspacePath(env, path); err != nil {
		return nil, lsp.Server{}, "", "", false, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, lsp.Server{}, "", "", false, err
	}
	if info.IsDir() {
		return nil, lsp.Server{}, "", "", false, fmt.Errorf("%s is a directory; pass a source file", displayPath(env, path))
	}
	root := env.Workspace
	if strings.TrimSpace(root) == "" {
		root = filepath.Dir(path)
	}
	client, server, err := lspManager().Client(ctx, root, path, env.Config.LSPServers)
	if err != nil {
		return nil, server, "", "", false, err
	}
	text, sent, err := client.Sync(path, server.Language)
	if err != nil {
		return nil, server, "", "", false, err
	}
	return client, server, path, text, sent, nil
}

// lspLocate resolves path, line and symbol or column to a protocol position.
func lspLocate(ctx context.Context, env *Env, args map[string]any) (lspTarget, error) {
	client, server, path, text, _, err := lspOpen(ctx, env, argString(args, "path", "file", "file_path"))
	if err != nil {
		return lspTarget{}, err
	}
	lines := strings.Split(text, "\n")
	line := argInt(args, "line", 0, 0, len(lines))
	if line < 1 {
		return lspTarget{}, fmt.Errorf("line is required (1-based, the file has %d lines)", len(lines))
	}
	content := strings.TrimRight(lines[line-1], "\r")
	byteCol := -1
	if symbol := strings.TrimSpace(argString(args, "symbol", "name")); symbol != "" {
		byteCol = findIdentifier(content, symbol)
		if byteCol < 0 {
			return lspTarget{}, fmt.Errorf("%q does not appear on line %d: %s", symbol, line, strings.TrimSpace(content))
		}
	} else if column := argInt(args, "column", 0, 0, 1<<20); column > 0 {
		byteCol = runeToByte(content, column-1)
	} else {
		return lspTarget{}, fmt.Errorf("pass symbol (the identifier on that line) or column (1-based)")
	}
	return lspTarget{
		client: client,
		server: server,
		path:   path,
		text:   text,
		pos:    lsp.Position{Line: line - 1, Character: lsp.UTF16Column(content, byteCol)},
	}, nil
}

// findIdentifier finds symbol as a whole word on a line, falling back to any
// occurrence, and returns its byte offset or -1.
func findIdentifier(line, symbol string) int {
	isWord := func(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }
	for start := 0; start <= len(line); {
		index := strings.Index(line[start:], symbol)
		if index < 0 {
			break
		}
		at := start + index
		before, _ := utf8.DecodeLastRuneInString(line[:at])
		after, _ := utf8.DecodeRuneInString(line[at+len(symbol):])
		if (at == 0 || !isWord(before)) && (at+len(symbol) == len(line) || !isWord(after)) {
			return at
		}
		start = at + 1
	}
	return strings.Index(line, symbol)
}

func runeToByte(line string, runeIndex int) int {
	count := 0
	for index := range line {
		if count == runeIndex {
			return index
		}
		count++
	}
	return len(line)
}

func positionSchema(extra map[string]any) map[string]any {
	properties := map[string]any{
		"path":   strProp("Source file, workspace-relative or absolute."),
		"line":   intProp("1-based line number of the identifier."),
		"symbol": strProp("The identifier on that line to act on. Preferred over column."),
		"column": intProp("1-based character column, used when symbol is not given."),
	}
	for key, value := range extra {
		properties[key] = value
	}
	return object(properties, "path", "line")
}

// lspContext bounds one language server call.
func lspContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, lspRequestTimeout)
}

// lspFailure turns an error into a tool result that names how to fix it.
func lspFailure(err error) (Result, error) {
	message := err.Error()
	if strings.Contains(message, "not installed") {
		message += ". Install the server or set lspServers in ~/.termixgo/config.json; until then use symbol_search and grep."
	}
	return Result{Output: message, IsError: true}, nil
}

// locationText renders locations as path:line:col with the source line.
func locationText(env *Env, locations []lsp.Location) string {
	if len(locations) == 0 {
		return ""
	}
	sort.SliceStable(locations, func(i, j int) bool {
		if locations[i].URI != locations[j].URI {
			return locations[i].URI < locations[j].URI
		}
		return locations[i].Range.Start.Line < locations[j].Range.Start.Line
	})
	files := map[string][]string{}
	var out []string
	shown := locations
	if len(shown) > lspMaxLocations {
		shown = shown[:lspMaxLocations]
	}
	for _, location := range shown {
		path := lsp.URIToPath(location.URI)
		lines, ok := files[path]
		if !ok {
			if data, err := os.ReadFile(path); err == nil {
				lines = strings.Split(string(data), "\n")
			}
			files[path] = lines
		}
		line := location.Range.Start.Line
		source := ""
		column := location.Range.Start.Character + 1
		if line >= 0 && line < len(lines) {
			content := strings.TrimRight(lines[line], "\r")
			column = lsp.RuneColumn(content, location.Range.Start.Character)
			source = "  " + Shorten(strings.TrimSpace(content), 160)
		}
		out = append(out, fmt.Sprintf("%s:%d:%d%s", displayPath(env, path), line+1, column, source))
	}
	if len(locations) > len(shown) {
		out = append(out, fmt.Sprintf("... %d more", len(locations)-len(shown)))
	}
	return strings.Join(out, "\n")
}

// decodeLocations reads a definition answer in any of its shapes.
func decodeLocations(raw json.RawMessage) []lsp.Location {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	var single lsp.Location
	if strings.HasPrefix(trimmed, "{") {
		if json.Unmarshal(raw, &single) == nil && single.URI != "" {
			return []lsp.Location{single}
		}
		return nil
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	var out []lsp.Location
	for _, item := range items {
		var location lsp.Location
		if json.Unmarshal(item, &location) == nil && location.URI != "" {
			out = append(out, location)
			continue
		}
		var link lsp.LocationLink
		if json.Unmarshal(item, &link) == nil && link.TargetURI != "" {
			out = append(out, lsp.Location{URI: link.TargetURI, Range: link.TargetSelectionRange})
		}
	}
	return out
}

func textDocumentPosition(target lspTarget) map[string]any {
	return map[string]any{
		"textDocument": map[string]any{"uri": lsp.PathToURI(target.path)},
		"position":     target.pos,
	}
}

// ---------------------------------------------------------------- definition

type lspDefinitionTool struct{}

func (t *lspDefinitionTool) Name() string { return "lsp_definition" }
func (t *lspDefinitionTool) Aliases() []string {
	return []string{"go_to_definition", "goto_definition"}
}
func (t *lspDefinitionTool) Mutating() bool { return false }
func (t *lspDefinitionTool) Risk() Risk     { return RiskEdit }
func (t *lspDefinitionTool) Label(a map[string]any) string {
	return "Finding definition of " + Shorten(argString(a, "symbol"), 40)
}
func (t *lspDefinitionTool) DoneLabel(a map[string]any) string {
	return "Found definition of " + Shorten(argString(a, "symbol"), 40)
}
func (t *lspDefinitionTool) Description() string {
	return "Jump to where a symbol is defined, using the project's language server (gopls, typescript-language-server, pyright, rust-analyzer, clangd). Type-aware, so it resolves methods, imports and overloads that text search cannot. Give the file, the 1-based line, and the identifier on that line."
}
func (t *lspDefinitionTool) Schema() map[string]any { return positionSchema(nil) }

func (t *lspDefinitionTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	ctx, cancel := lspContext(ctx)
	defer cancel()
	target, err := lspLocate(ctx, env, args)
	if err != nil {
		return lspFailure(err)
	}
	var raw json.RawMessage
	if err := target.client.Request(ctx, "textDocument/definition", textDocumentPosition(target), &raw); err != nil {
		return lspFailure(err)
	}
	text := locationText(env, decodeLocations(raw))
	if text == "" {
		return Result{Output: "The language server found no definition at that position."}, nil
	}
	return Result{Output: text}, nil
}

// ---------------------------------------------------------------- references

type lspReferencesTool struct{}

func (t *lspReferencesTool) Name() string      { return "lsp_references" }
func (t *lspReferencesTool) Aliases() []string { return []string{"find_references"} }
func (t *lspReferencesTool) Mutating() bool    { return false }
func (t *lspReferencesTool) Risk() Risk        { return RiskEdit }
func (t *lspReferencesTool) Label(a map[string]any) string {
	return "Finding references to " + Shorten(argString(a, "symbol"), 40)
}
func (t *lspReferencesTool) DoneLabel(a map[string]any) string {
	return "Found references to " + Shorten(argString(a, "symbol"), 40)
}
func (t *lspReferencesTool) Description() string {
	return "List every reference to a symbol across the project, using the language server. Unlike grep it ignores same-named unrelated identifiers and comments. Give the file, the 1-based line, and the identifier on that line."
}
func (t *lspReferencesTool) Schema() map[string]any {
	return positionSchema(map[string]any{
		"include_declaration": boolProp("Include the declaration itself. Defaults to true."),
	})
}

func (t *lspReferencesTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	ctx, cancel := lspContext(ctx)
	defer cancel()
	target, err := lspLocate(ctx, env, args)
	if err != nil {
		return lspFailure(err)
	}
	params := textDocumentPosition(target)
	params["context"] = map[string]any{"includeDeclaration": argBool(args, "include_declaration", true)}
	var locations []lsp.Location
	if err := target.client.Request(ctx, "textDocument/references", params, &locations); err != nil {
		return lspFailure(err)
	}
	if len(locations) == 0 {
		return Result{Output: "The language server found no references."}, nil
	}
	return Result{Output: fmt.Sprintf("%d reference(s):\n%s", len(locations), locationText(env, locations))}, nil
}

// ---------------------------------------------------------------- hover

type lspHoverTool struct{}

func (t *lspHoverTool) Name() string      { return "lsp_hover" }
func (t *lspHoverTool) Aliases() []string { return []string{"symbol_info", "type_info"} }
func (t *lspHoverTool) Mutating() bool    { return false }
func (t *lspHoverTool) Risk() Risk        { return RiskEdit }
func (t *lspHoverTool) Label(a map[string]any) string {
	return "Reading type of " + Shorten(argString(a, "symbol"), 40)
}
func (t *lspHoverTool) DoneLabel(a map[string]any) string {
	return "Read type of " + Shorten(argString(a, "symbol"), 40)
}
func (t *lspHoverTool) Description() string {
	return "Show the type signature and documentation of a symbol, as the editor hover would, using the language server."
}
func (t *lspHoverTool) Schema() map[string]any { return positionSchema(nil) }

func (t *lspHoverTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	ctx, cancel := lspContext(ctx)
	defer cancel()
	target, err := lspLocate(ctx, env, args)
	if err != nil {
		return lspFailure(err)
	}
	var hover struct {
		Contents json.RawMessage `json:"contents"`
	}
	if err := target.client.Request(ctx, "textDocument/hover", textDocumentPosition(target), &hover); err != nil {
		return lspFailure(err)
	}
	text := strings.TrimSpace(hoverText(hover.Contents))
	if text == "" {
		return Result{Output: "The language server has no information for that position."}, nil
	}
	return Result{Output: clipBytes(text, 8000)}, nil
}

// hoverText flattens MarkupContent, MarkedString and arrays of them.
func hoverText(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	var plain string
	if json.Unmarshal(raw, &plain) == nil {
		return plain
	}
	var markup struct {
		Value    string `json:"value"`
		Language string `json:"language"`
	}
	if strings.HasPrefix(trimmed, "{") && json.Unmarshal(raw, &markup) == nil {
		if markup.Language != "" {
			return "```" + markup.Language + "\n" + markup.Value + "\n```"
		}
		return markup.Value
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) == nil {
		parts := make([]string, 0, len(items))
		for _, item := range items {
			if part := strings.TrimSpace(hoverText(item)); part != "" {
				parts = append(parts, part)
			}
		}
		return strings.Join(parts, "\n\n")
	}
	return ""
}

// ---------------------------------------------------------------- symbols

type lspSymbolsTool struct{}

func (t *lspSymbolsTool) Name() string      { return "lsp_symbols" }
func (t *lspSymbolsTool) Aliases() []string { return []string{"document_symbols", "workspace_symbols"} }
func (t *lspSymbolsTool) Mutating() bool    { return false }
func (t *lspSymbolsTool) Risk() Risk        { return RiskEdit }
func (t *lspSymbolsTool) Label(a map[string]any) string {
	if query := argString(a, "query"); query != "" {
		return "Searching project symbols for " + Shorten(query, 40)
	}
	return "Listing symbols in " + Shorten(argString(a, "path"), 40)
}
func (t *lspSymbolsTool) DoneLabel(a map[string]any) string { return "Listed symbols" }
func (t *lspSymbolsTool) Description() string {
	return "List the symbols of one file (classes, functions, methods, fields with their lines), or with query search symbols across the whole project. Uses the language server; path selects the language even when query is given."
}
func (t *lspSymbolsTool) Schema() map[string]any {
	return object(map[string]any{
		"path":  strProp("Source file to outline; with query, any file of the language to search."),
		"query": strProp("Optional project-wide symbol search."),
	}, "path")
}

func (t *lspSymbolsTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	ctx, cancel := lspContext(ctx)
	defer cancel()
	client, _, path, _, _, err := lspOpen(ctx, env, argString(args, "path", "file"))
	if err != nil {
		return lspFailure(err)
	}
	if query := strings.TrimSpace(argString(args, "query")); query != "" {
		var found []lsp.DocumentSymbol
		if err := client.Request(ctx, "workspace/symbol", map[string]any{"query": query}, &found); err != nil {
			return lspFailure(err)
		}
		var lines []string
		for _, symbol := range found {
			if symbol.Location == nil {
				continue
			}
			where := locationText(env, []lsp.Location{*symbol.Location})
			lines = append(lines, fmt.Sprintf("%s %s  %s", lsp.SymbolKindName(symbol.Kind), symbol.Name, where))
			if len(lines) >= lspMaxLocations {
				break
			}
		}
		if len(lines) == 0 {
			return Result{Output: "No project symbol matches " + query + "."}, nil
		}
		return Result{Output: strings.Join(lines, "\n")}, nil
	}
	var symbols []lsp.DocumentSymbol
	if err := client.Request(ctx, "textDocument/documentSymbol", map[string]any{
		"textDocument": map[string]any{"uri": lsp.PathToURI(path)},
	}, &symbols); err != nil {
		return lspFailure(err)
	}
	var lines []string
	var walk func(items []lsp.DocumentSymbol, depth int)
	walk = func(items []lsp.DocumentSymbol, depth int) {
		for _, symbol := range items {
			line := symbol.SelectionRange.Start.Line + 1
			if symbol.Location != nil {
				line = symbol.Location.Range.Start.Line + 1
			}
			detail := ""
			if strings.TrimSpace(symbol.Detail) != "" {
				detail = "  " + Shorten(symbol.Detail, 80)
			}
			lines = append(lines, fmt.Sprintf("%s%d: %s %s%s", strings.Repeat("  ", depth), line, lsp.SymbolKindName(symbol.Kind), symbol.Name, detail))
			walk(symbol.Children, depth+1)
		}
	}
	walk(symbols, 0)
	if len(lines) == 0 {
		return Result{Output: "The language server reported no symbols in " + displayPath(env, path) + "."}, nil
	}
	return Result{Output: displayPath(env, path) + "\n" + strings.Join(lines, "\n")}, nil
}

// ---------------------------------------------------------------- diagnostics

type lspDiagnosticsTool struct{}

func (t *lspDiagnosticsTool) Name() string      { return "lsp_diagnostics" }
func (t *lspDiagnosticsTool) Aliases() []string { return []string{"language_diagnostics"} }
func (t *lspDiagnosticsTool) Mutating() bool    { return false }
func (t *lspDiagnosticsTool) Risk() Risk        { return RiskEdit }
func (t *lspDiagnosticsTool) Label(a map[string]any) string {
	return "Checking " + Shorten(argString(a, "path"), 40) + " with the language server"
}
func (t *lspDiagnosticsTool) DoneLabel(a map[string]any) string {
	return "Checked " + Shorten(argString(a, "path"), 40)
}
func (t *lspDiagnosticsTool) Description() string {
	return "Report compile and type errors, warnings and hints for a file from the language server, the same list an editor underlines. Faster than a full build for checking one edited file."
}
func (t *lspDiagnosticsTool) Schema() map[string]any {
	return object(map[string]any{
		"path":     strProp("Source file to check."),
		"severity": strProp("Lowest severity to report: error, warning (default), info or hint."),
	}, "path")
}

func (t *lspDiagnosticsTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	ctx, cancel := lspContext(ctx)
	defer cancel()
	rawPath := argString(args, "path", "file")
	path := resolvePath(env, rawPath)
	var before int
	if client, _, err := lspManager().Client(ctx, workspaceOr(env, path), path, env.Config.LSPServers); err == nil {
		before = client.DiagnosticsSeq(path)
	}
	client, _, path, _, sent, err := lspOpen(ctx, env, rawPath)
	if err != nil {
		return lspFailure(err)
	}
	var diags []lsp.Diagnostic
	fresh := false
	if !sent && before > 0 {
		// The file is unchanged since the last report, so that report stands.
		diags, fresh = client.WaitDiagnostics(ctx, path, before-1, 0)
	} else {
		diags, fresh = client.WaitDiagnostics(ctx, path, before, lspDiagnosticsWait)
	}
	limit := severityLimit(argString(args, "severity"))
	text := formatDiagnostics(env, path, diags, limit)
	if text == "" {
		if !fresh {
			return Result{Output: "The language server sent no report for " + displayPath(env, path) + " within " + lspDiagnosticsWait.String() + "; it may still be indexing. Try again or use run_checks."}, nil
		}
		return Result{Output: "No problems in " + displayPath(env, path) + "."}, nil
	}
	return Result{Output: text}, nil
}

func workspaceOr(env *Env, path string) string {
	if env != nil && strings.TrimSpace(env.Workspace) != "" {
		return env.Workspace
	}
	return filepath.Dir(path)
}

func severityLimit(name string) int {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "error", "errors":
		return 1
	case "info":
		return 3
	case "hint", "all":
		return 4
	default:
		return 2
	}
}

func formatDiagnostics(env *Env, path string, diags []lsp.Diagnostic, limit int) string {
	sort.SliceStable(diags, func(i, j int) bool { return diags[i].Range.Start.Line < diags[j].Range.Start.Line })
	var lines []string
	counts := map[string]int{}
	for _, diag := range diags {
		severity := diag.Severity
		if severity == 0 {
			severity = 1
		}
		if severity > limit {
			continue
		}
		name := lsp.SeverityName(severity)
		counts[name]++
		source := ""
		if diag.Source != "" {
			source = " [" + diag.Source + "]"
		}
		lines = append(lines, fmt.Sprintf("%s:%d:%d %s%s: %s", displayPath(env, path), diag.Range.Start.Line+1, diag.Range.Start.Character+1, name, source, Shorten(diag.Message, 300)))
	}
	if len(lines) == 0 {
		return ""
	}
	var summary []string
	for _, name := range []string{"error", "warning", "info", "hint"} {
		if counts[name] > 0 {
			summary = append(summary, fmt.Sprintf("%d %s", counts[name], name))
		}
	}
	return strings.Join(summary, ", ") + "\n" + strings.Join(lines, "\n")
}

// ---------------------------------------------------------------- rename

type lspRenameTool struct{}

func (t *lspRenameTool) Name() string      { return "lsp_rename" }
func (t *lspRenameTool) Aliases() []string { return []string{"rename_symbol"} }
func (t *lspRenameTool) Mutating() bool    { return true }
func (t *lspRenameTool) Risk() Risk        { return RiskEdit }
func (t *lspRenameTool) Label(a map[string]any) string {
	return fmt.Sprintf("Renaming %s to %s", Shorten(argString(a, "symbol"), 30), Shorten(argString(a, "new_name"), 30))
}
func (t *lspRenameTool) DoneLabel(a map[string]any) string {
	return fmt.Sprintf("Renamed %s to %s", Shorten(argString(a, "symbol"), 30), Shorten(argString(a, "new_name"), 30))
}
func (t *lspRenameTool) Description() string {
	return "Rename a symbol everywhere it is used, with the language server deciding what is the same symbol. Safer than text replacement: a same-named local, field or comment is left alone. Every changed file is backed up first, so undo_edit reverts it."
}
func (t *lspRenameTool) Schema() map[string]any {
	schema := positionSchema(map[string]any{"new_name": strProp("The new identifier.")})
	schema["required"] = []string{"path", "line", "new_name"}
	return schema
}

func (t *lspRenameTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	newName := strings.TrimSpace(argString(args, "new_name", "newName"))
	if newName == "" {
		return Result{Output: "new_name is required", IsError: true}, nil
	}
	ctx, cancel := lspContext(ctx)
	defer cancel()
	target, err := lspLocate(ctx, env, args)
	if err != nil {
		return lspFailure(err)
	}
	params := textDocumentPosition(target)
	params["newName"] = newName
	var edit lsp.WorkspaceEdit
	if err := target.client.Request(ctx, "textDocument/rename", params, &edit); err != nil {
		return lspFailure(err)
	}
	files := edit.Files()
	if len(files) == 0 {
		return Result{Output: "The language server returned no edits; the position may not be a renameable symbol.", IsError: true}, nil
	}
	// Every file is checked before any is written, so a rename that reaches
	// outside the workspace changes nothing at all.
	paths := make([]string, 0, len(files))
	for path := range files {
		if err := checkWorkspacePath(env, path); err != nil {
			return Result{Output: fmt.Sprintf("the rename touches %s, outside the workspace; nothing was changed", path), IsError: true}, nil
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	updated := make(map[string]string, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return Result{Output: fmt.Sprintf("read %s: %v; nothing was changed", displayPath(env, path), err), IsError: true}, nil
		}
		updated[path] = lsp.ApplyEdits(string(data), files[path])
	}
	var summary []string
	total := 0
	for _, path := range paths {
		backupFile(env, path)
		if err := os.WriteFile(path, []byte(updated[path]), 0o644); err != nil {
			return Result{Output: fmt.Sprintf("write %s: %v (files before it were already renamed; undo_edit reverts them)", displayPath(env, path), err), IsError: true}, nil
		}
		total += len(files[path])
		summary = append(summary, fmt.Sprintf("%s (%d)", displayPath(env, path), len(files[path])))
	}
	return Result{Output: fmt.Sprintf("Renamed to %s: %d edit(s) in %d file(s)\n%s", newName, total, len(paths), strings.Join(summary, "\n"))}, nil
}
