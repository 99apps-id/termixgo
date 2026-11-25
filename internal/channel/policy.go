package channel

import (
	"strings"

	"github.com/99apps-id/termixgo/internal/config"
)

// Decision is the gate's answer for one message.
//
// Reason is for logs and status lines only, never for the sender: a stranger
// learns nothing from being ignored.
type Decision struct {
	Allow  bool
	Admin  bool
	Reason string
}

// Decide applies one account's policy to one message.
//
// The order is deliberate: an admin may always speak, a group needs its policy
// and a mention, and a direct message needs the DM policy. A pairing-policy DM
// reaches the gate only after the pairing store had its chance, so the answer
// here means "nobody approved this sender yet".
func Decide(policy config.ChannelAccount, in Request) Decision {
	if !policy.Enabled {
		return Decision{Reason: "account disabled"}
	}
	matchesSender := func(list []string) bool {
		primary, alternate := SenderKey(in.Channel, in.SenderID), SenderKey(in.Channel, in.SenderAlt)
		for _, id := range list {
			key := SenderKey(in.Channel, id)
			if key != "" && (key == primary || key == alternate) {
				return true
			}
		}
		return false
	}
	isAdmin := matchesSender(policy.Admins)
	if in.IsGroup {
		switch config.NormalizeGroupPolicy(policy.GroupPolicy) {
		case config.GroupPolicyDisabled:
			return Decision{Admin: isAdmin, Reason: "group replies are disabled"}
		case config.GroupPolicyAll:
			// Every group message is answered; the allowlist below still applies.
		default:
			if !in.MentionsBot {
				return Decision{Admin: isAdmin, Reason: "group message without a mention"}
			}
		}
		if len(policy.GroupAllowFrom) > 0 && !matchesAny(policy.GroupAllowFrom, in.ChatID) {
			return Decision{Admin: isAdmin, Reason: "group is not on the allowlist"}
		}
		if isAdmin {
			return Decision{Allow: true, Admin: true, Reason: "admin"}
		}
		return Decision{Allow: true, Reason: "group"}
	}
	if isAdmin {
		return Decision{Allow: true, Admin: true, Reason: "admin"}
	}
	switch config.NormalizeDMPolicy(policy.DMPolicy) {
	case config.DMPolicyOpen:
		return Decision{Allow: true, Reason: "open DMs"}
	case config.DMPolicyDeny:
		return Decision{Reason: "DMs are denied"}
	case config.DMPolicyPairing:
		return Decision{Reason: "waiting for pairing approval"}
	default:
		if matchesSender(policy.AllowFrom) {
			return Decision{Allow: true, Reason: "allowlisted"}
		}
		return Decision{Reason: "sender is not on the allowlist"}
	}
}

var phoneSeparators = strings.NewReplacer(" ", "", "-", "", "(", "", ")", "")

// SenderKey keeps a WhatsApp private ID distinct from a phone number with
// the same digits. Bare numbers in a WhatsApp account refer to phones.
func SenderKey(kind, raw string) string {
	if kind != config.ChannelWhatsApp {
		return CanonicalID(raw)
	}
	user, domain, _ := strings.Cut(strings.ToLower(strings.TrimSpace(raw)), "@")
	if n := strings.IndexByte(user, ':'); n >= 0 {
		user, _, _ = strings.Cut(user[:n], ".")
	}
	user = phoneSeparators.Replace(user)
	user = strings.TrimPrefix(user, "+")
	if user == "" || strings.Trim(user, "0123456789") != "" {
		return ""
	}
	switch domain {
	case "lid", "hosted.lid":
		return user + "@lid"
	case "", "s.whatsapp.net", "c.us", "hosted":
		if strings.HasPrefix(user, "08") {
			user = "62" + user[1:]
		}
		return user + "@s.whatsapp.net"
	default:
		return ""
	}
}

// CanonicalID folds the shapes one identity is written in onto one comparison
// form: spaces, dashes and a leading plus are dropped, case is folded, and
// domain suffixes (@s.whatsapp.net, @g.us, @lid, @c.us) or device IDs (:device)
// are trimmed. A WhatsApp JID and the bare number, or a Telegram id with and
// without its group minus sign, then compare equal.
func CanonicalID(raw string) string {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	trimmed = strings.ReplaceAll(trimmed, " ", "")
	trimmed = strings.ReplaceAll(trimmed, "-", "")
	trimmed = strings.TrimPrefix(trimmed, "+")
	if at := strings.Index(trimmed, "@"); at > 0 {
		trimmed = trimmed[:at]
	}
	if colon := strings.Index(trimmed, ":"); colon > 0 {
		trimmed = trimmed[:colon]
	}
	return trimmed
}

// MatchesAny reports whether id matches any entry in list using CanonicalID.
func MatchesAny(list []string, id string) bool {
	return matchesAny(list, id)
}

func matchesAny(list []string, id string) bool {
	target := CanonicalID(id)
	if target == "" {
		return false
	}
	for _, candidate := range list {
		if CanonicalID(candidate) == target {
			return true
		}
	}
	return false
}

// AddID returns the list with an identity appended when it is not already
// there, comparing canonical forms.
func AddID(list []string, id string) []string {
	return AddSenderID("", list, id)
}

func AddSenderID(kind string, list []string, id string) []string {
	out := append([]string{}, list...)
	target := SenderKey(kind, id)
	if target == "" {
		return out
	}
	for _, existing := range list {
		if SenderKey(kind, existing) == target {
			return out
		}
	}
	return append(out, strings.TrimSpace(id))
}

// RemoveID returns the list without an identity, comparing canonical forms.
func RemoveID(list []string, id string) []string {
	return RemoveSenderID("", list, id)
}

func RemoveSenderID(kind string, list []string, id string) []string {
	target := SenderKey(kind, id)
	out := make([]string, 0, len(list))
	for _, candidate := range list {
		if target != "" && SenderKey(kind, candidate) == target {
			continue
		}
		out = append(out, candidate)
	}
	return out
}
