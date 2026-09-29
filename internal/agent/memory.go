package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/99apps-id/termixgo/internal/config"
)

// Memory limits keep the learned block small enough to ride in every prompt.
const (
	maxFactChars   = 500
	maxMemoryFacts = 100
	maxMemoryBytes = 16 * 1024
)

// Memory reads and appends the two learned-memory files: project scope at
// <workspace>/.termixgo/memory.md and global scope at ~/.termixgo/memory.md.
type Memory struct {
	mu        sync.Mutex
	workspace string
}

// NewMemory builds a memory reader for a workspace.
func NewMemory(workspace string) *Memory { return &Memory{workspace: workspace} }

func (m *Memory) projectPath() string {
	if strings.TrimSpace(m.workspace) == "" {
		return ""
	}
	return filepath.Join(m.workspace, ".termixgo", "memory.md")
}

func (m *Memory) globalPath() string {
	home, err := config.Home()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "memory.md")
}

// Remember appends a fact to one scope, enforcing the caps.
func (m *Memory) Remember(fact, scope string) error {
	trimmed := strings.TrimSpace(fact)
	if trimmed == "" {
		return errors.New("the fact is empty")
	}
	if len(trimmed) > maxFactChars {
		trimmed = clipBytes(trimmed, maxFactChars)
	}
	path := m.projectPath()
	if strings.EqualFold(scope, "global") {
		path = m.globalPath()
	}
	if path == "" {
		return errors.New("no memory file location is available")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	existing, _ := os.ReadFile(path)
	lines := splitFacts(string(existing))
	for _, line := range lines {
		if strings.EqualFold(line, trimmed) {
			return nil
		}
	}
	if len(lines) >= maxMemoryFacts {
		lines = lines[len(lines)-maxMemoryFacts+1:]
	}
	lines = append(lines, trimmed)

	var builder strings.Builder
	builder.WriteString("# Termixgo memory\n\n")
	for _, line := range lines {
		builder.WriteString("- " + line + "\n")
	}
	content := builder.String()
	if len(content) > maxMemoryBytes {
		content = content[len(content)-maxMemoryBytes:]
		if index := strings.Index(content, "\n- "); index >= 0 {
			content = "# Termixgo memory\n\n" + strings.TrimPrefix(content[index:], "\n")
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create memory directory: %w", err)
	}
	return os.WriteFile(path, []byte(content), 0o600)
}

// Read returns the project and global facts, newest last.
func (m *Memory) Read() (project, global []string) {
	projectData, _ := os.ReadFile(m.projectPath())
	globalData, _ := os.ReadFile(m.globalPath())
	return splitFacts(string(projectData)), splitFacts(string(globalData))
}

// PromptBlock renders the learned-memory block, or "" when nothing is stored.
func (m *Memory) PromptBlock() string {
	project, global := m.Read()
	if len(project) == 0 && len(global) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("\n\n## LEARNED MEMORY\nFacts the operator taught earlier. Trust them unless the code says otherwise.\n")
	if len(global) > 0 {
		builder.WriteString("\n### GLOBAL CONVENTIONS ( ~/.termixgo/memory.md )\n")
		for _, fact := range global {
			builder.WriteString("- " + fact + "\n")
		}
	}
	if len(project) > 0 {
		builder.WriteString("\n### PROJECT CONVENTIONS & FACTS ( .termixgo/memory.md )\n")
		for _, fact := range project {
			builder.WriteString("- " + fact + "\n")
		}
	}
	return builder.String()
}

// splitFacts reads "- fact" lines out of a memory file.
func splitFacts(text string) []string {
	var facts []string
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- ") {
			continue
		}
		fact := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
		if fact != "" {
			facts = append(facts, fact)
		}
	}
	return facts
}
