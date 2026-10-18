package agent

import (
	"os"

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
