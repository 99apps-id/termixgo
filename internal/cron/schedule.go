// Package cron runs scheduled agent turns: reminders, reports and a periodic
// heartbeat. A job is stored, not executed from memory, so a restart resumes
// from the next due time instead of replaying what it missed.
package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Kind is how a schedule decides its next run.
type Kind string

const (
	// Every fires a fixed interval after the previous run.
	Every Kind = "every"
	// At fires once at an absolute time.
	At Kind = "at"
	// Cron fires on a five-field calendar expression.
	Cron Kind = "cron"
)

// Schedule is when a job runs. Exactly one field is meaningful for each kind.
type Schedule struct {
	Kind  Kind          `json:"kind"`
	Every time.Duration `json:"every,omitempty"`
	At    time.Time     `json:"at,omitempty"`
	Expr  string        `json:"expr,omitempty"`
}

// Parse reads the operator-facing spec. Accepted forms:
//
//	every 30m
//	at 2026-01-02 15:04
//	at 15:04
//	cron 0 9 * * 1-5
//	@daily
//
// A bare five-field expression is treated as a cron expression.
func Parse(spec string) (Schedule, error) {
	trimmed := strings.TrimSpace(spec)
	if trimmed == "" {
		return Schedule{}, fmt.Errorf("the schedule is empty")
	}
	if macro, ok := cronMacros[strings.ToLower(trimmed)]; ok {
		return compileCron(macro)
	}
	lower := strings.ToLower(trimmed)
	switch {
	case strings.HasPrefix(lower, "every "):
		value := strings.TrimSpace(trimmed[len("every "):])
		interval, err := time.ParseDuration(value)
		if err != nil {
			return Schedule{}, fmt.Errorf("every needs a duration such as 30m or 2h: %w", err)
		}
		if interval < time.Minute {
			return Schedule{}, fmt.Errorf("the interval must be at least one minute")
		}
		return Schedule{Kind: Every, Every: interval}, nil
	case strings.HasPrefix(lower, "at "):
		value := strings.TrimSpace(trimmed[len("at "):])
		when, err := parseAt(value, time.Now())
		if err != nil {
			return Schedule{}, err
		}
		return Schedule{Kind: At, At: when}, nil
	case strings.HasPrefix(lower, "cron "):
		return compileCron(strings.TrimSpace(trimmed[len("cron "):]))
	default:
		return compileCron(trimmed)
	}
}

// parseAt accepts an absolute timestamp, a date and time, or a bare time on the
// next matching day. A bare time already passed today rolls to tomorrow.
func parseAt(value string, now time.Time) (time.Time, error) {
	layouts := []struct {
		layout string
		date   bool
	}{
		{time.RFC3339, true},
		{"2006-01-02 15:04", true},
		{"2006-01-02 15:04:05", true},
		{"15:04", false},
		{"15:04:05", false},
	}
	for _, candidate := range layouts {
		parsed, err := time.ParseInLocation(candidate.layout, value, now.Location())
		if err != nil {
			continue
		}
		if !candidate.date {
			when := time.Date(now.Year(), now.Month(), now.Day(), parsed.Hour(), parsed.Minute(), parsed.Second(), 0, now.Location())
			if !when.After(now) {
				when = when.AddDate(0, 0, 1)
			}
			return when, nil
		}
		return parsed, nil
	}
	return time.Time{}, fmt.Errorf("at needs a time such as 2026-01-02 15:04, 15:04 or an RFC3339 timestamp")
}

// cronMacros are the shorthands a five-field expression cannot spell.
var cronMacros = map[string]string{
	"@hourly":   "0 * * * *",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@weekly":   "0 0 * * 0",
	"@monthly":  "0 0 1 * *",
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
}

var monthNames = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

var weekdayNames = map[string]int{
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
}

// cronExpr is a compiled five-field expression. Each field is a bitmask, so a
// match is a shift and an AND rather than a scan of the spec.
type cronExpr struct {
	minute    uint64
	hour      uint64
	dom       uint64
	month     uint64
	dow       uint64
	domStar   bool
	dowStar   bool
	canonical string
}

func compileCron(expr string) (Schedule, error) {
	parsed, err := parseCronExpr(expr)
	if err != nil {
		return Schedule{}, err
	}
	return Schedule{Kind: Cron, Expr: parsed.canonical}, nil
}

func parseCronExpr(expr string) (cronExpr, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return cronExpr{}, fmt.Errorf("a cron expression needs five fields (minute hour day month weekday), got %d", len(fields))
	}
	minute, _, err := compileField(fields[0], 0, 59, nil)
	if err != nil {
		return cronExpr{}, fmt.Errorf("minute field: %w", err)
	}
	hour, _, err := compileField(fields[1], 0, 23, nil)
	if err != nil {
		return cronExpr{}, fmt.Errorf("hour field: %w", err)
	}
	dom, domStar, err := compileField(fields[2], 1, 31, nil)
	if err != nil {
		return cronExpr{}, fmt.Errorf("day-of-month field: %w", err)
	}
	month, _, err := compileField(fields[3], 1, 12, monthNames)
	if err != nil {
		return cronExpr{}, fmt.Errorf("month field: %w", err)
	}
	dow, dowStar, err := compileField(fields[4], 0, 7, weekdayNames)
	if err != nil {
		return cronExpr{}, fmt.Errorf("weekday field: %w", err)
	}
	// Sunday is accepted as both 0 and 7, which is the convention cron uses.
	if dow&(1<<7) != 0 {
		dow = (dow &^ (1 << 7)) | 1
	}
	return cronExpr{
		minute:    minute,
		hour:      hour,
		dom:       dom,
		month:     month,
		dow:       dow,
		domStar:   domStar,
		dowStar:   dowStar,
		canonical: strings.Join(fields, " "),
	}, nil
}

// compileField turns one field into a bitmask. names resolves month and weekday
// words; numeric tokens parse directly.
func compileField(spec string, min, max int, names map[string]int) (uint64, bool, error) {
	trimmed := strings.TrimSpace(spec)
	if trimmed == "" {
		return 0, false, fmt.Errorf("the field is empty")
	}
	star := trimmed == "*"
	var bits uint64
	for _, part := range strings.Split(trimmed, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return 0, false, fmt.Errorf("an empty list item")
		}
		step := 1
		base := part
		if slash := strings.Index(part, "/"); slash >= 0 {
			base = strings.TrimSpace(part[:slash])
			value, err := strconv.Atoi(strings.TrimSpace(part[slash+1:]))
			if err != nil || value < 1 {
				return 0, false, fmt.Errorf("bad step in %q", part)
			}
			step = value
		}
		low, high := min, max
		switch {
		case base == "*":
			// The full range; low and high already cover it.
		case strings.Contains(base, "-"):
			bounds := strings.SplitN(base, "-", 2)
			lo, err := resolveValue(bounds[0], names)
			if err != nil {
				return 0, false, err
			}
			hi, err := resolveValue(bounds[1], names)
			if err != nil {
				return 0, false, err
			}
			low, high = lo, hi
		default:
			value, err := resolveValue(base, names)
			if err != nil {
				return 0, false, err
			}
			low, high = value, value
			if step > 1 {
				high = max
			}
		}
		if low < min || high > max || low > high {
			return 0, false, fmt.Errorf("value outside %d..%d in %q", min, max, part)
		}
		for value := low; value <= high; value += step {
			bits |= 1 << uint(value)
		}
	}
	return bits, star, nil
}

func resolveValue(token string, names map[string]int) (int, error) {
	token = strings.ToLower(strings.TrimSpace(token))
	if names != nil {
		if value, ok := names[token]; ok {
			return value, nil
		}
	}
	value, err := strconv.Atoi(token)
	if err != nil {
		return 0, fmt.Errorf("bad value %q", token)
	}
	return value, nil
}

// Next reports the first run after from. ok is false for a one-shot that has
// already passed, or an expression with no match in the look-ahead window.
func (s Schedule) Next(from time.Time) (time.Time, bool) {
	switch s.Kind {
	case Every:
		if s.Every <= 0 {
			return time.Time{}, false
		}
		return from.Add(s.Every), true
	case At:
		if s.At.IsZero() || !s.At.After(from) {
			return time.Time{}, false
		}
		return s.At, true
	case Cron:
		parsed, err := parseCronExpr(s.Expr)
		if err != nil {
			return time.Time{}, false
		}
		return parsed.next(from)
	}
	return time.Time{}, false
}

func (c cronExpr) next(from time.Time) (time.Time, bool) {
	loc := from.Location()
	current := time.Date(from.Year(), from.Month(), from.Day(), from.Hour(), from.Minute(), 0, 0, loc).Add(time.Minute)
	limit := current.AddDate(5, 0, 0)
	for current.Before(limit) {
		if c.month&(1<<uint(current.Month())) == 0 {
			current = time.Date(current.Year(), current.Month(), 1, 0, 0, 0, 0, loc).AddDate(0, 1, 0)
			continue
		}
		if !c.dayMatch(current) {
			current = time.Date(current.Year(), current.Month(), current.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1)
			continue
		}
		if c.hour&(1<<uint(current.Hour())) == 0 {
			current = time.Date(current.Year(), current.Month(), current.Day(), current.Hour(), 0, 0, 0, loc).Add(time.Hour)
			continue
		}
		if c.minute&(1<<uint(current.Minute())) == 0 {
			current = current.Add(time.Minute)
			continue
		}
		return current, true
	}
	return time.Time{}, false
}

// dayMatch implements cron's day rule: when both day-of-month and weekday are
// restricted, a match on either is enough; otherwise both fields must agree.
func (c cronExpr) dayMatch(t time.Time) bool {
	dom := c.domStar || c.dom&(1<<uint(t.Day())) != 0
	dow := c.dowStar || c.dow&(1<<uint(int(t.Weekday()))) != 0
	switch {
	case c.domStar && c.dowStar:
		return true
	case !c.domStar && !c.dowStar:
		return dom || dow
	default:
		return dom && dow
	}
}

// String renders the spec back for display.
func (s Schedule) String() string {
	switch s.Kind {
	case Every:
		return "every " + s.Every.String()
	case At:
		return "at " + s.At.Format("2006-01-02 15:04")
	case Cron:
		return "cron " + s.Expr
	}
	return ""
}

// Valid reports whether the schedule can produce a run.
func (s Schedule) Valid() bool {
	switch s.Kind {
	case Every:
		return s.Every >= time.Minute
	case At:
		return !s.At.IsZero()
	case Cron:
		_, err := parseCronExpr(s.Expr)
		return err == nil
	}
	return false
}
