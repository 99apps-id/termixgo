//go:build windows

package ui

import (
	"bytes"
	"image/png"
	"os"
	"os/exec"
	"testing"
)

// TestLiveClipboardImageRoundTrip puts a real image on the Windows clipboard
// and reads it back through the path Ctrl+V and Alt+V use. It replaces the
// clipboard's content, so it only runs when TERMIXGO_CLIPBOARD_TEST=1, with
// TERMIXGO_CLIPBOARD_IMAGE naming the image to use.
func TestLiveClipboardImageRoundTrip(t *testing.T) {
	if os.Getenv("TERMIXGO_CLIPBOARD_TEST") != "1" {
		t.Skip("set TERMIXGO_CLIPBOARD_TEST=1 to exercise the real clipboard")
	}
	source := os.Getenv("TERMIXGO_CLIPBOARD_IMAGE")
	if source == "" {
		t.Skip("set TERMIXGO_CLIPBOARD_IMAGE to an image file")
	}
	script := `param([string]$Path)
Add-Type -AssemblyName System.Windows.Forms,System.Drawing
$image = [System.Drawing.Image]::FromFile($Path)
[System.Windows.Forms.Clipboard]::SetDataObject($image, $true)`
	dir := t.TempDir()
	scriptPath := dir + `\set.ps1`
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("powershell.exe", "-NoProfile", "-STA", "-ExecutionPolicy", "Bypass", "-File", scriptPath, "-Path", source).CombinedOutput(); err != nil {
		t.Fatalf("setting the clipboard failed: %v %s", err, out)
	}

	data, paths, err := readClipboardImageOrPaths()
	if err != nil {
		t.Fatalf("reading the clipboard image failed: %v", err)
	}
	if len(paths) > 0 {
		t.Fatalf("a bitmap on the clipboard should come back as pixels, got paths %v", paths)
	}
	picture, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("the clipboard image is not a PNG: %v", err)
	}
	t.Logf("clipboard image %dx%d, %d bytes", picture.Bounds().Dx(), picture.Bounds().Dy(), len(data))

	msg := readClipboardForPaste().(clipboardPasteMsg)
	if len(msg.image) == 0 && len(msg.paths) == 0 {
		t.Fatalf("the paste message carries no image: %+v", msg.err)
	}
}
