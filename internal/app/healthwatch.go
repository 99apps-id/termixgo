package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/health"
)

const (
	defaultHealthInterval = 5 * time.Minute
	defaultHealthCooldown = time.Hour
	defaultHealthPercent  = 90
	// firstHealthCheckDelay lets the daemon finish starting before it reads the
	// machine, and it is short enough that a disk already full at boot is named
	// before the next write fails.
	firstHealthCheckDelay = 30 * time.Second
	maxFailedUnitsListed  = 5
)

// healthThresholds are the percentages that arm an alert.
type healthThresholds struct {
	disk   int
	memory int
	swap   int
}

// healthAlerter decides which conditions are worth a message right now. One
// condition stays quiet for the cooldown after it was reported, so a disk that
// stays full does not page the operator every check.
type healthAlerter struct {
	thresholds healthThresholds
	cooldown   time.Duration
	last       map[string]time.Time
}

func newHealthAlerter(thresholds healthThresholds, cooldown time.Duration) *healthAlerter {
	return &healthAlerter{
		thresholds: thresholds,
		cooldown:   cooldown,
		last:       map[string]time.Time{},
	}
}

// evaluate returns the alert lines due at now and records them as sent.
func (h *healthAlerter) evaluate(report health.Report, now time.Time) []string {
	var alerts []string
	due := func(key string) bool {
		if last, ok := h.last[key]; ok && now.Sub(last) < h.cooldown {
			return false
		}
		h.last[key] = now
		return true
	}

	if report.DiskTotalBytes > 0 && report.DiskUsedPercent >= float64(h.thresholds.disk) && due("disk") {
		alerts = append(alerts, fmt.Sprintf("Disk is %.1f%% full, with %.1f GB free of %.1f GB.",
			report.DiskUsedPercent,
			float64(report.DiskFreeBytes)/(1<<30),
			float64(report.DiskTotalBytes)/(1<<30)))
	}
	if report.MemTotalBytes > 0 && report.MemUsedPercent >= float64(h.thresholds.memory) && due("memory") {
		alerts = append(alerts, fmt.Sprintf("Memory is %.1f%% used: %.1f GB of %.1f GB in play.",
			report.MemUsedPercent,
			float64(report.MemUsedBytes)/(1<<30),
			float64(report.MemTotalBytes)/(1<<30)))
	}
	if report.SwapTotalBytes > 0 && report.SwapUsedPercent >= float64(h.thresholds.swap) && due("swap") {
		alerts = append(alerts, fmt.Sprintf("Swap is %.1f%% used: %.1f GB of %.1f GB.",
			report.SwapUsedPercent,
			float64(report.SwapUsedBytes)/(1<<30),
			float64(report.SwapTotalBytes)/(1<<30)))
	}
	if len(report.FailedUnits) > 0 && due("units") {
		listed := report.FailedUnits
		if len(listed) > maxFailedUnitsListed {
			listed = listed[:maxFailedUnitsListed]
		}
		alerts = append(alerts, fmt.Sprintf("%d systemd unit(s) failed: %s.",
			len(report.FailedUnits), strings.Join(listed, ", ")))
	}
	if report.OOMCount > 0 && due("oom") {
		alerts = append(alerts, fmt.Sprintf("%d out-of-memory kill(s) were recorded.", report.OOMCount))
	}
	return alerts
}

// healthWatchSettings resolves the configured values onto their defaults and
// clamps a percentage that cannot mean anything.
func healthWatchSettings(cfg config.HealthAlerts) (time.Duration, healthThresholds, time.Duration, []string) {
	interval := defaultHealthInterval
	if parsed, err := time.ParseDuration(strings.TrimSpace(cfg.Interval)); err == nil && parsed >= time.Minute {
		interval = parsed
	}
	cooldown := defaultHealthCooldown
	if parsed, err := time.ParseDuration(strings.TrimSpace(cfg.Cooldown)); err == nil && parsed > 0 {
		cooldown = parsed
	}
	clamp := func(value int) int {
		switch {
		case value <= 0:
			return defaultHealthPercent
		case value > 100:
			return 100
		default:
			return value
		}
	}
	thresholds := healthThresholds{
		disk:   clamp(cfg.DiskPercent),
		memory: clamp(cfg.MemoryPercent),
		swap:   clamp(cfg.SwapPercent),
	}
	return interval, thresholds, cooldown, cfg.Notify
}

// StartHealthWatch reads the machine on a timer while ctx lives and reports
// pressure to the operator's chat. It does nothing unless healthAlerts.enabled
// is set. Only `serve` calls it: a terminal session pays nothing for it.
func (a *App) StartHealthWatch(ctx context.Context) error {
	a.mu.Lock()
	cfg := a.cfg.HealthAlerts
	a.mu.Unlock()
	if !cfg.Enabled {
		return nil
	}
	interval, thresholds, cooldown, notify := healthWatchSettings(cfg)
	alerter := newHealthAlerter(thresholds, cooldown)
	a.mu.Lock()
	a.healthStatus = fmt.Sprintf("on, every %s", interval)
	a.mu.Unlock()

	go func() {
		timer := time.NewTimer(firstHealthCheckDelay)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				a.checkHealth(ctx, alerter, notify)
				timer.Reset(interval)
			}
		}
	}()
	return nil
}

// checkHealth reads one report, sends whatever is due, and records the result
// for the status line. A delivery failure is named rather than hidden: an
// alert nobody received is the failure that matters here.
func (a *App) checkHealth(ctx context.Context, alerter *healthAlerter, notify []string) {
	report := health.Collect(a.workspace)
	alerts := alerter.evaluate(report, time.Now())
	if len(alerts) == 0 {
		a.mu.Lock()
		a.healthStatus = fmt.Sprintf("last check %s, all clear", time.Now().Format("15:04"))
		a.mu.Unlock()
		return
	}
	text := "Termixgo health warning:\n" + strings.Join(alerts, "\n")
	deliveryErrors := 0
	if len(notify) == 0 {
		if err := a.Notify(ctx, text); err != nil {
			deliveryErrors++
		}
	} else {
		for _, target := range notify {
			if err := a.DeliverTo(ctx, target, text); err != nil {
				deliveryErrors++
			}
		}
	}
	status := fmt.Sprintf("last check %s, %d alert(s)", time.Now().Format("15:04"), len(alerts))
	if deliveryErrors > 0 {
		status += fmt.Sprintf(", %d delivery failure(s)", deliveryErrors)
	}
	a.mu.Lock()
	a.healthStatus = status
	a.mu.Unlock()
}

// HealthStatus reports the watcher for /status and the serve banner.
func (a *App) HealthStatus() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.healthStatus
}
