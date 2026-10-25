package agent

import (
	"bufio"
	"bytes"
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

// RepoMapOptions configures how the repository map is generated.
type RepoMapOptions struct {
	TargetDir    string
	MaxTokens    int
	ExportedOnly bool
}

// DefaultMaxMapTokens is the standard token budget for repository maps (~6000 chars).
const DefaultMaxMapTokens = 1500

// GenerateRepoMap inspects source files and produces a token-budgeted,
// high-density structural symbol map of the codebase.
func GenerateRepoMap(workspace string, opts RepoMapOptions) (string, error) {
	if strings.TrimSpace(workspace) == "" {
		workspace = "."
	}
	cleanWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		cleanWorkspace = workspace
	}

	target := cleanWorkspace
	if opts.TargetDir != "" {
		target = resolvePath(&Env{Workspace: cleanWorkspace}, opts.TargetDir)
	}

	maxTokens := opts.MaxTokens
	if maxTokens <= 0 {
		maxTokens = DefaultMaxMapTokens
	}
	maxChars := maxTokens * 4

	fileMap := make(map[string][]string)
	var fileList []string

	err = filepath.WalkDir(target, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			name := entry.Name()
			if path != target && (search.IsSkippedDir(name) || name == ".agents") {
				return fs.SkipDir
			}
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if !isSymbolFile(path) && ext != ".rs" {
			return nil
		}

		info, statErr := entry.Info()
		if statErr != nil || info.Size() > maxSymbolBytes {
			return nil
		}

		rel, err := filepath.Rel(cleanWorkspace, path)
		if err != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)

		// Skip tests from high-level architectural map by default
		if strings.HasSuffix(rel, "_test.go") || strings.Contains(rel, "/test/") || strings.Contains(rel, "/tests/") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		var syms []string
		switch ext {
		case ".go":
			syms = extractGoSymbols(data, opts.ExportedOnly)
		case ".py":
			syms = extractPythonSymbols(data, opts.ExportedOnly)
		case ".ts", ".tsx", ".js", ".jsx":
			syms = extractTSSymbols(data)
		}

		if len(syms) > 0 {
			fileMap[rel] = syms
			fileList = append(fileList, rel)
		}
		return nil
	})

	if err != nil {
		return "", err
	}

	sort.Strings(fileList)

	var sb strings.Builder
	totalChars := 0
	filesIncluded := 0
	omittedFiles := 0

	for _, file := range fileList {
		syms := fileMap[file]
		var entry strings.Builder
		entry.WriteString(file)
		entry.WriteString(":\n")
		for _, s := range syms {
			entry.WriteString("  ")
			entry.WriteString(s)
			entry.WriteString("\n")
		}

		entryStr := entry.String()
		if totalChars+len(entryStr) > maxChars && filesIncluded > 0 {
			omittedFiles = len(fileList) - filesIncluded
			break
		}

		sb.WriteString(entryStr)
		totalChars += len(entryStr)
		filesIncluded++
	}

	if omittedFiles > 0 {
		sb.WriteString(fmt.Sprintf("\n... (%d additional files omitted; raise max_tokens to see more)\n", omittedFiles))
	}

	if sb.Len() == 0 {
		return "(No symbols found in target directory)", nil
	}

	return strings.TrimRight(sb.String(), "\n"), nil
}

func extractGoSymbols(data []byte, exportedOnly bool) []string {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, "", data, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}

	var syms []string
	for _, decl := range node.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			if d.Tok == token.TYPE {
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					if exportedOnly && !ts.Name.IsExported() {
						continue
					}
					switch ts.Type.(type) {
					case *ast.StructType:
						syms = append(syms, fmt.Sprintf("type %s struct", ts.Name.Name))
					case *ast.InterfaceType:
						syms = append(syms, fmt.Sprintf("type %s interface", ts.Name.Name))
					default:
						syms = append(syms, fmt.Sprintf("type %s %s", ts.Name.Name, nodeString(fset, ts.Type)))
					}
				}
			}
		case *ast.FuncDecl:
			if exportedOnly && !d.Name.IsExported() {
				continue
			}
			var sig strings.Builder
			if d.Recv != nil && len(d.Recv.List) > 0 {
				recvType := nodeString(fset, d.Recv.List[0].Type)
				fmt.Fprintf(&sig, "(%s) ", recvType)
			}
			sig.WriteString(d.Name.Name)
			sig.WriteString(formatFuncType(fset, d.Type))
			syms = append(syms, "func "+sig.String())
		}
	}
	return syms
}

var (
	pyClassPattern = regexp.MustCompile(`^class\s+([A-Za-z0-9_]+)`)
	pyFuncPattern  = regexp.MustCompile(`^(?:async\s+)?def\s+([A-Za-z0-9_]+)\s*\((.*?)\)`)
	tsExportDef    = regexp.MustCompile(`^export\s+(?:default\s+)?(?:async\s+)?(class|interface|type|enum|function|const)\s+([A-Za-z0-9_]+)`)
)

func extractPythonSymbols(data []byte, exportedOnly bool) []string {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	var syms []string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if matches := pyClassPattern.FindStringSubmatch(line); len(matches) > 1 {
			if !exportedOnly || !strings.HasPrefix(matches[1], "_") {
				syms = append(syms, "class "+matches[1])
			}
		} else if matches := pyFuncPattern.FindStringSubmatch(line); len(matches) > 2 {
			if !exportedOnly || !strings.HasPrefix(matches[1], "_") {
				syms = append(syms, fmt.Sprintf("def %s(%s)", matches[1], matches[2]))
			}
		}
	}
	return syms
}

func extractTSSymbols(data []byte) []string {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	var syms []string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if matches := tsExportDef.FindStringSubmatch(line); len(matches) > 2 {
			syms = append(syms, fmt.Sprintf("%s %s", matches[1], matches[2]))
		}
	}
	return syms
}

// repoMapTool exposes the repository map generator to the agent.
type repoMapTool struct{}

func (t *repoMapTool) Name() string      { return "repo_map" }
func (t *repoMapTool) Aliases() []string { return []string{"repomap", "codebase_map", "project_map"} }
func (t *repoMapTool) Mutating() bool    { return false }
func (t *repoMapTool) Risk() Risk        { return RiskEdit }
func (t *repoMapTool) Label(a map[string]any) string {
	return "Generating repository map"
}
func (t *repoMapTool) DoneLabel(a map[string]any) string {
	return "Generated repository map"
}
func (t *repoMapTool) Description() string {
	return "Generate a compact structural symbol map of files, exported types, interfaces, methods, and functions across the codebase. Token-efficient overview of repository architecture."
}
func (t *repoMapTool) Schema() map[string]any {
	return object(map[string]any{
		"path":          strProp("Optional directory to map. Defaults to workspace."),
		"max_tokens":    intProp("Token budget for the map (defaults to 1500)."),
		"exported_only": boolProp("Only include exported symbols (defaults to true)."),
	})
}

func (t *repoMapTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	opts := RepoMapOptions{
		TargetDir:    argString(args, "path", "dir"),
		MaxTokens:    argInt(args, "max_tokens", DefaultMaxMapTokens, 100, 10000),
		ExportedOnly: argBool(args, "exported_only", true),
	}

	result, err := GenerateRepoMap(env.Workspace, opts)
	if err != nil {
		return Result{Output: fmt.Sprintf("failed to generate repo map: %v", err), IsError: true}, nil
	}

	return Result{Output: result}, nil
}
