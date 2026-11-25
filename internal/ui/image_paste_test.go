package ui

import (
	"bytes"
	"encoding/base64"
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

// Pasting a data URL holding image bytes attaches the image, like a
// clipboard screenshot does.
func TestDataURLImagePasteIsAttached(t *testing.T) {
	model := chatModel(t)
	url := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngData(t))
	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(url), Paste: true})
	updated := next.(*Model)
	if len(updated.attachments) != 1 || updated.attachments[0].Placeholder != "[Image #1]" {
		t.Fatalf("attachments = %+v", updated.attachments)
	}
	if strings.Contains(updated.composer.Value(), "data:image") {
		t.Fatalf("composer = %q, want the placeholder", updated.composer.Value())
	}
	// A non-image data URL stays text.
	if got := pastedImageData("data:text/plain;base64," + base64.StdEncoding.EncodeToString([]byte("hi"))); got != nil {
		t.Fatalf("non-image data URL must stay text, got %d bytes", len(got))
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

func TestUnquotedSpaceImagePathAndEmptyPaste(t *testing.T) {
	model := chatModel(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "Screenshot 2026-11-21 212606.png")
	if err := os.WriteFile(path, pngData(t), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := pastedImagePaths(dir, path); len(got) != 1 || got[0] != path {
		t.Fatalf("unquoted image path with spaces failed: got %v", got)
	}

	// Empty bracketed paste triggers clipboard fallback read.
	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Paste: true})
	if cmd == nil {
		t.Fatal("empty bracketed paste should trigger readClipboardForPaste")
	}

	// Copied Explorer files delivered via clipboardPasteMsg.paths attach cleanly.
	model.applyClipboardPaste(clipboardPasteMsg{paths: []string{path}})
	if len(model.attachments) != 1 || model.attachments[0].Label != "Screenshot 2026-11-21 212606.png" {
		t.Fatalf("attachments = %+v", model.attachments)
	}
	if !strings.Contains(model.composer.Value(), "[Image #1]") {
		t.Fatalf("composer = %q, want [Image #1]", model.composer.Value())
	}
}
