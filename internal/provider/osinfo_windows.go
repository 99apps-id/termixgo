//go:build windows

package provider

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// codexOSInfo reports the Windows version the way the Codex client does:
// the NT version triple, so Windows 11 reads as 10.0.<build>.
func codexOSInfo() string {
	info := windows.RtlGetVersion()
	return fmt.Sprintf("Windows %d.%d.%d", info.MajorVersion, info.MinorVersion, info.BuildNumber)
}
