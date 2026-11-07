package app

import (
	"strings"
	"testing"
)

func TestEmptyTurnMessageNamesTheStopReason(t *testing.T) {
	cases := []struct {
		reason string
		want   string
	}{
		{"loop-guard", "kept repeating"},
		{"step-cap", "step budget"},
		{"cost-cap", "cost budget"},
		{"aborted", "stopped"},
		{"error", "failed"},
		{"", "finished without writing an answer"},
	}
	for _, test := range cases {
		got := emptyTurnMessage(test.reason)
		if strings.TrimSpace(got) == "" {
			t.Errorf("emptyTurnMessage(%q) is blank", test.reason)
			continue
		}
		if !strings.Contains(got, test.want) {
			t.Errorf("emptyTurnMessage(%q) = %q, want it to mention %q", test.reason, got, test.want)
		}
	}
}
