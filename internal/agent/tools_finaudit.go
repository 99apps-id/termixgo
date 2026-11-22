package agent

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------- calc

type calcTool struct{}

func (t *calcTool) Name() string      { return "calc" }
func (t *calcTool) Aliases() []string { return []string{"decimal_math", "calculate"} }
func (t *calcTool) Mutating() bool    { return false }
func (t *calcTool) Risk() Risk        { return RiskEdit }
func (t *calcTool) Label(a map[string]any) string {
	if file := argString(a, "file"); file != "" {
		return "Calculating over " + Shorten(file, 40)
	}
	return "Calculating " + Shorten(argString(a, "expression"), 40)
}
func (t *calcTool) DoneLabel(a map[string]any) string {
	if file := argString(a, "file"); file != "" {
		return "Calculated over " + Shorten(file, 40)
	}
	return "Calculated " + Shorten(argString(a, "expression"), 40)
}
func (t *calcTool) Description() string {
	return "Exact decimal arithmetic for accounting, audit and tax work: never estimate a figure, compute it. Give an expression (+ - * / ^ and parentheses, a trailing % means divide by 100, functions round(x,n) sum() avg() min() max() abs()), or a file and column to sum, count, average or find the min and max of. Results are rounded only at the end, with the rounding rule named. This tool holds no tax rates or rules: look the current regulation up and cite it."
}
func (t *calcTool) Schema() map[string]any {
	return object(map[string]any{
		"expression": strProp("Arithmetic to evaluate, for example (1250000 * 11%) + 5000 or round(100 / 3, 2). Decimal point is a dot; separate function arguments with commas."),
		"file":       strProp("CSV, TSV or .xlsx file in the workspace, instead of an expression."),
		"column":     strProp("Column of the file: header name, number or letter. Needed with file."),
		"op":         strProp("With file: sum, count, avg, min or max. Default sum."),
		"sheet":      intProp("Workbook sheet number, from 1. Default 1."),
		"decimal":    strProp("How numbers in the file are written: auto, dot (1,234.50) or comma (1.234,50). Default auto."),
		"scale":      intProp("Decimal places in the result, 0 to 10. Default 2."),
		"rounding":   strProp("half_up, half_even, down or up. Default half_up."),
	})
}

func (t *calcTool) Run(_ context.Context, env *Env, args map[string]any) (Result, error) {
	scale := argInt(args, "scale", 2, 0, 10)
	if _, set := args["scale"]; set && argInt(args, "scale", -1, -1, 10) == 0 {
		scale = 0
	}
	mode := strings.ToLower(strings.TrimSpace(argString(args, "rounding")))
	switch mode {
	case "", "half_up":
		mode = "half_up"
	case "half_even", "down", "up":
	default:
		return Result{Output: "rounding must be half_up, half_even, down or up", IsError: true}, nil
	}
	if file := strings.TrimSpace(argString(args, "file")); file != "" {
		return calcFile(env, file, args, scale, mode)
	}
	expression := strings.TrimSpace(argString(args, "expression"))
	if expression == "" {
		return Result{Output: "give an expression, or a file and a column", IsError: true}, nil
	}
	value, err := evalExpression(expression)
	if err != nil {
		return Result{Output: "cannot evaluate: " + err.Error(), IsError: true}, nil
	}
	exact := ""
	if !value.IsInt() {
		if text := value.FloatString(12); !strings.HasSuffix(text, "000000000000") {
			exact = fmt.Sprintf("\nExact value is not terminating or has more than %d places: %s...", scale, value.FloatString(12))
		}
	}
	return Result{
		Output:  fmt.Sprintf("%s = %s\nRounded to %d decimal place(s), %s.%s", expression, formatRat(value, scale, mode), scale, mode, exact),
		Display: formatRat(value, scale, mode),
	}, nil
}

func calcFile(env *Env, file string, args map[string]any, scale int, mode string) (Result, error) {
	loaded, shown, err := loadTableArg(env, file, argInt(args, "sheet", 1, 1, 200))
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	col, err := loaded.column(argString(args, "column"))
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	data := readNumericColumn(loaded, col, decimalArg(args))
	if len(data.values) == 0 {
		return Result{Output: fmt.Sprintf("column %q of %s has no numeric cells", strings.TrimSpace(loaded.header[col]), shown), IsError: true}, nil
	}
	op := strings.ToLower(strings.TrimSpace(argString(args, "op")))
	if op == "" {
		op = "sum"
	}
	total := new(big.Rat)
	lowest, highest := data.values[0], data.values[0]
	for _, value := range data.values {
		total.Add(total, value)
		if value.Cmp(lowest) < 0 {
			lowest = value
		}
		if value.Cmp(highest) > 0 {
			highest = value
		}
	}
	var answer *big.Rat
	switch op {
	case "sum":
		answer = total
	case "count":
		answer = new(big.Rat).SetInt64(int64(len(data.values)))
	case "avg", "average", "mean":
		answer = new(big.Rat).Quo(total, new(big.Rat).SetInt64(int64(len(data.values))))
	case "min":
		answer = lowest
	case "max":
		answer = highest
	default:
		return Result{Output: "op must be sum, count, avg, min or max", IsError: true}, nil
	}
	places := scale
	if op == "count" {
		places = 0
	}
	var out strings.Builder
	fmt.Fprintf(&out, "%s of column %q in %s = %s\n", op, strings.TrimSpace(loaded.header[col]), shown, formatRat(answer, places, mode))
	fmt.Fprintf(&out, "%d numeric cell(s) read", len(data.values))
	if data.skipped > 0 {
		fmt.Fprintf(&out, ", %d non-empty cell(s) were not numbers and were skipped", data.skipped)
	}
	out.WriteString(".")
	if data.ambiguous > 0 {
		fmt.Fprintf(&out, "\nWarning: %d cell(s) such as 1.234 or 1,234 could be a decimal or a thousands mark; they were read as thousands. Pass decimal=dot or decimal=comma to settle it.", data.ambiguous)
	}
	if op == "sum" && len(data.values) > 2 {
		last := data.values[len(data.values)-1]
		rest := new(big.Rat).Sub(total, last)
		if last.Sign() != 0 && rest.Cmp(last) == 0 {
			fmt.Fprintf(&out, "\nWarning: the last numeric cell (row %d) equals the sum of the others, so it looks like a total row that is now counted twice.", data.rows[len(data.rows)-1])
		}
	}
	return Result{Output: out.String(), Display: formatRat(answer, places, mode)}, nil
}

// expression parser: exact arithmetic on big.Rat.

type exprParser struct {
	text  string
	pos   int
	depth int
}

func evalExpression(text string) (*big.Rat, error) {
	if len(text) > 2000 {
		return nil, fmt.Errorf("expression is longer than 2000 characters")
	}
	parser := &exprParser{text: text}
	value, err := parser.sum()
	if err != nil {
		return nil, err
	}
	parser.skip()
	if parser.pos < len(parser.text) {
		return nil, fmt.Errorf("unexpected %q at position %d", string(parser.text[parser.pos]), parser.pos+1)
	}
	return value, nil
}

func (p *exprParser) skip() {
	for p.pos < len(p.text) && (p.text[p.pos] == ' ' || p.text[p.pos] == '\t' || p.text[p.pos] == '\n') {
		p.pos++
	}
}

func (p *exprParser) peek() byte {
	p.skip()
	if p.pos >= len(p.text) {
		return 0
	}
	return p.text[p.pos]
}

func (p *exprParser) sum() (*big.Rat, error) {
	left, err := p.product()
	if err != nil {
		return nil, err
	}
	for {
		switch p.peek() {
		case '+':
			p.pos++
			right, err := p.product()
			if err != nil {
				return nil, err
			}
			left = new(big.Rat).Add(left, right)
		case '-':
			p.pos++
			right, err := p.product()
			if err != nil {
				return nil, err
			}
			left = new(big.Rat).Sub(left, right)
		default:
			return left, nil
		}
	}
}

func (p *exprParser) product() (*big.Rat, error) {
	left, err := p.unary()
	if err != nil {
		return nil, err
	}
	for {
		switch p.peek() {
		case '*':
			p.pos++
			right, err := p.unary()
			if err != nil {
				return nil, err
			}
			left = new(big.Rat).Mul(left, right)
		case '/':
			p.pos++
			right, err := p.unary()
			if err != nil {
				return nil, err
			}
			if right.Sign() == 0 {
				return nil, fmt.Errorf("division by zero")
			}
			left = new(big.Rat).Quo(left, right)
		default:
			return left, nil
		}
	}
}

func (p *exprParser) unary() (*big.Rat, error) {
	switch p.peek() {
	case '-':
		p.pos++
		value, err := p.unary()
		if err != nil {
			return nil, err
		}
		return new(big.Rat).Neg(value), nil
	case '+':
		p.pos++
		return p.unary()
	}
	return p.power()
}

func (p *exprParser) power() (*big.Rat, error) {
	base, err := p.postfix()
	if err != nil {
		return nil, err
	}
	if p.peek() != '^' {
		return base, nil
	}
	p.pos++
	exponent, err := p.unary()
	if err != nil {
		return nil, err
	}
	if !exponent.IsInt() || !exponent.Num().IsInt64() {
		return nil, fmt.Errorf("the exponent must be a whole number")
	}
	n := exponent.Num().Int64()
	if n < -64 || n > 64 {
		return nil, fmt.Errorf("the exponent must be between -64 and 64")
	}
	negative := n < 0
	if negative {
		n = -n
	}
	result := new(big.Rat).SetInt64(1)
	for i := int64(0); i < n; i++ {
		result.Mul(result, base)
	}
	if negative {
		if result.Sign() == 0 {
			return nil, fmt.Errorf("division by zero")
		}
		result.Inv(result)
	}
	return result, nil
}

func (p *exprParser) postfix() (*big.Rat, error) {
	value, err := p.atom()
	if err != nil {
		return nil, err
	}
	for p.peek() == '%' {
		p.pos++
		value = new(big.Rat).Quo(value, big.NewRat(100, 1))
	}
	return value, nil
}

var exprNumber = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?|^\.[0-9]+`)

func (p *exprParser) atom() (*big.Rat, error) {
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > 100 {
		return nil, fmt.Errorf("expression is nested too deeply")
	}
	c := p.peek()
	switch {
	case c == 0:
		return nil, fmt.Errorf("the expression ends too early")
	case c == '(':
		p.pos++
		value, err := p.sum()
		if err != nil {
			return nil, err
		}
		if p.peek() != ')' {
			return nil, fmt.Errorf("missing )")
		}
		p.pos++
		return value, nil
	case (c >= '0' && c <= '9') || c == '.':
		match := exprNumber.FindString(p.text[p.pos:])
		if match == "" {
			return nil, fmt.Errorf("bad number at position %d", p.pos+1)
		}
		p.pos += len(match)
		value, ok := new(big.Rat).SetString(match)
		if !ok {
			return nil, fmt.Errorf("bad number %q", match)
		}
		return value, nil
	case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
		start := p.pos
		for p.pos < len(p.text) && ((p.text[p.pos] >= 'a' && p.text[p.pos] <= 'z') || (p.text[p.pos] >= 'A' && p.text[p.pos] <= 'Z')) {
			p.pos++
		}
		name := strings.ToLower(p.text[start:p.pos])
		if p.peek() != '(' {
			return nil, fmt.Errorf("unknown name %q; only the functions round, sum, avg, min, max and abs exist", name)
		}
		p.pos++
		var values []*big.Rat
		if p.peek() == ')' {
			p.pos++
		} else {
			for {
				value, err := p.sum()
				if err != nil {
					return nil, err
				}
				values = append(values, value)
				if p.peek() == ',' {
					p.pos++
					continue
				}
				if p.peek() != ')' {
					return nil, fmt.Errorf("missing ) after %s(", name)
				}
				p.pos++
				break
			}
		}
		return applyFunction(name, values)
	}
	return nil, fmt.Errorf("unexpected %q at position %d", string(c), p.pos+1)
}

func applyFunction(name string, values []*big.Rat) (*big.Rat, error) {
	need := func(n int) error {
		if len(values) != n {
			return fmt.Errorf("%s takes %d argument(s), got %d", name, n, len(values))
		}
		return nil
	}
	switch name {
	case "abs":
		if err := need(1); err != nil {
			return nil, err
		}
		return new(big.Rat).Abs(values[0]), nil
	case "round":
		if len(values) < 1 || len(values) > 2 {
			return nil, fmt.Errorf("round takes a value and optionally the decimal places")
		}
		places := 0
		if len(values) == 2 {
			if !values[1].IsInt() || !values[1].Num().IsInt64() || values[1].Num().Int64() < 0 || values[1].Num().Int64() > 10 {
				return nil, fmt.Errorf("round places must be a whole number from 0 to 10")
			}
			places = int(values[1].Num().Int64())
		}
		return roundRat(values[0], places, "half_up"), nil
	case "sum", "avg", "min", "max":
		if len(values) == 0 {
			return nil, fmt.Errorf("%s needs at least one value", name)
		}
		total := new(big.Rat)
		lowest, highest := values[0], values[0]
		for _, value := range values {
			total.Add(total, value)
			if value.Cmp(lowest) < 0 {
				lowest = value
			}
			if value.Cmp(highest) > 0 {
				highest = value
			}
		}
		switch name {
		case "sum":
			return total, nil
		case "avg":
			return total.Quo(total, new(big.Rat).SetInt64(int64(len(values)))), nil
		case "min":
			return new(big.Rat).Set(lowest), nil
		}
		return new(big.Rat).Set(highest), nil
	}
	return nil, fmt.Errorf("unknown function %q", name)
}

// ---------------------------------------------------------------- reconcile

type reconcileTool struct{}

func (t *reconcileTool) Name() string      { return "reconcile" }
func (t *reconcileTool) Aliases() []string { return []string{"match_ledgers", "compare_tables"} }
func (t *reconcileTool) Mutating() bool    { return false }
func (t *reconcileTool) Risk() Risk        { return RiskEdit }
func (t *reconcileTool) Label(a map[string]any) string {
	return "Reconciling " + Shorten(argString(a, "left_file"), 24) + " with " + Shorten(argString(a, "right_file"), 24)
}
func (t *reconcileTool) DoneLabel(a map[string]any) string {
	return "Reconciled " + Shorten(argString(a, "left_file"), 24) + " with " + Shorten(argString(a, "right_file"), 24)
}
func (t *reconcileTool) Description() string {
	return "Reconcile two tables of amounts, for example tax invoices against the general ledger, or a bank statement against the cash book. With key columns it matches by key (duplicate keys are summed and flagged); without them it pairs equal amounts one to one. Reports what matched, what is only on one side, and where the amounts differ beyond a tolerance, with the sheet row of each item. Arithmetic is exact."
}
func (t *reconcileTool) Schema() map[string]any {
	return object(map[string]any{
		"left_file":     strProp("First CSV, TSV or .xlsx file."),
		"left_amount":   strProp("Amount column of the first file: header name, number or letter."),
		"left_key":      strProp("Key column of the first file, such as an invoice number. Omit both keys to match by amount only."),
		"left_sheet":    intProp("Workbook sheet of the first file. Default 1."),
		"right_file":    strProp("Second file."),
		"right_amount":  strProp("Amount column of the second file."),
		"right_key":     strProp("Key column of the second file."),
		"right_sheet":   intProp("Workbook sheet of the second file. Default 1."),
		"tolerance":     strProp("Largest difference still counted as matching, for example 0.01 or 1000. Default 0."),
		"normalize_key": boolProp("Ignore case, spaces and punctuation in keys, so INV-001 matches inv001. Default true."),
		"decimal":       strProp("How numbers are written: auto, dot or comma. Default auto."),
		"limit":         intProp("Rows shown per section, 1 to 200. Default 25."),
	}, "left_file", "left_amount", "right_file", "right_amount")
}

type reconEntry struct {
	key    string
	label  string
	amount *big.Rat
	rows   []int
}

var nonAlnum = regexp.MustCompile(`[^\p{L}\p{N}]+`)

func normalizeKey(text string, normalize bool) string {
	text = strings.TrimSpace(text)
	if !normalize {
		return text
	}
	return strings.ToLower(nonAlnum.ReplaceAllString(text, ""))
}

func loadRecon(env *Env, args map[string]any, side string, decimal string, normalize bool) ([]reconEntry, string, int, int, error) {
	loaded, shown, err := loadTableArg(env, argString(args, side+"_file"), argInt(args, side+"_sheet", 1, 1, 200))
	if err != nil {
		return nil, "", 0, 0, fmt.Errorf("%s file: %w", side, err)
	}
	amountCol, err := loaded.column(argString(args, side+"_amount"))
	if err != nil {
		return nil, "", 0, 0, fmt.Errorf("%s amount: %w", side, err)
	}
	keyCol := -1
	if selector := strings.TrimSpace(argString(args, side+"_key")); selector != "" {
		keyCol, err = loaded.column(selector)
		if err != nil {
			return nil, "", 0, 0, fmt.Errorf("%s key: %w", side, err)
		}
	}
	var entries []reconEntry
	skipped, ambiguous := 0, 0
	for index, row := range loaded.rows {
		text := loaded.cell(row, amountCol)
		if text == "" {
			continue
		}
		parsed, ok := parseAmount(text, decimal)
		if !ok {
			skipped++
			continue
		}
		if parsed.ambiguous {
			ambiguous++
		}
		entry := reconEntry{amount: parsed.value, rows: []int{sheetRow(index)}}
		if keyCol >= 0 {
			entry.label = loaded.cell(row, keyCol)
			entry.key = normalizeKey(entry.label, normalize)
			if entry.key == "" {
				skipped++
				continue
			}
		}
		entries = append(entries, entry)
	}
	return entries, shown, skipped, ambiguous, nil
}

func (t *reconcileTool) Run(_ context.Context, env *Env, args map[string]any) (Result, error) {
	decimal := decimalArg(args)
	normalize := argBool(args, "normalize_key", true)
	limit := argInt(args, "limit", 25, 1, 200)
	tolerance := new(big.Rat)
	if text := strings.TrimSpace(argString(args, "tolerance")); text != "" {
		parsed, ok := parseAmount(text, decimal)
		if !ok {
			return Result{Output: fmt.Sprintf("tolerance %q is not a number", text), IsError: true}, nil
		}
		tolerance = new(big.Rat).Abs(parsed.value)
	}
	hasLeftKey := strings.TrimSpace(argString(args, "left_key")) != ""
	hasRightKey := strings.TrimSpace(argString(args, "right_key")) != ""
	if hasLeftKey != hasRightKey {
		return Result{Output: "give a key column for both files, or for neither", IsError: true}, nil
	}
	left, leftName, leftSkipped, leftAmbiguous, err := loadRecon(env, args, "left", decimal, normalize)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	right, rightName, rightSkipped, rightAmbiguous, err := loadRecon(env, args, "right", decimal, normalize)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}

	var report reconReport
	if hasLeftKey {
		report = reconcileByKey(left, right, tolerance)
	} else {
		report = reconcileByAmount(left, right, tolerance)
	}
	report.leftTotal, report.rightTotal = totalOf(left), totalOf(right)
	return Result{Output: renderRecon(report, leftName, rightName, len(left), len(right), leftSkipped+rightSkipped, leftAmbiguous+rightAmbiguous, tolerance, limit, hasLeftKey)}, nil
}

type reconDiff struct {
	label       string
	left, right *big.Rat
	diff        *big.Rat
	leftRows    []int
	rightRows   []int
}

type reconReport struct {
	matched    int
	matchedSum *big.Rat
	mismatches []reconDiff
	leftOnly   []reconEntry
	rightOnly  []reconEntry
	dupLeft    []reconEntry
	dupRight   []reconEntry
	leftTotal  *big.Rat
	rightTotal *big.Rat
}

func totalOf(entries []reconEntry) *big.Rat {
	total := new(big.Rat)
	for _, entry := range entries {
		total.Add(total, entry.amount)
	}
	return total
}

func groupByKey(entries []reconEntry) (map[string]*reconEntry, []string, []reconEntry) {
	groups := map[string]*reconEntry{}
	var order []string
	var duplicates []reconEntry
	for _, entry := range entries {
		if existing, ok := groups[entry.key]; ok {
			existing.amount = new(big.Rat).Add(existing.amount, entry.amount)
			existing.rows = append(existing.rows, entry.rows...)
			continue
		}
		copied := entry
		groups[entry.key] = &copied
		order = append(order, entry.key)
	}
	for _, key := range order {
		if len(groups[key].rows) > 1 {
			duplicates = append(duplicates, *groups[key])
		}
	}
	return groups, order, duplicates
}

func reconcileByKey(left, right []reconEntry, tolerance *big.Rat) reconReport {
	report := reconReport{matchedSum: new(big.Rat)}
	leftGroups, leftOrder, dupLeft := groupByKey(left)
	rightGroups, rightOrder, dupRight := groupByKey(right)
	report.dupLeft, report.dupRight = dupLeft, dupRight
	for _, key := range leftOrder {
		l := leftGroups[key]
		r, ok := rightGroups[key]
		if !ok {
			report.leftOnly = append(report.leftOnly, *l)
			continue
		}
		diff := new(big.Rat).Sub(l.amount, r.amount)
		if new(big.Rat).Abs(diff).Cmp(tolerance) <= 0 {
			report.matched++
			report.matchedSum.Add(report.matchedSum, l.amount)
			continue
		}
		report.mismatches = append(report.mismatches, reconDiff{label: l.label, left: l.amount, right: r.amount, diff: diff, leftRows: l.rows, rightRows: r.rows})
	}
	for _, key := range rightOrder {
		if _, ok := leftGroups[key]; !ok {
			report.rightOnly = append(report.rightOnly, *rightGroups[key])
		}
	}
	sort.SliceStable(report.mismatches, func(i, j int) bool {
		return new(big.Rat).Abs(report.mismatches[i].diff).Cmp(new(big.Rat).Abs(report.mismatches[j].diff)) > 0
	})
	return report
}

func reconcileByAmount(left, right []reconEntry, tolerance *big.Rat) reconReport {
	report := reconReport{matchedSum: new(big.Rat)}
	used := make([]bool, len(right))
	for _, l := range left {
		best, bestDiff := -1, (*big.Rat)(nil)
		for index, r := range right {
			if used[index] {
				continue
			}
			diff := new(big.Rat).Abs(new(big.Rat).Sub(l.amount, r.amount))
			if diff.Cmp(tolerance) > 0 {
				continue
			}
			if bestDiff == nil || diff.Cmp(bestDiff) < 0 {
				best, bestDiff = index, diff
				if diff.Sign() == 0 {
					break
				}
			}
		}
		if best < 0 {
			report.leftOnly = append(report.leftOnly, l)
			continue
		}
		used[best] = true
		report.matched++
		report.matchedSum.Add(report.matchedSum, l.amount)
	}
	for index, r := range right {
		if !used[index] {
			report.rightOnly = append(report.rightOnly, r)
		}
	}
	return report
}

func rowList(rows []int) string {
	if len(rows) == 0 {
		return ""
	}
	parts := make([]string, 0, len(rows))
	for index, row := range rows {
		if index == 6 {
			parts = append(parts, fmt.Sprintf("+%d more", len(rows)-6))
			break
		}
		parts = append(parts, strconv.Itoa(row))
	}
	return strings.Join(parts, ",")
}

func renderRecon(report reconReport, leftName, rightName string, leftN, rightN, skipped, ambiguous int, tolerance *big.Rat, limit int, byKey bool) string {
	var out strings.Builder
	mode := "by amount only (equal amounts paired one to one)"
	if byKey {
		mode = "by key"
	}
	fmt.Fprintf(&out, "Reconciliation %s\nLeft:  %s (%d rows, total %s)\nRight: %s (%d rows, total %s)\nTolerance: %s\n",
		mode, leftName, leftN, formatRat(report.leftTotal, 2, "half_up"), rightName, rightN, formatRat(report.rightTotal, 2, "half_up"), formatRat(tolerance, 2, "half_up"))
	net := new(big.Rat).Sub(report.leftTotal, report.rightTotal)
	fmt.Fprintf(&out, "Net difference of the totals (left - right): %s\n\n", formatRat(net, 2, "half_up"))
	fmt.Fprintf(&out, "Matched: %d (sum %s)\nAmount differs: %d\nOnly on the left: %d (sum %s)\nOnly on the right: %d (sum %s)\n",
		report.matched, formatRat(report.matchedSum, 2, "half_up"), len(report.mismatches),
		len(report.leftOnly), formatRat(totalOf(report.leftOnly), 2, "half_up"),
		len(report.rightOnly), formatRat(totalOf(report.rightOnly), 2, "half_up"))
	if skipped > 0 {
		fmt.Fprintf(&out, "Rows skipped because the amount or key could not be read: %d\n", skipped)
	}
	if ambiguous > 0 {
		fmt.Fprintf(&out, "Warning: %d amount(s) such as 1.234 could be decimal or thousands and were read as thousands. Pass decimal=dot or decimal=comma.\n", ambiguous)
	}
	section := func(title string, count int, lines func(int) string) {
		if count == 0 {
			return
		}
		fmt.Fprintf(&out, "\n%s (%d)\n", title, count)
		for index := 0; index < count && index < limit; index++ {
			out.WriteString(lines(index) + "\n")
		}
		if count > limit {
			fmt.Fprintf(&out, "... %d more not shown\n", count-limit)
		}
	}
	section("Amount differs", len(report.mismatches), func(i int) string {
		m := report.mismatches[i]
		return fmt.Sprintf("%s: left %s (row %s) vs right %s (row %s), difference %s", m.label,
			formatRat(m.left, 2, "half_up"), rowList(m.leftRows), formatRat(m.right, 2, "half_up"), rowList(m.rightRows), formatRat(m.diff, 2, "half_up"))
	})
	entryLine := func(e reconEntry) string {
		if e.label != "" {
			return fmt.Sprintf("%s: %s (row %s)", e.label, formatRat(e.amount, 2, "half_up"), rowList(e.rows))
		}
		return fmt.Sprintf("%s (row %s)", formatRat(e.amount, 2, "half_up"), rowList(e.rows))
	}
	sorted := func(entries []reconEntry) []reconEntry {
		out := append([]reconEntry(nil), entries...)
		sort.SliceStable(out, func(i, j int) bool {
			return new(big.Rat).Abs(out[i].amount).Cmp(new(big.Rat).Abs(out[j].amount)) > 0
		})
		return out
	}
	leftOnly, rightOnly := sorted(report.leftOnly), sorted(report.rightOnly)
	section("Only on the left, largest first", len(leftOnly), func(i int) string { return entryLine(leftOnly[i]) })
	section("Only on the right, largest first", len(rightOnly), func(i int) string { return entryLine(rightOnly[i]) })
	section("Duplicate keys on the left (amounts were summed)", len(report.dupLeft), func(i int) string { return entryLine(report.dupLeft[i]) })
	section("Duplicate keys on the right (amounts were summed)", len(report.dupRight), func(i int) string { return entryLine(report.dupRight[i]) })
	return strings.TrimRight(out.String(), "\n")
}

// ---------------------------------------------------------------- benford

type benfordTool struct{}

func (t *benfordTool) Name() string      { return "benford_test" }
func (t *benfordTool) Aliases() []string { return []string{"benford", "digit_analysis"} }
func (t *benfordTool) Mutating() bool    { return false }
func (t *benfordTool) Risk() Risk        { return RiskEdit }
func (t *benfordTool) Label(a map[string]any) string {
	return "Testing digits of " + Shorten(argString(a, "file"), 40)
}
func (t *benfordTool) DoneLabel(a map[string]any) string {
	return "Tested digits of " + Shorten(argString(a, "file"), 40)
}
func (t *benfordTool) Description() string {
	return "Fraud and anomaly screening of a column of amounts: first-digit distribution against Benford's law (chi-square and mean absolute deviation with Nigrini's bands), plus round amounts, repeated amounts and amounts just under an approval threshold. A deviation is a lead to examine, not proof of error or fraud, and a small sample proves nothing."
}
func (t *benfordTool) Schema() map[string]any {
	return object(map[string]any{
		"file":      strProp("CSV, TSV or .xlsx file in the workspace."),
		"column":    strProp("Amount column: header name, number or letter."),
		"values":    arrayProp("Amounts given directly, instead of file and column.", map[string]any{"type": "string"}),
		"sheet":     intProp("Workbook sheet. Default 1."),
		"threshold": strProp("An approval or reporting limit. Amounts within 5% below it are listed."),
		"decimal":   strProp("How numbers are written: auto, dot or comma. Default auto."),
	})
}

func (t *benfordTool) Run(_ context.Context, env *Env, args map[string]any) (Result, error) {
	decimal := decimalArg(args)
	var values []*big.Rat
	source := "the given values"
	skipped := 0
	if file := strings.TrimSpace(argString(args, "file")); file != "" {
		loaded, shown, err := loadTableArg(env, file, argInt(args, "sheet", 1, 1, 200))
		if err != nil {
			return Result{Output: err.Error(), IsError: true}, nil
		}
		col, err := loaded.column(argString(args, "column"))
		if err != nil {
			return Result{Output: err.Error(), IsError: true}, nil
		}
		data := readNumericColumn(loaded, col, decimal)
		values, skipped = data.values, data.skipped
		source = fmt.Sprintf("column %q of %s", strings.TrimSpace(loaded.header[col]), shown)
	} else if list := argList(args, "values"); len(list) > 0 {
		for _, text := range list {
			if parsed, ok := parseAmount(text, decimal); ok {
				values = append(values, parsed.value)
			} else {
				skipped++
			}
		}
	} else {
		return Result{Output: "give a file and a column, or a list of values", IsError: true}, nil
	}
	var threshold *big.Rat
	if text := strings.TrimSpace(argString(args, "threshold")); text != "" {
		parsed, ok := parseAmount(text, decimal)
		if !ok {
			return Result{Output: fmt.Sprintf("threshold %q is not a number", text), IsError: true}, nil
		}
		threshold = new(big.Rat).Abs(parsed.value)
	}
	return Result{Output: analyseBenford(values, skipped, source, threshold)}, nil
}

func analyseBenford(values []*big.Rat, skipped int, source string, threshold *big.Rat) string {
	var counts [10]int
	n := 0
	frequency := map[string]int{}
	roundCount := 0
	var nearThreshold []string
	low := new(big.Rat)
	if threshold != nil {
		low = new(big.Rat).Mul(threshold, big.NewRat(95, 100))
	}
	zeros := 0
	for _, value := range values {
		abs := new(big.Rat).Abs(value)
		if abs.Sign() == 0 {
			zeros++
			continue
		}
		f, _ := abs.Float64()
		if f <= 0 || math.IsInf(f, 0) || math.IsNaN(f) {
			continue
		}
		digit := int(strconv.FormatFloat(f, 'e', -1, 64)[0] - '0')
		if digit < 1 || digit > 9 {
			continue
		}
		counts[digit]++
		n++
		frequency[abs.FloatString(2)]++
		if abs.IsInt() && abs.Cmp(big.NewRat(1000, 1)) >= 0 && new(big.Int).Mod(abs.Num(), big.NewInt(1000)).Sign() == 0 {
			roundCount++
		}
		if threshold != nil && abs.Cmp(low) >= 0 && abs.Cmp(threshold) < 0 {
			nearThreshold = append(nearThreshold, abs.FloatString(2))
		}
	}
	if n == 0 {
		return "No non-zero amounts to test."
	}
	var out strings.Builder
	fmt.Fprintf(&out, "First-digit test of %s\nAmounts tested: %d", source, n)
	if skipped > 0 || zeros > 0 {
		fmt.Fprintf(&out, " (%d not numbers, %d zero, left out)", skipped, zeros)
	}
	out.WriteString("\n\nDigit  Actual   Expected  Difference\n")
	chi, mad := 0.0, 0.0
	for digit := 1; digit <= 9; digit++ {
		expected := math.Log10(1 + 1/float64(digit))
		actual := float64(counts[digit]) / float64(n)
		chi += math.Pow(float64(counts[digit])-expected*float64(n), 2) / (expected * float64(n))
		mad += math.Abs(actual - expected)
		fmt.Fprintf(&out, "%d      %5.1f%%   %5.1f%%    %+5.1f pts\n", digit, actual*100, expected*100, (actual-expected)*100)
	}
	mad /= 9
	band := "nonconformity"
	switch {
	case mad <= 0.006:
		band = "close conformity"
	case mad <= 0.012:
		band = "acceptable conformity"
	case mad <= 0.015:
		band = "marginally acceptable conformity"
	}
	significance := "not significant at 5%"
	switch {
	case chi > 20.09:
		significance = "significant at 1%"
	case chi > 15.51:
		significance = "significant at 5%"
	}
	fmt.Fprintf(&out, "\nChi-square (8 degrees of freedom): %.2f, %s (critical values 15.51 at 5%%, 20.09 at 1%%)\n", chi, significance)
	fmt.Fprintf(&out, "Mean absolute deviation: %.4f, %s (Nigrini: up to 0.006 close, 0.012 acceptable, 0.015 marginal)\n", mad, band)
	switch {
	case n < 30:
		out.WriteString("Warning: fewer than 30 amounts. This test says nothing reliable on a sample this small.\n")
	case n < 100:
		out.WriteString("Warning: fewer than 100 amounts. Treat the result as weak; the chi-square test is unreliable here.\n")
	}
	fmt.Fprintf(&out, "\nRound amounts (whole multiples of 1,000): %d of %d (%.1f%%)\n", roundCount, n, 100*float64(roundCount)/float64(n))
	type repeat struct {
		amount string
		count  int
	}
	var repeats []repeat
	for amount, count := range frequency {
		if count >= 3 {
			repeats = append(repeats, repeat{amount, count})
		}
	}
	sort.Slice(repeats, func(i, j int) bool {
		if repeats[i].count != repeats[j].count {
			return repeats[i].count > repeats[j].count
		}
		return repeats[i].amount < repeats[j].amount
	})
	if len(repeats) == 0 {
		out.WriteString("Amounts that repeat three or more times: none\n")
	} else {
		out.WriteString("Amounts that repeat three or more times:\n")
		for index, item := range repeats {
			if index == 5 {
				fmt.Fprintf(&out, "  ... %d more\n", len(repeats)-5)
				break
			}
			fmt.Fprintf(&out, "  %s x%d\n", item.amount, item.count)
		}
	}
	if threshold != nil {
		fmt.Fprintf(&out, "Amounts within 5%% below the threshold %s: %d\n", formatRat(threshold, 2, "half_up"), len(nearThreshold))
		for index, amount := range nearThreshold {
			if index == 10 {
				fmt.Fprintf(&out, "  ... %d more\n", len(nearThreshold)-10)
				break
			}
			fmt.Fprintf(&out, "  %s\n", amount)
		}
	}
	out.WriteString("\nBenford's law fits amounts that span several orders of magnitude and are not assigned or capped (fixed fees, prices, numbering series and narrow ranges do not follow it). A deviation is a lead to examine, not a finding.")
	return out.String()
}
