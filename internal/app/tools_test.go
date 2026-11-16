package app

import (
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
)

func TestBuildDefaultToolsFollowsDesktopControl(t *testing.T) {
	off := buildDefaultTools(config.Config{})
	for _, name := range []string{"desktop_screenshot", "desktop_input", "desktop_windows"} {
		if _, ok := off.Lookup(name); ok {
			t.Fatalf("%s must be absent while desktop control is off", name)
		}
	}
	if _, ok := off.Lookup("read_file"); !ok {
		t.Fatal("read_file must survive the filter")
	}

	on := buildDefaultTools(config.Config{DesktopControl: true})
	for _, name := range []string{"desktop_screenshot", "desktop_input", "desktop_windows"} {
		if _, ok := on.Lookup(name); !ok {
			t.Fatalf("%s must be offered when desktop control is on", name)
		}
	}
}

// TestDefaultRegistryDefersToToolSearch pins the token fix: the built-in
// registry is large enough that sending every schema on every step costs
// tens of thousands of tokens per step, so a default turn must defer to
// tool search instead of staying under a too-high inline cap.
func TestDefaultRegistryDefersToToolSearch(t *testing.T) {
	count := len(buildDefaultTools(config.Config{}).Tools())
	if !shouldDeferTools(count) {
		t.Fatalf("default registry of %d tools must defer to tool search, not send every schema inline", count)
	}
}

func TestShouldDeferToolsAboveTheInlineCap(t *testing.T) {
	if shouldDeferTools(maxInlineTools) {
		t.Fatalf("%d tools still fit inline", maxInlineTools)
	}
	if !shouldDeferTools(maxInlineTools + 1) {
		t.Fatalf("%d tools must switch to tool search", maxInlineTools+1)
	}
}
