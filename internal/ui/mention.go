package ui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// mentionLimits bound the file lookup and the expansion. Completion walks
// the workspace on every keystroke, so the walk stops early; expansion
// inlines file contents, so each file and the total stay capped.
const (
	mentionWalkCap  = 2000
	mentionMatchCap = 10
	mentionFileCap  = 32 * 1024
	mentionTotalCap = 100 * 1024
	mentionFilesCap = 5
)

// mentionDirs are never offered: version control, dependencies and the agent
// state itself are not files the operator means to attach.
var mentionDirs = map[string]bool{
	".git": true, "node_modules": true, ".termixgo": true,
	"dist": true, "bin": true, "__pycache__": true, ".venv": true,
}

// parseMention finds the @token being typed at the end of the composer
// value. It returns the token and the byte index of the @. The @ must start
// the value or follow whitespace, so an email address never opens the menu,
// and a token stops at whitespace, so finished text does not match.
func parseMention(value string) (token string, at int, ok bool) {
	end := len(value)
	start := end
	for start > 0 && isMentionChar(value[start-1]) {
		start--
	}
	if start > 0 && value[start-1] == '@' {
		if start-2 >= 0 && !isMentionBoundary(value[start-2]) {
			return "", 0, false
		}
		return value[start:end], start - 1, true
	}
	// An empty token still matches: "@" alone opens the menu.
	if end > 0 && value[end-1] == '@' && (end-2 < 0 || isMentionBoundary(value[end-2])) {
		return "", end - 1, true
	}
	return "", 0, false
}

func isMentionChar(char byte) bool {
	if char >= 'a' && char <= 'z' {
		return true
	}
	if char >= 'A' && char <= 'Z' {
		return true
	}
	if char >= '0' && char <= '9' {
		return true
	}
	switch char {
	case '-', '_', '.', '/', '+':
		return true
	}
	return false
}

func isMentionBoundary(char byte) bool {
	return char == ' ' || char == '\t' || char == '\n'
}

// mentionCandidates lists workspace files matching the token, prefix matches
// on the base name or the relative path first, then substring matches.
func mentionCandidates(workspace, token string, limit int) []string {
	if strings.TrimSpace(workspace) == "" {
		return nil
	}
	if limit <= 0 || limit > mentionMatchCap {
		limit = mentionMatchCap
	}
	needle := strings.ToLower(token)
	var prefix, anywhere []string
	visited := 0
	_ = filepath.WalkDir(workspace, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if visited >= mentionWalkCap || len(prefix)+len(anywhere) >= mentionWalkCap {
			return filepath.SkipAll
		}
		visited++
		rel, err := filepath.Rel(workspace, path)
		if err != nil || rel == "." {
			return nil
		}
		if entry.IsDir() {
			if mentionDirs[entry.Name()] || strings.HasPrefix(entry.Name(), ".") && entry.Name() != "." {
				return filepath.SkipDir
			}
			return nil
		}
		slash := filepath.ToSlash(rel)
		lower := strings.ToLower(slash)
		base := strings.ToLower(entry.Name())
		switch {
		case needle == "":
			prefix = append(prefix, slash)
		case strings.HasPrefix(base, needle) || strings.HasPrefix(lower, needle):
			prefix = append(prefix, slash)
		case strings.Contains(lower, needle):
			anywhere = append(anywhere, slash)
		}
		return nil
	})
	sort.Strings(prefix)
	sort.Strings(anywhere)
	matches := append(prefix, anywhere...)
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return matches
}

// applyMentionCompletion replaces the typed @token with the chosen path.
func applyMentionCompletion(value, choice string, at int) string {
	token, _, ok := parseMention(value)
	if !ok {
		return value
	}
	end := at + 1 + len(token)
	suffix := ""
	if rest := value[end:]; !strings.HasPrefix(rest, " ") && strings.TrimSpace(rest) != "" {
		suffix = " " + strings.TrimLeft(rest, " ")
	} else if strings.TrimSpace(rest) == "" {
		suffix = " "
	} else {
		suffix = rest
	}
	return value[:at] + "@" + choice + suffix
}

// ExpandMentions inlines the contents of @path tokens so the turn carries
// the file the operator pointed at. Unknown paths and files outside the
// workspace stay as typed text for the model to read with its own tools.
func ExpandMentions(workspace, input string) string {
	if !strings.Contains(input, "@") || strings.TrimSpace(workspace) == "" {
		return input
	}
	base, err := filepath.Abs(workspace)
	if err != nil {
		return input
	}
	var builder strings.Builder
	builder.WriteString(input)
	total := 0
	attached := 0
	for _, token := range mentionTokens(input) {
		if attached >= mentionFilesCap || total >= mentionTotalCap {
			break
		}
		target := filepath.Join(base, filepath.FromSlash(token))
		relative, err := filepath.Rel(base, target)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		info, err := os.Stat(target)
		if err != nil || info.IsDir() {
			continue
		}
		if info.Size() > mentionFileCap {
			continue
		}
		data, err := os.ReadFile(target)
		if err != nil {
			continue
		}
		content := string(data)
		if len(content) > mentionFileCap {
			content = content[:mentionFileCap] + "\n... [truncated]"
		}
		builder.WriteString("\n\n--- @")
		builder.WriteString(token)
		builder.WriteString(" ---\n")
		builder.WriteString(strings.TrimRight(content, "\n"))
		total += len(content)
		attached++
	}
	return builder.String()
}

// mentionTokens lists every @path token in the input, in order.
func mentionTokens(input string) []string {
	var tokens []string
	for index := 0; index < len(input); {
		at := strings.IndexByte(input[index:], '@')
		if at < 0 {
			break
		}
		at += index
		if at > 0 && !isMentionBoundary(input[at-1]) {
			index = at + 1
			continue
		}
		end := at + 1
		for end < len(input) && isMentionChar(input[end]) {
			end++
		}
		if token := input[at+1 : end]; token != "" {
			tokens = append(tokens, token)
		}
		index = end
	}
	return tokens
}
