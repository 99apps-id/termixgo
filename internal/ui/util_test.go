package ui

import "testing"

// TestSanitizeTextDropsMouseReports pins the leak: a tool can emit SGR mouse
// reports (ESC[<b;x;yM, mode 1006) that the old [0-9;?]* CSI class did not
// match, so the parameter bytes rendered as "[<35;106;27M" in the transcript.
func TestSanitizeTextDropsMouseReports(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"whole sequence", "before\x1b[<35;106;27M after", "before after"},
		{"release event", "x\x1b[<0;10;5m y", "x y"},
		{"esc already stripped", "run[<35;106;27Mdone", "rundone"},
		{"dec private mode", "a\x1b[?25lb", "ab"},
		{"sgr colour is stripped too", "keep\x1b[31mred\x1b[0m", "keepred"},
		{"plain text untouched", "hello world", "hello world"},
	}
	for _, testCase := range cases {
		if got := sanitizeText(testCase.in); got != testCase.want {
			t.Errorf("%s: sanitizeText(%q) = %q, want %q", testCase.name, testCase.in, got, testCase.want)
		}
	}
}
