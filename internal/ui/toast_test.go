package ui

import (
	"strings"
	"testing"
)

// The notification text never enters the script or AppleScript source, so a
// detail with quotes, < or & cannot break the toast or inject code.
func TestToastTextTravelsOnlyInTheEnvironment(t *testing.T) {
	hostile := `rm -rf "/" & <b>'x'</b>`
	for _, goos := range []string{"windows", "darwin"} {
		name, args, env := toastCommand(goos, "Approval needed", hostile)
		if name == "" {
			t.Fatalf("%s: no command", goos)
		}
		for _, arg := range args {
			if strings.Contains(arg, hostile) {
				t.Fatalf("%s: the message is in the arguments", goos)
			}
		}
		if !strings.Contains(strings.Join(env, "\n"), "TERMIXGO_TOAST_MESSAGE="+hostile) {
			t.Fatalf("%s: env = %q", goos, env)
		}
	}
	_, args, env := toastCommand("windows", "t", "m")
	// -WindowStyle Hidden would hide the shared console, which is the terminal
	// the operator is typing in. The helper gets its own console instead.
	for _, arg := range args {
		if strings.EqualFold(arg, "-WindowStyle") {
			t.Fatalf("the Windows toast must not ask PowerShell to hide a window: %q", args)
		}
	}
	if !strings.Contains(args[len(args)-1], "SecurityElement]::Escape") || !strings.Contains(strings.Join(env, " "), "WindowsPowerShell") {
		t.Fatalf("the Windows toast must escape its text and use a registered app id")
	}
}
