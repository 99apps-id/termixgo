package ui

import (
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
)

// garbleSentence is a real assistant sentence once seen mangled on screen
// while the stored session bytes stayed clean. These tests pin the pipeline
// from streamed deltas to pixels: assembly must be byte-exact and rendering
// must preserve every word in order.
const garbleSentence = "Saya cek dulu mana yang dimaksud dan apakah ada cara mengukurnya di mesin ini."

// TestStreamedDeltasAssembleExactly feeds the sentence in tiny chunks the way
// the provider stream delivers them and requires the transcript block to
// hold the exact bytes afterwards.
func TestStreamedDeltasAssembleExactly(t *testing.T) {
	model := chatModel(t)
	runes := []rune(garbleSentence)
	for start := 0; start < len(runes); {
		end := start + 3
		if end > len(runes) {
			end = len(runes)
		}
		model.applyEvent(agent.Event{Kind: agent.EventText, Text: string(runes[start:end])})
		start = end
	}
	var found *block
	for index := range model.blocks {
		if model.blocks[index].kind == blockAssistant {
			found = &model.blocks[index]
		}
	}
	if found == nil {
		t.Fatalf("no assistant block was assembled")
	}
	if found.text != garbleSentence {
		t.Errorf("assembled = %q, want %q", found.text, garbleSentence)
	}
}

// TestRenderPreservesWordOrder renders the sentence through the full
// transcript at several widths and requires every word to survive in order.
// Wrapping may break lines but must never drop, duplicate or reorder words.
func TestRenderPreservesWordOrder(t *testing.T) {
	words := strings.Fields(garbleSentence)
	for _, width := range []int{40, 60, 80, 120, 200} {
		styles := NewStyles(DefaultPalette())
		rendered := stripANSI(transcript([]block{{kind: blockAssistant, text: garbleSentence}}, styles, width, true))
		got := strings.Fields(rendered)
		if len(got) != len(words) {
			t.Errorf("width %d: %d words, want %d:\n%q", width, len(got), len(words), rendered)
			continue
		}
		for index, want := range words {
			if got[index] != want {
				t.Errorf("width %d word %d = %q, want %q:\n%s", width, index, got[index], want, rendered)
			}
		}
	}
}

// TestTranscriptStripsTerminalControlSequences is the frame-injection guard:
// model or tool output that carries a raw escape or carriage return must never
// reach the terminal, because the terminal obeys it and rewrites the frame under
// the cursor, which reads as words cut and pasted from a neighbouring line.
func TestTranscriptStripsTerminalControlSequences(t *testing.T) {
	styles := NewStyles(DefaultPalette())
	text := "before\x1b[2Ja\x1b[1mb\rcd\tef"
	rendered := transcript([]block{{kind: blockAssistant, text: text}}, styles, 80, true)
	if strings.ContainsAny(rendered, "\x1b\r\t") {
		t.Errorf("a control character survived the render: %q", rendered)
	}
	plain := stripANSI(rendered)
	if !strings.Contains(plain, "before") || !strings.Contains(plain, "abcd") {
		t.Errorf("text was lost while stripping controls: %q", plain)
	}
}

// TestRenderKeepsThinkingAndAnswerSeparate ensures a thinking block followed
// by its answer renders both texts verbatim, so a collapsed reasoning header
// can never swallow answer characters.
func TestRenderKeepsThinkingAndAnswerSeparate(t *testing.T) {
	styles := NewStyles(DefaultPalette())
	blocks := []block{
		{kind: blockThinking, reasoning: "considering the workspace layout", seconds: 7},
		{kind: blockAssistant, text: garbleSentence},
	}
	for _, showDetails := range []bool{true, false} {
		rendered := stripANSI(transcript(blocks, styles, 120, showDetails))
		if !strings.Contains(rendered, garbleSentence) {
			t.Errorf("showDetails=%v lost the answer:\n%s", showDetails, rendered)
		}
	}
}
