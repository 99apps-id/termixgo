package agent

import (
	"context"
	"fmt"
	"strings"
)

// documentExtractTool reads a document the model cannot read as plain text:
// a PDF, a Word or Excel or PowerPoint file, an EPUB, or HTML.
type documentExtractTool struct{}

func (t *documentExtractTool) Name() string { return "document_extract" }
func (t *documentExtractTool) Aliases() []string {
	return []string{"read_document", "extract_document", "read_pdf", "read_docx", "read_xlsx", "read_pptx"}
}
func (t *documentExtractTool) Mutating() bool { return false }
func (t *documentExtractTool) Risk() Risk     { return RiskEdit }
func (t *documentExtractTool) Label(a map[string]any) string {
	return "Reading document " + displayName(a)
}
func (t *documentExtractTool) DoneLabel(a map[string]any) string {
	return "Read document " + displayName(a)
}
func (t *documentExtractTool) Description() string {
	return "Extract the text of a document in the workspace: PDF, Word (.docx), Excel (.xlsx), PowerPoint (.pptx), EPUB, HTML, or any text and code file. Use read_image for a picture and read_file for plain text. A scanned PDF has no text layer; read_image on its pages or an OCR tool is the way to see it."
}
func (t *documentExtractTool) Schema() map[string]any {
	return object(map[string]any{
		"path": strProp("Document path, absolute or relative to the workspace."),
	}, "path")
}

func (t *documentExtractTool) Run(_ context.Context, env *Env, args map[string]any) (Result, error) {
	raw := strings.TrimSpace(argString(args, "path"))
	if raw == "" {
		return Result{Output: "path is required", IsError: true}, nil
	}
	path := resolvePath(env, raw)
	if err := checkWorkspacePath(env, path); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	doc, err := ExtractDocument(path)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	note := ""
	if doc.Truncated {
		note = ", truncated"
	}
	pages := ""
	if doc.Pages > 0 {
		pages = fmt.Sprintf(", %d pages", doc.Pages)
	}
	return Result{Output: fmt.Sprintf("%s (%s%s, %d chars%s)\n\n%s",
		displayPath(env, path), doc.Kind, pages, len(doc.Text), note, doc.Text)}, nil
}
