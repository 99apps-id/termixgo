package app

import (
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
)

func TestSetActiveAgentSwitchesWorkspaceAndSession(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg = cfg.WithAgent("sales", config.Agent{})
	if err := config.Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	application, err := newApp(t, t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	defaultSession := application.Session()
	if application.ActiveAgent() != config.FinalAgentName {
		t.Fatalf("ActiveAgent = %q, want %q", application.ActiveAgent(), config.FinalAgentName)
	}
	if err := application.SetActiveAgent("sales"); err != nil {
		t.Fatalf("SetActiveAgent: %v", err)
	}
	if application.ActiveAgent() != "sales" {
		t.Fatalf("ActiveAgent = %q, want sales", application.ActiveAgent())
	}
	salesSession := application.Session()
	if salesSession == defaultSession {
		t.Fatalf("switching agents must switch the live session")
	}
	if salesSession.Agent() != "sales" {
		t.Errorf("the new session belongs to %q, want sales", salesSession.Agent())
	}
	if salesSession.StoreDir() == "" || !strings.HasPrefix(salesSession.StoreDir(), application.AgentHome("sales")) {
		t.Errorf("the sales session must live in the sales home: %q", salesSession.StoreDir())
	}

	salesWorkspace := application.AgentWorkspace("sales")
	if salesWorkspace == application.Workspace() {
		t.Errorf("a named agent must not share the launch workspace")
	}
	if !strings.HasPrefix(salesWorkspace, application.AgentHome("sales")) {
		t.Errorf("the default workspace of a named agent belongs in its home: %q", salesWorkspace)
	}

	reloaded, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if reloaded.ActiveAgent != "sales" {
		t.Errorf("the active agent was not persisted: %q", reloaded.ActiveAgent)
	}

	if err := application.SetActiveAgent(config.FinalAgentName); err != nil {
		t.Fatalf("switch back: %v", err)
	}
	if application.Session() != defaultSession {
		t.Errorf("switching back must resume the session the agent was left on")
	}
}

func TestSetActiveAgentRefusesUnknownNames(t *testing.T) {
	application := newTestApp(t)
	if err := application.SetActiveAgent("ghost"); err == nil {
		t.Fatalf("an unknown agent must be refused")
	}
	if application.ActiveAgent() != config.FinalAgentName {
		t.Errorf("a refused switch must not change the active agent")
	}
}

func TestAgentContextsAreSeparate(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg = cfg.WithAgent("sales", config.Agent{})
	if err := config.Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	application, err := newApp(t, t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	defaultCtx := application.agentContextFor(config.FinalAgentName)
	salesCtx := application.agentContextFor("sales")
	if defaultCtx == salesCtx {
		t.Fatalf("two agents must not share one context")
	}
	if defaultCtx.memory == salesCtx.memory {
		t.Errorf("agents share one memory store")
	}
	if defaultCtx.workspace == salesCtx.workspace {
		t.Errorf("agents share one workspace")
	}
	if defaultCtx.search == salesCtx.search && defaultCtx.search != nil {
		t.Errorf("agents share one search index")
	}
}

func TestNewSessionStaysWithItsAgent(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg = cfg.WithAgent("sales", config.Agent{})
	if err := config.Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	application, err := newApp(t, t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := application.SetActiveAgent("sales"); err != nil {
		t.Fatalf("SetActiveAgent: %v", err)
	}
	application.NewSession()
	session := application.Session()
	if session.Agent() != "sales" {
		t.Errorf("a new session on the sales agent belongs to %q", session.Agent())
	}
	if !strings.HasPrefix(session.StoreDir(), application.AgentHome("sales")) {
		t.Errorf("a new sales session must be stored in the sales home: %q", session.StoreDir())
	}
	if list := application.AgentNames(); len(list) != 2 || list[0] != config.FinalAgentName {
		t.Errorf("AgentNames = %v, want default first", list)
	}
}
