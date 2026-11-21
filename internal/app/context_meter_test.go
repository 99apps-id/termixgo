package app

import (
	"strings"
	"testing"
)

// The status gauge uses the provider's count once a step has reported one,
// adds an estimate only for what came after, and ignores a figure from
// another session.
func TestContextUsagePrefersTheMeasuredCount(t *testing.T) {
	application := newTestApp(t)
	estimated := application.ContextUsage()
	if !strings.HasPrefix(estimated, "ctx: ~") {
		t.Fatalf("before any step the gauge is an estimate: %q", estimated)
	}

	application.recordMeasuredContext(50000)
	measured := application.ContextUsage()
	if !strings.HasPrefix(measured, "ctx: 50.0k/") {
		t.Fatalf("the measured count should be shown: %q", measured)
	}

	application.Session().AddUser(strings.Repeat("more text ", 400))
	grown := application.ContextUsage()
	if strings.HasPrefix(grown, "ctx: 50.0k/") || strings.HasPrefix(grown, "ctx: ~") {
		t.Fatalf("messages after the measurement should be added to it: %q", grown)
	}

	application.NewSession()
	if fresh := application.ContextUsage(); !strings.HasPrefix(fresh, "ctx: ~") {
		t.Fatalf("a new session must not inherit the old measurement: %q", fresh)
	}
}
