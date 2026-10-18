package agent

import (
	"bytes"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
)

// maxImageDimension caps the longest side of an attached image. A full-page
// screenshot can be tens of thousands of pixels tall, and a provider rejects
// one past its dimension limit with a generic "unsupported image" error, so
// anything larger is scaled down to fit before it is attached. DeepSeek, for
// one, accepts a side of 8192 and rejects 8193.
const maxImageDimension = 8192

// ShrinkImage scales an image down so neither side exceeds maxImageDimension,
// re-encoding it as PNG. It returns the original bytes and false when the image
// already fits, when it cannot be decoded (an unregistered format such as webp
// or bmp), or when re-encoding fails: a guard that cannot read the image must
// not drop it.
func ShrinkImage(data []byte) ([]byte, bool) {
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return data, false
	}
	if config.Width <= maxImageDimension && config.Height <= maxImageDimension {
		return data, false
	}
	source, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return data, false
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, scaleToFit(source, maxImageDimension)); err != nil {
		return data, false
	}
	return buf.Bytes(), true
}

// scaleToFit box-averages an image down so its longest side is at most maxSide.
// A box average keeps text legible where nearest-neighbor would alias it away,
// and it needs no resampling dependency.
func scaleToFit(source image.Image, maxSide int) image.Image {
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	longest := width
	if height > longest {
		longest = height
	}
	if longest <= maxSide {
		return source
	}
	targetW := width * maxSide / longest
	targetH := height * maxSide / longest
	if targetW < 1 {
		targetW = 1
	}
	if targetH < 1 {
		targetH = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, targetW, targetH))
	for y := 0; y < targetH; y++ {
		y0 := bounds.Min.Y + y*height/targetH
		y1 := bounds.Min.Y + (y+1)*height/targetH
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < targetW; x++ {
			x0 := bounds.Min.X + x*width/targetW
			x1 := bounds.Min.X + (x+1)*width/targetW
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var r, g, b, a, n uint64
			for yy := y0; yy < y1; yy++ {
				for xx := x0; xx < x1; xx++ {
					cr, cg, cb, ca := source.At(xx, yy).RGBA()
					r += uint64(cr)
					g += uint64(cg)
					b += uint64(cb)
					a += uint64(ca)
					n++
				}
			}
			if n == 0 {
				continue
			}
			dst.SetRGBA(x, y, color.RGBA{
				R: uint8((r / n) >> 8),
				G: uint8((g / n) >> 8),
				B: uint8((b / n) >> 8),
				A: uint8((a / n) >> 8),
			})
		}
	}
	return dst
}
