package whatsapp

import "strings"

// PhoneFromJID is the user part of a JID, which is what a human reads.
func PhoneFromJID(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if index := strings.Index(trimmed, "@"); index > 0 {
		return trimmed[:index]
	}
	return trimmed
}
