package ui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

// isShiftPressed reports whether a Shift modifier is physically held down on
// platforms where Bubble Tea v1 drops Shift on Return (Windows). Overridden
// in unit tests.
var isShiftPressed = shiftKeyDown

// isShiftEnterCSI reports whether message is Bubble Tea v1's
// unknownCSISequenceMsg for a Shift+Enter escape sequence emitted by modern
// terminals (Kitty keyboard protocol CSI 13;2u / CSI 13;2:1u, or xterm
// modifyOtherKeys CSI 27;2;13~).
func isShiftEnterCSI(message tea.Msg) bool {
	if _, isKey := message.(tea.KeyMsg); isKey {
		return false
	}
	s, ok := message.(fmt.Stringer)
	if !ok {
		return false
	}
	// Bubble Tea v1.3.10 formats unknownCSISequenceMsg as "?CSI%+v?" of raw
	// bytes after "\x1b[". Checking the exact formatted signatures avoids
	// reflection and never touches normal KeyMsg values.
	switch s.String() {
	case "?CSI[49 51 59 50 117]?", // \x1b[13;2u
		"?CSI[49 51 59 50 58 49 117]?",    // \x1b[13;2:1u
		"?CSI[50 55 59 50 59 49 51 126]?": // \x1b[27;2;13~
		return true
	}
	return false
}
