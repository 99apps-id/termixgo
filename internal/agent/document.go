package agent

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// maxDocumentText bounds one extraction. A long report is meant to inform the
// turn, not to crowd out the conversation that asked for it.
const maxDocumentText = 200 * 1024

// maxDocumentSource bounds the file read from disk before extraction, so a
// mislabelled archive cannot be pulled into memory whole.
const maxDocumentSource = 64 * 1024 * 1024

// Document is the text one file yielded, with the container it came from.
type Document struct {
	// Path is the file that was read.
	Path string
	// Kind names the container: text, html, pdf, docx, xlsx, pptx or epub.
	Kind string
	// Text is the extracted body, clipped to maxDocumentText.
	Text string
	// Pages is the page or sheet or slide count when the container has one.
	Pages int
	// Truncated reports that Text was clipped.
	Truncated bool
}

var documentTagRE = regexp.MustCompile(`(?s)<[^>]+>`)
var documentBlankRE = regexp.MustCompile(`\n{3,}`)

// textDocumentExtensions are containers that are already text. A code file is
// included on purpose: a pasted or attached source file is a document the
// operator means the agent to read.
var textDocumentExtensions = map[string]bool{
	".txt": true, ".md": true, ".markdown": true, ".rst": true, ".log": true,
	".json": true, ".jsonl": true, ".ndjson": true, ".yaml": true, ".yml": true,
	".toml": true, ".ini": true, ".cfg": true, ".conf": true, ".env": true,
	".csv": true, ".tsv": true, ".sql": true, ".xml": true,
	".go": true, ".py": true, ".js": true, ".mjs": true, ".cjs": true,
	".ts": true, ".tsx": true, ".jsx": true, ".java": true, ".kt": true,
	".c": true, ".h": true, ".cc": true, ".cpp": true, ".hpp": true,
	".rs": true, ".rb": true, ".php": true, ".sh": true, ".bash": true,
	".zsh": true, ".ps1": true, ".bat": true, ".cmd": true, ".swift": true,
	".scala": true, ".cs": true, ".lua": true, ".r": true, ".pl": true,
	".dart": true, ".vue": true, ".svelte": true, ".proto": true,
	".graphql": true, ".gql": true,
}

// ExtractDocument reads one file and returns its text. The work is local: PDF
// uses pdftotext when poppler is installed, and the Office containers are read
// with the standard library, so no build gains a cgo dependency. A container
// with no text layer is an error that names the way forward rather than an
// empty success the caller would read as "this document says nothing".
func ExtractDocument(path string) (Document, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Document{}, fmt.Errorf("cannot read %s: %w", path, err)
	}
	if info.IsDir() {
		return Document{}, fmt.Errorf("%s is a directory", path)
	}
	if info.Size() > maxDocumentSource {
		return Document{}, fmt.Errorf("%s is %d bytes, over the %d byte document cap", path, info.Size(), maxDocumentSource)
	}

	doc := Document{Path: path}
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".pdf":
		text, pages, err := extractPDF(path)
		if err != nil {
			return Document{}, err
		}
		doc.Kind = "pdf"
		doc.Text = text
		doc.Pages = pages
	case ".docx":
		text, err := extractDOCX(path)
		if err != nil {
			return Document{}, err
		}
		doc.Kind = "docx"
		doc.Text = text
	case ".xlsx":
		text, sheets, err := extractXLSX(path)
		if err != nil {
			return Document{}, err
		}
		doc.Kind = "xlsx"
		doc.Text = text
		doc.Pages = sheets
	case ".pptx":
		text, slides, err := extractPPTX(path)
		if err != nil {
			return Document{}, err
		}
		doc.Kind = "pptx"
		doc.Text = text
		doc.Pages = slides
	case ".epub":
		text, err := extractEPUB(path)
		if err != nil {
			return Document{}, err
		}
		doc.Kind = "epub"
		doc.Text = text
	case ".html", ".htm":
		data, err := readDocumentSource(path, info.Size())
		if err != nil {
			return Document{}, err
		}
		text, _ := parseBrowserPage(string(data))
		doc.Kind = "html"
		doc.Text = text
	case ".doc", ".xls", ".ppt", ".odt", ".ods", ".odp", ".rtf":
		return Document{}, fmt.Errorf("%s is a legacy or OpenDocument format: save it as .docx, .xlsx, .pptx or .pdf first", ext)
	default:
		if !textDocumentExtensions[ext] {
			return Document{}, fmt.Errorf("unsupported document type %q: supported are text and code files, .pdf, .docx, .xlsx, .pptx, .epub and .html", ext)
		}
		data, err := readDocumentSource(path, info.Size())
		if err != nil {
			return Document{}, err
		}
		doc.Kind = "text"
		doc.Text = string(data)
	}

	doc.Text = tidyDocumentText(doc.Text)
	if len(doc.Text) > maxDocumentText {
		doc.Text = clipDocumentText(doc.Text, maxDocumentText)
		doc.Truncated = true
	}
	if strings.TrimSpace(doc.Text) == "" {
		return Document{}, fmt.Errorf("%s yielded no text: it may be empty, or a scan that needs OCR or a vision model", filepath.Base(path))
	}
	return doc, nil
}

// readDocumentSource reads a text container and refuses a binary one: a .docx
// renamed to .txt should name the mismatch, not decode as mojibake.
func readDocumentSource(path string, size int64) ([]byte, error) {
	if size > maxDocumentSource {
		return nil, fmt.Errorf("%s is %d bytes, over the %d byte document cap", path, size, maxDocumentSource)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", path, err)
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return nil, fmt.Errorf("%s looks binary: use read_image for a picture, or convert the file to text first", path)
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("%s is not valid UTF-8 text", path)
	}
	return data, nil
}

// extractPDF shells out to pdftotext. A pure-Go PDF parser is a large
// dependency for a text layer that poppler already reads well, so a machine
// without poppler gets a named install hint instead of a silent empty result.
func extractPDF(path string) (string, int, error) {
	binary, err := exec.LookPath("pdftotext")
	if err != nil {
		return "", 0, errors.New("pdftotext is not installed: install poppler-utils (or poppler on macOS), or send the PDF through a chat channel so a vision model can read it")
	}
	var stderr bytes.Buffer
	command := exec.Command(binary, "-layout", "-enc", "UTF-8", path, "-")
	command.Stderr = &stderr
	out, err := command.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", 0, fmt.Errorf("pdftotext failed: %s", detail)
	}
	raw := string(out)
	text := strings.TrimSpace(strings.ReplaceAll(raw, "\f", "\n\n"))
	if text == "" {
		return "", 0, errors.New("the PDF has no text layer, which means it is a scan: attach it as an image for a vision model, or run OCR on its pages")
	}
	return text, strings.Count(raw, "\f"), nil
}

// extractDOCX reads word/document.xml and keeps paragraph boundaries.
func extractDOCX(path string) (string, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return "", fmt.Errorf("cannot open %s as a .docx: %w", filepath.Base(path), err)
	}
	defer reader.Close()
	entry := findZipEntry(reader.File, "word/document.xml")
	if entry == nil {
		return "", fmt.Errorf("%s is not a Word document: word/document.xml is missing", filepath.Base(path))
	}
	raw, err := readZipEntry(entry)
	if err != nil {
		return "", err
	}
	replacer := strings.NewReplacer(
		"</w:p>", "\n",
		"<w:br/>", "\n",
		"<w:br />", "\n",
		"<w:tab/>", "\t",
		"<w:tab />", "\t",
		"</w:tr>", "\n",
		"</w:tc>", "\t",
	)
	text := replacer.Replace(string(raw))
	text = documentTagRE.ReplaceAllString(text, "")
	return html.UnescapeString(text), nil
}

// extractXLSX reads every worksheet, using the shared string table for the
// cells that reference it.
func extractXLSX(path string) (string, int, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return "", 0, fmt.Errorf("cannot open %s as a .xlsx: %w", filepath.Base(path), err)
	}
	defer reader.Close()

	shared := parseSharedStrings(reader.File)
	var sheets []*zip.File
	for _, entry := range reader.File {
		if strings.HasPrefix(entry.Name, "xl/worksheets/sheet") && strings.HasSuffix(entry.Name, ".xml") {
			sheets = append(sheets, entry)
		}
	}
	if len(sheets) == 0 {
		return "", 0, fmt.Errorf("%s is not a workbook: no worksheet was found", filepath.Base(path))
	}
	sort.Slice(sheets, func(i, j int) bool { return sheets[i].Name < sheets[j].Name })

	var out strings.Builder
	for index, sheet := range sheets {
		raw, err := readZipEntry(sheet)
		if err != nil {
			return "", 0, err
		}
		rows := parseWorksheetRows(raw, shared)
		fmt.Fprintf(&out, "## Sheet %d\n%s\n\n", index+1, strings.Join(rows, "\n"))
	}
	return out.String(), len(sheets), nil
}

// extractPPTX reads every slide, one paragraph per line.
func extractPPTX(path string) (string, int, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return "", 0, fmt.Errorf("cannot open %s as a .pptx: %w", filepath.Base(path), err)
	}
	defer reader.Close()

	var slides []*zip.File
	for _, entry := range reader.File {
		if strings.HasPrefix(entry.Name, "ppt/slides/slide") && strings.HasSuffix(entry.Name, ".xml") {
			slides = append(slides, entry)
		}
	}
	if len(slides) == 0 {
		return "", 0, fmt.Errorf("%s is not a presentation: no slide was found", filepath.Base(path))
	}
	sort.Slice(slides, func(i, j int) bool { return slides[i].Name < slides[j].Name })

	var out strings.Builder
	for index, slide := range slides {
		raw, err := readZipEntry(slide)
		if err != nil {
			return "", 0, err
		}
		fmt.Fprintf(&out, "## Slide %d\n%s\n\n", index+1, collectXMLParagraphs(raw, "t", "p"))
	}
	return out.String(), len(slides), nil
}

// extractEPUB reads the XHTML chapters in archive order.
func extractEPUB(path string) (string, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return "", fmt.Errorf("cannot open %s as an .epub: %w", filepath.Base(path), err)
	}
	defer reader.Close()

	var out strings.Builder
	chapters := 0
	for _, entry := range reader.File {
		lower := strings.ToLower(entry.Name)
		if !strings.HasSuffix(lower, ".xhtml") && !strings.HasSuffix(lower, ".html") && !strings.HasSuffix(lower, ".htm") {
			continue
		}
		raw, err := readZipEntry(entry)
		if err != nil {
			continue
		}
		text, _ := parseBrowserPage(string(raw))
		if strings.TrimSpace(text) == "" {
			continue
		}
		out.WriteString(text)
		out.WriteString("\n\n")
		chapters++
	}
	if chapters == 0 {
		return "", fmt.Errorf("%s holds no readable chapter", filepath.Base(path))
	}
	return out.String(), nil
}

// parseSharedStrings builds the string table an xlsx cell references by index.
func parseSharedStrings(files []*zip.File) []string {
	entry := findZipEntry(files, "xl/sharedStrings.xml")
	if entry == nil {
		return nil
	}
	raw, err := readZipEntry(entry)
	if err != nil {
		return nil
	}
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	var table []string
	var current strings.Builder
	depth := 0
	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		switch typed := token.(type) {
		case xml.StartElement:
			if typed.Name.Local == "si" {
				current.Reset()
				depth = 1
			} else if depth > 0 {
				depth++
			}
		case xml.CharData:
			if depth > 0 {
				current.Write(typed)
			}
		case xml.EndElement:
			if typed.Name.Local == "si" {
				table = append(table, current.String())
				depth = 0
			} else if depth > 0 {
				depth--
			}
		}
	}
	return table
}

// parseWorksheetRows renders one sheet as tab-separated rows.
func parseWorksheetRows(raw []byte, shared []string) []string {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	var rows []string
	var cells []string
	var cell strings.Builder
	cellType := ""
	inValue := false
	inRow := false
	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		switch typed := token.(type) {
		case xml.StartElement:
			switch typed.Name.Local {
			case "row":
				inRow = true
				cells = nil
			case "c":
				cellType = ""
				for _, attr := range typed.Attr {
					if attr.Name.Local == "t" {
						cellType = attr.Value
					}
				}
				cell.Reset()
			case "v", "t":
				inValue = true
			}
		case xml.CharData:
			if inValue {
				cell.Write(typed)
			}
		case xml.EndElement:
			switch typed.Name.Local {
			case "v", "t":
				inValue = false
			case "c":
				value := strings.TrimSpace(cell.String())
				if cellType == "s" {
					if index, err := parseInteger(value); err == nil && index >= 0 && index < len(shared) {
						value = shared[index]
					}
				}
				cells = append(cells, value)
			case "row":
				if inRow {
					rows = append(rows, strings.Join(cells, "\t"))
					inRow = false
				}
			}
		}
	}
	return rows
}

// collectXMLParagraphs keeps character data inside a text element and breaks
// the line at each paragraph boundary.
func collectXMLParagraphs(raw []byte, textLocal, paragraphLocal string) string {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	var out strings.Builder
	inText := false
	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		switch typed := token.(type) {
		case xml.StartElement:
			if typed.Name.Local == textLocal {
				inText = true
			}
		case xml.CharData:
			if inText {
				out.Write(typed)
			}
		case xml.EndElement:
			if typed.Name.Local == textLocal {
				inText = false
			}
			if typed.Name.Local == paragraphLocal {
				out.WriteString("\n")
			}
		}
	}
	return out.String()
}

func findZipEntry(files []*zip.File, name string) *zip.File {
	for _, entry := range files {
		if entry.Name == name {
			return entry
		}
	}
	return nil
}

func readZipEntry(entry *zip.File) ([]byte, error) {
	handle, err := entry.Open()
	if err != nil {
		return nil, fmt.Errorf("cannot open %s: %w", entry.Name, err)
	}
	defer handle.Close()
	data, err := io.ReadAll(io.LimitReader(handle, maxDocumentSource))
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", entry.Name, err)
	}
	return data, nil
}

func parseInteger(value string) (int, error) {
	number := 0
	if value == "" {
		return 0, errors.New("empty number")
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return 0, errors.New("not a number")
		}
		number = number*10 + int(char-'0')
	}
	return number, nil
}

// tidyDocumentText normalises newlines and collapses the empty run an XML
// container leaves behind, so the text reads as prose rather than markup.
func tidyDocumentText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		lines[index] = strings.TrimRight(line, " \t")
	}
	text = strings.Join(lines, "\n")
	text = documentBlankRE.ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text)
}

// clipDocumentText cuts at a rune boundary, so a multi-byte character is never
// split into an invalid sequence the provider would reject.
func clipDocumentText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "\n... [document truncated]"
}
