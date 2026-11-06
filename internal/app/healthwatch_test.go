package app

import (
	"strings"
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/health"
)

func TestHealthWatchSettingsDefaults(t *testing.T) {
	interval, thresholds, cooldown, notify := healthWatchSettings(config.HealthAlerts{})
	if interval != defaultHealthInterval {
		t.Fatalf("interval = %s", interval)
	}
	if cooldown != defaultHealthCooldown {
		t.Fatalf("cooldown = %s", cooldown)
	}
	if thresholds.disk != 90 || thresholds.memory != 90 || thresholds.swap != 90 {
		t.Fatalf("thresholds = %+v", thresholds)
	}
	if len(notify) != 0 {
		t.Fatalf("notify = %v", notify)
	}
}

func TestHealthWatchSettingsOverrides(t *testing.T) {
	interval, thresholds, cooldown, notify := healthWatchSettings(config.HealthAlerts{
		Interval:      "2m",
		Cooldown:      "30m",
		DiskPercent:   80,
		MemoryPercent: 150,
		SwapPercent:   -1,
		Notify:        []string{"telegram:default:1"},
	})
	if interval != 2*time.Minute || cooldown != 30*time.Minute {
		t.Fatalf("interval=%s cooldown=%s", interval, cooldown)
	}
	if thresholds.disk != 80 {
		t.Fatalf("disk threshold = %d", thresholds.disk)
	}
	// A value over 100 would never fire, so it is clamped; a negative one
	// falls back to the default.
	if thresholds.memory != 100 || thresholds.swap != 90 {
		t.Fatalf("thresholds = %+v", thresholds)
	}
	if len(notify) != 1 {
		t.Fatalf("notify = %v", notify)
	}
}

func TestHealthAlerterThresholds(t *testing.T) {
	alerter := newHealthAlerter(healthThresholds{disk: 90, memory: 90, swap: 90}, time.Hour)
	now := time.Now()

	quiet := health.Report{DiskTotalBytes: 100 << 30, DiskFreeBytes: 20 << 30, DiskUsedPercent: 80}
	if alerts := alerter.evaluate(quiet, now); len(alerts) != 0 {
		t.Fatalf("a healthy machine produced %v", alerts)
	}

	full := health.Report{
		DiskTotalBytes:  100 << 30,
		DiskFreeBytes:   5 << 30,
		DiskUsedPercent: 95,
		MemTotalBytes:   8 << 30,
		MemUsedBytes:    8 << 30,
		MemUsedPercent:  97,
		SwapTotalBytes:  2 << 30,
		SwapUsedBytes:   2 << 30,
		SwapUsedPercent: 99,
	}
	alerts := alerter.evaluate(full, now)
	if len(alerts) != 3 {
		t.Fatalf("alerts = %v", alerts)
	}
	joined := strings.Join(alerts, "\n")
	for _, want := range []string{"Disk", "Memory", "Swap"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("alerts %q missing %q", joined, want)
		}
	}
}

func TestHealthAlerterCooldown(t *testing.T) {
	alerter := newHealthAlerter(healthThresholds{disk: 90, memory: 90, swap: 90}, time.Hour)
	now := time.Now()
	full := health.Report{DiskTotalBytes: 100, DiskFreeBytes: 1, DiskUsedPercent: 99}

	if alerts := alerter.evaluate(full, now); len(alerts) != 1 {
		t.Fatalf("first check = %v", alerts)
	}
	if alerts := alerter.evaluate(full, now.Add(30*time.Minute)); len(alerts) != 0 {
		t.Fatalf("a condition inside the cooldown reported again: %v", alerts)
	}
	if alerts := alerter.evaluate(full, now.Add(61*time.Minute)); len(alerts) != 1 {
		t.Fatalf("a condition past the cooldown stayed quiet: %v", alerts)
	}
}

func TestHealthAlerterIgnoresUnknownCounters(t *testing.T) {
	alerter := newHealthAlerter(healthThresholds{disk: 0, memory: 0, swap: 0}, time.Hour)
	// A host that reports no swap total must not be read as 0% used and must
	// not be read as 100% either.
	alerts := alerter.evaluate(health.Report{MemUsedPercent: 99}, time.Now())
	if len(alerts) != 0 {
		t.Fatalf("alerts = %v", alerts)
	}
}

func TestHealthAlerterReportsUnitsAndOOM(t *testing.T) {
	alerter := newHealthAlerter(healthThresholds{disk: 90, memory: 90, swap: 90}, time.Hour)
	report := health.Report{
		FailedUnits: []string{"a.service", "b.service", "c.service", "d.service", "e.service", "f.service"},
		OOMCount:    2,
	}
	alerts := alerter.evaluate(report, time.Now())
	if len(alerts) != 2 {
		t.Fatalf("alerts = %v", alerts)
	}
	joined := strings.Join(alerts, "\n")
	if !strings.Contains(joined, "6 systemd unit(s) failed") {
		t.Fatalf("alerts = %q", joined)
	}
	// The list is capped so one message stays readable.
	if strings.Contains(joined, "f.service") {
		t.Fatalf("unit list was not capped: %q", joined)
	}
	if !strings.Contains(joined, "2 out-of-memory") {
		t.Fatalf("alerts = %q", joined)
	}
}
