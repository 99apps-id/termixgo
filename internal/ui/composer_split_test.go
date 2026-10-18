package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// keysFromRead reproduces what bubbletea v1.3.10 hands to Update for one read
// of the ANSI input path. detectOneMsg decodes a leading ESC plus exactly one
// rune as Alt+that rune (alt is cleared after the first rune), so a read that
// opens with ESC becomes Alt+first-rune followed by the remaining bytes as one
// KeyRunes burst. A read that does not open with ESC is a single KeyRunes.
func keysFromRead(read string) []tea.KeyMsg {
	if read == "" {
		return nil
	}
	body := []rune(read)
	if body[0] != 0x1b {
		return []tea.KeyMsg{{Type: tea.KeyRunes, Runes: body}}
	}
	body = body[1:]
	if len(body) == 0 {
		return []tea.KeyMsg{{Type: tea.KeyEscape}}
	}
	keys := []tea.KeyMsg{{Type: tea.KeyRunes, Runes: body[:1], Alt: true}}
	if len(body) > 1 {
		keys = append(keys, tea.KeyMsg{Type: tea.KeyRunes, Runes: body[1:]})
	}
	return keys
}

// Every way one terminal report can be cut across reads must leave the draft
// clean. This walks all cut points of the report shapes a terminal can send
// while the composer holds text, so a leak cannot hide in an untested split.
func TestEveryReportSplitLeavesTheDraftClean(t *testing.T) {
	// Only the reports this program can actually receive: it enables mouse
	// reporting as cell motion with SGR encoding (?1002h, ?1006h) and
	// bracketed paste (?2004h), and it does not enable X10 mouse or focus
	// reporting. A cursor position or device attributes reply is included
	// because a terminal may send one unsolicited.
	reports := []string{
		"\x1b[<35;106;27M", // SGR wheel down
		"\x1b[<64;10;20M",  // SGR wheel up
		"\x1b[<0;12;5m",    // SGR release
		"\x1b[<35;1;1;1M",  // SGR with an extra parameter
		"\x1b[12;40R",      // cursor position report
		"\x1b[?62;1;6c",    // device attributes
		"\x1b[200~",        // bracketed paste start
		"\x1b[201~",        // bracketed paste end
	}

	for _, report := range reports {
		for cut := 1; cut < len(report); cut++ {
			t.Run(reportName(report, cut), func(t *testing.T) {
				model := chatModel(t)
				model.composer.SetValue("draft")
				current := model
				for _, key := range append(keysFromRead(report[:cut]), keysFromRead(report[cut:])...) {
					step, _ := send(t, current, key)
					current = step
				}
				if got := current.composer.Value(); got != "draft" {
					t.Errorf("report %q cut at %d leaked: composer = %q", report, cut, got)
				}
			})
		}
	}
}

func reportName(report string, cut int) string {
	name := ""
	for i, r := range []rune(report) {
		if i == cut {
			name += "|"
		}
		switch {
		case r == 0x1b:
			name += "ESC"
		case r < 0x20:
			name += "."
		default:
			name += string(r)
		}
	}
	return name
}
