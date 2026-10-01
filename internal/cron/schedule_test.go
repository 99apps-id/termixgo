package cron

import (
	"testing"
	"time"
)

func TestParseForms(t *testing.T) {
	cases := []struct {
		spec string
		kind Kind
	}{
		{"every 30m", Every},
		{"every 2h", Every},
		{"at 15:04", At},
		{"at 2026-01-02 15:04", At},
		{"@daily", Cron},
		{"cron 0 9 * * 1-5", Cron},
		{"0 9 * * 1-5", Cron},
	}
	for _, testCase := range cases {
		schedule, err := Parse(testCase.spec)
		if err != nil {
			t.Fatalf("Parse(%q): %v", testCase.spec, err)
		}
		if schedule.Kind != testCase.kind {
			t.Errorf("Parse(%q) kind = %q, want %q", testCase.spec, schedule.Kind, testCase.kind)
		}
	}
}

func TestParseRejectsBadInput(t *testing.T) {
	for _, spec := range []string{"", "nonsense", "every 5s", "cron 99 * * * *", "0 9 * *"} {
		if _, err := Parse(spec); err == nil {
			t.Errorf("Parse(%q) should fail", spec)
		}
	}
}

func TestEveryNext(t *testing.T) {
	schedule := Schedule{Kind: Every, Every: 30 * time.Minute}
	now := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	next, ok := schedule.Next(now)
	if !ok || !next.Equal(now.Add(30*time.Minute)) {
		t.Fatalf("next = %v ok=%v, want %v", next, ok, now.Add(30*time.Minute))
	}
}

func TestAtNext(t *testing.T) {
	now := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	future := Schedule{Kind: At, At: now.Add(time.Hour)}
	if next, ok := future.Next(now); !ok || !next.Equal(future.At) {
		t.Fatalf("future next = %v ok=%v", next, ok)
	}
	past := Schedule{Kind: At, At: now.Add(-time.Hour)}
	if _, ok := past.Next(now); ok {
		t.Fatalf("a past one-shot should have no next run")
	}
}

func TestCronNext(t *testing.T) {
	// 2026-01-05 is a Monday.
	monday := time.Date(2026, 1, 5, 8, 0, 0, 0, time.UTC)
	cases := []struct {
		expr string
		want time.Time
	}{
		{"0 9 * * 1-5", time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)},
		{"*/15 * * * *", time.Date(2026, 1, 5, 8, 15, 0, 0, time.UTC)},
		{"0 0 1 * *", time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)},
		{"0 0 * * 5", time.Date(2026, 1, 9, 0, 0, 0, 0, time.UTC)},
		// Both day fields restricted: cron fires when either matches.
		{"0 0 13 * 5", time.Date(2026, 1, 9, 0, 0, 0, 0, time.UTC)},
	}
	for _, testCase := range cases {
		schedule, err := Parse("cron " + testCase.expr)
		if err != nil {
			t.Fatalf("Parse(cron %s): %v", testCase.expr, err)
		}
		next, ok := schedule.Next(monday)
		if !ok || !next.Equal(testCase.want) {
			t.Errorf("cron %q next = %v ok=%v, want %v", testCase.expr, next, ok, testCase.want)
		}
	}
}
