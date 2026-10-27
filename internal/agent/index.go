package agent

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/99apps-id/termixgo/internal/search"
)

// IndexLearnedContent refreshes the full-text documents for the two things the
// agent learns outside the workspace: the memory files and the error journal.
//
// It is the one place that knows how those files map to index documents, so the
// startup path and the remember tool agree. A file that has gone missing is
// removed from the index rather than left behind as a stale document; one that
// cannot be read for another reason keeps whatever the index already held.
//
// The workspace file index is not touched here: the store's own loop refreshes
// it on a timer. The cost of this call is two small reads, which is why the
// remember tool can afford to run it after every write.
func IndexLearnedContent(store *search.Store, memory *Memory, journal *ErrorJournal) {
	if store == nil {
		return
	}
	if memory != nil {
		project, global := memory.Paths()
		indexFile(store, "memory", "memory.md", "Project Memory", project)
		indexFile(store, "memory", "global-memory.md", "Global Memory", global)
	}
	if journal != nil {
		indexFile(store, "journal", "error-journal.jsonl", "Error Journal", journal.Path())
	}
}

// indexFile indexes one file as one document, or removes the document when the
// file is gone.
func indexFile(store *search.Store, scope, documentPath, title, file string) {
	if store == nil || file == "" {
		return
	}
	data, err := os.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			_ = store.Remove(scope, documentPath)
		}
		return
	}
	_ = store.Index(scope, documentPath, title, string(data))
}

// IndexAgentDocs indexes the agent's own documents: the persona, the operator
// profile, the curated memory and every skill. A question about who the agent
// is or what it can do is then answered from a file with a citation, not from
// the model's imagination.
func IndexAgentDocs(store *search.Store, home string) {
	trimmed := strings.TrimSpace(home)
	if store == nil || trimmed == "" {
		return
	}
	indexFile(store, "agent", "SOUL.md", "Agent persona", filepath.Join(trimmed, "SOUL.md"))
	indexFile(store, "agent", "USER.md", "Operator profile", filepath.Join(trimmed, "USER.md"))
	indexFile(store, "agent", "memories/MEMORY.md", "Agent memory", filepath.Join(trimmed, "memories", "MEMORY.md"))
	entries, err := os.ReadDir(filepath.Join(trimmed, "skills"))
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		indexFile(store, "agent", "skills/"+name+"/SKILL.md", "Skill: "+name, filepath.Join(trimmed, "skills", name, "SKILL.md"))
	}
}
