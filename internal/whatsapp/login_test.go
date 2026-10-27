package whatsapp

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
)

func TestRunLoginRendersCodesUntilSuccess(t *testing.T) {
	items := make(chan whatsmeow.QRChannelItem, 4)
	items <- whatsmeow.QRChannelItem{Event: whatsmeow.QRChannelEventCode, Code: "first"}
	items <- whatsmeow.QRChannelItem{Event: whatsmeow.QRChannelEventCode, Code: "second"}
	items <- whatsmeow.QRChannelSuccess
	close(items)

	var rendered []string
	if err := runLogin(context.Background(), items, func(code string) { rendered = append(rendered, code) }); err != nil {
		t.Fatalf("runLogin: %v", err)
	}
	if len(rendered) != 2 || rendered[0] != "first" || rendered[1] != "second" {
		t.Errorf("rendered = %v", rendered)
	}
}

func TestRunLoginReportsExpiryAndRejection(t *testing.T) {
	timedOut := make(chan whatsmeow.QRChannelItem, 1)
	timedOut <- whatsmeow.QRChannelTimeout
	if err := runLogin(context.Background(), timedOut, func(string) {}); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Errorf("a timeout must be named, got %v", err)
	}

	rejected := make(chan whatsmeow.QRChannelItem, 1)
	rejected <- whatsmeow.QRChannelItem{Event: "err-client-outdated"}
	if err := runLogin(context.Background(), rejected, func(string) {}); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Errorf("a rejection must be named, got %v", err)
	}

	closed := make(chan whatsmeow.QRChannelItem)
	close(closed)
	if err := runLogin(context.Background(), closed, func(string) {}); err == nil {
		t.Errorf("a closed channel before pairing must fail")
	}
}

func TestRunLoginStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	items := make(chan whatsmeow.QRChannelItem)
	cancel()
	_ = time.AfterFunc(time.Second, func() { close(items) })
	if err := runLogin(ctx, items, func(string) {}); err == nil {
		t.Errorf("a cancelled login must report the cancellation")
	}
}
