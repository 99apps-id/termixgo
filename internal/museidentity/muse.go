package museidentity

import (
	"fmt"
	"runtime"
)

// CLIVersion is the Muse Code client identity accepted by Meta's gateway.
const CLIVersion = "1.4.24"

// UserAgent returns the official Muse build identity for the current platform.
func UserAgent() string {
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x86_64"
	}
	return fmt.Sprintf("muse-build/%s (non-interactive; %s-%s)", CLIVersion, runtime.GOOS, arch)
}
