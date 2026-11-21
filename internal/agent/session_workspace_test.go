package agent

import (
	"path/filepath"
	"testing"
)

// LatestSessionIn only returns a conversation from the requested workspace,
// so switching agents never carries one project's history into another.
func TestLatestSessionInFiltersByWorkspace(t *testing.T) {
	dir := t.TempDir()
	projectA := filepath.Join(t.TempDir(), "a")
	projectB := filepath.Join(t.TempDir(), "b")

	inA := NewSessionFor("default", projectA, "m")
	inA.SetStoreDir(dir)
	inA.AddUser("work in A")
	if err := inA.Save(); err != nil {
		t.Fatal(err)
	}

	if _, ok, err := LatestSessionIn("default", dir, projectB); err != nil || ok {
		t.Fatalf("project B has no session, got ok=%v err=%v", ok, err)
	}
	summary, ok, err := LatestSessionIn("default", dir, projectA)
	if err != nil || !ok || summary.ID != inA.ID() {
		t.Fatalf("project A should resume its own session, got %+v ok=%v err=%v", summary, ok, err)
	}
	if _, ok, _ := LatestSessionFor("default", dir); !ok {
		t.Fatalf("the unfiltered lookup should still find it")
	}
}
