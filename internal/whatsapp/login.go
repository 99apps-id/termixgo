package whatsapp

import (
	"context"
	"fmt"
	"time"

	"go.mau.fi/whatsmeow"
)

// Login runs the QR flow for one device. render is called with every code, so
// the caller decides how the terminal shows it.
//
// GetQRChannel must be read before connecting, and the codes rotate until the
// phone scans one; the loop ends on success, timeout, rejection or cancellation.
func Login(ctx context.Context, client *whatsmeow.Client, render func(code string)) error {
	if client == nil {
		return fmt.Errorf("no WhatsApp client")
	}
	items, err := client.GetQRChannel(ctx)
	if err != nil {
		return fmt.Errorf("start the WhatsApp QR channel: %w", err)
	}
	if err := client.Connect(); err != nil {
		return fmt.Errorf("connect to WhatsApp: %w", err)
	}
	return runLogin(ctx, items, render)
}

// runLogin is the channel loop, split out so a test can feed items.
func runLogin(ctx context.Context, items <-chan whatsmeow.QRChannelItem, render func(string)) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case item, ok := <-items:
			if !ok {
				return fmt.Errorf("the WhatsApp login channel closed before the device paired")
			}
			switch item.Event {
			case whatsmeow.QRChannelEventCode:
				render(item.Code)
			case whatsmeow.QRChannelSuccess.Event:
				return nil
			case whatsmeow.QRChannelTimeout.Event:
				return fmt.Errorf("the QR code expired; run the login again")
			default:
				return fmt.Errorf("WhatsApp rejected the login (%s)", item.Event)
			}
		}
	}
}

// PairWithPhone requests an eight-digit pairing code instead of a QR code. The
// phone number is the one that will link this device.
//
// The code is generated right after the connection opens, which is the window
// WhatsApp allows.
func PairWithPhone(ctx context.Context, client *whatsmeow.Client, phone, displayName string) (string, error) {
	if client == nil {
		return "", fmt.Errorf("no WhatsApp client")
	}
	if displayName == "" {
		displayName = "Chrome (Linux)"
	}
	if err := client.Connect(); err != nil {
		return "", fmt.Errorf("connect to WhatsApp: %w", err)
	}
	code, err := client.PairPhone(ctx, phone, true, whatsmeow.PairClientChrome, displayName)
	if err != nil {
		return "", fmt.Errorf("request a WhatsApp pairing code: %w", err)
	}
	return code, nil
}

// WaitForLogin blocks until the device is logged in, the context ends, or the
// window closes. It is the pairing-code path's counterpart to the QR loop.
func WaitForLogin(ctx context.Context, client *whatsmeow.Client, window time.Duration) error {
	if client == nil {
		return fmt.Errorf("no WhatsApp client")
	}
	deadline := time.Now().Add(window)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if client.IsLoggedIn() {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the pairing code expired before the phone confirmed")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
