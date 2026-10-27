package whatsapp

import "testing"

func TestPhoneFromJID(t *testing.T) {
	if got := PhoneFromJID("628111@s.whatsapp.net"); got != "628111" {
		t.Errorf("PhoneFromJID = %q", got)
	}
	if got := PhoneFromJID("628111:2@s.whatsapp.net"); got != "628111" {
		t.Errorf("PhoneFromJID with device = %q, want 628111", got)
	}
	if got := PhoneFromJID("628111"); got != "628111" {
		t.Errorf("a bare number must pass through, got %q", got)
	}
}
