package agent

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"context"
	"encoding/csv"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ----------------------------------------------------------------------------
// read_document tool
// ----------------------------------------------------------------------------

type readDocumentTool struct{}

func (t *readDocumentTool) Name() string { return "read_document" }
func (t *readDocumentTool) Aliases() []string {
	return []string{"read_doc", "doc_read", "read_pdf", "read_excel", "read_docx"}
}
func (t *readDocumentTool) Mutating() bool { return false }
func (t *readDocumentTool) Risk() Risk     { return RiskEdit }

func (t *readDocumentTool) Label(a map[string]any) string {
	return "Reading document " + displayName(a)
}

func (t *readDocumentTool) DoneLabel(a map[string]any) string {
	return "Read document " + displayName(a)
}

func (t *readDocumentTool) Description() string {
	return "Read and extract text, tables, or data from document files including PDF (.pdf), Microsoft Word (.docx), Microsoft Excel (.xlsx), CSV (.csv, .tsv), and plain text documents."
}

func (t *readDocumentTool) Schema() map[string]any {
	return object(map[string]any{
		"path":      strProp("File path, absolute or relative to the workspace."),
		"max_chars": intProp("Maximum characters to return (defaults to 50000)."),
	}, "path")
}

func (t *readDocumentTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	rel := argString(args, "path", "file")
	if rel == "" {
		return Result{Output: "path is required", IsError: true}, nil
	}
	path := resolvePath(env, rel)
	if err := checkWorkspacePath(env, path); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	maxChars := argInt(args, "max_chars", 50000, 1000, 200000)

	ext := strings.ToLower(filepath.Ext(path))
	var content string
	var err error

	switch ext {
	case ".pdf":
		content, err = extractPDFText(ctx, path)
	case ".docx":
		content, err = extractDOCXText(path)
	case ".xlsx":
		content, err = extractXLSXText(path)
	case ".csv", ".tsv":
		content, err = extractCSVText(path, ext == ".tsv")
	case ".pptx", ".epub", ".html", ".htm":
		// The formats the shared extractor knows and this switch does not:
		// reusing it keeps one parser for the tool and the TUI attachment path.
		doc, docErr := ExtractDocument(path)
		content, err = doc.Text, docErr
	default:
		// Text file fallback
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return Result{Output: fmt.Sprintf("cannot read file %s: %v", rel, readErr), IsError: true}, nil
		}
		content = string(data)
	}

	if err != nil {
		return Result{Output: fmt.Sprintf("error reading document %s: %v", rel, err), IsError: true}, nil
	}

	if len(content) > maxChars {
		content = clipBytes(content, maxChars) + fmt.Sprintf("\n... [truncated to %d bytes; total %d]", maxChars, len(content))
	}
	return Result{Output: content}, nil
}

// ----------------------------------------------------------------------------
// create_office_document tool
// ----------------------------------------------------------------------------

type createOfficeDocumentTool struct{}

func (t *createOfficeDocumentTool) Name() string { return "create_office_document" }
func (t *createOfficeDocumentTool) Aliases() []string {
	return []string{"create_document", "make_docx", "make_excel", "write_office_document"}
}
func (t *createOfficeDocumentTool) Mutating() bool { return true }
func (t *createOfficeDocumentTool) Risk() Risk     { return RiskEdit }

func (t *createOfficeDocumentTool) Label(a map[string]any) string {
	return "Creating office document " + displayName(a)
}

func (t *createOfficeDocumentTool) DoneLabel(a map[string]any) string {
	return "Created office document " + displayName(a)
}

func (t *createOfficeDocumentTool) Description() string {
	return "Create standard Office documents including Word documents (.docx), Excel spreadsheets (.xlsx), and CSV files (.csv). For Word, supply markdown or plain text in content. For Excel, supply sheets or rows data."
}

func (t *createOfficeDocumentTool) Schema() map[string]any {
	return object(map[string]any{
		"path":    strProp("Destination file path (.docx, .xlsx, or .csv)."),
		"title":   strProp("Document title or sheet name."),
		"content": strProp("Document content (paragraphs, markdown headings #, bullet lists, or text for .docx / .csv)."),
		"rows": arrayProp("Array of row arrays for spreadsheets, e.g. [[\"Header1\", \"Header2\"], [\"Val1\", \"Val2\"]]", map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "string",
			},
		}),
		"sheets": arrayProp("For .xlsx with several worksheets: [{\"name\": \"Sales\", \"rows\": [[\"Month\", \"Total\"], [\"Jan\", \"1200\"]]}]. Numbers are stored as numbers; the first row is bold and frozen.", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
				"rows": map[string]any{"type": "array", "items": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}},
			},
		}),
	}, "path")
}

// parseSheetsArg reads [{"name": "...", "rows": [[...], ...]}, ...].
func parseSheetsArg(raw any) []xlsxSheet {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	var sheets []xlsxSheet
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		sheet := xlsxSheet{Name: fmt.Sprint(entry["name"])}
		if entry["name"] == nil {
			sheet.Name = ""
		}
		if rows, ok := entry["rows"].([]any); ok {
			for _, row := range rows {
				cells, ok := row.([]any)
				if !ok {
					continue
				}
				values := make([]string, len(cells))
				for i, cell := range cells {
					if cell == nil {
						continue
					}
					values[i] = fmt.Sprint(cell)
				}
				sheet.Rows = append(sheet.Rows, values)
			}
		}
		sheets = append(sheets, sheet)
	}
	return sheets
}

func (t *createOfficeDocumentTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	rel := argString(args, "path", "file")
	if rel == "" {
		return Result{Output: "path is required", IsError: true}, nil
	}
	path := resolvePath(env, rel)
	if err := checkWorkspacePath(env, path); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Result{Output: fmt.Sprintf("cannot create parent directory: %v", err), IsError: true}, nil
	}

	title := argString(args, "title")
	content := argString(args, "content", "text")
	ext := strings.ToLower(filepath.Ext(path))

	var rawRows [][]string
	if rowsArg, ok := args["rows"].([]any); ok {
		for _, r := range rowsArg {
			if rSlice, ok := r.([]any); ok {
				rowStr := make([]string, len(rSlice))
				for i, v := range rSlice {
					rowStr[i] = fmt.Sprint(v)
				}
				rawRows = append(rawRows, rowStr)
			}
		}
	}

	switch ext {
	case ".docx":
		if err := writeDOCX(path, title, content, rawRows); err != nil {
			return Result{Output: fmt.Sprintf("failed to create docx: %v", err), IsError: true}, nil
		}
		return Result{Output: fmt.Sprintf("Successfully created Word document %s", rel)}, nil

	case ".xlsx":
		if sheets := parseSheetsArg(args["sheets"]); len(sheets) > 0 {
			if err := writeXLSXSheets(path, sheets); err != nil {
				return Result{Output: fmt.Sprintf("failed to create xlsx: %v", err), IsError: true}, nil
			}
			total := 0
			for _, sheet := range sheets {
				total += len(sheet.Rows)
			}
			return Result{Output: fmt.Sprintf("Successfully created Excel workbook %s with %d sheet(s) and %d rows", rel, len(sheets), total)}, nil
		}
		sheetName := title
		if sheetName == "" {
			sheetName = "Sheet1"
		}
		if len(rawRows) == 0 && strings.TrimSpace(content) != "" {
			rawRows = parseDelimitedTable(content)
		}
		if len(rawRows) == 0 {
			rawRows = [][]string{{"Info"}, {"Document created by Termixgo"}}
		}
		if err := writeXLSX(path, sheetName, rawRows); err != nil {
			return Result{Output: fmt.Sprintf("failed to create xlsx: %v", err), IsError: true}, nil
		}
		return Result{Output: fmt.Sprintf("Successfully created Excel spreadsheet %s with %d rows", rel, len(rawRows))}, nil

	case ".csv":
		if len(rawRows) == 0 && strings.TrimSpace(content) != "" {
			rawRows = parseDelimitedTable(content)
		}
		f, err := os.Create(path)
		if err != nil {
			return Result{Output: fmt.Sprintf("cannot create file: %v", err), IsError: true}, nil
		}
		defer f.Close()
		writer := csv.NewWriter(f)
		if err := writer.WriteAll(rawRows); err != nil {
			return Result{Output: fmt.Sprintf("cannot write csv: %v", err), IsError: true}, nil
		}
		return Result{Output: fmt.Sprintf("Successfully created CSV document %s with %d rows", rel, len(rawRows))}, nil

	default:
		return Result{Output: fmt.Sprintf("unsupported extension %s; use .docx, .xlsx, or .csv", ext), IsError: true}, nil
	}
}

// ----------------------------------------------------------------------------
// convert_pdf_to_excel tool
// ----------------------------------------------------------------------------

type convertPDFToExcelTool struct{}

func (t *convertPDFToExcelTool) Name() string { return "convert_pdf_to_excel" }
func (t *convertPDFToExcelTool) Aliases() []string {
	return []string{"pdf_to_excel", "pdf2xlsx", "pdf_to_sheets"}
}
func (t *convertPDFToExcelTool) Mutating() bool { return true }
func (t *convertPDFToExcelTool) Risk() Risk     { return RiskEdit }

func (t *convertPDFToExcelTool) Label(a map[string]any) string {
	return "Converting PDF to Excel " + displayName(a)
}

func (t *convertPDFToExcelTool) DoneLabel(a map[string]any) string {
	return "Converted PDF to Excel " + displayName(a)
}

func (t *convertPDFToExcelTool) Description() string {
	return "Extract tabular data from a PDF document and convert/export it into an Excel spreadsheet (.xlsx) or CSV (.csv)."
}

func (t *convertPDFToExcelTool) Schema() map[string]any {
	return object(map[string]any{
		"pdf_path":    strProp("Source PDF file path."),
		"output_path": strProp("Destination .xlsx or .csv path (defaults to replacing .pdf with .xlsx)."),
		"sheet_name":  strProp("Excel sheet name (defaults to 'Data')."),
	}, "pdf_path")
}

func (t *convertPDFToExcelTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	relPDF := argString(args, "pdf_path", "path", "file")
	if relPDF == "" {
		return Result{Output: "pdf_path is required", IsError: true}, nil
	}
	pdfPath := resolvePath(env, relPDF)
	if err := checkWorkspacePath(env, pdfPath); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}

	relOut := argString(args, "output_path", "out")
	if relOut == "" {
		relOut = strings.TrimSuffix(relPDF, filepath.Ext(relPDF)) + ".xlsx"
	}
	outPath := resolvePath(env, relOut)
	if err := checkWorkspacePath(env, outPath); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}

	sheetName := argString(args, "sheet_name")
	if sheetName == "" {
		sheetName = "Data"
	}

	text, err := extractPDFText(ctx, pdfPath)
	if err != nil {
		return Result{Output: fmt.Sprintf("failed to extract PDF %s: %v", relPDF, err), IsError: true}, nil
	}

	rows := parseDelimitedTable(text)
	if len(rows) == 0 {
		return Result{Output: fmt.Sprintf("no tabular text rows found in %s", relPDF), IsError: true}, nil
	}

	ext := strings.ToLower(filepath.Ext(outPath))
	if ext == ".csv" {
		f, err := os.Create(outPath)
		if err != nil {
			return Result{Output: fmt.Sprintf("cannot create file: %v", err), IsError: true}, nil
		}
		defer f.Close()
		w := csv.NewWriter(f)
		if err := w.WriteAll(rows); err != nil {
			return Result{Output: fmt.Sprintf("cannot write csv: %v", err), IsError: true}, nil
		}
	} else {
		if err := writeXLSX(outPath, sheetName, rows); err != nil {
			return Result{Output: fmt.Sprintf("cannot create excel file: %v", err), IsError: true}, nil
		}
	}

	return Result{
		Output: fmt.Sprintf("Successfully converted %s to %s (%d rows extracted)", relPDF, relOut, len(rows)),
	}, nil
}

// ----------------------------------------------------------------------------
// Helpers: Document Extractors & OpenXML Writers
// ----------------------------------------------------------------------------

func extractDOCXText(path string) (string, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return "", err
	}
	defer r.Close()

	for _, f := range r.File {
		if f.Name == "word/document.xml" {
			rc, err := f.Open()
			if err != nil {
				return "", err
			}
			defer rc.Close()

			data, err := io.ReadAll(rc)
			if err != nil {
				return "", err
			}
			return parseWordXML(data), nil
		}
	}
	return "", errors.New("invalid docx file: word/document.xml not found")
}

func parseWordXML(data []byte) string {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var out strings.Builder
	inParagraph := false

	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		switch elem := token.(type) {
		case xml.StartElement:
			if elem.Name.Local == "p" {
				inParagraph = true
			}
		case xml.EndElement:
			if elem.Name.Local == "p" {
				out.WriteString("\n")
				inParagraph = false
			} else if elem.Name.Local == "tab" {
				out.WriteString("\t")
			}
		case xml.CharData:
			if inParagraph {
				out.Write(elem)
			}
		}
	}
	return strings.TrimSpace(out.String())
}

func extractXLSXText(path string) (string, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return "", err
	}
	defer r.Close()

	var sharedStrings []string
	var sheetData []byte

	for _, f := range r.File {
		if f.Name == "xl/sharedStrings.xml" {
			rc, err := f.Open()
			if err == nil {
				data, _ := io.ReadAll(rc)
				rc.Close()
				sharedStrings = parseSharedStrings(data)
			}
		} else if strings.HasPrefix(f.Name, "xl/worksheets/sheet") && strings.HasSuffix(f.Name, ".xml") && len(sheetData) == 0 {
			rc, err := f.Open()
			if err == nil {
				sheetData, _ = io.ReadAll(rc)
				rc.Close()
			}
		}
	}

	if len(sheetData) == 0 {
		return "", errors.New("no sheets found in xlsx")
	}

	return parseSheetXML(sheetData, sharedStrings), nil
}

func parseSharedStrings(data []byte) []string {
	var list []string
	decoder := xml.NewDecoder(bytes.NewReader(data))
	inT := false
	var buf strings.Builder

	for {
		tok, err := decoder.Token()
		if err != nil {
			break
		}
		switch elem := tok.(type) {
		case xml.StartElement:
			if elem.Name.Local == "t" {
				inT = true
				buf.Reset()
			}
		case xml.EndElement:
			if elem.Name.Local == "t" && inT {
				list = append(list, buf.String())
				inT = false
			}
		case xml.CharData:
			if inT {
				buf.Write(elem)
			}
		}
	}
	return list
}

func parseSheetXML(data []byte, sharedStrings []string) string {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var out strings.Builder
	var currentRow []string
	var cellType string
	var cellVal strings.Builder
	inV := false
	inIs := false

	flushRow := func() {
		if len(currentRow) > 0 {
			out.WriteString(strings.Join(currentRow, "\t") + "\n")
			currentRow = nil
		}
	}

	for {
		tok, err := decoder.Token()
		if err != nil {
			break
		}
		switch elem := tok.(type) {
		case xml.StartElement:
			switch elem.Name.Local {
			case "row":
				currentRow = nil
			case "c":
				cellType = ""
				for _, attr := range elem.Attr {
					if attr.Name.Local == "t" {
						cellType = attr.Value
					}
				}
				cellVal.Reset()
			case "v":
				inV = true
			case "is", "t":
				inIs = true
			}
		case xml.EndElement:
			switch elem.Name.Local {
			case "row":
				flushRow()
			case "v":
				inV = false
			case "is", "t":
				inIs = false
			case "c":
				val := cellVal.String()
				if cellType == "s" {
					if idx, err := strconv.Atoi(val); err == nil && idx >= 0 && idx < len(sharedStrings) {
						val = sharedStrings[idx]
					}
				}
				currentRow = append(currentRow, val)
			}
		case xml.CharData:
			if inV || inIs {
				cellVal.Write(elem)
			}
		}
	}
	flushRow()
	return strings.TrimSpace(out.String())
}

func extractCSVText(path string, isTSV bool) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	r := csv.NewReader(bytes.NewReader(data))
	if isTSV {
		r.Comma = '\t'
	}
	rows, err := r.ReadAll()
	if err != nil {
		// Return raw string if csv parsing fails
		return string(data), nil
	}
	var out strings.Builder
	for _, row := range rows {
		out.WriteString(strings.Join(row, "\t") + "\n")
	}
	return strings.TrimSpace(out.String()), nil
}

func extractPDFText(ctx context.Context, path string) (string, error) {
	// First attempt pdftotext if available on the system
	if bin, err := exec.LookPath("pdftotext"); err == nil {
		cmd := exec.CommandContext(ctx, bin, "-layout", path, "-")
		if out, err := cmd.Output(); err == nil && len(bytes.TrimSpace(out)) > 0 {
			return string(out), nil
		}
	}

	// Python fallback if pypdf or pdfplumber is installed
	if py, err := exec.LookPath("python3"); err == nil {
		code := fmt.Sprintf(`
import sys
try:
    from pypdf import PdfReader
    r = PdfReader(%q)
    print("\n".join(p.extract_text() or "" for p in r.pages))
    sys.exit(0)
except Exception:
    pass
`, path)
		cmd := exec.CommandContext(ctx, py, "-c", code)
		if out, err := cmd.Output(); err == nil && len(bytes.TrimSpace(out)) > 0 {
			return string(out), nil
		}
	}

	// Pure Go basic stream text extraction
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return extractPureGoPDF(data), nil
}

func extractPureGoPDF(data []byte) string {
	var lines []string
	streamRegex := regexp.MustCompile(`(?s)stream\r?\n(.*?)endstream`)
	matches := streamRegex.FindAllSubmatch(data, -1)

	for _, m := range matches {
		streamBytes := m[1]
		// Try zlib decompression
		zr, err := zlib.NewReader(bytes.NewReader(streamBytes))
		var decompressed []byte
		if err == nil {
			decompressed, _ = io.ReadAll(zr)
			zr.Close()
		} else {
			decompressed = streamBytes
		}

		text := extractPDFOperators(decompressed)
		if strings.TrimSpace(text) != "" {
			lines = append(lines, text)
		}
	}

	if len(lines) == 0 {
		// Fallback regex over raw data
		text := extractPDFOperators(data)
		if strings.TrimSpace(text) != "" {
			lines = append(lines, text)
		} else if utf8.Valid(data) {
			lines = append(lines, string(data))
		}
	}

	return strings.Join(lines, "\n")
}

func extractPDFOperators(data []byte) string {
	var out strings.Builder
	// Match (text) Tj
	tjRegex := regexp.MustCompile(`\((.*?)\)\s*Tj`)
	for _, m := range tjRegex.FindAllSubmatch(data, -1) {
		out.Write(m[1])
		out.WriteString(" ")
	}
	// Match [(t)(ext)] TJ
	bigTjRegex := regexp.MustCompile(`\[(.*?)\]\s*TJ`)
	innerRegex := regexp.MustCompile(`\((.*?)\)`)
	for _, m := range bigTjRegex.FindAllSubmatch(data, -1) {
		inners := innerRegex.FindAllSubmatch(m[1], -1)
		for _, in := range inners {
			out.Write(in[1])
		}
		out.WriteString(" ")
	}
	return strings.TrimSpace(out.String())
}

func parseDelimitedTable(text string) [][]string {
	var rows [][]string
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Split by tab, pipe, or multiple spaces
		var cols []string
		if strings.Contains(trimmed, "\t") {
			cols = strings.Split(trimmed, "\t")
		} else if strings.Contains(trimmed, "|") {
			raw := strings.Split(trimmed, "|")
			for _, c := range raw {
				ct := strings.TrimSpace(c)
				if ct != "" && !strings.HasPrefix(ct, "---") {
					cols = append(cols, ct)
				}
			}
		} else if strings.Contains(trimmed, ",") {
			r := csv.NewReader(strings.NewReader(trimmed))
			if rec, err := r.Read(); err == nil {
				cols = rec
			}
		}
		if len(cols) == 0 {
			cols = regexp.MustCompile(`\s{2,}`).Split(trimmed, -1)
		}
		if len(cols) > 0 {
			cleanCols := make([]string, len(cols))
			for i, c := range cols {
				cleanCols[i] = strings.TrimSpace(c)
			}
			rows = append(rows, cleanCols)
		}
	}
	return rows
}

// ----------------------------------------------------------------------------
// OpenXML Builders for .docx and .xlsx
// ----------------------------------------------------------------------------

func writeDOCX(path, title, content string, tables [][]string) error {
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)

	// [Content_Types].xml
	w, err := zw.Create("[Content_Types].xml")
	if err != nil {
		return err
	}
	io.WriteString(w, `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
</Types>`)

	// _rels/.rels
	w, err = zw.Create("_rels/.rels")
	if err != nil {
		return err
	}
	io.WriteString(w, `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`)

	// word/document.xml
	w, err = zw.Create("word/document.xml")
	if err != nil {
		return err
	}

	var body strings.Builder
	if title != "" {
		body.WriteString(fmt.Sprintf(`<w:p><w:pPr><w:jc w:val="center"/></w:pPr><w:r><w:rPr><w:b/><w:sz w:val="36"/></w:rPr><w:t>%s</w:t></w:r></w:p>`, xmlEscape(title)))
	}

	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "# ") {
			text := strings.TrimPrefix(trimmed, "# ")
			body.WriteString(fmt.Sprintf(`<w:p><w:r><w:rPr><w:b/><w:sz w:val="30"/></w:rPr><w:t>%s</w:t></w:r></w:p>`, xmlEscape(text)))
		} else if strings.HasPrefix(trimmed, "## ") {
			text := strings.TrimPrefix(trimmed, "## ")
			body.WriteString(fmt.Sprintf(`<w:p><w:r><w:rPr><w:b/><w:sz w:val="26"/></w:rPr><w:t>%s</w:t></w:r></w:p>`, xmlEscape(text)))
		} else if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
			text := strings.TrimPrefix(strings.TrimPrefix(trimmed, "- "), "* ")
			body.WriteString(fmt.Sprintf(`<w:p><w:r><w:t>• %s</w:t></w:r></w:p>`, xmlEscape(text)))
		} else {
			body.WriteString(fmt.Sprintf(`<w:p><w:r><w:t>%s</w:t></w:r></w:p>`, xmlEscape(trimmed)))
		}
	}

	if len(tables) > 0 {
		body.WriteString(`<w:tbl><w:tblPr><w:tblBorders><w:top w:val="single" w:sz="4" w:space="0" w:color="auto"/><w:left w:val="single" w:sz="4" w:space="0" w:color="auto"/><w:bottom w:val="single" w:sz="4" w:space="0" w:color="auto"/><w:right w:val="single" w:sz="4" w:space="0" w:color="auto"/></w:tblBorders></w:tblPr>`)
		for _, row := range tables {
			body.WriteString(`<w:tr>`)
			for _, cell := range row {
				body.WriteString(fmt.Sprintf(`<w:tc><w:p><w:r><w:t>%s</w:t></w:r></w:p></w:tc>`, xmlEscape(cell)))
			}
			body.WriteString(`</w:tr>`)
		}
		body.WriteString(`</w:tbl>`)
	}

	docXML := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>%s</w:body>
</w:document>`, body.String())

	if _, err := io.WriteString(w, docXML); err != nil {
		return err
	}

	if err := zw.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func writeXLSX(path, sheetName string, rows [][]string) error {
	return writeXLSXSheets(path, []xlsxSheet{{Name: sheetName, Rows: rows}})
}

// xlsxSheet is one worksheet of a workbook.
type xlsxSheet struct {
	Name string
	Rows [][]string
}

// xlsxSheetName makes a name Excel accepts: at most 31 characters, none of
// []:*?/\, and unique within the workbook.
func xlsxSheetName(name string, index int, used map[string]bool) string {
	name = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`[]:*?/\`, r) {
			return '-'
		}
		return r
	}, strings.TrimSpace(name))
	if name == "" {
		name = fmt.Sprintf("Sheet%d", index+1)
	}
	if utf8.RuneCountInString(name) > 31 {
		name = string([]rune(name)[:31])
	}
	base := name
	for suffix := 2; used[strings.ToLower(name)]; suffix++ {
		tail := fmt.Sprintf(" (%d)", suffix)
		runes := []rune(base)
		if len(runes)+len(tail) > 31 {
			runes = runes[:31-len(tail)]
		}
		name = string(runes) + tail
	}
	used[strings.ToLower(name)] = true
	return name
}

// xlsxNumber reports whether a cell should be stored as a number. A value
// with a leading zero, such as a phone number or a postcode, stays text so
// Excel does not strip the zero.
func xlsxNumber(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed != value {
		return false
	}
	digits := strings.TrimPrefix(trimmed, "-")
	if len(digits) > 1 && digits[0] == '0' && digits[1] != '.' {
		return false
	}
	if len(digits) > 15 {
		return false
	}
	_, err := strconv.ParseFloat(trimmed, 64)
	return err == nil && !strings.ContainsAny(trimmed, "eEinfINFnaNA")
}

// writeXLSXSheets writes a workbook with one worksheet per entry. The first
// row of every sheet is bold and frozen, because it is almost always the
// header.
func writeXLSXSheets(path string, sheets []xlsxSheet) error {
	if len(sheets) == 0 {
		sheets = []xlsxSheet{{Name: "Sheet1"}}
	}
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)
	write := func(name, body string) error {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = io.WriteString(w, body)
		return err
	}
	const header = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n"

	var overrides, rels, entries strings.Builder
	used := map[string]bool{}
	for index, sheet := range sheets {
		n := index + 1
		fmt.Fprintf(&overrides, `<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`, n)
		fmt.Fprintf(&rels, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>`, n, n)
		fmt.Fprintf(&entries, `<sheet name="%s" sheetId="%d" r:id="rId%d"/>`, xmlEscape(xlsxSheetName(sheet.Name, index, used)), n, n)
	}
	stylesID := len(sheets) + 1
	fmt.Fprintf(&rels, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>`, stylesID)

	parts := []struct{ name, body string }{
		{"[Content_Types].xml", header + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
			`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
			`<Default Extension="xml" ContentType="application/xml"/>` +
			`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>` +
			`<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>` +
			overrides.String() + `</Types>`},
		{"_rels/.rels", header + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`},
		{"xl/_rels/workbook.xml.rels", header + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` + rels.String() + `</Relationships>`},
		{"xl/workbook.xml", header + `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>` + entries.String() + `</sheets></workbook>`},
		{"xl/styles.xml", header + `<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">` +
			`<fonts count="2"><font><sz val="11"/><name val="Calibri"/></font><font><b/><sz val="11"/><name val="Calibri"/></font></fonts>` +
			`<fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills>` +
			`<borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders>` +
			`<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>` +
			`<cellXfs count="2"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/><xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="0" applyFont="1"/></cellXfs>` +
			`</styleSheet>`},
	}
	for _, part := range parts {
		if err := write(part.name, part.body); err != nil {
			return err
		}
	}
	for index, sheet := range sheets {
		if err := write(fmt.Sprintf("xl/worksheets/sheet%d.xml", index+1), xlsxSheetXML(sheet.Rows)); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// xlsxSheetXML renders one worksheet: numbers as numbers, text inline, the
// header row bold and frozen, and column widths fitted to the content.
func xlsxSheetXML(rows [][]string) string {
	widths := map[int]int{}
	for _, row := range rows {
		for c, value := range row {
			if w := utf8.RuneCountInString(value); w > widths[c] {
				widths[c] = w
			}
		}
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n")
	b.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)
	if len(rows) > 1 {
		b.WriteString(`<sheetViews><sheetView workbookViewId="0"><pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/></sheetView></sheetViews>`)
	}
	if len(widths) > 0 {
		b.WriteString(`<cols>`)
		for c := 0; c < len(widths)+1; c++ {
			w, ok := widths[c]
			if !ok {
				continue
			}
			width := w + 2
			if width < 8 {
				width = 8
			}
			if width > 60 {
				width = 60
			}
			fmt.Fprintf(&b, `<col min="%d" max="%d" width="%d" customWidth="1"/>`, c+1, c+1, width)
		}
		b.WriteString(`</cols>`)
	}
	b.WriteString(`<sheetData>`)
	for r, row := range rows {
		fmt.Fprintf(&b, `<row r="%d">`, r+1)
		style := ""
		if r == 0 && len(rows) > 1 {
			style = ` s="1"`
		}
		for c, value := range row {
			ref := fmt.Sprintf("%s%d", colIndexToLetters(c), r+1)
			if xlsxNumber(value) {
				fmt.Fprintf(&b, `<c r="%s"%s><v>%s</v></c>`, ref, style, value)
				continue
			}
			fmt.Fprintf(&b, `<c r="%s"%s t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>`, ref, style, xmlEscape(value))
		}
		b.WriteString(`</row>`)
	}
	b.WriteString(`</sheetData></worksheet>`)
	return b.String()
}

func colIndexToLetters(idx int) string {
	res := ""
	idx++
	for idx > 0 {
		rem := (idx - 1) % 26
		res = string(rune('A'+rem)) + res
		idx = (idx - 1) / 26
	}
	return res
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s))
	return b.String()
}
