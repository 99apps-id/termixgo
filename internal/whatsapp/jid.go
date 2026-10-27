package whatsapp

import "strings"

// PhoneFromJID is the user part of a JID, which is what a human reads.
func PhoneFromJID(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if index := strings.Index(trimmed, "@"); index > 0 {
		trimmed = trimmed[:index]
	}
	if colon := strings.Index(trimmed, ":"); colon > 0 {
		trimmed = trimmed[:colon]
	}
	return trimmed
}
