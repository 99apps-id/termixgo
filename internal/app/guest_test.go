package app

import (
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/config"
)

func TestGuestToolRegistryDropsTheAdminClass(t *testing.T) {
	application := newTestApp(t)

	guest := application.toolsFor(config.FinalAgentName, true)
	for _, name := range []string{
		"run_command", "run_checks", "run_background", "kill", "subagent",
		"parallel_batch", "git_push", "checkpoint", "rewind", "env_get",
		"http_request", "browser_open", "github_create_pr", "ask_user", "remember",
	} {
		if _, ok := guest.Lookup(name); ok {
			t.Errorf("%s must not be offered to a guest", name)
		}
	}
	for _, name := range []string{"read_file", "grep", "web_search", "web_fetch", "write_file", "edit"} {
		if _, ok := guest.Lookup(name); !ok {
			t.Errorf("%s must stay available to a guest", name)
		}
	}

	admin := application.toolsFor(config.FinalAgentName, false)
	if _, ok := admin.Lookup("run_command"); !ok {
		t.Errorf("the operator must keep the execution tools")
	}
}

func TestAgentToolDenyAppliesToEveryone(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	cfg := config.Default()
	cfg = cfg.WithAgent("sales", config.Agent{Tools: config.AgentTools{Deny: []string{"web_search"}}})
	if err := config.Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	application, err := newApp(t, t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	registry := application.toolsFor("sales", false)
	if _, ok := registry.Lookup("web_search"); ok {
		t.Errorf("the agent deny-list must remove the tool for its own runs")
	}
	if _, ok := application.toolsFor(config.FinalAgentName, false).Lookup("web_search"); !ok {
		t.Errorf("another agent must keep the tool")
	}
}

func TestGuestPolicyAlwaysAsksAndForgetsGrants(t *testing.T) {
	application := newTestApp(t)
	application.AllowTool("write_file")

	guest := application.policyFor(config.FinalAgentName, true)
	if guest.Mode != agent.ApprovalAsk {
		t.Errorf("a guest run must ask for every mutating tool, got %v", guest.Mode)
	}
	if len(guest.AlwaysAllowed) != 0 || len(guest.SessionAllowed) != 0 {
		t.Errorf("a guest must not inherit the operator's grants: %+v", guest)
	}
	if _, ok := guest.AlwaysAllowed["write_file"]; ok {
		t.Errorf("the operator's always-allow leaked into a guest run")
	}

	admin := application.policyFor(config.FinalAgentName, false)
	if admin.AlwaysAllowed["write_file"] != true {
		t.Errorf("the operator keeps their own grants")
	}
}

func TestAgentApprovalModeOverride(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	cfg := config.Default()
	cfg.ApprovalMode = config.ApprovalEdits
	cfg = cfg.WithAgent("sales", config.Agent{ApprovalMode: "plan"})
	if err := config.Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	application, err := newApp(t, t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if mode := application.policyFor("sales", false).Mode; mode != agent.ApprovalPlan {
		t.Errorf("sales mode = %v, want plan", mode)
	}
	if mode := application.policyFor(config.FinalAgentName, false).Mode; mode != agent.ApprovalEdits {
		t.Errorf("the default agent must inherit the global mode, got %v", mode)
	}
}

func TestGuestEnvironmentWithholdsSecrets(t *testing.T) {
	application := newTestApp(t)
	guest := application.envForRun(config.FinalAgentName, true)
	if guest.Secrets != nil {
		t.Errorf("a guest environment must not carry the secret store")
	}
	if !guest.Guest {
		t.Errorf("the environment must be marked as a guest run")
	}
	admin := application.envForRun(config.FinalAgentName, false)
	if admin.Secrets == nil {
		t.Errorf("the operator environment keeps the secret store")
	}
}
