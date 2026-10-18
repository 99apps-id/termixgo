package agent

import (
	"bytes"
	"image"
	"image/png"
	"testing"
)

// tallPNG renders a one-pixel-wide column stretched to the given height, which
// is what a full-page screenshot looks like to the size guard.
func tallPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, width, height))); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.Bytes()
}

// TestShrinkImageScalesATallImage pins the DeepSeek failure: an image whose side
// is past 8192 must come back with both sides within the cap, as a valid PNG.
func TestShrinkImageScalesATallImage(t *testing.T) {
	out, changed := ShrinkImage(tallPNG(t, 100, maxImageDimension+1))
	if !changed {
		t.Fatalf("a side of %d must be scaled down", maxImageDimension+1)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("the scaled image must decode: %v", err)
	}
	if config.Width > maxImageDimension || config.Height > maxImageDimension {
		t.Errorf("scaled image = %dx%d, want both sides <= %d", config.Width, config.Height, maxImageDimension)
	}
}

// TestShrinkImageLeavesASmallImageAlone keeps the common case byte-for-byte.
func TestShrinkImageLeavesASmallImageAlone(t *testing.T) {
	original := tallPNG(t, 40, 30)
	out, changed := ShrinkImage(original)
	if changed {
		t.Fatalf("an image within the cap must not be scaled")
	}
	if !bytes.Equal(out, original) {
		t.Errorf("a small image's bytes changed")
	}
}

// TestShrinkImageLeavesAnUndecodableImageAlone keeps a format the decoder does
// not know (webp, bmp, or junk) on its way to the provider unchanged.
func TestShrinkImageLeavesAnUndecodableImageAlone(t *testing.T) {
	raw := []byte("not an image at all")
	out, changed := ShrinkImage(raw)
	if changed || !bytes.Equal(out, raw) {
		t.Errorf("an undecodable payload must pass through unchanged")
	}
}
