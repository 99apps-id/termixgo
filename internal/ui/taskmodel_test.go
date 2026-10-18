package ui

import (
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
)

// TestSlashTaskModelShowsAndSets covers the command loop: a bare call names
// both jobs, a set updates one, and a typo is refused rather than stored.
func TestSlashTaskModelShowsAndSets(t *testing.T) {
	model := newTestModel(t)

	if _, out := runSlash(t, model, "/taskmodel"); !strings.Contains(out, "title") || !strings.Contains(out, "compaction") {
		t.Errorf("a bare /taskmodel should name both jobs, got %q", out)
	}

	if _, out := runSlash(t, model, "/taskmodel title deepseek:deepseek-v4.1-flash"); !strings.Contains(out, "deepseek:deepseek-v4.1-flash") {
		t.Errorf("/taskmodel title should confirm the model, got %q", out)
	}
	if got := model.app.TaskModel(agent.TaskTitle); got != "deepseek:deepseek-v4.1-flash" {
		t.Errorf("title model = %q", got)
	}

	if _, out := runSlash(t, model, "/taskmodel title not-a-provider:bogus"); !strings.Contains(out, "unknown subagent model") {
		t.Errorf("an unresolvable model should be refused, got %q", out)
	}
	if got := model.app.TaskModel(agent.TaskTitle); got != "deepseek:deepseek-v4.1-flash" {
		t.Errorf("a refused model must leave the setting alone, got %q", got)
	}

	if _, out := runSlash(t, model, "/taskmodel title default"); !strings.Contains(out, "active model") {
		t.Errorf("/taskmodel title default should confirm the fallback, got %q", out)
	}
	if got := model.app.TaskModel(agent.TaskTitle); got != "" {
		t.Errorf("title model = %q, want empty after default", got)
	}
}

// TestSlashTaskModelRejectsAnUnknownJob keeps a mistyped job name from
// silently editing the wrong setting.
func TestSlashTaskModelRejectsAnUnknownJob(t *testing.T) {
	model := newTestModel(t)
	if _, out := runSlash(t, model, "/taskmodel summary foo"); !strings.Contains(out, "usage:") {
		t.Errorf("an unknown job should print the usage, got %q", out)
	}
	if got := model.app.TaskModel(agent.TaskCompaction); got != "" {
		t.Errorf("compaction model = %q, want it untouched", got)
	}
}
