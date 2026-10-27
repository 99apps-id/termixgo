package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSessionCarriesItsAgentAndDirectory(t *testing.T) {
	dir := t.TempDir()
	session := NewSessionFor("sales", t.TempDir(), "test-model")
	if session.Agent() != "sales" {
		t.Fatalf("Agent() = %q, want sales", session.Agent())
	}
	session.SetStoreDir(dir)
	session.AddUser("halo")
	if err := session.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	path := filepath.Join(dir, session.ID()+".json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the session was not written into its own directory: %v", err)
	}

	loaded, err := LoadSessionIn(dir, session.ID())
	if err != nil {
		t.Fatalf("LoadSessionIn: %v", err)
	}
	if loaded.Agent() != "sales" {
		t.Errorf("the agent did not survive a round trip: %q", loaded.Agent())
	}
	if loaded.StoreDir() != dir {
		t.Errorf("StoreDir = %q, want %q", loaded.StoreDir(), dir)
	}
	ledger, err := loaded.LedgerPath()
	if err != nil {
		t.Fatalf("LedgerPath: %v", err)
	}
	if filepath.Dir(ledger) != dir {
		t.Errorf("the ledger must live beside the session: %q", ledger)
	}
}

func TestLatestSessionForFiltersByAgent(t *testing.T) {
	dir := t.TempDir()
	sales := NewSessionFor("sales", t.TempDir(), "")
	sales.SetStoreDir(dir)
	sales.AddUser("satu")
	if err := sales.Save(); err != nil {
		t.Fatalf("save sales: %v", err)
	}

	summary, ok, err := LatestSessionFor("sales", dir)
	if err != nil || !ok {
		t.Fatalf("LatestSessionFor(sales) = ok %v, err %v", ok, err)
	}
	if summary.ID != sales.ID() || summary.Agent != "sales" {
		t.Errorf("wrong summary: %+v", summary)
	}
	if _, ok, _ := LatestSessionFor("support", dir); ok {
		t.Errorf("another agent must not match the session")
	}
	// A session stored before the registry existed has no agent and belongs to
	// the default agent.
	legacy := NewSession(t.TempDir(), "")
	legacy.SetStoreDir(dir)
	legacy.AddUser("lama")
	if err := legacy.Save(); err != nil {
		t.Fatalf("save legacy: %v", err)
	}
	if _, ok, _ := LatestSessionFor("default", dir); !ok {
		t.Errorf("an agentless session must read as the default agent's")
	}
}

func TestSessionsInOtherDirectoryStayInvisible(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	session := NewSessionFor("sales", t.TempDir(), "")
	session.SetStoreDir(first)
	session.AddUser("rahasia")
	if err := session.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	summaries, err := ListSessionsIn(second)
	if err != nil {
		t.Fatalf("ListSessionsIn: %v", err)
	}
	if len(summaries) != 0 {
		t.Errorf("a session leaked into another directory: %+v", summaries)
	}
	if _, err := LoadSessionIn(second, session.ID()); err == nil {
		t.Errorf("loading from the wrong directory must fail")
	}
}
