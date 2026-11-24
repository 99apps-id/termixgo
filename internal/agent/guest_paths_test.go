package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGuestSensitive(t *testing.T) {
	blocked := []string{".env", ".env.local", "app/.env.production", ".git/config", "sub/.git/HEAD", ".ssh/id_rsa",
		".termixgo/config.json", ".termixgo/sessions/a.json", "certs/server.pem", "deploy.key", "config/secrets.json",
		"aws/credentials", "my_secret_notes.txt", ".npmrc", "keys/id_ed25519"}
	for _, path := range blocked {
		if !guestSensitive(path) {
			t.Errorf("%q must be off limits to a guest", path)
		}
	}
	allowed := []string{"main.go", "docs/readme.md", ".termixgo/uploads/report.pdf", "src/environment.go", "a/b/c.txt", ".gitignore", ""}
	for _, path := range allowed {
		if guestSensitive(path) {
			t.Errorf("%q must stay readable", path)
		}
	}
}

func guestEnv(workspace string) *Env {
	return &Env{Workspace: workspace, Guest: true}
}

func TestGuestCannotReadCredentialFilesInTheWorkspace(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, ".env"), []byte("DB_PASSWORD=hunter2hunter2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	read := &readFileTool{}
	denied, _ := read.Run(context.Background(), guestEnv(workspace), map[string]any{"path": ".env"})
	if !denied.IsError || strings.Contains(denied.Output, "hunter2") {
		t.Fatalf("a guest read .env: %+v", denied)
	}
	fine, _ := read.Run(context.Background(), guestEnv(workspace), map[string]any{"path": "notes.txt"})
	if fine.IsError || !strings.Contains(fine.Output, "hello") {
		t.Fatalf("an ordinary file must still read for a guest: %+v", fine)
	}
	// The operator is not affected.
	own, _ := read.Run(context.Background(), &Env{Workspace: workspace}, map[string]any{"path": ".env"})
	if own.IsError {
		t.Fatalf("the operator must still read .env: %+v", own)
	}
}

func TestGuestSearchSkipsCredentialFiles(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, ".env"), []byte("API_TOKEN=findmeplease"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "a.txt"), []byte("findmeplease"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, _ := (&grepTool{}).Run(context.Background(), guestEnv(workspace), map[string]any{"pattern": "findmeplease"})
	if strings.Contains(result.Output, ".env") || !strings.Contains(result.Output, "a.txt") {
		t.Fatalf("grep leaked or lost a file: %s", result.Output)
	}
	listed, _ := (&globTool{}).Run(context.Background(), guestEnv(workspace), map[string]any{"pattern": "**/*"})
	if strings.Contains(listed.Output, ".env") {
		t.Fatalf("glob listed a credential file: %s", listed.Output)
	}
}

func TestGuestCannotSendAWorkspaceSecret(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, ".env"), []byte("X=1"), 0o600); err != nil {
		t.Fatal(err)
	}
	sent := false
	env := guestEnv(workspace)
	env.CallerChatID = "1"
	env.CallerChannel = "telegram"
	env.SendChannel = func(context.Context, ChannelSendRequest) (string, error) {
		sent = true
		return "ok", nil
	}
	result, _ := (&sendChannelTool{}).Run(context.Background(), env, map[string]any{"file_path": ".env"})
	if !result.IsError || sent {
		t.Fatalf("a guest was sent the workspace .env: %+v", result)
	}
}

// The prompt answers questions about itself, so a guest prompt must not hold
// what belongs to the operator.
func TestGuestPromptHoldsNothingOfTheOperators(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("PROJECT-NOTE-7731"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "USER.md"), []byte("- Name: Operator-Private-Name\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	guest, _ := BuildSystemParts(&Env{Workspace: workspace, AgentHome: home, Guest: true}, "m")
	for _, leaked := range []string{"PROJECT-NOTE-7731", "Operator-Private-Name"} {
		if strings.Contains(guest, leaked) {
			t.Errorf("the guest prompt carries %q", leaked)
		}
	}
	operator, _ := BuildSystemParts(&Env{Workspace: workspace, AgentHome: home}, "m")
	for _, kept := range []string{"PROJECT-NOTE-7731", "Operator-Private-Name"} {
		if !strings.Contains(operator, kept) {
			t.Errorf("the operator prompt lost %q", kept)
		}
	}
}
