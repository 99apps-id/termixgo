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
