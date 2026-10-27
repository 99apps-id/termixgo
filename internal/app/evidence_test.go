package app

import (
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/provider"
	"github.com/99apps-id/termixgo/internal/secrets"
)

func TestEvidenceRetryNeeded(t *testing.T) {
	long := strings.Repeat("jawaban panjang tanpa bukti. ", 3)
	cases := []struct {
		name      string
		grounding string
		answer    string
		toolCalls int
		wantRetry bool
	}{
		{"strict without evidence", config.GroundingStrict, long, 0, true},
		{"strict with evidence", config.GroundingStrict, long, 1, false},
		{"normal without evidence", config.GroundingNormal, long, 0, false},
		{"honest not found", config.GroundingStrict, "Maaf, saya tidak menemukannya setelah memeriksa workspace. Coba sebutkan file yang dimaksud.", 0, false},
		{"short greeting", config.GroundingStrict, "Halo!", 0, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := evidenceRetryNeeded(testCase.grounding, testCase.answer, testCase.toolCalls)
			if got != testCase.wantRetry {
				t.Errorf("evidenceRetryNeeded = %v, want %v", got, testCase.wantRetry)
			}
		})
	}
}

func TestGuestRunIsAlwaysStrict(t *testing.T) {
	application := newTestApp(t)
	guest := application.envForRun(config.FinalAgentName, true)
	if guest.Grounding != config.GroundingStrict {
		t.Errorf("a guest run must be strict, got %q", guest.Grounding)
	}
	admin := application.envForRun(config.FinalAgentName, false)
	if admin.Grounding != config.GroundingNormal {
		t.Errorf("an agent without an override is normal, got %q", admin.Grounding)
	}
}

func TestRedactForChannelUsesTheStoredSecrets(t *testing.T) {
	application := newTestApp(t)
	if err := application.store.Set(secrets.ProviderKey("openai"), "sk-live-abcdefghijklmnopqrstuvwxyz"); err != nil {
		t.Fatalf("store secret: %v", err)
	}
	redacted := application.RedactForChannel("token saya sk-live-abcdefghijklmnopqrstuvwxyz ya")
	if strings.Contains(redacted, "sk-live-abcdefghijklmnopqrstuvwxyz") {
		t.Errorf("the stored secret was not redacted: %q", redacted)
	}
	if !strings.Contains(redacted, "token saya") {
		t.Errorf("ordinary words were lost: %q", redacted)
	}
}

func TestToolCallCountSince(t *testing.T) {
	session := agent.NewSession(t.TempDir(), "")
	session.AddUser("pertanyaan")
	mark := session.MessageCount()
	session.AddAssistant("", "", []provider.ToolCall{{ID: "1", Name: "read_file", Arguments: `{"path":"a"}`}})
	session.AddAssistant("jawaban", "", nil)

	if got := session.ToolCallCountSince(mark); got != 1 {
		t.Errorf("ToolCallCountSince = %d, want 1", got)
	}
	if got := session.ToolCallCountSince(session.MessageCount()); got != 0 {
		t.Errorf("a mark at the end counts nothing, got %d", got)
	}
}
