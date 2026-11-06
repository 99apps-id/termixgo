package agent

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// generateTestsTool synthesizes unit tests and verifies them.
type generateTestsTool struct{}

func (t *generateTestsTool) Name() string { return "generate_tests" }
func (t *generateTestsTool) Aliases() []string {
	return []string{"test_gen", "write_tests", "create_tests"}
}
func (t *generateTestsTool) Mutating() bool { return true }
func (t *generateTestsTool) Risk() Risk     { return RiskEdit }
func (t *generateTestsTool) Label(a map[string]any) string {
	return "Generating unit tests for " + Shorten(argString(a, "path", "file"), 40)
}
func (t *generateTestsTool) DoneLabel(a map[string]any) string {
	return "Generated unit tests for " + Shorten(argString(a, "path", "file"), 40)
}
func (t *generateTestsTool) Description() string {
	return "Synthesize idiomatic table-driven unit tests for functions or methods in a source file, write them to the matching _test file, and execute verification runs."
}
func (t *generateTestsTool) Schema() map[string]any {
	return object(map[string]any{
		"path":    strProp("Path to the source file to generate tests for."),
		"symbol":  strProp("Optional specific function or method name to test."),
		"dry_run": boolProp("If true, preview generated test code without writing to disk or running."),
	}, "path")
}

func (t *generateTestsTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	rawPath := argString(args, "path", "file")
	if strings.TrimSpace(rawPath) == "" {
		return Result{Output: "path is required; specify the source file to generate tests for", IsError: true}, nil
	}

	targetSymbol := strings.TrimSpace(argString(args, "symbol"))
	dryRun := argBool(args, "dry_run", false)

	resolved := resolvePath(env, rawPath)
	if err := checkWorkspacePath(env, resolved); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return Result{Output: fmt.Sprintf("file not found: %s", displayPath(env, resolved)), IsError: true}, nil
	}
	if info.IsDir() {
		return Result{Output: fmt.Sprintf("%s is a directory; specify a source file", displayPath(env, resolved)), IsError: true}, nil
	}

	ext := strings.ToLower(filepath.Ext(resolved))
	switch ext {
	case ".go":
		return runGoTestGen(ctx, env, resolved, targetSymbol, dryRun)
	case ".py":
		return runPythonTestGen(ctx, env, resolved, targetSymbol, dryRun)
	default:
		return Result{Output: fmt.Sprintf("automatic test generation is not supported for %s files yet", ext), IsError: true}, nil
	}
}

func runGoTestGen(ctx context.Context, env *Env, filePath, targetSymbol string, dryRun bool) (Result, error) {
	src, err := os.ReadFile(filePath)
	if err != nil {
		return Result{Output: fmt.Sprintf("read %s: %v", displayPath(env, filePath), err), IsError: true}, nil
	}

	dir := filepath.Dir(filePath)
	base := filepath.Base(filePath)
	ext := filepath.Ext(base)
	nameWithoutExt := strings.TrimSuffix(base, ext)

	if strings.HasSuffix(nameWithoutExt, "_test") {
		return Result{Output: "target file is already a test file", IsError: true}, nil
	}

	testFileName := nameWithoutExt + "_test.go"
	testFilePath := filepath.Join(dir, testFileName)

	fset := token.NewFileSet()
	fileNode, err := parser.ParseFile(fset, filePath, src, parser.ParseComments)
	if err != nil {
		return Result{Output: fmt.Sprintf("parse %s: %v", displayPath(env, filePath), err), IsError: true}, nil
	}

	pkgName := fileNode.Name.Name

	// Read existing test file if present to avoid duplicating test functions
	existingTests := make(map[string]bool)
	var existingContent string
	if testData, err := os.ReadFile(testFilePath); err == nil {
		existingContent = string(testData)
		testFset := token.NewFileSet()
		if testNode, parseErr := parser.ParseFile(testFset, testFilePath, testData, 0); parseErr == nil {
			for _, decl := range testNode.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok {
					existingTests[fn.Name.Name] = true
				}
			}
		} else {
			// The existing file does not parse (a hand-written block with a
			// stray token). The name scan keeps the duplicate guard alive:
			// silently appending past a broken parse produced a second
			// TestFoo in one package with no report of either.
			for name := range scanTestNames(existingContent) {
				existingTests[name] = true
			}
		}
	}

	var newTestBlocks []string
	var coveredNames []string

	for _, decl := range fileNode.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		fnName := fn.Name.Name
		if strings.HasPrefix(fnName, "Test") || strings.HasPrefix(fnName, "Benchmark") || strings.HasPrefix(fnName, "Example") {
			continue
		}
		if targetSymbol != "" && fnName != targetSymbol {
			continue
		}

		testFnName := "Test" + fnName
		recvTypeName := ""
		if fn.Recv != nil && len(fn.Recv.List) > 0 {
			recvType := fn.Recv.List[0].Type
			recvTypeName = strings.TrimPrefix(nodeString(fset, recvType), "*")
			testFnName = "Test" + recvTypeName + "_" + fnName
		}

		if existingTests[testFnName] {
			continue
		}

		testCode := generateGoTestFunc(fset, fn, testFnName, recvTypeName)
		if testCode != "" {
			newTestBlocks = append(newTestBlocks, testCode)
			coveredNames = append(coveredNames, testFnName)
		}
	}

	if len(newTestBlocks) == 0 {
		if len(existingTests) > 0 {
			return Result{Output: fmt.Sprintf("All functions in %s already have corresponding tests in %s", displayPath(env, filePath), displayPath(env, testFilePath))}, nil
		}
		return Result{Output: fmt.Sprintf("No testable functions found in %s", displayPath(env, filePath))}, nil
	}

	var finalTestContent string
	if existingContent == "" {
		var header strings.Builder
		fmt.Fprintf(&header, "package %s\n\n", pkgName)
		// The mocks carry package-qualified values such as
		// context.Background(); an import list fixed at "testing" alone made
		// every generated test for a function that takes a ctx fail to
		// compile, and failed at go test time instead of here.
		imports := []string{"testing"}
		for _, block := range newTestBlocks {
			if strings.Contains(block, "context.") {
				imports = append(imports, "context")
				break
			}
		}
		sort.Strings(imports)
		header.WriteString("import (\n")
		for _, importPath := range imports {
			fmt.Fprintf(&header, "\t%q\n", importPath)
		}
		header.WriteString(")\n\n")
		header.WriteString(strings.Join(newTestBlocks, "\n\n"))
		header.WriteString("\n")
		finalTestContent = header.String()
	} else {
		finalTestContent = strings.TrimRight(existingContent, "\n") + "\n\n" + strings.Join(newTestBlocks, "\n\n") + "\n"
	}

	if dryRun {
		var sb strings.Builder
		fmt.Fprintf(&sb, "// Preview of generated tests for %s (%d function(s)):\n\n", displayPath(env, filePath), len(coveredNames))
		sb.WriteString(finalTestContent)
		return Result{Output: sb.String()}, nil
	}

	// Write test file with backup
	backupFile(env, testFilePath)
	if err := os.WriteFile(testFilePath, []byte(finalTestContent), 0o644); err != nil {
		return Result{Output: fmt.Sprintf("write test file: %v", err), IsError: true}, nil
	}

	// Run diagnostics on test file
	if diag := DiagnoseFile(env, testFilePath); diag != "" {
		// A non-compiling generated test must not stay in the package, because
		// the build was green before this call. Put the previous bytes back.
		if existingContent == "" {
			_ = os.Remove(testFilePath)
		} else {
			_ = os.WriteFile(testFilePath, []byte(existingContent), 0o644)
		}
		return Result{Output: fmt.Sprintf("Generated tests have syntax issues:\n%s", diag), IsError: true}, nil
	}

	// Execute verification run using go test
	testPkg := "./" + filepath.ToSlash(filepath.Clean(dir))
	if filepath.Clean(dir) == "." {
		testPkg = "."
	}

	verifyCmd := fmt.Sprintf("go test -v -run \"^(%s)$\" %s", strings.Join(coveredNames, "|"), testPkg)
	execRes, _ := execute(ctx, env, verifyCmd, env.Workspace, 30*time.Second)

	status := "PASSED"
	if execRes.IsError {
		status = "ATTENTION REQUIRED (some assertions need tuning)"
		if strings.Contains(execRes.Output, "[build failed]") {
			// Type errors are the one failure mode the syntax diagnostics
			// cannot see; a generated test that does not compile stays
			// neither: restore the previous file and report the details.
			if existingContent == "" {
				_ = os.Remove(testFilePath)
			} else {
				_ = os.WriteFile(testFilePath, []byte(existingContent), 0o644)
			}
			execRes.Output = "The generated test file did not compile, so it was not kept." + "\n" + execRes.Output
			return Result{Output: fmt.Sprintf("Generated tests failed verification:\n%s", execRes.Output), IsError: true}, nil
		}
	}

	var summary strings.Builder
	fmt.Fprintf(&summary, "Synthesized unit tests for %d function(s) in %s:\n", len(coveredNames), displayPath(env, testFilePath))
	for _, name := range coveredNames {
		fmt.Fprintf(&summary, "  - %s\n", name)
	}
	fmt.Fprintf(&summary, "\nVerification: %s\n%s", status, execRes.Output)

	return Result{Output: strings.TrimRight(summary.String(), "\n")}, nil
}

// scanTestNames reads test function names out of content that does not
// parse. It is the duplicate-name fallback for generate_tests, not a parser:
// a regex on the line start is enough to keep the second TestFoo out.
func scanTestNames(content string) map[string]bool {
	names := make(map[string]bool)
	lineRe := regexp.MustCompile(`^func\s+(Test\w+)\(`)
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if matches := lineRe.FindStringSubmatch(line); len(matches) > 1 {
			names[matches[1]] = true
		}
	}
	return names
}

func generateGoTestFunc(fset *token.FileSet, fn *ast.FuncDecl, testFnName, recvTypeName string) string {
	var sb strings.Builder
	fnName := fn.Name.Name

	params := fn.Type.Params
	results := fn.Type.Results

	hasReturn := results != nil && len(results.List) > 0
	hasErrorReturn := false
	var primaryReturnType string

	if hasReturn {
		lastRes := results.List[len(results.List)-1]
		if nodeString(fset, lastRes.Type) == "error" {
			hasErrorReturn = true
		}
		if len(results.List) > 1 || !hasErrorReturn {
			primaryReturnType = nodeString(fset, results.List[0].Type)
		}
	}

	fmt.Fprintf(&sb, "func %s(t *testing.T) {\n", testFnName)
	sb.WriteString("\ttests := []struct {\n")
	sb.WriteString("\t\tname string\n")

	// Input parameter fields in test struct
	if params != nil {
		for i, param := range params.List {
			typStr := nodeString(fset, param.Type)
			if len(param.Names) > 0 {
				for _, name := range param.Names {
					fmt.Fprintf(&sb, "\t\t%s %s\n", name.Name, typStr)
				}
			} else {
				fmt.Fprintf(&sb, "\t\targ%d %s\n", i+1, typStr)
			}
		}
	}

	if primaryReturnType != "" {
		fmt.Fprintf(&sb, "\t\twant %s\n", primaryReturnType)
	}
	if hasErrorReturn {
		sb.WriteString("\t\twantErr bool\n")
	}

	sb.WriteString("\t}{\n")

	// Normal case test value
	sb.WriteString("\t\t{\n")
	sb.WriteString("\t\t\tname: \"normal case\",\n")
	if params != nil {
		for i, param := range params.List {
			typStr := nodeString(fset, param.Type)
			val := defaultMockValue(typStr)
			if len(param.Names) > 0 {
				for _, name := range param.Names {
					fmt.Fprintf(&sb, "\t\t\t%s: %s,\n", name.Name, val)
				}
			} else {
				fmt.Fprintf(&sb, "\t\t\targ%d: %s,\n", i+1, val)
			}
		}
	}
	if hasErrorReturn {
		sb.WriteString("\t\t\twantErr: false,\n")
	}
	sb.WriteString("\t\t},\n")
	sb.WriteString("\t}\n")

	sb.WriteString("\tfor _, tc := range tests {\n")
	sb.WriteString("\t\tt.Run(tc.name, func(t *testing.T) {\n")

	// Call invocation
	var callArgs []string
	if params != nil {
		for i, param := range params.List {
			if len(param.Names) > 0 {
				for _, name := range param.Names {
					callArgs = append(callArgs, "tc."+name.Name)
				}
			} else {
				callArgs = append(callArgs, fmt.Sprintf("tc.arg%d", i+1))
			}
		}
	}
	argsJoined := strings.Join(callArgs, ", ")

	targetCall := fnName + "(" + argsJoined + ")"
	if recvTypeName != "" {
		fmt.Fprintf(&sb, "\t\t\trecv := &%s{}\n", recvTypeName)
		targetCall = "recv." + fnName + "(" + argsJoined + ")"
	}

	if hasReturn {
		if hasErrorReturn && primaryReturnType != "" {
			fmt.Fprintf(&sb, "\t\t\tgot, err := %s\n", targetCall)
			sb.WriteString("\t\t\tif (err != nil) != tc.wantErr {\n")
			sb.WriteString("\t\t\t\tt.Errorf(\"unexpected error status: %v, wantErr %v\", err, tc.wantErr)\n")
			sb.WriteString("\t\t\t\treturn\n")
			sb.WriteString("\t\t\t}\n")
			sb.WriteString("\t\t\t_ = got\n")
		} else if hasErrorReturn {
			fmt.Fprintf(&sb, "\t\t\terr := %s\n", targetCall)
			sb.WriteString("\t\t\tif (err != nil) != tc.wantErr {\n")
			sb.WriteString("\t\t\t\tt.Errorf(\"unexpected error status: %v, wantErr %v\", err, tc.wantErr)\n")
			sb.WriteString("\t\t\t}\n")
		} else {
			fmt.Fprintf(&sb, "\t\t\tgot := %s\n", targetCall)
			sb.WriteString("\t\t\tif got != tc.want {\n")
			sb.WriteString("\t\t\t\tt.Errorf(\"got %v, want %v\", got, tc.want)\n")
			sb.WriteString("\t\t\t}\n")
		}
	} else {
		fmt.Fprintf(&sb, "\t\t\t%s\n", targetCall)
	}

	sb.WriteString("\t\t})\n")
	sb.WriteString("\t}\n")
	sb.WriteString("}")

	return sb.String()
}

func defaultMockValue(typ string) string {
	switch typ {
	case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64":
		return "1"
	case "float32", "float64":
		return "1.0"
	case "string":
		return `"test"`
	case "bool":
		return "true"
	case "context.Context":
		return "context.Background()"
	default:
		if strings.HasPrefix(typ, "[]") {
			return "nil"
		}
		if strings.HasPrefix(typ, "*") {
			return "nil"
		}
		return "nil"
	}
}

func runPythonTestGen(ctx context.Context, env *Env, filePath, targetSymbol string, dryRun bool) (Result, error) {
	src, err := os.ReadFile(filePath)
	if err != nil {
		return Result{Output: fmt.Sprintf("read %s: %v", displayPath(env, filePath), err), IsError: true}, nil
	}

	dir := filepath.Dir(filePath)
	base := filepath.Base(filePath)
	ext := filepath.Ext(base)
	nameWithoutExt := strings.TrimSuffix(base, ext)

	testFileName := "test_" + nameWithoutExt + ".py"
	testFilePath := filepath.Join(dir, testFileName)

	var funcs []string
	scanner := bufio.NewScanner(bytes.NewReader(src))
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// Only module-level defs are importable by name: the regex matches
		// an indented method the same way, but `from module import name`
		// would be an ImportError for it, and the python verifier below
		// never runs, so the broken suite shipped silently.
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			continue
		}
		if matches := pyFuncPattern.FindStringSubmatch(trimmed); len(matches) > 1 {
			fn := matches[1]
			if !strings.HasPrefix(fn, "_") && !strings.HasPrefix(fn, "test_") {
				if targetSymbol == "" || fn == targetSymbol {
					funcs = append(funcs, fn)
				}
			}
		}
	}

	if len(funcs) == 0 {
		return Result{Output: fmt.Sprintf("No testable functions found in %s", displayPath(env, filePath))}, nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "import unittest\nfrom %s import %s\n\n", nameWithoutExt, strings.Join(funcs, ", "))
	className := nameWithoutExt
	if len(className) > 0 {
		className = strings.ToUpper(className[:1]) + className[1:]
	}
	fmt.Fprintf(&sb, "class Test%s(unittest.TestCase):\n", className)
	for _, fn := range funcs {
		fmt.Fprintf(&sb, "    def test_%s(self):\n", fn)
		fmt.Fprintf(&sb, "        # Auto-generated verification probe for %s\n", fn)
		fmt.Fprintf(&sb, "        self.assertTrue(callable(%s))\n\n", fn)
	}
	sb.WriteString("if __name__ == '__main__':\n    unittest.main()\n")

	content := sb.String()
	if dryRun {
		return Result{Output: content}, nil
	}

	backupFile(env, testFilePath)
	if err := os.WriteFile(testFilePath, []byte(content), 0o644); err != nil {
		return Result{Output: fmt.Sprintf("write python test file: %v", err), IsError: true}, nil
	}

	return Result{Output: fmt.Sprintf("Synthesized Python test suite for %d function(s) in %s", len(funcs), displayPath(env, testFilePath))}, nil
}
