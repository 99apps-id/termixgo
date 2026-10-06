// Package builtin carries the skills compiled into the binary.
//
// hallmark (design bar for new pages and redesigns) and impeccable (polish
// bar for refining an existing interface) ship as defaults so the agent
// consults them on every UI/UX build or refactor. Embedding keeps them
// available on a fresh install with no network and no skill folder, and a
// project or user skill of the same name still shadows the builtin one: the
// operator's explicit choice always wins.
package builtin

import "embed"

//go:embed *.md
var files embed.FS

// Names lists the builtin skill documents in a stable order.
var Names = []string{"hallmark", "impeccable"}

// Read returns one builtin document by skill name, or false when unknown.
func Read(name string) (string, bool) {
	data, err := files.ReadFile(name + ".md")
	if err != nil {
		return "", false
	}
	return string(data), true
}
