package config

import "strings"

const (
	ThemeDark  = "dark"
	ThemeLight = "light"
)

// NormalizeTheme keeps the accepted palette set deliberately small. Unknown
// values fall back to dark so a hand-edited config cannot make the TUI unusable.
func NormalizeTheme(raw string) string {
	if strings.EqualFold(strings.TrimSpace(raw), ThemeLight) {
		return ThemeLight
	}
	return ThemeDark
}

// StatuslineComponents are the components an operator may order or omit.
var StatuslineComponents = []string{
	"agent", "workspace", "model", "effort", "context", "plan", "task",
	"session", "turns", "approval", "queue", "scroll", "tokens", "cost", "quota",
}

// NormalizeStatusline trims, deduplicates and drops unknown component names.
// Empty means the default statusline, so a config written by an older build
// keeps the exact existing behavior.
func NormalizeStatusline(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	allowed := make(map[string]bool, len(StatuslineComponents))
	for _, name := range StatuslineComponents {
		allowed[name] = true
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, raw := range values {
		name := strings.ToLower(strings.TrimSpace(raw))
		if !allowed[name] || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
