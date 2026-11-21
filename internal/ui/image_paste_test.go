package ui

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func pngData(t *testing.T) []byte {
	t.Helper()
	picture := image.NewRGBA(image.Rect(0, 0, 4, 4))
	picture.Set(1, 1, color.RGBA{R: 255, A: 255})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, picture); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

// A clipboard image becomes an attachment with a placeholder at the cursor.
func TestClipboardImagePasteInsertsAPlaceholder(t *testing.T) {
	model := chatModel(t)
	model.composer.SetValue("what is this ")
	model.composer.CursorEnd()
	model.applyClipboardPaste(clipboardPasteMsg{image: pngData(t)})

	if len(model.attachments) != 1 || model.attachments[0].Placeholder != "[Image #1]" {
		t.Fatalf("attachments = %+v", model.attachments)
	}
	if got := model.composer.Value(); got != "what is this [Image #1] " {
		t.Fatalf("composer = %q", got)
	}
	model.applyClipboardPaste(clipboardPasteMsg{image: pngData(t)})
	if model.attachments[1].Placeholder != "[Image #2]" {
		t.Fatalf("the second image should be numbered 2, got %q", model.attachments[1].Placeholder)
	}
}

// Ctrl+V and Alt+V read the clipboard off the event loop.
func TestCtrlVReadsTheClipboardAsACommand(t *testing.T) {
	model := chatModel(t)
	for _, keyMsg := range []tea.KeyMsg{{Type: tea.KeyCtrlV}, {Type: tea.KeyRunes, Runes: []rune("v"), Alt: true}} {
		_, cmd := model.Update(keyMsg)
		if cmd == nil {
			t.Fatalf("%q should start a clipboard read", keyMsg.String())
		}
	}
}

// Deleting a placeholder from the draft drops its image from the turn.
func TestDeletedPlaceholderDropsTheImage(t *testing.T) {
	items := []attachment{
		{Label: "a.png", Kind: "image", Placeholder: "[Image #1]"},
		{Label: "b.png", Kind: "image", Placeholder: "[Image #2]"},
		{Label: "notes.txt", Kind: "text"},
	}
	kept := keptAttachments(items, "compare with [Image #2]")
	if len(kept) != 2 || kept[0].Label != "b.png" || kept[1].Label != "notes.txt" {
		t.Fatalf("kept = %+v", kept)
	}
}

// Dragging an image onto the terminal pastes its path; that path is attached
// instead of typed.
func TestPastedImagePathIsAttached(t *testing.T) {
	model := chatModel(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "shot one.png")
	if err := os.WriteFile(path, pngData(t), 0o644); err != nil {
		t.Fatal(err)
	}
	pasted := press(t, model, "")
	next, _ := pasted.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(`"` + path + `"`), Paste: true})
	updated := next.(*Model)
	if len(updated.attachments) != 1 || updated.attachments[0].Label != "shot one.png" {
		t.Fatalf("attachments = %+v", updated.attachments)
	}
	if strings.Contains(updated.composer.Value(), path) || !strings.Contains(updated.composer.Value(), "[Image #1]") {
		t.Fatalf("composer = %q", updated.composer.Value())
	}
}

// A paste that is not only image paths stays text.
func TestOrdinaryPasteIsNotAnAttachment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.png")
	if err := os.WriteFile(path, pngData(t), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := pastedImagePaths(dir, "look at "+path); got != nil {
		t.Fatalf("mixed text must stay text, got %v", got)
	}
	if got := pastedImagePaths(dir, filepath.Join(dir, "missing.png")); got != nil {
		t.Fatalf("a missing file must stay text, got %v", got)
	}
	if got := pastedImagePaths(dir, "a.png"); len(got) != 1 {
		t.Fatalf("a workspace-relative image should attach, got %v", got)
	}
}
