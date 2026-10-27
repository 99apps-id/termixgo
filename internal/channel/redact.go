package channel

import (
	"regexp"
	"strings"
)

// RedactedPlaceholder replaces anything that looks like a secret before a
// message leaves for a chat.
const RedactedPlaceholder = "[rahasia disunting]"

// secretPatterns are the token shapes a model might echo from a file or a tool
// result. They are deliberately narrow: a false positive costs one edited
// sentence, while a missed key costs the operator a credential.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`sk-[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`),
	regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`),
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	regexp.MustCompile(`AIza[0-9A-Za-z_-]{30,}`),
	regexp.MustCompile(`\b[0-9]{8,10}:[A-Za-z0-9_-]{30,}\b`),
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]{16,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`),
}

// Redact removes token-shaped text and any stored secret value from an answer.
// known is the list of live secret values; a short value is skipped, because a
// common word would be edited out of every sentence.
func Redact(text string, known []string) string {
	if strings.TrimSpace(text) == "" {
		return text
	}
	out := text
	for _, value := range known {
		if len(value) < 8 {
			continue
		}
		out = strings.ReplaceAll(out, value, RedactedPlaceholder)
	}
	for _, pattern := range secretPatterns {
		out = pattern.ReplaceAllString(out, RedactedPlaceholder)
	}
	return out
}

// WrapUntrusted marks chat text as data. Anyone on the other end of an account
// writes it, so it must not read as an instruction that changes policy; the
// block in the system prompt says as much, and the wrapper makes the boundary
// visible to the model.
func WrapUntrusted(text string) string {
	return "<pesan-dari-pengguna>\n" + strings.TrimSpace(text) + "\n</pesan-dari-pengguna>"
}
