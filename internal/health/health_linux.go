//go:build linux

package health

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Collect gathers system health metrics for Linux.
func Collect(dir string) Report {
	var r Report
	if dir == "" {
		dir = "."
	}

	// 1. Disk usage via statfs
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err == nil {
		r.DiskTotalBytes = stat.Blocks * uint64(stat.Bsize)
		r.DiskFreeBytes = stat.Bavail * uint64(stat.Bsize)
		if r.DiskTotalBytes > 0 {
			used := r.DiskTotalBytes - r.DiskFreeBytes
			r.DiskUsedPercent = float64(used) / float64(r.DiskTotalBytes) * 100.0
			if r.DiskUsedPercent >= 90.0 {
				r.DiskWarning = fmt.Sprintf("disk usage is critical (%.1f%% >= 90%%)", r.DiskUsedPercent)
			}
		}
	}

	// 2. Memory & Swap from /proc/meminfo
	if memInfo, err := os.ReadFile("/proc/meminfo"); err == nil {
		var memTotal, memAvail, swapTotal, swapFree uint64
		scanner := bufio.NewScanner(bytes.NewReader(memInfo))
		for scanner.Scan() {
			line := scanner.Text()
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				val, _ := strconv.ParseUint(fields[1], 10, 64)
				valBytes := val * 1024
				switch fields[0] {
				case "MemTotal:":
					memTotal = valBytes
				case "MemAvailable:":
					memAvail = valBytes
				case "SwapTotal:":
					swapTotal = valBytes
				case "SwapFree:":
					swapFree = valBytes
				}
			}
		}
		r.MemTotalBytes = memTotal
		if memTotal > 0 && memTotal >= memAvail {
			r.MemUsedBytes = memTotal - memAvail
			r.MemUsedPercent = float64(r.MemUsedBytes) / float64(memTotal) * 100.0
		}
		r.SwapTotalBytes = swapTotal
		if swapTotal > 0 && swapTotal >= swapFree {
			r.SwapUsedBytes = swapTotal - swapFree
			r.SwapUsedPercent = float64(r.SwapUsedBytes) / float64(swapTotal) * 100.0
			if r.SwapUsedPercent >= 50.0 || r.SwapUsedBytes > (500<<20) {
				r.SwapWarning = fmt.Sprintf("swap usage is high (%.1f%%, %d MB used)", r.SwapUsedPercent, r.SwapUsedBytes>>20)
			}
		}
	}

	// 3. System Load from /proc/loadavg
	if loadData, err := os.ReadFile("/proc/loadavg"); err == nil {
		parts := strings.Fields(string(loadData))
		if len(parts) >= 3 {
			r.Load1, _ = strconv.ParseFloat(parts[0], 64)
			r.Load5, _ = strconv.ParseFloat(parts[1], 64)
			r.Load15, _ = strconv.ParseFloat(parts[2], 64)
		}
	}

	// 4. Open File Descriptors
	if fdEntries, err := os.ReadDir("/proc/self/fd"); err == nil {
		r.OpenFileFDs = len(fdEntries)
	}

	// 5. Uptime from /proc/uptime
	if uptimeData, err := os.ReadFile("/proc/uptime"); err == nil {
		parts := strings.Fields(string(uptimeData))
		if len(parts) >= 1 {
			if secs, err := strconv.ParseFloat(parts[0], 64); err == nil {
				r.UptimeDuration = time.Duration(secs * float64(time.Second))
			}
		}
	}

	// 6. Systemd Failed Units
	cmd := exec.Command("systemctl", "--failed", "--no-legend", "--no-pager")
	if out, err := cmd.Output(); err == nil && len(out) > 0 {
		scanner := bufio.NewScanner(bytes.NewReader(out))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" {
				fields := strings.Fields(line)
				if len(fields) > 0 {
					unit := fields[0]
					if (unit == "●" || unit == "*" || unit == "×") && len(fields) > 1 {
						unit = fields[1]
					}
					r.FailedUnits = append(r.FailedUnits, unit)
				}
			}
		}
	}

	// 7. OOM & Auth Anomaly scanning
	checkKernelLogs(&r)

	return r
}

// logReadCap bounds how much of a rotating log one health check reads. The
// auth and kernel logs on a public VPS grow by megabytes a day under scan
// traffic, and the old whole-file reads charged every one of those bytes to a
// machine the project budgets at two gigabytes.
const logReadCap = 8 * 1024 * 1024

// readLogTail reads at most the last logReadCap bytes of a file: anomalies
// are recent by definition, and the head of a rotated log is the least
// interesting part of it.
func readLogTail(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	offset := int64(0)
	if info.Size() > logReadCap {
		offset = info.Size() - logReadCap
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(file, logReadCap))
}

func checkKernelLogs(r *Report) {
	// Check dmesg or /var/log/dmesg for OOM kills (best effort)
	cmd := exec.Command("dmesg", "-T")
	out, err := cmd.Output()
	if err != nil {
		// Fall back to reading /var/log/dmesg if dmesg is restricted
		out, _ = readLogTail("/var/log/dmesg")
	} else if int64(len(out)) > logReadCap {
		out = out[len(out)-logReadCap:]
	}
	if len(out) > 0 {
		oomMatches := strings.Count(string(out), "Out of memory: Kill process")
		oomMatches += strings.Count(string(out), "invoked oom-killer")
		r.OOMCount = oomMatches
	}

	// Check auth.log for ssh protocol mismatch if readable
	for _, logPath := range []string{"/var/log/auth.log", "/var/log/secure"} {
		if data, err := readLogTail(filepath.Clean(logPath)); err == nil {
			r.AuthAnomalies = strings.Count(string(data), "Did not receive identification string") +
				strings.Count(string(data), "Bad protocol version identification")
			break
		}
	}
}
