package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSendChannelAsksForOutsideFilesAndStrangers(t *testing.T) {
	workspace := t.TempDir()
	inside := filepath.Join(workspace, "report.txt")
	outside := filepath.Join(t.TempDir(), "id_rsa")
	for _, path := range []string{inside, outside} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	env := &Env{Workspace: workspace, CallerChatID: "123"}
	tool := &sendChannelTool{}

	cases := []struct {
		name string
		args map[string]any
		want bool
	}{
		{"workspace file to the caller", map[string]any{"file_path": "report.txt"}, false},
		{"file outside the workspace", map[string]any{"file_path": outside}, true},
		{"outside file under an alias", map[string]any{"path": outside}, true},
		{"reply to the caller chat", map[string]any{"message": "hi", "recipient": "123"}, false},
		{"message to another recipient", map[string]any{"message": "hi", "recipient": "999"}, true},
		{"default recipient", map[string]any{"message": "hi"}, false},
	}
	for _, tc := range cases {
		if got := tool.NeedsApprovalForCall(env, tc.args); got != tc.want {
			t.Errorf("%s: needs approval = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestSendChannelGateRespectsOperatorAllowances(t *testing.T) {
	env := &Env{Workspace: t.TempDir()}
	args := map[string]any{"message": "hi", "recipient": "999"}
	tool := &sendChannelTool{}

	asking := &Runner{Policy: &ApprovalPolicy{Mode: ApprovalAsk}, Env: env}
	if !asking.callNeedsApproval(tool, args) {
		t.Fatalf("a send to a stranger should ask in ask mode")
	}
	all := &Runner{Policy: &ApprovalPolicy{Mode: ApprovalAll}, Env: env}
	if all.callNeedsApproval(tool, args) {
		t.Fatalf("the all mode should not ask")
	}
	allowed := &Runner{Policy: &ApprovalPolicy{Mode: ApprovalAsk, SessionAllowed: map[string]bool{"send_channel": true}}, Env: env}
	if allowed.callNeedsApproval(tool, args) {
		t.Fatalf("a session allowance should not ask again")
	}
}

func TestSendChannelRefusesGuestFilesOutsideTheWorkspace(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	sent := false
	env := &Env{
		Workspace:     t.TempDir(),
		Guest:         true,
		CallerChatID:  "1",
		CallerChannel: "telegram",
		SendChannel: func(context.Context, ChannelSendRequest) (string, error) {
			sent = true
			return "ok", nil
		},
	}
	result, _ := (&sendChannelTool{}).Run(context.Background(), env, map[string]any{"file_path": outside})
	if !result.IsError || sent {
		t.Fatalf("a guest must not receive a file from outside the workspace: %+v", result)
	}
}
