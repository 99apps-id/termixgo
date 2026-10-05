package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/99apps-id/termixgo/internal/provider"
)

// imageCaptureClient records the request it was sent, so a test can inspect the
// message the runner built.
type imageCaptureClient struct {
	mu   sync.Mutex
	req  provider.ChatRequest
	sent bool
}

func (c *imageCaptureClient) ID() string { return "capture" }

func (c *imageCaptureClient) FetchQuota(context.Context) *provider.QuotaSnapshot { return nil }

func (c *imageCaptureClient) Stream(_ context.Context, req provider.ChatRequest, emit func(provider.StreamEvent) error) error {
	c.mu.Lock()
	c.req = req
	c.sent = true
	c.mu.Unlock()
	return emit(provider.StreamEvent{Type: provider.EventTextDelta, Text: "seen"})
}

// TestTurnImagesAttachToTheFirstUserMessage is the upload path: an image handed
// to the runner rides on the turn's user message, which is where providers read
// images from.
func TestTurnImagesAttachToTheFirstUserMessage(t *testing.T) {
	client := &imageCaptureClient{}
	runner, env, _ := newTestRunner(t, client, &ApprovalPolicy{Mode: ApprovalAll}, nil)
	runner.TurnImages = []provider.Image{{MediaType: "image/png", Data: "QUJD"}}
	session := NewSession(env.Workspace, "test-model")

	if err := runner.Run(context.Background(), session, "what is this?"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	client.mu.Lock()
	req := client.req
	client.mu.Unlock()
	var user *provider.Message
	for index := range req.Messages {
		if req.Messages[index].Role == provider.RoleUser {
			user = &req.Messages[index]
			break
		}
	}
	if user == nil {
		t.Fatalf("no user message was sent")
	}
	if len(user.Images) != 1 || user.Images[0].MediaType != "image/png" || user.Images[0].Data != "QUJD" {
		t.Errorf("user images = %+v, want the attached image", user.Images)
	}
}
