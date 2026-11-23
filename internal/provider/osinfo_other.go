//go:build !windows

package provider

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// codexOSInfo reports the operating system name and version the way the Codex
// client does: the distribution on Linux, Mac OS with the product version on
// macOS. A host it cannot read is named by its kernel family alone.
func codexOSInfo() string {
	switch runtime.GOOS {
	case "darwin":
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "sw_vers", "-productVersion").Output(); err == nil {
			if version := strings.TrimSpace(string(out)); version != "" {
				return "Mac OS " + version
			}
		}
		return "Mac OS"
	case "linux":
		if data, err := os.ReadFile("/etc/os-release"); err == nil {
			fields := map[string]string{}
			for _, line := range strings.Split(string(data), "\n") {
				if key, value, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
					fields[key] = strings.Trim(value, `"'`)
				}
			}
			if name := fields["NAME"]; name != "" {
				if version := fields["VERSION_ID"]; version != "" {
					return name + " " + version
				}
				return name
			}
		}
		return "Linux"
	}
	return strings.ToUpper(runtime.GOOS[:1]) + runtime.GOOS[1:]
}
