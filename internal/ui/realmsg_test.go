package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/viewport"
)

func cleanMarkers(word string) string {
	word = strings.ReplaceAll(word, "**", "")
	word = strings.ReplaceAll(word, "`", "")
	// Bullets render "-" as "*".
	if word == "-" || word == "*" {
		return "•"
	}
	return word
}

func extractAssistant22(t *testing.T, data []byte) string {
	t.Helper()
	var parsed struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse session: %v", err)
	}
	for _, message := range parsed.Messages {
		if message.Role == "assistant" && strings.Contains(message.Content, "Struktur utama") {
			return message.Content
		}
	}
	t.Fatal("summary message not found")
	return ""
}

// TestRealSessionMessageRendersClean loads the exact stored assistant
// message once seen mangled on screen and renders it through the full
// production path. Any drop or swap fails here, not in production.
func TestRealSessionMessageRendersClean(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	data, err := os.ReadFile(filepath.Join(home, ".termixgo", "sessions", "ffe34a69f1925d97.json"))
	if err != nil {
		t.Skip("session fixture not present")
	}
	text := extractAssistant22(t, data)

	forceColor(t)
	styles := NewStyles(DefaultPalette())
	const width = 120
	rendered := transcript([]block{{kind: blockAssistant, text: text}}, styles, width, true)

	viewportModel := viewport.New(width, 40)
	viewportModel.SetContent(rendered)
	viewportModel.GotoBottom()
	visible := stripANSI(viewportModel.View())

	plain := strings.ReplaceAll(text, "`", "")
	plain = strings.ReplaceAll(plain, "**", "")
	want := strings.Fields(plain)
	got := strings.Fields(visible)
	if len(got) != len(want) {
		t.Fatalf("word count %d, want %d:\n%s", len(got), len(want), visible)
	}
	for index := range want {
		if cleanMarkers(got[index]) != cleanMarkers(want[index]) {
			t.Fatalf("word %d = %q, want %q", index, got[index], want[index])
		}
	}
}
