package channel

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/config"
)

func TestInvokeInboundHookWithNativeInterceptor(t *testing.T) {
	called := false
	interceptor := func(ctx context.Context, payload HookPayload) (*HookResponse, error) {
		called = true
		if payload.Text != "halo" {
			t.Errorf("payload.Text = %q, want halo", payload.Text)
		}
		if payload.Channel != "whatsapp" {
			t.Errorf("payload.Channel = %q, want whatsapp", payload.Channel)
		}
		return &HookResponse{Action: ActionReply, Reply: "Halo dari REST hook!"}, nil
	}

	req := Request{
		Channel:  "whatsapp",
		Account:  "default",
		ChatID:   "628111@s.whatsapp.net",
		SenderID: "628111@s.whatsapp.net",
		Text:     "halo",
		Admin:    true,
	}

	resp, err := InvokeInboundHook(context.Background(), interceptor, "", "", req, "halo", "")
	if err != nil {
		t.Fatalf("InvokeInboundHook: %v", err)
	}
	if !called {
		t.Fatal("interceptor was not called")
	}
	if resp.Action != ActionReply || resp.Reply != "Halo dari REST hook!" {
		t.Fatalf("unexpected resp: %#v", resp)
	}
}

func TestInvokeInboundHookGlobalInterceptor(t *testing.T) {
	orig := GlobalInterceptor()
	defer SetGlobalInterceptor(orig)

	SetGlobalInterceptor(func(ctx context.Context, payload HookPayload) (*HookResponse, error) {
		return &HookResponse{Action: ActionModify, Prompt: "Modified: " + payload.Prompt}, nil
	})

	req := Request{
		Channel:  "telegram",
		Account:  "default",
		ChatID:   "123456",
		SenderID: "123456",
		Text:     "tanya fitur",
		Admin:    true,
	}

	resp, err := InvokeInboundHook(context.Background(), nil, "", "", req, "tanya fitur", "")
	if err != nil {
		t.Fatalf("InvokeInboundHook: %v", err)
	}
	if resp.Action != ActionModify || resp.Prompt != "Modified: tanya fitur" {
		t.Fatalf("unexpected resp: %#v", resp)
	}
}

func TestPostHTTPHookSuccessAndHMAC(t *testing.T) {
	secret := "secret-token-123"
	var receivedHeaderAuth string
	var receivedSignature string
	var receivedBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaderAuth = r.Header.Get("Authorization")
		receivedSignature = r.Header.Get("X-Termixgo-Signature")
		receivedBody, _ = io.ReadAll(r.Body)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(HookResponse{
			Action: ActionReply,
			Reply:  "Jawaban dari server REST",
		})
	}))
	defer srv.Close()

	payload := HookPayload{
		Event:     EventInboundMessage,
		Channel:   "whatsapp",
		Account:   "default",
		ChatID:    "628999@s.whatsapp.net",
		Text:      "cek status",
		Prompt:    "cek status",
		Timestamp: time.Now().Unix(),
	}

	resp, err := PostHTTPHook(context.Background(), srv.URL, secret, payload)
	if err != nil {
		t.Fatalf("PostHTTPHook: %v", err)
	}
	if resp.Action != ActionReply || resp.Reply != "Jawaban dari server REST" {
		t.Errorf("resp = %#v", resp)
	}
	if receivedHeaderAuth != "Bearer "+secret {
		t.Errorf("auth header = %q, want Bearer %s", receivedHeaderAuth, secret)
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(receivedBody)
	expectedSig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if receivedSignature != expectedSig {
		t.Errorf("signature = %q, want %q", receivedSignature, expectedSig)
	}
}

func TestPostHTTPHookNoContentAndEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	resp, err := PostHTTPHook(context.Background(), srv.URL, "", HookPayload{Text: "hi"})
	if err != nil {
		t.Fatalf("PostHTTPHook: %v", err)
	}
	if resp.Action != ActionContinue {
		t.Errorf("action = %q, want continue", resp.Action)
	}
}

func TestPostHTTPHookHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := PostHTTPHook(context.Background(), srv.URL, "", HookPayload{Text: "hi"})
	if err == nil {
		t.Fatal("expected error on HTTP 500, got nil")
	}
}

func TestPingHTTPHook(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Termixgo-Event") != EventPing {
			t.Errorf("X-Termixgo-Event = %q, want %q", r.Header.Get("X-Termixgo-Event"), EventPing)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	}))
	defer srv.Close()

	latency, err := PingHTTPHook(context.Background(), srv.URL, "")
	if err != nil {
		t.Fatalf("PingHTTPHook failed: %v", err)
	}
	if latency <= 0 {
		t.Errorf("latency = %v, want > 0", latency)
	}
}

func TestResolveHookTarget(t *testing.T) {
	acct := config.ChannelAccount{
		HookURL:    "http://account.local/hook",
		HookSecret: "acct-secret",
	}
	url, secret := ResolveHookTarget(acct, "http://fallback.local/hook", "fallback-secret")
	if url != "http://account.local/hook" || secret != "acct-secret" {
		t.Errorf("got %q, %q; want account values", url, secret)
	}

	emptyAcct := config.ChannelAccount{}
	url, secret = ResolveHookTarget(emptyAcct, "http://fallback.local/hook", "fallback-secret")
	if url != "http://fallback.local/hook" || secret != "fallback-secret" {
		t.Errorf("got %q, %q; want fallback values", url, secret)
	}

	_ = os.Setenv("TERMIXGO_CHANNEL_HOOK_URL", "http://env.local/hook")
	defer os.Unsetenv("TERMIXGO_CHANNEL_HOOK_URL")
	url, _ = ResolveHookTarget(emptyAcct, "", "")
	if url != "http://env.local/hook" {
		t.Errorf("got %q, want env URL", url)
	}
}
