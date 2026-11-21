package lsp

import (
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Position is a zero-based line and UTF-16 character offset, as LSP sends it.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range is a half-open span between two positions.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Location is a range in one document.
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

// LocationLink is the richer answer some servers give for definition.
type LocationLink struct {
	TargetURI            string `json:"targetUri"`
	TargetRange          Range  `json:"targetRange"`
	TargetSelectionRange Range  `json:"targetSelectionRange"`
}

// Diagnostic is one problem a server reported.
type Diagnostic struct {
	Range    Range  `json:"range"`
	Severity int    `json:"severity"`
	Source   string `json:"source"`
	Message  string `json:"message"`
	Code     any    `json:"code"`
}

// SeverityName names an LSP severity.
func SeverityName(severity int) string {
	switch severity {
	case 1:
		return "error"
	case 2:
		return "warning"
	case 3:
		return "info"
	case 4:
		return "hint"
	default:
		return "issue"
	}
}

// DocumentSymbol is the hierarchical symbol shape.
type DocumentSymbol struct {
	Name           string           `json:"name"`
	Detail         string           `json:"detail"`
	Kind           int              `json:"kind"`
	Range          Range            `json:"range"`
	SelectionRange Range            `json:"selectionRange"`
	Children       []DocumentSymbol `json:"children"`
	// Location is set instead of the ranges by servers that answer with the
	// flat SymbolInformation shape.
	Location      *Location `json:"location"`
	ContainerName string    `json:"containerName"`
}

// TextEdit replaces a range with new text.
type TextEdit struct {
	Range   Range  `json:"range"`
	NewText string `json:"newText"`
}

// WorkspaceEdit is a rename answer in either of its two shapes.
type WorkspaceEdit struct {
	Changes         map[string][]TextEdit `json:"changes"`
	DocumentChanges []struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
		Edits []TextEdit `json:"edits"`
		// Kind is set on create, rename and delete operations, which are not
		// applied: a symbol rename only edits text.
		Kind string `json:"kind"`
	} `json:"documentChanges"`
}

// Files flattens the edit into per-file text edits.
func (w WorkspaceEdit) Files() map[string][]TextEdit {
	out := map[string][]TextEdit{}
	for uri, edits := range w.Changes {
		out[URIToPath(uri)] = append(out[URIToPath(uri)], edits...)
	}
	for _, change := range w.DocumentChanges {
		if change.Kind != "" || change.TextDocument.URI == "" {
			continue
		}
		path := URIToPath(change.TextDocument.URI)
		out[path] = append(out[path], change.Edits...)
	}
	return out
}

// SymbolKindName names the common LSP symbol kinds.
func SymbolKindName(kind int) string {
	names := map[int]string{
		1: "file", 2: "module", 3: "namespace", 4: "package", 5: "class", 6: "method",
		7: "property", 8: "field", 9: "constructor", 10: "enum", 11: "interface",
		12: "function", 13: "variable", 14: "constant", 15: "string", 16: "number",
		17: "boolean", 18: "array", 19: "object", 20: "key", 21: "null",
		22: "enum member", 23: "struct", 24: "event", 25: "operator", 26: "type parameter",
	}
	if name, ok := names[kind]; ok {
		return name
	}
	return "symbol"
}

// PathToURI turns an absolute path into a file URI.
func PathToURI(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	slashed := filepath.ToSlash(abs)
	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	return (&url.URL{Scheme: "file", Path: slashed}).String()
}

// URIToPath turns a file URI back into a local path.
func URIToPath(uri string) string {
	parsed, err := url.Parse(uri)
	if err != nil || parsed.Scheme != "file" {
		return uri
	}
	path := parsed.Path
	if runtime.GOOS == "windows" {
		path = strings.TrimPrefix(path, "/")
	}
	return filepath.FromSlash(path)
}

// normalizeURI makes two spellings of one file compare equal: servers differ
// on drive letter case and on escaping the colon.
func normalizeURI(uri string) string {
	path := URIToPath(uri)
	cleaned := filepath.Clean(path)
	if runtime.GOOS == "windows" {
		cleaned = strings.ToLower(cleaned)
	}
	return cleaned
}

// UTF16Column converts a byte offset within line to a UTF-16 offset.
func UTF16Column(line string, byteOffset int) int {
	if byteOffset > len(line) {
		byteOffset = len(line)
	}
	units := 0
	for _, r := range line[:byteOffset] {
		units += len(utf16.Encode([]rune{r}))
	}
	return units
}

// ByteColumn converts a UTF-16 offset within line to a byte offset.
func ByteColumn(line string, utf16Offset int) int {
	units := 0
	for index, r := range line {
		if units >= utf16Offset {
			return index
		}
		units += len(utf16.Encode([]rune{r}))
	}
	return len(line)
}

// RuneColumn converts a UTF-16 offset within line to a one-based rune column,
// which is what a person counts.
func RuneColumn(line string, utf16Offset int) int {
	return utf8.RuneCountInString(line[:ByteColumn(line, utf16Offset)]) + 1
}

// ApplyEdits applies text edits to a document. Edits are applied from the
// end so earlier offsets stay valid; overlapping edits are rejected by the
// caller's server, so they are not checked again here.
func ApplyEdits(text string, edits []TextEdit) string {
	lines := splitKeepingEnds(text)
	offset := func(pos Position) int {
		total := 0
		for index := 0; index < pos.Line && index < len(lines); index++ {
			total += len(lines[index])
		}
		if pos.Line >= len(lines) {
			return len(text)
		}
		content := strings.TrimRight(lines[pos.Line], "\r\n")
		return total + ByteColumn(content, pos.Character)
	}
	type span struct {
		start, end int
		text       string
	}
	spans := make([]span, 0, len(edits))
	for _, edit := range edits {
		spans = append(spans, span{offset(edit.Range.Start), offset(edit.Range.End), edit.NewText})
	}
	// Sort descending by start, keeping the server's order for equal starts.
	for i := 1; i < len(spans); i++ {
		for j := i; j > 0 && spans[j].start > spans[j-1].start; j-- {
			spans[j], spans[j-1] = spans[j-1], spans[j]
		}
	}
	for _, s := range spans {
		if s.start < 0 || s.end > len(text) || s.start > s.end {
			continue
		}
		text = text[:s.start] + s.text + text[s.end:]
	}
	return text
}

func splitKeepingEnds(text string) []string {
	var lines []string
	for len(text) > 0 {
		index := strings.IndexByte(text, '\n')
		if index < 0 {
			lines = append(lines, text)
			break
		}
		lines = append(lines, text[:index+1])
		text = text[index+1:]
	}
	return lines
}
