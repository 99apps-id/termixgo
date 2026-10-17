package oauth

import (
	"os/exec"
	"runtime"
	"strings"
)

// openBrowser opens a login URL in the default browser. A failure is ignored:
// the caller has printed the URL, so the operator can open it by hand.
//
// Only http/https URLs reach the launcher: the login flow builds these URLs
// itself, and refusing anything else keeps a malformed provider response from
// turning the opener into a generic program launcher. The helper resolves
// through PATH for the same reason.
func openBrowser(link string) {
	trimmed := strings.TrimSpace(link)
	lower := strings.ToLower(trimmed)
	if trimmed == "" || (!strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://")) {
		return
	}
	var names []string
	var fixedArgs []string
	switch runtime.GOOS {
	case "windows":
		names = []string{"rundll32"}
		fixedArgs = []string{"url.dll,FileProtocolHandler"}
	case "darwin":
		names = []string{"open"}
	default:
		names = []string{"xdg-open"}
	}
	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil {
			_ = exec.Command(path, append(fixedArgs, trimmed)...).Start()
			return
		}
	}
}
