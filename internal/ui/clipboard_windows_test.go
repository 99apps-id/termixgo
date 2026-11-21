//go:build windows

package ui

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"testing"
)

func TestDecodeDIBToPNG32BitZeroAlphaBecomesOpaque(t *testing.T) {
	// 2x2 top-down 32-bit BI_RGB DIB with zeroed alpha bytes (common in Win32 GDI).
	dib := make([]byte, 40+16)
	binary.LittleEndian.PutUint32(dib[0:4], 40)
	binary.LittleEndian.PutUint32(dib[4:8], 2)
	height := int32(-2)
	binary.LittleEndian.PutUint32(dib[8:12], uint32(height))
	binary.LittleEndian.PutUint16(dib[12:14], 1)
	binary.LittleEndian.PutUint16(dib[14:16], 32)
	binary.LittleEndian.PutUint32(dib[16:20], biRGB)

	// Top-left pixel: B=10, G=20, R=200, A=0.
	dib[40], dib[41], dib[42], dib[43] = 10, 20, 200, 0

	pngBytes, err := decodeDIBToPNG(dib)
	if err != nil {
		t.Fatalf("decodeDIBToPNG: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("png.Decode: %v", err)
	}
	r, g, b, a := img.At(0, 0).RGBA()
	if r>>8 != 200 || g>>8 != 20 || b>>8 != 10 || a>>8 != 255 {
		t.Fatalf("pixel(0,0) = (%d,%d,%d,%d), want (200,20,10,255)", r>>8, g>>8, b>>8, a>>8)
	}
}

func TestDecodeDIBToPNG24BitBottomUp(t *testing.T) {
	// 1x2 bottom-up 24-bit BI_RGB DIB; each row is padded to 4 bytes.
	dib := make([]byte, 40+8)
	binary.LittleEndian.PutUint32(dib[0:4], 40)
	binary.LittleEndian.PutUint32(dib[4:8], 1)
	binary.LittleEndian.PutUint32(dib[8:12], 2)
	binary.LittleEndian.PutUint16(dib[12:14], 1)
	binary.LittleEndian.PutUint16(dib[14:16], 24)

	// Bottom row (y=1): blue.
	dib[40], dib[41], dib[42] = 250, 0, 0
	// Top row (y=0): green.
	dib[44], dib[45], dib[46] = 0, 240, 0

	pngBytes, err := decodeDIBToPNG(dib)
	if err != nil {
		t.Fatalf("decodeDIBToPNG: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("png.Decode: %v", err)
	}
	_, g0, _, _ := img.At(0, 0).RGBA()
	_, _, b1, _ := img.At(0, 1).RGBA()
	if g0>>8 != 240 || b1>>8 != 250 {
		t.Fatalf("got top green=%d bottom blue=%d, want 240 and 250", g0>>8, b1>>8)
	}
}
