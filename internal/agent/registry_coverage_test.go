package agent

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// toolNamePattern finds the literal name a tool reports, for example
// `func (t *readFileTool) Name() string { return "read_file" }`.
var toolNamePattern = regexp.MustCompile(`func \(t \*\w+\) Name\(\)\s+string\s+\{ return "([a-z0-9_]+)" \}`)

// TestEveryDefinedToolIsRegistered is the guard for a tool that ships
// unreachable: search_memory was written, documented and tested, but never added
// to DefaultRegistry, so the model could not call it and the full-text index
// behind it was dead code. A tool that is not registered is invisible in a way
// no behavioural test notices, so the check is made against the source itself.
func TestEveryDefinedToolIsRegistered(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}

	defined := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "tools") || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, match := range toolNamePattern.FindAllStringSubmatch(string(data), -1) {
			defined[match[1]] = true
		}
	}
	if len(defined) < 20 {
		t.Fatalf("only %d tool definitions were found, so the scan is broken", len(defined))
	}

	registry := DefaultRegistry()
	var missing []string
	for name := range defined {
		if _, ok := registry.Lookup(name); !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("these tools are defined but never registered, so the model cannot call them: %s", strings.Join(missing, ", "))
	}
}
