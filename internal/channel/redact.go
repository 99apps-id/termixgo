package channel

import (
	"encoding/base64"
	"regexp"
	"strings"
	"unicode/utf8"
)

// RedactedPlaceholder replaces anything that looks like a secret before a
// message leaves for a chat.
const RedactedPlaceholder = "[rahasia disunting]"

// BlockedBase64Placeholder replaces a Base64-encoded script or prompt-injection
// payload inside an untrusted guest message before the model sees it.
const BlockedBase64Placeholder = "[payload base64 mencurigakan diblokir]"

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

var base64CandidatePattern = regexp.MustCompile(`[A-Za-z0-9+/=_-]{16,}`)

var suspiciousDecodedMarkers = []string{
	"#!/", "/bin/sh", "/bin/bash", "bash -c", "sh -c", "curl ", "wget ",
	"nc -e", "netcat", "python -c", "python3 -c", "perl -e", "ruby -e",
	"php -r", "node -e", "eval(", "exec(", "os.system", "subprocess",
	"child_process", "powershell", "chmod +x", "rm -rf", "base64 -d",
	"base64 --decode", "/etc/passwd", "/etc/shadow", ".ssh/", "authorized_keys",
	"ignore previous", "ignore all previous", "disregard previous",
	"abaikan semua", "abaikan instruksi", "abaikan aturan", "system prompt",
	"you are now", "developer mode", "jailbreak", "run_command", "write_file",
	"apply_patch", "send_channel", "secrets.json", ".termixgo", "api_key",
	"apikey", "pesan-dari-pengguna",
}

// Redact removes token-shaped text and any stored secret value (including its
// Base64-encoded form) from an answer. known is the list of live secret values;
// a short value is skipped, because a common word would be edited out of every
// sentence.
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
		b64Std := base64.StdEncoding.EncodeToString([]byte(value))
		if len(b64Std) >= 12 {
			out = strings.ReplaceAll(out, b64Std, RedactedPlaceholder)
		}
		b64Raw := base64.RawStdEncoding.EncodeToString([]byte(value))
		if len(b64Raw) >= 12 {
			out = strings.ReplaceAll(out, b64Raw, RedactedPlaceholder)
		}
	}
	for _, pattern := range secretPatterns {
		out = pattern.ReplaceAllString(out, RedactedPlaceholder)
	}
	return out
}

// WrapUntrusted marks chat text as data, neutralizes XML boundary breakouts,
// and strips Base64-disguised scripts or prompt-injection payloads before the
// model sees the message.
func WrapUntrusted(text string) string {
	sanitized := SanitizeUntrusted(strings.TrimSpace(text))
	return "<pesan-dari-pengguna>\n" + sanitized + "\n</pesan-dari-pengguna>"
}

// SanitizeUntrusted neutralizes <pesan-dari-pengguna> tag breakouts and replaces
// Base64-encoded script or prompt-injection payloads with a safe placeholder.
func SanitizeUntrusted(text string) string {
	if text == "" {
		return ""
	}
	replacer := strings.NewReplacer(
		"<pesan-dari-pengguna>", "&lt;pesan-dari-pengguna&gt;",
		"</pesan-dari-pengguna>", "&lt;/pesan-dari-pengguna&gt;",
		"<PESAN-DARI-PENGGUNA>", "&lt;pesan-dari-pengguna&gt;",
		"</PESAN-DARI-PENGGUNA>", "&lt;/pesan-dari-pengguna&gt;",
	)
	cleaned := replacer.Replace(text)
	return base64CandidatePattern.ReplaceAllStringFunc(cleaned, func(token string) string {
		if isSuspiciousBase64Token(token) {
			return BlockedBase64Placeholder
		}
		return token
	})
}

func isSuspiciousBase64Token(token string) bool {
	current := strings.TrimSpace(token)
	for depth := 0; depth < 2; depth++ {
		decoded, ok := tryDecodeBase64(current)
		if !ok {
			return false
		}
		lowered := strings.ToLower(decoded)
		for _, marker := range suspiciousDecodedMarkers {
			if strings.Contains(lowered, marker) {
				return true
			}
		}
		current = strings.TrimSpace(decoded)
	}
	return false
}

func tryDecodeBase64(raw string) (string, bool) {
	if len(raw) < 16 {
		return "", false
	}
	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	for _, enc := range encodings {
		data, err := enc.DecodeString(raw)
		if err != nil || len(data) < 6 || !utf8.Valid(data) {
			continue
		}
		printable := 0
		for _, r := range string(data) {
			if r == '\n' || r == '\r' || r == '\t' || (r >= 0x20 && r <= 0x7e) || r >= 0xa0 {
				printable++
			}
		}
		if printable*10 >= utf8.RuneCount(data)*9 {
			return string(data), true
		}
	}
	return "", false
}
