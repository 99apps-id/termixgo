//go:build linux

package app

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// currentBootTime is when the machine booted, in unix seconds, or 0 when it
// cannot be read. A different value from the last run means the host restarted.
func currentBootTime() int64 {
	file, err := os.Open("/proc/stat")
	if err != nil {
		return 0
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if value, ok := strings.CutPrefix(scanner.Text(), "btime "); ok {
			boot, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			return boot
		}
	}
	return 0
}
