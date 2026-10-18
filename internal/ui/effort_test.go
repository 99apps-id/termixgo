package ui

import (
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
)

// TestSlashEffortSetsAndClearsTheLevel covers the command loop: a level is
// accepted, an unknown one is refused instead of being written to the config,
// and "default" clears the setting.
func TestSlashEffortSetsAndClearsTheLevel(t *testing.T) {
	model := newTestModel(t)

	if _, out := runSlash(t, model, "/effort high"); !strings.Contains(out, "high") {
		t.Errorf("/effort high reported %q", out)
	}
	if got := model.app.EffortFor(model.app.CurrentModel().ID); got != "high" {
		t.Errorf("effort after /effort high = %q, want high", got)
	}

	if _, out := runSlash(t, model, "/effort turbo"); !strings.Contains(out, "effort must be one of") {
		t.Errorf("/effort turbo should be refused, reported %q", out)
	}
	if got := model.app.EffortFor(model.app.CurrentModel().ID); got != "high" {
		t.Errorf("a refused level must leave the setting alone, got %q", got)
	}

	if _, out := runSlash(t, model, "/effort default"); !strings.Contains(out, "default") {
		t.Errorf("/effort default reported %q", out)
	}
	if got := model.app.EffortFor(model.app.CurrentModel().ID); got != "" {
		t.Errorf("effort after /effort default = %q, want empty", got)
	}
}

// TestSlashEffortWithoutArgumentsShowsTheLevels keeps the command discoverable:
// a bare /effort names the current level and every accepted one.
func TestSlashEffortWithoutArgumentsShowsTheLevels(t *testing.T) {
	model := newTestModel(t)
	if _, out := runSlash(t, model, "/effort"); !strings.Contains(out, "(provider default)") {
		t.Errorf("a bare /effort should name the provider default, got %q", out)
	}
	for _, level := range config.EffortLevels {
		if _, out := runSlash(t, model, "/effort"); !strings.Contains(out, level) {
			t.Errorf("a bare /effort should list %q, got %q", level, out)
		}
	}
}

// TestHeaderNamesTheEffortOnlyWhenItIsSet keeps the header honest: an unset
// level means the vendor decides, and printing that next to every model would
// be noise on a line that has to survive 80 columns.
func TestHeaderNamesTheEffortOnlyWhenItIsSet(t *testing.T) {
	model := newTestModel(t)
	resize(model, 120, 40)
	before := model.viewHeader()
	if strings.Contains(before, "medium") || strings.Contains(before, "  high") {
		t.Errorf("no effort is set, so the header should not name one:\n%s", before)
	}

	if _, out := runSlash(t, model, "/effort max"); !strings.Contains(out, "max") {
		t.Fatalf("/effort max failed: %q", out)
	}
	after := model.viewHeader()
	if !strings.Contains(after, "max") {
		t.Errorf("the header should name the effort once it is set:\n%s", after)
	}
}
