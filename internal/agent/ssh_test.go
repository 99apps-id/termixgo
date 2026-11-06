package agent

import "testing"

func TestSshConnectionCRUDAndRedaction(t *testing.T) {
	conn := SshConnection{ID: "test-ssh-1", Name: "prod", Host: "example.com", Port: 22, User: "deploy", Password: "topsecret123"}
	AddSshConnection(conn)
	defer RemoveSshConnection("test-ssh-1")

	got := GetSshConnection("test-ssh-1")
	if got == nil {
		t.Fatal("GetSshConnection nil after Add")
	}
	if got.Host != "example.com" || got.User != "deploy" || got.Port != 22 {
		t.Fatalf("unexpected connection: %+v", got)
	}

	found := false
	for _, c := range ListSshConnections() {
		if c.ID == "test-ssh-1" {
			found = true
		}
	}
	if !found {
		t.Fatal("ListSshConnections missing added connection")
	}

	summary := safeSummary(conn)
	if _, ok := summary["password"]; ok {
		t.Fatal("safeSummary must not include a password key")
	}
	for k, v := range summary {
		if s, ok := v.(string); ok && s == "topsecret123" {
			t.Fatalf("safeSummary leaked password under %q", k)
		}
	}

	RemoveSshConnection("test-ssh-1")
	if GetSshConnection("test-ssh-1") != nil {
		t.Fatal("connection still present after Remove")
	}
}
