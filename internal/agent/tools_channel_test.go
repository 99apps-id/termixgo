package agent

import (
	"context"
	"strings"
	"testing"
)

func TestSendChannelToolDeliversAttachmentAndRespectsGuestRestriction(t *testing.T) {
	registry := DefaultRegistry()
	for _, name := range []string{"send_channel", "send_channel_file", "send_telegram", "send_whatsapp"} {
		if _, ok := registry.Lookup(name); !ok {
			t.Fatalf("expected %q to be registered in DefaultRegistry", name)
		}
	}

	tool, _ := registry.Lookup("send_channel")

	// 1. Guest session must be refused.
	guestRes, err := tool.Run(context.Background(), &Env{Guest: true}, map[string]any{
		"channel":   "telegram",
		"file_path": ".termixgo/uploads/report.pdf",
	})
	if err != nil {
		t.Fatalf("guest Run error: %v", err)
	}
	if !guestRes.IsError || !strings.Contains(guestRes.Output, "restricted to the operator") {
		t.Fatalf("expected guest refusal, got: %+v", guestRes)
	}

	// 2. Operator session forwards attachment and caption to SendChannel.
	var captured ChannelSendRequest
	env := &Env{
		Workspace: t.TempDir(),
		SendChannel: func(_ context.Context, req ChannelSendRequest) (string, error) {
			captured = req
			return "Sent attachment \"report.pdf\" (128 bytes) to telegram/default (chat 450913223).", nil
		},
	}
	res, err := tool.Run(context.Background(), env, map[string]any{
		"channel":   "telegram",
		"file_path": ".termixgo/uploads/report.pdf",
		"message":   "Laporan bulanan dari WhatsApp",
	})
	if err != nil {
		t.Fatalf("operator Run error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", res.Output)
	}
	if captured.Channel != "telegram" || captured.FilePath != ".termixgo/uploads/report.pdf" || captured.Message != "Laporan bulanan dari WhatsApp" {
		t.Fatalf("unexpected captured request: %+v", captured)
	}
}
