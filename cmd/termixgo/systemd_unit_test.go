package main

import (
	"strings"
	"testing"
)

// The generated unit keeps the assistant up around the clock: always restart,
// no start-rate limit, and the binary path in ExecStart.
func TestSystemdUnitRestartsAlways(t *testing.T) {
	unit := systemdUnit("/home/u/.local/bin/termixgo")
	for _, want := range []string{
		"ExecStart=/home/u/.local/bin/termixgo serve",
		"Restart=always",
		"StartLimitIntervalSec=0",
		"WantedBy=default.target",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit lacks %q:\n%s", want, unit)
		}
	}
	if strings.Contains(unit, "on-failure") {
		t.Fatalf("a clean exit must also restart:\n%s", unit)
	}
	if !strings.Contains(unit, `%h/.local/bin`) {
		t.Fatalf("the %%h specifier must reach systemd unescaped:\n%s", unit)
	}
}
