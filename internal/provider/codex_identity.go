package provider

import (
	"encoding/json"
	"os"
	"runtime"
	"strings"
)

// codexOriginator is the originator the Codex terminal client sends. Only the
// non-interactive exec mode changes it, so the interactive value is the one a
// terminal agent presents.
const codexOriginator = "codex_cli_rs"

// codexArch is the CPU spelling in the Codex User-Agent.
func codexArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64"
	case "arm64":
		if runtime.GOOS == "linux" {
			return "aarch64"
		}
		return "arm64"
	case "386":
		return "x86"
	}
	return runtime.GOARCH
}

// codexTerminal is the terminal token the client reports, taken from the
// environment the same way it does: the program name when the terminal sets
// one, then the TERM value.
func codexTerminal() string {
	if program := strings.TrimSpace(os.Getenv("TERM_PROGRAM")); program != "" {
		if version := strings.TrimSpace(os.Getenv("TERM_PROGRAM_VERSION")); version != "" {
			return program + "/" + version
		}
		return program
	}
	if strings.TrimSpace(os.Getenv("WT_SESSION")) != "" {
		return "WindowsTerminal"
	}
	if term := strings.TrimSpace(os.Getenv("TERM")); term != "" {
		return term
	}
	return "unknown"
}

// codexUserAgent is the interactive Codex CLI identity:
// originator/version (os version; arch) terminal (client; version).
func codexUserAgent() string {
	return codexOriginator + "/" + codexCLIVersion + " (" + codexOSInfo() + "; " + codexArch() + ") " +
		codexTerminal() + " (codex-tui; " + codexCLIVersion + ")"
}

// codexQuotaUserAgent is the shorter identity the client uses for its
// account calls (usage and model list), which omit the trailing client pair.
func codexQuotaUserAgent() string {
	return codexOriginator + "/" + codexCLIVersion + " (" + codexOSInfo() + "; " + codexArch() + ") " + codexTerminal()
}

// codexTurnMetadata is the X-Codex-Turn-Metadata header: one JSON object that
// names the installation, session, thread, turn and window.
func codexTurnMetadata(session, turn string) string {
	encoded, err := json.Marshal(map[string]any{
		"installation_id": installationUUID(),
		"session_id":      session,
		"thread_id":       session,
		"turn_id":         turn,
		"window_id":       session + ":0",
		"request_kind":    "turn",
		"thread_source":   "user",
	})
	if err != nil {
		return "{}"
	}
	return string(encoded)
}
