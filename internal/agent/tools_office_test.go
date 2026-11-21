package agent

import (
	"archive/zip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func zipPart(t *testing.T, path, name string) string {
	t.Helper()
	reader, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	for _, file := range reader.File {
		if file.Name == name {
			rc, _ := file.Open()
			data, _ := io.ReadAll(rc)
			rc.Close()
			return string(data)
		}
	}
	t.Fatalf("%s has no %s", path, name)
	return ""
}

func TestCreateWorkbookWithSeveralSheets(t *testing.T) {
	env := &Env{Workspace: t.TempDir(), Trusted: true}
	result, _ := (&createOfficeDocumentTool{}).Run(context.Background(), env, map[string]any{
		"path": "report.xlsx",
		"sheets": []any{
			map[string]any{"name": "Sales: 2026", "rows": []any{
				[]any{"Month", "Total", "Phone"},
				[]any{"Jan", "1200.5", "08123"},
			}},
			map[string]any{"name": "Notes", "rows": []any{[]any{"ok"}}},
		},
	})
	if result.IsError || !strings.Contains(result.Output, "2 sheet(s)") {
		t.Fatalf("result = %+v", result)
	}
	path := filepath.Join(env.Workspace, "report.xlsx")
	workbook := zipPart(t, path, "xl/workbook.xml")
	if !strings.Contains(workbook, `name="Sales- 2026"`) || !strings.Contains(workbook, `name="Notes"`) {
		t.Fatalf("sheet names should be sanitised and kept:\n%s", workbook)
	}
	sheet := zipPart(t, path, "xl/worksheets/sheet1.xml")
	if !strings.Contains(sheet, `<c r="B2"><v>1200.5</v></c>`) {
		t.Fatalf("a number should be stored as a number:\n%s", sheet)
	}
	if !strings.Contains(sheet, `08123`) || strings.Contains(sheet, `<v>08123</v>`) {
		t.Fatalf("a leading-zero value must stay text:\n%s", sheet)
	}
	if !strings.Contains(sheet, `state="frozen"`) || !strings.Contains(sheet, `s="1"`) {
		t.Fatalf("the header should be bold and frozen:\n%s", sheet)
	}
	if doc, err := ExtractDocument(path); err != nil || !strings.Contains(doc.Text, "Jan") {
		t.Fatalf("the workbook should read back: %v %q", err, doc.Text)
	}
}

func TestXLSXNumberDetection(t *testing.T) {
	for value, want := range map[string]bool{
		"12": true, "-3.5": true, "0.25": true, "0": true,
		"007": false, "1e5": false, "NaN": false, " 4": false, "": false, "1234567890123456": false,
	} {
		if got := xlsxNumber(value); got != want {
			t.Errorf("%q = %v, want %v", value, got, want)
		}
	}
}

// The real conversion runs only where LibreOffice or Microsoft Office is
// installed.
func TestConvertDocumentToPDF(t *testing.T) {
	if testing.Short() {
		t.Skip("starts an office suite")
	}
	if findLibreOffice() == "" && !officeAvailable("word") {
		t.Skip("no LibreOffice or Microsoft Word")
	}
	env := &Env{Workspace: t.TempDir(), Trusted: true}
	if err := writeDOCX(filepath.Join(env.Workspace, "letter.docx"), "Letter", "# Hello\n\nThis is a test.", nil); err != nil {
		t.Fatal(err)
	}
	result, _ := (&convertDocumentTool{}).Run(context.Background(), env, map[string]any{"path": "letter.docx", "format": "pdf"})
	if result.IsError {
		t.Fatalf("result = %+v", result)
	}
	data, err := os.ReadFile(filepath.Join(env.Workspace, "letter.pdf"))
	if err != nil || !strings.HasPrefix(string(data), "%PDF") {
		t.Fatalf("a PDF should be written: %v", err)
	}
	again, _ := (&convertDocumentTool{}).Run(context.Background(), env, map[string]any{"path": "letter.docx", "format": "pdf"})
	if !again.IsError || !strings.Contains(again.Output, "already exists") {
		t.Fatalf("an existing output must not be overwritten: %+v", again)
	}

	if findLibreOffice() == "" && !officeAvailable("excel") {
		return
	}
	if err := writeXLSXSheets(filepath.Join(env.Workspace, "book.xlsx"), []xlsxSheet{{Name: "Data", Rows: [][]string{{"a", "b"}, {"1", "2"}}}}); err != nil {
		t.Fatal(err)
	}
	sheet, _ := (&convertDocumentTool{}).Run(context.Background(), env, map[string]any{"path": "book.xlsx", "format": "pdf", "output": "out/book.pdf"})
	if sheet.IsError {
		t.Fatalf("workbook conversion = %+v", sheet)
	}
	if data, err := os.ReadFile(filepath.Join(env.Workspace, "out", "book.pdf")); err != nil || !strings.HasPrefix(string(data), "%PDF") {
		t.Fatalf("the workbook PDF should be written: %v", err)
	}
}

func TestConvertDocumentRejectsBadInput(t *testing.T) {
	env := &Env{Workspace: t.TempDir()}
	if result, _ := (&convertDocumentTool{}).Run(context.Background(), env, map[string]any{"path": "missing.docx", "format": "pdf"}); !result.IsError {
		t.Fatalf("a missing input should fail")
	}
	path := filepath.Join(env.Workspace, "a.docx")
	_ = os.WriteFile(path, []byte("x"), 0o644)
	if result, _ := (&convertDocumentTool{}).Run(context.Background(), env, map[string]any{"path": "a.docx", "format": "exe"}); !result.IsError {
		t.Fatalf("an unknown format should fail")
	}
	if result, _ := (&convertDocumentTool{}).Run(context.Background(), env, map[string]any{"path": "a.docx", "format": "docx"}); !result.IsError {
		t.Fatalf("converting onto the input itself should fail")
	}
}
