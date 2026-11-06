package agent

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// readPDFTool extracts text from a PDF file.
type readPDFTool struct{}

func (t *readPDFTool) Name() string      { return "read_pdf" }
func (t *readPDFTool) Aliases() []string { return []string{"pdf_text", "extract_pdf"} }
func (t *readPDFTool) Mutating() bool    { return false }
func (t *readPDFTool) Risk() Risk        { return RiskEdit }
func (t *readPDFTool) Description() string {
	return "Extract text from a PDF file using pdftotext or pdfplumber."
}
func (t *readPDFTool) Schema() map[string]any {
	return object(map[string]any{
		"path":  strProp("Path to the PDF file."),
		"pages": strProp("Optional page range, e.g., 1-5 or 1,3,5."),
	})
}
func (t *readPDFTool) Label(a map[string]any) string {
	return "Reading PDF " + Shorten(argString(a, "path"), 40)
}
func (t *readPDFTool) DoneLabel(a map[string]any) string {
	return "Read PDF " + Shorten(argString(a, "path"), 40)
}

func (t *readPDFTool) Run(_ context.Context, env *Env, args map[string]any) (Result, error) {
	path := argString(args, "path")
	if path == "" {
		return Result{Output: "path is required", IsError: true}, nil
	}
	if err := checkWorkspacePath(env, path); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}

	pages := argString(args, "pages")

	// Try pdftotext first.
	cmd := exec.Command("pdftotext")
	if pages != "" {
		cmd.Args = append(cmd.Args, "-l", pages)
	}
	cmd.Args = append(cmd.Args, path, "-")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return Result{Output: truncateOutput(string(out), 16000)}, nil
	}

	// Fallback: python with PyPDF2 or pdfminer.
	pyCmd := fmt.Sprintf("import sys; from pathlib import Path; p=Path(%q); text=''; "+
		"exec(open(p).read()) if False else None; "+
		"import importlib; "+
		"mods=['pypdf','PyPDF2','pdfminer']; "+
		"for m in mods: "+
		" try: mod=importlib.import_module(m); text=mod.__dict__.get('extract_text', lambda x: '')(str(p)); break; "+
		" except: pass; "+
		"print(text or '')", path)
	cmd = exec.Command("python", "-c", pyCmd)
	out, err = cmd.CombinedOutput()
	if err == nil && strings.TrimSpace(string(out)) != "" {
		return Result{Output: truncateOutput(string(out), 16000)}, nil
	}

	return Result{Output: fmt.Sprintf("PDF extraction failed. Install pdftotext (poppler) or python pypdf/pdfminer.\nError: %v", err), IsError: true}, nil
}
