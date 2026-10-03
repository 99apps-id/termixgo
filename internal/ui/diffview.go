package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// diffLine is one parsed unified-diff row.
type diffLine struct {
	kind    string // "add", "del", "ctx", "hdr", "hunk"
	text    string // content without the leading marker
	raw     string // original line for width measurement
	oldNum  int    // old file line number, -1 when absent
	newNum  int    // new file line number, -1 when absent
	file    string // file the line belongs to
	hunkHdr string // hunk header the line belongs to
}

// diffFile groups parsed rows per file.
type diffFile struct {
	oldPath string
	newPath string
	label   string
	lines   []diffLine
	adds    int
	dels    int
}

// diffIsClean reports whether git_diff said there is nothing to show. The tool
// answers with "No changes."; testing for the word "clean" anywhere instead
// would also swallow a real diff that merely touches a file named cleanup.go.
func diffIsClean(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	return trimmed == "" || trimmed == "No changes."
}

// sanitizeDiffFiles drops terminal control characters from parsed diff text
// before it can reach the renderer. A diff carries file content, which is not
// trusted: an escape sequence inside a changed line would otherwise be obeyed
// by the terminal instead of shown.
func sanitizeDiffFiles(files []diffFile) []diffFile {
	for index := range files {
		files[index].label = sanitizeText(files[index].label)
		for line := range files[index].lines {
			files[index].lines[line].text = sanitizeText(files[index].lines[line].text)
			files[index].lines[line].raw = sanitizeText(files[index].lines[line].raw)
			files[index].lines[line].hunkHdr = sanitizeText(files[index].lines[line].hunkHdr)
		}
	}
	return files
}

// parseUnifiedDiff turns git diff output into files with numbered rows.
// It never fails: an unrecognised line becomes context so nothing is lost.
func parseUnifiedDiff(raw string) []diffFile {
	var files []diffFile
	var current *diffFile
	oldNum, newNum := 0, 0
	var hunkHdr string

	flush := func() {
		if current != nil {
			files = append(files, *current)
			current = nil
		}
	}

	for _, line := range strings.Split(raw, "\n") {
		if line == "" && current == nil {
			continue
		}
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
			current = &diffFile{}
			oldNum, newNum = 0, 0
			hunkHdr = ""
			current.lines = append(current.lines, diffLine{kind: "hdr", text: line, raw: line, oldNum: -1, newNum: -1})
		case strings.HasPrefix(line, "--- "):
			if current == nil {
				current = &diffFile{}
			}
			current.oldPath = strings.TrimSpace(strings.TrimPrefix(line, "--- "))
			current.lines = append(current.lines, diffLine{kind: "hdr", text: line, raw: line, oldNum: -1, newNum: -1, file: current.label})
		case strings.HasPrefix(line, "+++ "):
			if current == nil {
				current = &diffFile{}
			}
			current.newPath = strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
			current.label = diffLabel(current.oldPath, current.newPath)
			for i := range current.lines {
				current.lines[i].file = current.label
			}
			current.lines = append(current.lines, diffLine{kind: "hdr", text: line, raw: line, oldNum: -1, newNum: -1, file: current.label})
		case strings.HasPrefix(line, "@@ "):
			hunkHdr = line
			oldNum, newNum = parseHunkHeader(line)
			if current == nil {
				current = &diffFile{}
			}
			current.lines = append(current.lines, diffLine{kind: "hunk", text: line, raw: line, oldNum: -1, newNum: -1, file: current.label, hunkHdr: line})
		case current == nil:
			current = &diffFile{label: ""}
			current.lines = append(current.lines, diffLine{kind: "ctx", text: line, raw: line, oldNum: -1, newNum: -1})
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			newNum++
			current.lines = append(current.lines, diffLine{kind: "add", text: line[1:], raw: line, oldNum: -1, newNum: newNum, file: current.label, hunkHdr: hunkHdr})
			current.adds++
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			oldNum++
			current.lines = append(current.lines, diffLine{kind: "del", text: line[1:], raw: line, oldNum: oldNum, newNum: -1, file: current.label, hunkHdr: hunkHdr})
			current.dels++
		case strings.HasPrefix(line, "\\ "):
			if current == nil {
				continue
			}
			current.lines = append(current.lines, diffLine{kind: "hdr", text: line, raw: line, oldNum: -1, newNum: -1, file: current.label, hunkHdr: hunkHdr})
		default:
			body := line
			if strings.HasPrefix(line, " ") {
				body = line[1:]
			}
			if line == "" {
				continue
			}
			oldNum++
			newNum++
			current.lines = append(current.lines, diffLine{kind: "ctx", text: body, raw: line, oldNum: oldNum, newNum: newNum, file: current.label, hunkHdr: hunkHdr})
		}
	}
	flush()
	for i := range files {
		if files[i].label == "" {
			files[i].label = diffLabel(files[i].oldPath, files[i].newPath)
		}
	}
	return files
}

// diffLabel prefers the new path, which is what the operator recognises.
func diffLabel(oldPath, newPath string) string {
	label := strings.TrimSpace(newPath)
	if label == "" || label == "/dev/null" {
		label = strings.TrimSpace(oldPath)
	}
	label = strings.TrimPrefix(label, "a/")
	label = strings.TrimPrefix(label, "b/")
	if strings.TrimSpace(label) == "" || label == "/dev/null" {
		return "(unknown file)"
	}
	return label
}

// parseHunkHeader reads "@@ -old,count +new,count @@" into start numbers.
func parseHunkHeader(line string) (int, int) {
	oldNum, newNum := 0, 0
	rest := strings.TrimSpace(strings.TrimPrefix(line, "@@"))
	parts := strings.Fields(rest)
	if len(parts) >= 2 {
		oldNum = atoiPrefix(strings.TrimPrefix(parts[0], "-"))
		newNum = atoiPrefix(strings.TrimPrefix(parts[1], "+"))
		oldNum--
		newNum--
	}
	return oldNum, newNum
}

// atoiPrefix reads the digits before an optional comma.
func atoiPrefix(text string) int {
	if index := strings.Index(text, ","); index >= 0 {
		text = text[:index]
	}
	num := 0
	for _, r := range text {
		if r < '0' || r > '9' {
			break
		}
		num = num*10 + int(r-'0')
	}
	return num
}

// diffNumWidth is the column width for one line number.
const diffNumWidth = 5

// renderDiffSideBySide draws two columns: old lines left, new lines right.
// Paired del/add rows share one visual row so a change reads as one edit,
// not two distant lines. Long lines are clipped to their column; headers
// and hunk markers span the full width.
func renderDiffSideBySide(files []diffFile, styles Styles, width, maxLines int) string {
	if width < 40 {
		width = 40
	}
	col := (width - 3) / 2
	if col < 12 {
		col = 12
	}
	var out []string
	count := 0
	emit := func(line string) bool {
		if maxLines > 0 && count >= maxLines {
			return false
		}
		out = append(out, line)
		count++
		return true
	}

	for _, file := range files {
		title := "  " + file.label
		if file.adds > 0 || file.dels > 0 {
			title += "  (+" + itoa(file.adds) + " -" + itoa(file.dels) + ")"
		}
		if !emit(styles.Heading.Render(truncate(title, width))) {
			break
		}
		rows := pairDiffRows(file.lines)
		for _, row := range rows {
			var line string
			switch row.span {
			case true:
				line = styles.Dim.Render(truncate("  "+row.left.text, width))
			default:
				left := renderDiffCell(row.left, col, styles, false)
				right := renderDiffCell(row.right, col, styles, true)
				line = left + styles.Dim.Render(" | ") + right
				line = truncateANSI(line, width)
			}
			if !emit(line) {
				break
			}
		}
	}
	return strings.Join(out, "\n")
}

// diffRow is one visual row: either a full-width span or an old/new pair.
type diffRow struct {
	span  bool
	left  diffLine
	right diffLine
}

// pairDiffRows aligns deletions with the additions that follow them.
func pairDiffRows(lines []diffLine) []diffRow {
	var rows []diffRow
	index := 0
	for index < len(lines) {
		line := lines[index]
		switch line.kind {
		case "hdr", "hunk":
			rows = append(rows, diffRow{span: true, left: line})
			index++
		case "del":
			var dels []diffLine
			for index < len(lines) && lines[index].kind == "del" {
				dels = append(dels, lines[index])
				index++
			}
			var adds []diffLine
			for index < len(lines) && lines[index].kind == "add" {
				adds = append(adds, lines[index])
				index++
			}
			size := len(dels)
			if len(adds) > size {
				size = len(adds)
			}
			for i := 0; i < size; i++ {
				row := diffRow{}
				if i < len(dels) {
					row.left = dels[i]
				}
				if i < len(adds) {
					row.right = adds[i]
				}
				rows = append(rows, row)
			}
		case "add":
			var adds []diffLine
			for index < len(lines) && lines[index].kind == "add" {
				adds = append(adds, lines[index])
				index++
			}
			for _, add := range adds {
				rows = append(rows, diffRow{right: add})
			}
		default:
			rows = append(rows, diffRow{left: line, right: line})
			index++
		}
	}
	return rows
}

// renderDiffCell draws one column with its line number.
func renderDiffCell(line diffLine, col int, styles Styles, isNew bool) string {
	num := line.oldNum
	if isNew {
		num = line.newNum
	}
	numText := ""
	if num > 0 {
		numText = itoa(num)
	}
	cell := " " + padLeft(numText, diffNumWidth) + " " + line.text
	plain := truncate(cell, col)
	switch line.kind {
	case "add":
		return styles.ToolDone.Render(plain)
	case "del":
		return styles.ToolError.Render(plain)
	case "hunk", "hdr":
		return styles.Dim.Render(plain)
	default:
		if strings.TrimSpace(line.text) == "" && num <= 0 {
			return strings.Repeat(" ", minInt(col, 8))
		}
		return styles.Dim.Render(plain)
	}
}

// renderDiffUnified draws the classic single column with numbers on both sides.
func renderDiffUnified(files []diffFile, styles Styles, width, maxLines int) string {
	var out []string
	count := 0
	for _, file := range files {
		head := "  " + file.label
		if file.adds > 0 || file.dels > 0 {
			head += "  (+" + itoa(file.adds) + " -" + itoa(file.dels) + ")"
		}
		out = append(out, styles.Heading.Render(truncate(head, width)))
		count++
		for _, line := range file.lines {
			if maxLines > 0 && count >= maxLines {
				out = append(out, styles.Dim.Render("  ... (more lines)"))
				return strings.Join(out, "\n")
			}
			left := padLeft(numOrEmpty(line.oldNum), diffNumWidth)
			right := padLeft(numOrEmpty(line.newNum), diffNumWidth)
			marker := " "
			switch line.kind {
			case "add":
				marker = "+"
			case "del":
				marker = "-"
			case "hunk":
				out = append(out, styles.Dim.Render(truncate("  "+line.text, width)))
				count++
				continue
			case "hdr":
				out = append(out, styles.Dim.Render(truncate("  "+line.text, width)))
				count++
				continue
			}
			text := truncate("  "+left+" "+right+" "+marker+" "+line.text, width)
			switch line.kind {
			case "add":
				out = append(out, styles.ToolDone.Render(text))
			case "del":
				out = append(out, styles.ToolError.Render(text))
			default:
				out = append(out, styles.Dim.Render(text))
			}
			count++
		}
	}
	return strings.Join(out, "\n")
}

// numOrEmpty blanks an absent line number.
func numOrEmpty(num int) string {
	if num <= 0 {
		return ""
	}
	return itoa(num)
}

// padLeft right-aligns text in a field.
func padLeft(text string, width int) string {
	if len(text) >= width {
		return text
	}
	return strings.Repeat(" ", width-len(text)) + text
}

// itoa without importing strconv into the render path.
func itoa(num int) string {
	if num == 0 {
		return "0"
	}
	neg := num < 0
	if neg {
		num = -num
	}
	var buf [20]byte
	pos := len(buf)
	for num > 0 {
		pos--
		buf[pos] = byte('0' + num%10)
		num /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// truncateANSI clips styled text to a display width.
func truncateANSI(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if ansi.StringWidth(text) <= width {
		return text
	}
	return ansi.Truncate(text, width, "")
}

// minInt picks the smaller value.
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
