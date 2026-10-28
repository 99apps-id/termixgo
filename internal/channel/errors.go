package channel

import (
	"context"
	"errors"
	"strings"
)

// ErrBusy reports that another turn is already running. A transport turns it
// into one friendly line instead of showing the caller a raw error.
var ErrBusy = errors.New("a turn is already running")

// IsAuthError reports whether err indicates an upstream authentication or OAuth token failure.
func IsAuthError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "401") ||
		strings.Contains(msg, "unauthorized") ||
		strings.Contains(msg, "invalid authentication") ||
		strings.Contains(msg, "oauth 2 access token") ||
		strings.Contains(msg, "invalid api key") ||
		strings.Contains(msg, "authentication failed")
}

// NaturalError turns a runtime failure into a friendly, localized sentence.
// Internal tool names, stack traces, and filesystem paths never leak to a chat.
func NaturalError(err error, lang ...string) string {
	l := "id"
	if len(lang) > 0 && strings.TrimSpace(lang[0]) != "" {
		l = strings.ToLower(strings.TrimSpace(lang[0]))
	}
	if l == "id" {
		switch {
		case errors.Is(err, ErrBusy):
			return "Sebentar ya, aku masih menyelesaikan permintaan sebelumnya. Coba lagi sesaat lagi."
		case errors.Is(err, context.Canceled):
			return "Permintaan dibatalkan."
		case IsAuthError(err):
			return "Model mengalami kendala autentikasi (kredensial atau token perlu diperbarui). Silakan ganti model dengan /model stepfun atau hubungi operator."
		default:
			return "Maaf, ada gangguan sebentar. Coba lagi ya."
		}
	}
	switch {
	case errors.Is(err, ErrBusy):
		return "Please wait a moment, I am still finishing the previous request. Please try again shortly."
	case errors.Is(err, context.Canceled):
		return "Request was canceled."
	case IsAuthError(err):
		return "The model encountered an authentication issue (credentials or token need renewal). Please switch model using /model stepfun or contact the operator."
	default:
		return "Sorry, an error occurred. Please try again."
	}
}
