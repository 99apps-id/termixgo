package agent

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/99apps-id/termixgo/internal/search"
)

// Bounds for a workspace-wide symbol scan: enough to map a package in one
// call, small enough that a huge tree cannot stall a turn.
const (
	maxSymbolFiles    = 3000
	maxSymbolBytes    = 1024 * 1024
	defaultMaxSymbols = 50
	maxSymbolResults  = 500
)

// symbolHit is one named declaration: what it is, where it is, and the
// source line that declares it.
type symbolHit struct {
	path   string
	line   int
	kind   string
	name   string
	detail string
}

// symbolSearchTool finds named declarations across the workspace: functions,
// methods, types, classes and their equivalents. It is the navigation half of
// code_outline, which describes one file: this one answers "where is X
// defined" without opening every file first.
type symbolSearchTool struct{}

func (t *symbolSearchTool) Name() string      { return "symbol_search" }
func (t *symbolSearchTool) Aliases() []string { return []string{"find_symbol"} }
func (t *symbolSearchTool) Mutating() bool    { return false }
func (t *symbolSearchTool) Risk() Risk        { return RiskEdit }
func (t *symbolSearchTool) Label(a map[string]any) string {
	return "Searching symbols for " + Shorten(argString(a, "query"), 40)
}
func (t *symbolSearchTool) DoneLabel(a map[string]any) string {
	return "Searched symbols"
}
func (t *symbolSearchTool) Description() string {
	return "Find where a function, method, type, class or other named symbol is defined across the workspace. Go files are parsed precisely; Python, TypeScript, JavaScript, Rust and Java are matched structurally. Returns file, line and kind; structurally matched languages also return the declaring source line."
}
func (t *symbolSearchTool) Schema() map[string]any {
	return object(map[string]any{
		"query":       strProp("Substring to match against symbol names, case-insensitive."),
		"path":        strProp("Directory or file to search. Defaults to the workspace."),
		"max_results": intProp("Maximum symbols to return, 1 to 500. Defaults to 50."),
	}, "query")
}

func (t *symbolSearchTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	query := strings.TrimSpace(argString(args, "query"))
	if query == "" {
		return Result{Output: "query is required; pass the symbol name to find", IsError: true}, nil
	}
	root := resolvePath(env, argString(args, "path", "root"))
	if root == "" {
		root = env.Workspace
	}
	if err := checkWorkspacePath(env, root); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	maxResults := argInt(args, "max_results", defaultMaxSymbols, 1, maxSymbolResults)

	var hits []symbolHit
	truncated := false
	scanned := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if entry.IsDir() {
			if path != root && search.IsSkippedDir(entry.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !isSymbolFile(path) {
			return nil
		}
		info, statErr := entry.Info()
		if statErr != nil || info.Size() > maxSymbolBytes {
			return nil
		}
		scanned++
		if scanned > maxSymbolFiles {
			truncated = true
			return fs.SkipAll
		}
		found, readErr := symbolsInFile(path, query)
		if readErr != nil {
			return nil
		}
		for _, hit := range found {
			hit.path = relativeSlash(env, path)
			hits = append(hits, hit)
			if len(hits) >= maxResults {
				truncated = true
				return fs.SkipAll
			}
		}
		return nil
	})
	if err != nil && err != context.Canceled && err != context.DeadlineExceeded {
		return Result{Output: fmt.Sprintf("symbol search failed: %v", err), IsError: true}, nil
	}
	if len(hits) == 0 {
		return Result{Output: fmt.Sprintf("No symbols match %q.", query)}, nil
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].path != hits[j].path {
			return hits[i].path < hits[j].path
		}
		return hits[i].line < hits[j].line
	})
	var builder strings.Builder
	header := fmt.Sprintf("%d symbol(s) match %q", len(hits), query)
	if truncated {
		header += " (limit reached)"
	}
	builder.WriteString(header + "\n")
	for _, hit := range hits {
		fmt.Fprintf(&builder, "%s:%d: %s %s", hit.path, hit.line, hit.kind, hit.name)
		if hit.detail != "" {
			fmt.Fprintf(&builder, "  // %s", Shorten(hit.detail, 100))
		}
		builder.WriteString("\n")
	}
	return Result{Output: strings.TrimRight(builder.String(), "\n")}, nil
}

// isSymbolFile reports whether a path holds declarations worth scanning.
func isSymbolFile(path string) bool {
	if !isTextFile(path) {
		return false
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go", ".py", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs",
		".rs", ".java", ".rb", ".php", ".cs", ".c", ".h", ".cpp", ".hpp":
		return true
	}
	return false
}

// symbolsInFile collects the declarations in one file whose name contains
// the query. Go is parsed with go/parser; anything that fails to parse, and
// every other language, falls back to structural line patterns.
func symbolsInFile(path, query string) ([]symbolHit, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	needle := strings.ToLower(query)
	var hits []symbolHit
	keep := func(name, kind string, line int, detail string) {
		if name == "" || !strings.Contains(strings.ToLower(name), needle) {
			return
		}
		hits = append(hits, symbolHit{line: line, kind: kind, name: name, detail: strings.TrimSpace(detail)})
	}
	if strings.ToLower(filepath.Ext(path)) == ".go" {
		if collected, ok := goSymbols(data, needle); ok {
			return collected, nil
		}
	}
	lines := strings.Split(string(data), "\n")
	for index, raw := range lines {
		indented := strings.TrimLeft(raw, " \t") != raw
		line := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(raw), "{"))
		if name, kind, ok := matchSymbolLine(filepath.Ext(path), line, indented); ok {
			keep(name, kind, index+1, raw)
		}
	}
	return hits, nil
}

// goSymbols parses Go declarations precisely: functions, methods with their
// receiver, types, and top-level constants and variables. A declaration is
// collected when its name contains the needle, and a method also when its
// receiver type does, so a query for a type finds its methods. The precise
// parse needs no source line, so Go hits carry no detail.
func goSymbols(data []byte, needle string) ([]symbolHit, bool) {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, "", data, parser.SkipObjectResolution)
	if err != nil {
		return nil, false
	}
	var hits []symbolHit
	matched := func(name, receiver string) bool {
		if strings.Contains(strings.ToLower(name), needle) {
			return true
		}
		return receiver != "" && strings.Contains(strings.ToLower(receiver), needle)
	}
	keep := func(name, kind string, pos token.Pos, receiver string) {
		if name == "" || !matched(name, receiver) {
			return
		}
		hits = append(hits, symbolHit{line: fset.Position(pos).Line, kind: kind, name: name})
	}
	for _, decl := range node.Decls {
		switch typed := decl.(type) {
		case *ast.FuncDecl:
			if typed.Recv != nil && len(typed.Recv.List) > 0 {
				keep(typed.Name.Name, "method", typed.Pos(), nodeString(fset, typed.Recv.List[0].Type))
			} else {
				keep(typed.Name.Name, "func", typed.Pos(), "")
			}
		case *ast.GenDecl:
			for _, spec := range typed.Specs {
				switch item := spec.(type) {
				case *ast.TypeSpec:
					kind := "type"
					if _, ok := item.Type.(*ast.StructType); ok {
						kind = "struct"
					} else if _, ok := item.Type.(*ast.InterfaceType); ok {
						kind = "interface"
					}
					keep(item.Name.Name, kind, item.Pos(), "")
				case *ast.ValueSpec:
					kind := "var"
					if typed.Tok == token.CONST {
						kind = "const"
					}
					for _, name := range item.Names {
						keep(name.Name, kind, name.Pos(), "")
					}
				}
			}
		}
	}
	return hits, true
}

// symbolPatterns matches declarations line by line for languages without a
// vendored parser. Each pattern names its kind and captures the symbol name.
// A method-looking line only counts when it is indented: a bare `name(args)`
// at column zero is a call, not a declaration.
type symbolPattern struct {
	kind         string
	indentedOnly bool
	expression   *regexp.Regexp
}

var symbolPatterns = map[string][]symbolPattern{
	".py": {
		{"class", false, regexp.MustCompile(`^class\s+(\w+)`)},
		{"func", false, regexp.MustCompile(`^def\s+(\w+)`)},
	},
	".ts": {
		{"class", false, regexp.MustCompile(`^(?:export\s+)?(?:abstract\s+)?class\s+(\w+)`)},
		{"interface", false, regexp.MustCompile(`^(?:export\s+)?interface\s+(\w+)`)},
		{"func", false, regexp.MustCompile(`^(?:export\s+)?(?:async\s+)?function\s+(\w+)`)},
		{"method", true, regexp.MustCompile(`^(?:public\s+|private\s+|protected\s+|static\s+|async\s+)*(?:get\s+|set\s+)?(\w+)\s*\(`)},
		{"const", false, regexp.MustCompile(`^(?:export\s+)?(?:const|let|var)\s+(\w+)\s*=`)},
	},
	".rs": {
		{"func", false, regexp.MustCompile(`^(?:pub(?:\([^)]*\))?\s+)?(?:async\s+)?fn\s+(\w+)`)},
		{"struct", false, regexp.MustCompile(`^(?:pub(?:\([^)]*\))?\s+)?struct\s+(\w+)`)},
		{"enum", false, regexp.MustCompile(`^(?:pub(?:\([^)]*\))?\s+)?enum\s+(\w+)`)},
		{"trait", false, regexp.MustCompile(`^(?:pub(?:\([^)]*\))?\s+)?trait\s+(\w+)`)},
		{"impl", false, regexp.MustCompile(`^impl\s+(?:<[^>]*>\s*)?(\w[\w<>, ]*)`)},
	},
	".java": {
		{"class", false, regexp.MustCompile(`^(?:public\s+|private\s+|protected\s+|abstract\s+|final\s+)*class\s+(\w+)`)},
		{"interface", false, regexp.MustCompile(`^(?:public\s+|private\s+|protected\s+)*interface\s+(\w+)`)},
		{"enum", false, regexp.MustCompile(`^(?:public\s+|private\s+|protected\s+)*enum\s+(\w+)`)},
	},
}

// matchSymbolLine applies the structural patterns for one trimmed source
// line. The .tsx/.jsx/.mjs/.cjs family shares the TypeScript shapes, and the
// C-likes without their own table fall back to the generic function and
// class shapes.
func matchSymbolLine(ext, line string, indented bool) (string, string, bool) {
	lowered := strings.ToLower(ext)
	patterns, ok := symbolPatterns[lowered]
	if !ok {
		switch lowered {
		case ".tsx", ".js", ".jsx", ".mjs", ".cjs":
			patterns = symbolPatterns[".ts"]
		case ".rb":
			patterns = symbolPatterns[".py"]
		default:
			patterns = []symbolPattern{
				{"func", false, regexp.MustCompile(`^(?:[\w<>\*:&,\s]+\s+)?(\w+)\s*\(.*\)\s*(?:const\s*)?(?:\{)?$`)},
				{"class", false, regexp.MustCompile(`^(?:class|struct)\s+(\w+)`)},
			}
		}
	}
	for _, pattern := range patterns {
		if pattern.indentedOnly && !indented {
			continue
		}
		match := pattern.expression.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		name := match[len(match)-1]
		if name == "" || name == "if" || name == "for" || name == "while" || name == "switch" || name == "return" {
			continue
		}
		return name, pattern.kind, true
	}
	return "", "", false
}
