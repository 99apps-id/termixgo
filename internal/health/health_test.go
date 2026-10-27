package health

import (
	"strings"
	"testing"
	"time"
)

func TestCollectRunsWithoutPanic(t *testing.T) {
	dir := t.TempDir()
	r := Collect(dir)
	formatted := r.Format()
	if !strings.Contains(formatted, "System Health & Observability") {
		t.Fatalf("Format should contain header, got: %s", formatted)
	}
}

func TestFormatOutputsWarnings(t *testing.T) {
	r := Report{
		DiskTotalBytes:  100 << 30,
		DiskFreeBytes:   5 << 30,
		DiskUsedPercent: 95.0,
		DiskWarning:     "disk usage is critical (95.0% >= 90%)",

		MemTotalBytes:  16 << 30,
		MemUsedBytes:   14 << 30,
		MemUsedPercent: 87.5,

		SwapTotalBytes:  2 << 30,
		SwapUsedBytes:   1500 << 20,
		SwapUsedPercent: 75.0,
		SwapWarning:     "swap usage is high (75.0%, 1500 MB used)",

		Load1:          2.5,
		Load5:          1.8,
		Load15:         1.2,
		OpenFileFDs:    42,
		UptimeDuration: 48 * time.Hour,
		FailedUnits:    []string{"failed-service.service"},
		OOMCount:       1,
		AuthAnomalies:  2,
	}

	formatted := r.Format()
	if !strings.Contains(formatted, "WARNING: disk usage is critical") {
		t.Errorf("missing disk warning in %s", formatted)
	}
	if !strings.Contains(formatted, "WARNING: swap usage is high") {
		t.Errorf("missing swap warning in %s", formatted)
	}
	if !strings.Contains(formatted, "failed-service.service") {
		t.Errorf("missing failed service in %s", formatted)
	}
	if !strings.Contains(formatted, "OOM Kills") {
		t.Errorf("missing OOM kills in %s", formatted)
	}
	if !strings.Contains(formatted, "Auth:") {
		t.Errorf("missing Auth anomalies in %s", formatted)
	}
}
