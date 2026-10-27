package health

import (
	"fmt"
	"strings"
	"time"
)

// Report holds system health and diagnostic counters.
type Report struct {
	DiskTotalBytes  uint64
	DiskFreeBytes   uint64
	DiskUsedPercent float64
	DiskWarning     string

	MemTotalBytes  uint64
	MemUsedBytes   uint64
	MemUsedPercent float64

	SwapTotalBytes  uint64
	SwapUsedBytes   uint64
	SwapUsedPercent float64
	SwapWarning     string

	Load1  float64
	Load5  float64
	Load15 float64

	OpenFileFDs    int
	FailedUnits    []string
	UptimeDuration time.Duration
	OOMCount       int
	AuthAnomalies  int
}

// Format returns human-readable multi-line summary of the health report.
func (r Report) Format() string {
	var b strings.Builder
	b.WriteString("=== System Health & Observability ===\n\n")

	// Disk
	if r.DiskTotalBytes > 0 {
		b.WriteString(fmt.Sprintf("Disk:       %.1f GB total, %.1f GB free (%.1f%% used)\n",
			float64(r.DiskTotalBytes)/(1<<30),
			float64(r.DiskFreeBytes)/(1<<30),
			r.DiskUsedPercent))
		if r.DiskWarning != "" {
			b.WriteString(fmt.Sprintf("            WARNING: %s\n", r.DiskWarning))
		}
	}

	// Memory
	if r.MemTotalBytes > 0 {
		b.WriteString(fmt.Sprintf("Memory:     %.1f GB total, %.1f GB used (%.1f%% used)\n",
			float64(r.MemTotalBytes)/(1<<30),
			float64(r.MemUsedBytes)/(1<<30),
			r.MemUsedPercent))
	}

	// Swap
	if r.SwapTotalBytes > 0 {
		b.WriteString(fmt.Sprintf("Swap:       %.1f GB total, %.1f GB used (%.1f%% used)\n",
			float64(r.SwapTotalBytes)/(1<<30),
			float64(r.SwapUsedBytes)/(1<<30),
			r.SwapUsedPercent))
		if r.SwapWarning != "" {
			b.WriteString(fmt.Sprintf("            WARNING: %s\n", r.SwapWarning))
		}
	}

	// Load & FDs
	if r.Load1 > 0 || r.Load5 > 0 || r.Load15 > 0 {
		b.WriteString(fmt.Sprintf("Load:       %.2f, %.2f, %.2f\n", r.Load1, r.Load5, r.Load15))
	}
	if r.OpenFileFDs > 0 {
		b.WriteString(fmt.Sprintf("Open FDs:   %d open descriptors\n", r.OpenFileFDs))
	}

	// Uptime
	if r.UptimeDuration > 0 {
		days := int(r.UptimeDuration.Hours()) / 24
		hours := int(r.UptimeDuration.Hours()) % 24
		mins := int(r.UptimeDuration.Minutes()) % 60
		b.WriteString(fmt.Sprintf("Uptime:     %dd %dh %dm\n", days, hours, mins))
	}

	// Services
	if len(r.FailedUnits) == 0 {
		b.WriteString("Services:   0 failed units\n")
	} else {
		b.WriteString(fmt.Sprintf("Services:   %d FAILED unit(s):\n", len(r.FailedUnits)))
		for _, unit := range r.FailedUnits {
			b.WriteString(fmt.Sprintf("            - %s\n", unit))
		}
	}

	// OOM & Auth
	if r.OOMCount > 0 {
		b.WriteString(fmt.Sprintf("OOM Kills:  WARNING: %d OOM kill event(s) detected\n", r.OOMCount))
	}
	if r.AuthAnomalies > 0 {
		b.WriteString(fmt.Sprintf("Auth:       WARNING: %d auth anomaly/mismatch event(s) detected\n", r.AuthAnomalies))
	}

	return b.String()
}
