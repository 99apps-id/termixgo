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
	if matchesAny(policy.Admins, in.SenderID) {
		return Decision{Allow: true, Admin: true, Reason: "admin"}
	}
	if in.IsGroup {
		switch config.NormalizeGroupPolicy(policy.GroupPolicy) {
		case config.GroupPolicyDisabled:
			return Decision{Reason: "group replies are disabled"}
		case config.GroupPolicyAll:
			// Every group message is answered; the allowlist below still applies.
		default:
			if !in.MentionsBot {
				return Decision{Reason: "group message without a mention"}
			}
		}
		if len(policy.GroupAllowFrom) > 0 && !matchesAny(policy.GroupAllowFrom, in.ChatID) {
			return Decision{Reason: "group is not on the allowlist"}
		}
		return Decision{Allow: true, Reason: "group"}
	}
	switch config.NormalizeDMPolicy(policy.DMPolicy) {
	case config.DMPolicyOpen:
		return Decision{Allow: true, Reason: "open DMs"}
	case config.DMPolicyDeny:
		return Decision{Reason: "DMs are denied"}
	case config.DMPolicyPairing:
		return Decision{Reason: "waiting for pairing approval"}
	default:
		if matchesAny(policy.AllowFrom, in.SenderID) {
			return Decision{Allow: true, Reason: "allowlisted"}
		}
		return Decision{Reason: "sender is not on the allowlist"}
	}
}

// CanonicalID folds the shapes one identity is written in onto one comparison
// form: spaces, dashes and a leading plus are dropped and case is folded. A
// WhatsApp JID and the bare number, or a Telegram id with and without its
// group minus sign, then compare equal.
func CanonicalID(raw string) string {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	trimmed = strings.ReplaceAll(trimmed, " ", "")
	trimmed = strings.ReplaceAll(trimmed, "-", "")
	trimmed = strings.TrimPrefix(trimmed, "+")
	return trimmed
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
	trimmed := strings.TrimSpace(id)
	if trimmed == "" || matchesAny(list, trimmed) {
		return append([]string{}, list...)
	}
	return append(append([]string{}, list...), trimmed)
}

// RemoveID returns the list without an identity, comparing canonical forms.
func RemoveID(list []string, id string) []string {
	target := CanonicalID(id)
	out := make([]string, 0, len(list))
	for _, candidate := range list {
		if target != "" && CanonicalID(candidate) == target {
			continue
		}
		out = append(out, candidate)
	}
	return out
}
