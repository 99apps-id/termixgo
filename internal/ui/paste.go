package ui

import (
	"fmt"
	"regexp"
	"strings"
)

// A paste at or above these sizes is collapsed to a placeholder in the
// composer, the way Claude Code shows "[Pasted text #1 +42 lines]". The model
// still receives the whole text; the composer and the transcript stay short.
const (
	pasteCollapseLines = 6
	pasteCollapseChars = 1000
)

var pastePlaceholderPattern = regexp.MustCompile(`\[Pasted text #\d+(?: \+\d+ lines)?\]`)

// shouldCollapsePaste reports whether a paste is long enough to collapse.
func shouldCollapsePaste(text string) bool {
	return strings.Count(text, "\n")+1 >= pasteCollapseLines || len(text) > pasteCollapseChars
}

// collapsePaste stores a long paste and inserts its placeholder at the
// cursor.
func (m *Model) collapsePaste(text string) {
	if m.pastes == nil {
		m.pastes = map[string]string{}
	}
	m.pasteSeq++
	lines := strings.Count(strings.TrimRight(text, "\n"), "\n") + 1
	placeholder := fmt.Sprintf("[Pasted text #%d +%d lines]", m.pasteSeq, lines)
	m.pastes[placeholder] = text
	m.composer.InsertString(placeholder)
}

// expandPastes replaces each placeholder with the text it stands for. A
// placeholder the operator edited no longer matches and is left as typed.
func (m *Model) expandPastes(value string) string {
	if len(m.pastes) == 0 {
		return value
	}
	return pastePlaceholderPattern.ReplaceAllStringFunc(value, func(token string) string {
		if text, ok := m.pastes[token]; ok {
			return text
		}
		return token
	})
}

// forgetPastes drops the pastes a sent input used, keeping the ones a queued
// input still needs.
func (m *Model) forgetPastes(value string) {
	for _, token := range pastePlaceholderPattern.FindAllString(value, -1) {
		still := false
		for _, queued := range m.queue {
			if strings.Contains(queued, token) {
				still = true
				break
			}
		}
		if !still {
			delete(m.pastes, token)
		}
	}
}
