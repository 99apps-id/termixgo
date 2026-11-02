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
)

func writeTestPNG(t *testing.T, path string) {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			canvas.Set(x, y, color.RGBA{R: 20, G: 40, B: 60, A: 255})
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, canvas); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	if err := os.WriteFile(path, buffer.Bytes(), 0o600); err != nil {
		t.Fatalf("write png: %v", err)
	}
}

func TestSplitAttachmentArgs(t *testing.T) {
	got := splitAttachmentArgs(`a.png "two words.pdf" 'three.txt'`)
	want := []string{"a.png", "two words.pdf", "three.txt"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestBuildAttachmentImage(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "shot.png")
	writeTestPNG(t, path)

	att, err := buildAttachment(workspace, "shot.png")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if att.Kind != "image" || att.MediaType != "image/png" {
		t.Fatalf("kind=%q media=%q", att.Kind, att.MediaType)
	}
	if len(att.Data) == 0 {
		t.Fatal("image bytes are empty")
	}
}

func TestBuildAttachmentDocument(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "spec.md"), []byte("attached spec"), 0o600); err != nil {
		t.Fatal(err)
	}
	att, err := buildAttachment(workspace, "spec.md")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if att.Kind != "text" || !strings.Contains(att.Text, "attached spec") {
		t.Fatalf("kind=%q text=%q", att.Kind, att.Text)
	}
}

func TestBuildAttachmentRejectsDirectory(t *testing.T) {
	workspace := t.TempDir()
	if _, err := buildAttachment(workspace, "."); err == nil {
		t.Fatal("expected a directory refusal")
	}
}

func TestConsumeAttachments(t *testing.T) {
	model := &Model{attachments: []attachment{
		{Kind: "image", MediaType: "image/png", Data: []byte("pixels")},
		{Kind: "text", Label: "spec.md", Text: "the body"},
	}}
	prompt, images := model.consumeAttachments("do the work")
	if !strings.Contains(prompt, "the body") || !strings.Contains(prompt, "do the work") {
		t.Fatalf("prompt = %q", prompt)
	}
	if len(images) != 1 || images[0].MediaType != "image/png" {
		t.Fatalf("images = %+v", images)
	}
	if len(model.attachments) != 0 {
		t.Fatal("attachments must be consumed")
	}
}

func TestAttachmentSummaryAndBlock(t *testing.T) {
	items := []attachment{
		{Kind: "image", Label: "shot.png"},
		{Kind: "pdf", Label: "report.pdf", Text: "page one"},
	}
	summary := attachmentSummary(items)
	if !strings.Contains(summary, "shot.png (image)") || !strings.Contains(summary, "/detach") {
		t.Fatalf("summary = %q", summary)
	}
	block := attachmentDocumentBlock(items)
	if !strings.Contains(block, "page one") || strings.Contains(block, "shot.png") {
		t.Fatalf("block = %q", block)
	}
	if attachmentSummary(nil) != "" {
		t.Fatal("an empty set has no summary")
	}
}

func TestSpoolLongInput(t *testing.T) {
	workspace := t.TempDir()
	input := strings.Repeat("x", pasteSpoolThreshold+50)
	spooled := spoolLongInput(workspace, input)
	if !strings.Contains(spooled, "was saved to .termixgo/pastes/") {
		t.Fatalf("spooled = %q", spooled)
	}
	matches, err := filepath.Glob(filepath.Join(workspace, ".termixgo", "pastes", "paste-*.txt"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("matches=%v err=%v", matches, err)
	}
	if info, err := os.Stat(matches[0]); err != nil || info.Size() != int64(len(input)) {
		t.Fatalf("spool file size mismatch: %v", err)
	}
	if short := spoolLongInput(workspace, "short"); short != "short" {
		t.Fatalf("a short input must pass through, got %q", short)
	}
}

func TestAttachPastedText(t *testing.T) {
	workspace := t.TempDir()
	text := strings.Repeat("line\n", 20000)
	att, err := attachPastedText(workspace, text)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if att.Kind != "text" || len(att.Text) == 0 {
		t.Fatalf("kind=%q textLen=%d", att.Kind, len(att.Text))
	}
	matches, _ := filepath.Glob(filepath.Join(workspace, ".termixgo", "pastes", "paste-*.txt"))
	if len(matches) != 1 {
		t.Fatalf("expected one spooled file, got %v", matches)
	}
	if info, err := os.Stat(matches[0]); err != nil || info.Size() != int64(len(text)) {
		t.Fatalf("spooled size mismatch: %v", err)
	}
}

func TestDisplayInputClips(t *testing.T) {
	long := strings.Repeat("y", attachmentDisplayCap+10)
	shown := displayInput(long)
	if len(shown) > attachmentDisplayCap+64 {
		t.Fatalf("display was not clipped: %d", len(shown))
	}
	if short := displayInput("hello"); short != "hello" {
		t.Fatalf("short input changed: %q", short)
	}
}
