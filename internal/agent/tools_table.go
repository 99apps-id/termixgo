package agent

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// The audit tools read tables (CSV, TSV and the first sheets of an .xlsx) and
// do their arithmetic on exact decimals, so a total can be reproduced to the
// cent instead of being a float's best guess.

const (
	maxTableBytes = 40 << 20
	maxTableRows  = 300000
)

// table is a header row and the data rows under it.
type table struct {
	header []string
	rows   [][]string
}

// loadTable reads a workspace file. sheet is 1-based and only matters for a
// workbook. The first row is the header.
func loadTable(path string, sheet int) (table, error) {
	info, err := os.Stat(path)
	if err != nil {
		return table{}, err
	}
	if info.IsDir() {
		return table{}, fmt.Errorf("%s is a directory", filepath.Base(path))
	}
	if info.Size() > maxTableBytes {
		return table{}, fmt.Errorf("%s is larger than %d MB", filepath.Base(path), maxTableBytes>>20)
	}
	var records [][]string
	switch strings.ToLower(filepath.Ext(path)) {
	case ".xlsx", ".xlsm":
		records, err = readXLSXRecords(path, sheet)
	default:
		records, err = readDelimitedRecords(path)
	}
	if err != nil {
		return table{}, err
	}
	if len(records) == 0 {
		return table{}, fmt.Errorf("%s has no rows", filepath.Base(path))
	}
	if len(records) > maxTableRows+1 {
		return table{}, fmt.Errorf("%s has more than %d rows", filepath.Base(path), maxTableRows)
	}
	return table{header: records[0], rows: records[1:]}, nil
}

func readXLSXRecords(path string, sheet int) ([][]string, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("cannot open %s as a workbook: %w", filepath.Base(path), err)
	}
	defer reader.Close()
	shared := parseWorkbookSharedStrings(reader.File)
	var sheets []*zip.File
	for _, entry := range reader.File {
		if strings.HasPrefix(entry.Name, "xl/worksheets/sheet") && strings.HasSuffix(entry.Name, ".xml") {
			sheets = append(sheets, entry)
		}
	}
	if len(sheets) == 0 {
		return nil, fmt.Errorf("%s has no worksheet", filepath.Base(path))
	}
	sort.Slice(sheets, func(i, j int) bool { return naturalLess(sheets[i].Name, sheets[j].Name) })
	if sheet < 1 {
		sheet = 1
	}
	if sheet > len(sheets) {
		return nil, fmt.Errorf("%s has %d sheet(s); sheet %d does not exist", filepath.Base(path), len(sheets), sheet)
	}
	raw, err := readZipEntry(sheets[sheet-1])
	if err != nil {
		return nil, err
	}
	var records [][]string
	for _, row := range parseWorksheetRows(raw, shared) {
		records = append(records, strings.Split(row, "\t"))
	}
	return records, nil
}

// naturalLess orders sheet2 before sheet10.
func naturalLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

func readDelimitedRecords(path string) ([][]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxTableBytes))
	if err != nil {
		return nil, err
	}
	text := string(bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF}))
	reader := csv.NewReader(strings.NewReader(text))
	reader.Comma = sniffDelimiter(text, path)
	reader.LazyQuotes = true
	reader.FieldsPerRecord = -1
	return reader.ReadAll()
}

func sniffDelimiter(text, path string) rune {
	if strings.EqualFold(filepath.Ext(path), ".tsv") {
		return '\t'
	}
	line := text
	if cut := strings.IndexByte(line, '\n'); cut >= 0 {
		line = line[:cut]
	}
	best, bestCount := ',', strings.Count(line, ",")
	for _, candidate := range []rune{';', '\t', '|'} {
		if count := strings.Count(line, string(candidate)); count > bestCount {
			best, bestCount = candidate, count
		}
	}
	return best
}

// column resolves a selector to a column index: a header name (exact, then
// contained), a 1-based number, or a spreadsheet letter such as C.
func (t table) column(selector string) (int, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return -1, fmt.Errorf("a column is required")
	}
	lower := strings.ToLower(selector)
	for index, name := range t.header {
		if strings.ToLower(strings.TrimSpace(name)) == lower {
			return index, nil
		}
	}
	found := -1
	for index, name := range t.header {
		if strings.Contains(strings.ToLower(name), lower) {
			if found >= 0 {
				return -1, fmt.Errorf("column %q matches more than one header (%s, %s); use the exact name or a number", selector, strings.TrimSpace(t.header[found]), strings.TrimSpace(name))
			}
			found = index
		}
	}
	if found >= 0 {
		return found, nil
	}
	if number, err := strconv.Atoi(selector); err == nil && number >= 1 && number <= len(t.header) {
		return number - 1, nil
	}
	if letters := strings.ToUpper(selector); len(letters) <= 2 && isLetters(letters) {
		index := 0
		for _, letter := range letters {
			index = index*26 + int(letter-'A') + 1
		}
		if index >= 1 && index <= len(t.header) {
			return index - 1, nil
		}
	}
	return -1, fmt.Errorf("no column %q; the headers are: %s", selector, strings.Join(trimAll(t.header), " | "))
}

func isLetters(text string) bool {
	for _, r := range text {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return text != ""
}

func trimAll(items []string) []string {
	out := make([]string, len(items))
	for index, item := range items {
		out[index] = strings.TrimSpace(item)
	}
	return out
}

func (t table) cell(row []string, col int) string {
	if col < 0 || col >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[col])
}

// sheetRow is the 1-based row number as the spreadsheet shows it: the header
// is row 1, so the first data row is 2.
func sheetRow(index int) int { return index + 2 }

// amountResult is what parseAmount learned from one cell.
type amountResult struct {
	value     *big.Rat
	ambiguous bool
}

var currencyWords = []string{"rp.", "rp", "idr", "usd", "sgd", "eur", "$", "€", "£", "¥"}

// parseAmount reads a number written the Indonesian way (1.234.567,89), the
// English way (1,234,567.89), or with a currency mark, parentheses or a
// trailing minus for a negative. decimal is "auto", "dot" or "comma". A lone
// separator followed by exactly three digits is ambiguous in auto mode: it is
// read as a thousands mark and the result says so, so the caller can tell the
// operator instead of guessing silently.
func parseAmount(text, decimal string) (amountResult, bool) {
	s := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(text, " ", " ")))
	if s == "" {
		return amountResult{}, false
	}
	negative := false
	if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		negative = true
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	for changed := true; changed; {
		changed = false
		for _, word := range currencyWords {
			if strings.HasPrefix(s, word) {
				s = strings.TrimSpace(strings.TrimPrefix(s, word))
				changed = true
			}
		}
	}
	if strings.HasPrefix(s, "-") {
		negative = !negative
		s = strings.TrimSpace(s[1:])
	} else if strings.HasPrefix(s, "+") {
		s = strings.TrimSpace(s[1:])
	}
	if strings.HasSuffix(s, "-") {
		negative = !negative
		s = strings.TrimSpace(s[:len(s)-1])
	}
	for _, word := range currencyWords {
		if strings.HasSuffix(s, word) {
			s = strings.TrimSpace(strings.TrimSuffix(s, word))
		}
	}
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "'", "")
	if s == "" {
		return amountResult{}, false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && r != '.' && r != ',' {
			return amountResult{}, false
		}
	}
	dots, commas := strings.Count(s, "."), strings.Count(s, ",")
	ambiguous := false
	var decimalMark rune
	switch strings.ToLower(decimal) {
	case "dot":
		decimalMark = '.'
	case "comma":
		decimalMark = ','
	default:
		switch {
		case dots > 0 && commas > 0:
			if strings.LastIndexByte(s, '.') > strings.LastIndexByte(s, ',') {
				decimalMark = '.'
			} else {
				decimalMark = ','
			}
		case dots > 1 || commas > 1:
			decimalMark = 0
		case dots == 1 || commas == 1:
			mark := byte('.')
			if commas == 1 {
				mark = ','
			}
			at := strings.IndexByte(s, mark)
			after := len(s) - at - 1
			if after == 3 && at >= 1 && at <= 3 {
				ambiguous = true
				decimalMark = 0
			} else {
				decimalMark = rune(mark)
			}
		}
	}
	if decimalMark == 0 && (dots > 0 || commas > 0) {
		// Only thousands marks are left, so every group after the first has
		// to be three digits: "12.34.56" is not a number.
		groups := strings.FieldsFunc(s, func(r rune) bool { return r == '.' || r == ',' })
		for index, group := range groups {
			if index > 0 && len(group) != 3 {
				return amountResult{}, false
			}
		}
	}
	var integer, fraction strings.Builder
	seenDecimal := false
	for _, r := range s {
		switch {
		case r == '.' || r == ',':
			if decimalMark != 0 && r == decimalMark && !seenDecimal {
				seenDecimal = true
			} else if r == decimalMark {
				return amountResult{}, false
			}
		case seenDecimal:
			fraction.WriteRune(r)
		default:
			integer.WriteRune(r)
		}
	}
	digits := integer.String()
	if digits == "" {
		digits = "0"
	}
	if fraction.Len() > 0 {
		digits += "." + fraction.String()
	}
	value, ok := new(big.Rat).SetString(digits)
	if !ok {
		return amountResult{}, false
	}
	if negative {
		value.Neg(value)
	}
	return amountResult{value: value, ambiguous: ambiguous}, true
}

// roundRat rounds to scale decimal places. mode is half_up (the usual
// commercial rule, halves go away from zero), half_even, down (toward zero) or
// up (away from zero).
func roundRat(value *big.Rat, scale int, mode string) *big.Rat {
	factor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)
	scaled := new(big.Rat).Mul(value, new(big.Rat).SetInt(factor))
	negative := scaled.Sign() < 0
	num := new(big.Int).Abs(scaled.Num())
	den := scaled.Denom()
	quotient, remainder := new(big.Int).QuoRem(num, den, new(big.Int))
	twice := new(big.Int).Lsh(remainder, 1)
	compare := twice.Cmp(den)
	increment := false
	switch strings.ToLower(mode) {
	case "down":
	case "up":
		increment = remainder.Sign() > 0
	case "half_even":
		increment = compare > 0 || (compare == 0 && quotient.Bit(0) == 1)
	default:
		increment = compare >= 0
	}
	if increment {
		quotient.Add(quotient, big.NewInt(1))
	}
	if negative {
		quotient.Neg(quotient)
	}
	return new(big.Rat).SetFrac(quotient, factor)
}

// formatRat writes a value rounded to scale places with thousands grouping.
func formatRat(value *big.Rat, scale int, mode string) string {
	rounded := roundRat(value, scale, mode)
	text := rounded.FloatString(scale)
	sign := ""
	if strings.HasPrefix(text, "-") {
		sign, text = "-", text[1:]
	}
	integer, fraction := text, ""
	if cut := strings.IndexByte(text, '.'); cut >= 0 {
		integer, fraction = text[:cut], text[cut:]
	}
	var grouped strings.Builder
	for index, digit := range integer {
		if index > 0 && (len(integer)-index)%3 == 0 {
			grouped.WriteByte(',')
		}
		grouped.WriteRune(digit)
	}
	return sign + grouped.String() + fraction
}

// numericColumn reads every numeric cell of a column. It counts what it could
// not read, so a text cell or an ambiguous separator is reported, not dropped.
type numericColumn struct {
	values    []*big.Rat
	rows      []int
	skipped   int
	ambiguous int
}

func readNumericColumn(t table, col int, decimal string) numericColumn {
	var out numericColumn
	for index, row := range t.rows {
		text := t.cell(row, col)
		if text == "" {
			continue
		}
		parsed, ok := parseAmount(text, decimal)
		if !ok {
			out.skipped++
			continue
		}
		if parsed.ambiguous {
			out.ambiguous++
		}
		out.values = append(out.values, parsed.value)
		out.rows = append(out.rows, sheetRow(index))
	}
	return out
}

// loadTableArg resolves the file, sheet and a column selector from a tool's
// arguments, with the workspace guard the other file tools apply.
func loadTableArg(env *Env, path string, sheet int) (table, string, error) {
	resolved := resolvePath(env, path)
	if resolved == "" {
		return table{}, "", fmt.Errorf("a file is required")
	}
	if err := checkWorkspacePath(env, resolved); err != nil {
		return table{}, "", fmt.Errorf("cannot read a file outside the workspace: %v", err)
	}
	loaded, err := loadTable(resolved, sheet)
	if err != nil {
		return table{}, "", err
	}
	return loaded, displayPath(env, resolved), nil
}

func decimalArg(args map[string]any) string {
	switch strings.ToLower(strings.TrimSpace(argString(args, "decimal"))) {
	case "dot", ".", "en":
		return "dot"
	case "comma", ",", "id":
		return "comma"
	}
	return "auto"
}
