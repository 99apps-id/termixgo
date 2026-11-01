package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDocumentToolsRegistration(t *testing.T) {
	reg := DefaultRegistry()
	tools := []string{"read_document", "create_office_document", "convert_pdf_to_excel"}
	for _, name := range tools {
		tool, ok := reg.Lookup(name)
		if !ok || tool == nil {
			t.Fatalf("expected tool %q to be registered in DefaultRegistry", name)
		}
	}
}

func TestCreateAndReadOfficeDocuments(t *testing.T) {
	tmpDir := t.TempDir()
	env := &Env{Workspace: tmpDir}
	ctx := context.Background()

	createTool := &createOfficeDocumentTool{}
	readTool := &readDocumentTool{}

	// Test 1: Word document (.docx)
	docxPath := "test_doc.docx"
	createRes, err := createTool.Run(ctx, env, map[string]any{
		"path":    docxPath,
		"title":   "Project Documentation",
		"content": "# Section 1\nThis is paragraph one.\n- Bullet point A\n- Bullet point B",
	})
	if err != nil || createRes.IsError {
		t.Fatalf("failed to create docx: %v, output: %s", err, createRes.Output)
	}

	readRes, err := readTool.Run(ctx, env, map[string]any{
		"path": docxPath,
	})
	if err != nil || readRes.IsError {
		t.Fatalf("failed to read docx: %v, output: %s", err, readRes.Output)
	}
	if !strings.Contains(readRes.Output, "Project Documentation") {
		t.Errorf("expected docx to contain title, got: %s", readRes.Output)
	}
	if !strings.Contains(readRes.Output, "Section 1") {
		t.Errorf("expected docx to contain section, got: %s", readRes.Output)
	}

	// Test 2: Excel spreadsheet (.xlsx)
	xlsxPath := "test_sheet.xlsx"
	createRes, err = createTool.Run(ctx, env, map[string]any{
		"path":  xlsxPath,
		"title": "Quarterly Sales",
		"rows": []any{
			[]any{"Product", "Q1", "Q2"},
			[]any{"Widget A", "100", "150"},
			[]any{"Widget B", "200", "250"},
		},
	})
	if err != nil || createRes.IsError {
		t.Fatalf("failed to create xlsx: %v, output: %s", err, createRes.Output)
	}

	readRes, err = readTool.Run(ctx, env, map[string]any{
		"path": xlsxPath,
	})
	if err != nil || readRes.IsError {
		t.Fatalf("failed to read xlsx: %v, output: %s", err, readRes.Output)
	}
	if !strings.Contains(readRes.Output, "Widget A") || !strings.Contains(readRes.Output, "250") {
		t.Errorf("expected xlsx to contain cells, got: %s", readRes.Output)
	}

	// Test 3: CSV document (.csv)
	csvPath := "test_data.csv"
	createRes, err = createTool.Run(ctx, env, map[string]any{
		"path": csvPath,
		"rows": []any{
			[]any{"ID", "Name", "Status"},
			[]any{"1", "Server Alpha", "Active"},
			[]any{"2", "Server Beta", "Idle"},
		},
	})
	if err != nil || createRes.IsError {
		t.Fatalf("failed to create csv: %v, output: %s", err, createRes.Output)
	}

	readRes, err = readTool.Run(ctx, env, map[string]any{
		"path": csvPath,
	})
	if err != nil || readRes.IsError {
		t.Fatalf("failed to read csv: %v, output: %s", err, readRes.Output)
	}
	if !strings.Contains(readRes.Output, "Server Alpha") {
		t.Errorf("expected csv to contain data, got: %s", readRes.Output)
	}
}

func TestConvertPDFToExcel(t *testing.T) {
	tmpDir := t.TempDir()
	env := &Env{Workspace: tmpDir}
	ctx := context.Background()

	// Write mock text representation or simple pdf to test parser
	mockPath := filepath.Join(tmpDir, "table.pdf")
	// Write plain text with tabular formatting
	err := os.WriteFile(mockPath, []byte("Name\tAge\tCity\nAlice\t30\nJakarta\nBob\t25\nBandung"), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	convTool := &convertPDFToExcelTool{}
	res, err := convTool.Run(ctx, env, map[string]any{
		"pdf_path":    "table.pdf",
		"output_path": "table.xlsx",
	})
	if err != nil || res.IsError {
		t.Fatalf("convert_pdf_to_excel failed: %v, output: %s", err, res.Output)
	}

	// Verify table.xlsx was created
	readTool := &readDocumentTool{}
	readRes, err := readTool.Run(ctx, env, map[string]any{
		"path": "table.xlsx",
	})
	if err != nil || readRes.IsError {
		t.Fatalf("failed to read generated xlsx: %v, output: %s", err, readRes.Output)
	}
	if !strings.Contains(readRes.Output, "Alice") {
		t.Errorf("expected converted excel to contain data, got: %s", readRes.Output)
	}
}
