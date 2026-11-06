package agent

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeZip(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	writer := zip.NewWriter(file)
	for name, body := range entries {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatalf("create entry %s: %v", name, err)
		}
		if _, err := entry.Write([]byte(body)); err != nil {
			t.Fatalf("write entry %s: %v", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}
}

func TestExtractDocumentText(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(path, []byte("first line\r\nsecond line\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := ExtractDocument(path)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if doc.Kind != "text" {
		t.Fatalf("kind = %q, want text", doc.Kind)
	}
	if doc.Text != "first line\nsecond line" {
		t.Fatalf("text = %q", doc.Text)
	}
}

func TestExtractDocumentRefusesBinary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fake.txt")
	if err := os.WriteFile(path, []byte("abc\x00def"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ExtractDocument(path); err == nil {
		t.Fatal("expected a binary refusal")
	}
}

func TestExtractDocumentDocx(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.docx")
	writeZip(t, path, map[string]string{
		"word/document.xml": `<w:document><w:body><w:p><w:r><w:t>Hello</w:t></w:r></w:p>` +
			`<w:p><w:r><w:t>World &amp; friends</w:t></w:r></w:p></w:body></w:document>`,
	})
	doc, err := ExtractDocument(path)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if doc.Kind != "docx" {
		t.Fatalf("kind = %q", doc.Kind)
	}
	if doc.Text != "Hello\nWorld & friends" {
		t.Fatalf("text = %q", doc.Text)
	}
}

func TestExtractDocumentXlsx(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sheet.xlsx")
	writeZip(t, path, map[string]string{
		"xl/sharedStrings.xml": `<sst><si><t>Name</t></si><si><t>Ada</t></si></sst>`,
		"xl/worksheets/sheet1.xml": `<worksheet><sheetData>` +
			`<row><c t="s"><v>0</v></c><c t="s"><v>1</v></c></row>` +
			`<row><c><v>42</v></c></row>` +
			`</sheetData></worksheet>`,
	})
	doc, err := ExtractDocument(path)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if doc.Kind != "xlsx" || doc.Pages != 1 {
		t.Fatalf("kind = %q pages = %d", doc.Kind, doc.Pages)
	}
	for _, want := range []string{"Name\tAda", "42"} {
		if !strings.Contains(doc.Text, want) {
			t.Fatalf("text %q missing %q", doc.Text, want)
		}
	}
}

func TestExtractDocumentPptx(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deck.pptx")
	writeZip(t, path, map[string]string{
		"ppt/slides/slide1.xml": `<p:sld><p:cSld><p:spTree><a:p><a:r><a:t>Quarterly plan</a:t></a:r></a:p>` +
			`<a:p><a:r><a:t>Second line</a:t></a:r></a:p></p:spTree></p:cSld></p:sld>`,
	})
	doc, err := ExtractDocument(path)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if doc.Pages != 1 {
		t.Fatalf("pages = %d", doc.Pages)
	}
	if !strings.Contains(doc.Text, "Quarterly plan") || !strings.Contains(doc.Text, "Second line") {
		t.Fatalf("text = %q", doc.Text)
	}
}

func TestExtractDocumentHTMLStripsTags(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "page.html")
	body := `<html><head><title>T</title><style>.x{}</style></head>` +
		`<body><p>Readable <b>text</b></p><script>ignore()</script></body></html>`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := ExtractDocument(path)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if !strings.Contains(doc.Text, "Readable") || strings.Contains(doc.Text, "ignore()") {
		t.Fatalf("text = %q", doc.Text)
	}
}

func TestExtractDocumentUnsupported(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "archive.rar")
	if err := os.WriteFile(path, []byte("not really"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ExtractDocument(path); err == nil {
		t.Fatal("expected an unsupported-type error")
	}
}

func TestExtractDocumentTruncates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("a", maxDocumentText+100)), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := ExtractDocument(path)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if !doc.Truncated {
		t.Fatal("expected truncation")
	}
	if len(doc.Text) > maxDocumentText+64 {
		t.Fatalf("text is %d bytes", len(doc.Text))
	}
}

func TestReadDocumentToolReadsPptx(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deck.pptx")
	writeZip(t, path, map[string]string{
		"ppt/slides/slide1.xml": `<p:sld><p:cSld><p:spTree><a:p><a:r><a:t>Quarterly plan</a:t></a:r></a:p></p:spTree></p:cSld></p:sld>`,
	})
	env := &Env{Workspace: dir}
	result, err := (&readDocumentTool{}).Run(context.Background(), env, map[string]any{"path": "deck.pptx"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Output)
	}
	if !strings.Contains(result.Output, "Quarterly plan") {
		t.Fatalf("output = %q", result.Output)
	}
}

func TestReadDocumentToolRejectsOutsidePath(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	path := filepath.Join(outside, "secret.md")
	if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := &Env{Workspace: workspace}
	result, err := (&readDocumentTool{}).Run(context.Background(), env, map[string]any{"path": path})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected the workspace guard to refuse")
	}
}

func TestImageMediaType(t *testing.T) {
	if media, ok := ImageMediaType("shot.PNG"); !ok || media != "image/png" {
		t.Fatalf("got %q %v", media, ok)
	}
	if _, ok := ImageMediaType("notes.md"); ok {
		t.Fatal("markdown is not an image")
	}
}
