//go:build !linux

package health

import (
	"runtime"
	"time"
)

// Collect gathers basic runtime health metrics on non-Linux platforms.
func Collect(dir string) Report {
	var r Report
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	r.MemTotalBytes = m.Sys
	r.MemUsedBytes = m.Alloc
	if r.MemTotalBytes > 0 {
		r.MemUsedPercent = float64(r.MemUsedBytes) / float64(r.MemTotalBytes) * 100.0
	}

	r.UptimeDuration = time.Since(processStartTime)
	return r
}

var processStartTime = time.Now()
