package ui

import (
	"strings"
	"testing"
)

func askModel(t *testing.T, width, height int, question string, options []string) *Model {
	t.Helper()
	model := chatModel(t)
	model.width, model.height = width, height
	model.pendingAsk = &askRequestMsg{question: question, options: options, reply: make(chan string, 1)}
	return model
}

// plainView is the dialog without styling, one string per row.
func plainView(view string) string { return stripANSI(view) }

// A long choice is wrapped under its number, not cut to one line: every word of
// it must still be on screen.
func TestAskDialogShowsEveryWordOfALongChoice(t *testing.T) {
	long := "Lanjutkan dengan migrasi penuh sekarang, termasuk mengubah skema database produksi dan menjalankan seluruh pengujian regresi sebelum rilis berikutnya"
	model := askModel(t, 80, 40, "Pilih langkah berikutnya untuk perubahan ini?", []string{long, "Batalkan dan tinggalkan semuanya seperti semula"})
	view := plainView(model.viewAsk())
	for _, word := range strings.Fields(long) {
		if !strings.Contains(view, word) {
			t.Fatalf("the word %q of choice 1 is missing from the dialog:\n%s", word, view)
		}
	}
	if strings.Contains(view, "...") {
		t.Errorf("nothing needed clipping here, yet the dialog has an ellipsis:\n%s", view)
	}
	// The wrapped lines hang under the text, not under the number.
	lines := strings.Split(view, "\n")
	for index, line := range lines {
		if strings.Contains(line, "1. Lanjutkan") {
			next := lines[index+1]
			if !strings.Contains(next, "   ") {
				t.Errorf("a continuation line should be indented under the choice text: %q", next)
			}
		}
	}
}

func TestAskDialogShowsTheWholeQuestion(t *testing.T) {
	question := "Berkas konfigurasi ini sudah dimodifikasi oleh proses lain sejak terakhir dibaca, jadi isinya mungkin berbeda dari yang saya lihat tadi. Apakah saya boleh menimpanya dengan versi saya sendiri?"
	model := askModel(t, 70, 40, question, nil)
	view := plainView(model.viewAsk())
	for _, word := range strings.Fields(question) {
		if !strings.Contains(view, word) {
			t.Fatalf("the word %q of the question is missing:\n%s", word, view)
		}
	}
}

// Every row of the dialog fits the width, so the terminal never wraps one on its
// own and shifts the frame.
func TestAskDialogRowsFitTheTerminal(t *testing.T) {
	long := strings.Repeat("kalimat panjang tanpa henti ", 12)
	model := askModel(t, 60, 30, long, []string{long, long, "pendek"})
	for _, line := range strings.Split(plainView(model.viewAsk()), "\n") {
		if width := len([]rune(line)); width > 60 {
			t.Errorf("a row is %d columns wide, over the 60 of the terminal: %q", width, line)
		}
	}
}

// On a short terminal the choices share the room evenly and a clipped one ends
// in an ellipsis, so a cut text never reads as complete.
func TestAskOptionsAreClippedEvenlyWhenRoomRunsOut(t *testing.T) {
	long := strings.Repeat("kata ", 40)
	fitted, hidden := fitAskOptions([]string{long, long, long}, 40, 6)
	if hidden != 0 || len(fitted) != 3 {
		t.Fatalf("hidden=%d options=%d, want every choice kept", hidden, len(fitted))
	}
	for index, lines := range fitted {
		if len(lines) != 2 {
			t.Errorf("choice %d has %d lines, want an even share of 2", index+1, len(lines))
		}
		if !strings.HasSuffix(lines[len(lines)-1], "...") {
			t.Errorf("choice %d was clipped without an ellipsis: %q", index+1, lines[len(lines)-1])
		}
	}
}

func TestAskOptionsDropTheTailOnlyWhenOneLineEachDoesNotFit(t *testing.T) {
	options := []string{"satu", "dua", "tiga", "empat", "lima"}
	fitted, hidden := fitAskOptions(options, 40, 3)
	if len(fitted) != 2 || hidden != 3 {
		t.Fatalf("shown=%d hidden=%d, want 2 shown and 3 counted", len(fitted), hidden)
	}
	whole, none := fitAskOptions(options, 40, 5)
	if len(whole) != 5 || none != 0 {
		t.Fatalf("shown=%d hidden=%d, want all five", len(whole), none)
	}
}
