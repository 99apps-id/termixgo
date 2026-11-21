//go:build windows

package ui

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/99apps-id/termixgo/internal/agent"
)

const (
	cfDIB         = 8
	cfUnicodeText = 13
	cfHDrop       = 15
	cfDIBV5       = 17

	biRGB       = 0
	biBitfields = 3
)

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	kernel32                = windows.NewLazySystemDLL("kernel32.dll")
	shell32                 = windows.NewLazySystemDLL("shell32.dll")
	procOpenClipboard       = user32.NewProc("OpenClipboard")
	procCloseClipboard      = user32.NewProc("CloseClipboard")
	procIsClipboardFmtAvail = user32.NewProc("IsClipboardFormatAvailable")
	procGetClipboardData    = user32.NewProc("GetClipboardData")
	procRegisterClipboard   = user32.NewProc("RegisterClipboardFormatW")
	procGlobalLock          = kernel32.NewProc("GlobalLock")
	procGlobalUnlock        = kernel32.NewProc("GlobalUnlock")
	procGlobalSize          = kernel32.NewProc("GlobalSize")
	procRtlMoveMemory       = kernel32.NewProc("RtlMoveMemory")
	procDragQueryFileW      = shell32.NewProc("DragQueryFileW")
)

func openClipboardWithRetry() error {
	for attempt := 0; attempt < 6; attempt++ {
		ret, _, _ := procOpenClipboard.Call(0)
		if ret != 0 {
			return nil
		}
		time.Sleep(15 * time.Millisecond)
	}
	return fmt.Errorf("clipboard is busy")
}

func closeClipboard() {
	_, _, _ = procCloseClipboard.Call()
}

func isClipboardFormatAvailable(format uint32) bool {
	ret, _, _ := procIsClipboardFmtAvail.Call(uintptr(format))
	return ret != 0
}

func registerClipboardFormat(name string) uint32 {
	ptr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0
	}
	ret, _, _ := procRegisterClipboard.Call(uintptr(unsafe.Pointer(ptr)))
	return uint32(ret)
}

func readClipboardGlobalBytes(format uint32) ([]byte, bool) {
	handle, _, _ := procGetClipboardData.Call(uintptr(format))
	if handle == 0 {
		return nil, false
	}
	size, _, _ := procGlobalSize.Call(handle)
	if size == 0 || size > maxAttachmentImageBytes {
		return nil, false
	}
	ptr, _, _ := procGlobalLock.Call(handle)
	if ptr == 0 {
		return nil, false
	}
	defer procGlobalUnlock.Call(handle)

	out := make([]byte, int(size))
	_, _, _ = procRtlMoveMemory.Call(uintptr(unsafe.Pointer(&out[0])), ptr, size)
	return out, true
}

func readClipboardImageNative() ([]byte, []string, error) {
	if err := openClipboardWithRetry(); err != nil {
		return nil, nil, err
	}
	defer closeClipboard()

	if cfPNG := registerClipboardFormat("PNG"); cfPNG != 0 && isClipboardFormatAvailable(cfPNG) {
		if data, ok := readClipboardGlobalBytes(cfPNG); ok && bytes.HasPrefix(data, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
			return data, nil, nil
		}
	}

	if isClipboardFormatAvailable(cfHDrop) {
		if paths := readClipboardDropImagePaths(); len(paths) > 0 {
			return nil, paths, nil
		}
	}

	for _, format := range []uint32{cfDIBV5, cfDIB} {
		if !isClipboardFormatAvailable(format) {
			continue
		}
		if dib, ok := readClipboardGlobalBytes(format); ok {
			if pngBytes, err := decodeDIBToPNG(dib); err == nil && len(pngBytes) > 0 {
				return pngBytes, nil, nil
			}
		}
	}

	return nil, nil, fmt.Errorf("the clipboard holds no image")
}

func readClipboardDropImagePaths() []string {
	handle, _, _ := procGetClipboardData.Call(uintptr(cfHDrop))
	if handle == 0 {
		return nil
	}
	count, _, _ := procDragQueryFileW.Call(handle, 0xFFFFFFFF, 0, 0)
	n := int(uint32(count))
	if n <= 0 || n > maxAttachments {
		return nil
	}
	var paths []string
	buf := make([]uint16, 1024)
	for i := 0; i < n; i++ {
		copied, _, _ := procDragQueryFileW.Call(handle, uintptr(i), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		if copied == 0 {
			return nil
		}
		path := windows.UTF16ToString(buf[:copied])
		if _, ok := agent.ImageMediaType(path); !ok {
			return nil
		}
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			return nil
		}
		paths = append(paths, path)
	}
	return paths
}

func readClipboardTextNative() (string, error) {
	if err := openClipboardWithRetry(); err != nil {
		return "", err
	}
	defer closeClipboard()

	if !isClipboardFormatAvailable(cfUnicodeText) {
		return "", nil
	}
	handle, _, _ := procGetClipboardData.Call(uintptr(cfUnicodeText))
	if handle == 0 {
		return "", nil
	}
	size, _, _ := procGlobalSize.Call(handle)
	if size < 2 || size > 4*1024*1024 {
		return "", nil
	}
	ptr, _, _ := procGlobalLock.Call(handle)
	if ptr == 0 {
		return "", fmt.Errorf("cannot lock clipboard text")
	}
	defer procGlobalUnlock.Call(handle)

	buf := make([]uint16, int(size/2))
	_, _, _ = procRtlMoveMemory.Call(uintptr(unsafe.Pointer(&buf[0])), ptr, uintptr(len(buf)*2))
	text := windows.UTF16ToString(buf)
	return strings.ReplaceAll(text, "\r\n", "\n"), nil
}

// decodeDIBToPNG converts an uncompressed 24-bit or 32-bit Windows CF_DIB /
// CF_DIBV5 memory block into a standard PNG image.
func decodeDIBToPNG(dib []byte) ([]byte, error) {
	if len(dib) < 40 {
		return nil, fmt.Errorf("short DIB header")
	}
	headerSize := int(binary.LittleEndian.Uint32(dib[0:4]))
	if headerSize < 40 || len(dib) < headerSize {
		return nil, fmt.Errorf("invalid DIB header size %d", headerSize)
	}
	width := int(int32(binary.LittleEndian.Uint32(dib[4:8])))
	rawHeight := int(int32(binary.LittleEndian.Uint32(dib[8:12])))
	planes := int(binary.LittleEndian.Uint16(dib[12:14]))
	bpp := int(binary.LittleEndian.Uint16(dib[14:16]))
	compression := binary.LittleEndian.Uint32(dib[16:20])
	clrUsed := int(binary.LittleEndian.Uint32(dib[32:36]))

	if planes != 1 || width <= 0 || rawHeight == 0 || width > 16384 {
		return nil, fmt.Errorf("unsupported DIB geometry %dx%d", width, rawHeight)
	}
	topDown := rawHeight < 0
	height := rawHeight
	if topDown {
		height = -rawHeight
	}
	if height > 16384 {
		return nil, fmt.Errorf("unsupported DIB height %d", height)
	}
	if compression != biRGB && compression != biBitfields {
		return nil, fmt.Errorf("unsupported DIB compression %d", compression)
	}
	if bpp != 24 && bpp != 32 {
		return nil, fmt.Errorf("unsupported DIB bit depth %d", bpp)
	}

	offset := headerSize
	// BITMAPINFOHEADER (size 40) with BI_BITFIELDS appends three 4-byte masks
	// immediately after the 40-byte header; V4/V5 already include masks.
	var rMask, gMask, bMask, aMask uint32 = 0x00FF0000, 0x0000FF00, 0x000000FF, 0xFF000000
	if headerSize == 40 && compression == biBitfields {
		if len(dib) < offset+12 {
			return nil, fmt.Errorf("truncated DIB bitfields")
		}
		rMask = binary.LittleEndian.Uint32(dib[offset : offset+4])
		gMask = binary.LittleEndian.Uint32(dib[offset+4 : offset+8])
		bMask = binary.LittleEndian.Uint32(dib[offset+8 : offset+12])
		aMask = 0
		offset += 12
	} else if headerSize >= 56 && compression == biBitfields {
		rMask = binary.LittleEndian.Uint32(dib[40:44])
		gMask = binary.LittleEndian.Uint32(dib[44:48])
		bMask = binary.LittleEndian.Uint32(dib[48:52])
		aMask = binary.LittleEndian.Uint32(dib[52:56])
	}
	if clrUsed > 0 {
		offset += clrUsed * 4
	}

	rowStride := ((width*bpp + 31) / 32) * 4
	if offset+rowStride*height > len(dib) {
		return nil, fmt.Errorf("truncated DIB pixel data")
	}

	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	hasAlpha := false
	for y := 0; y < height; y++ {
		srcY := y
		if !topDown {
			srcY = height - 1 - y
		}
		row := dib[offset+srcY*rowStride : offset+(srcY+1)*rowStride]
		for x := 0; x < width; x++ {
			if bpp == 24 {
				p := row[x*3:]
				img.SetNRGBA(x, y, color.NRGBA{R: p[2], G: p[1], B: p[0], A: 255})
				continue
			}
			px := binary.LittleEndian.Uint32(row[x*4 : x*4+4])
			r := extractMaskByte(px, rMask)
			g := extractMaskByte(px, gMask)
			b := extractMaskByte(px, bMask)
			var a uint8 = 255
			if aMask != 0 {
				a = extractMaskByte(px, aMask)
				if a != 0 {
					hasAlpha = true
				}
			}
			img.SetNRGBA(x, y, color.NRGBA{R: r, G: g, B: b, A: a})
		}
	}

	// Many Windows GDI sources leave the alpha channel zeroed in 32-bit DIBs.
	// If no pixel carries non-zero alpha, treat the whole image as opaque.
	if bpp == 32 && aMask != 0 && !hasAlpha {
		for i := 3; i < len(img.Pix); i += 4 {
			img.Pix[i] = 255
		}
	}

	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func extractMaskByte(px, mask uint32) uint8 {
	if mask == 0 {
		return 0
	}
	shift := uint(0)
	for (mask>>shift)&1 == 0 && shift < 32 {
		shift++
	}
	width := uint(0)
	for ((mask >> (shift + width)) & 1) == 1 {
		width++
	}
	val := (px & mask) >> shift
	if width == 8 {
		return uint8(val)
	}
	if width == 0 {
		return 0
	}
	maxVal := (uint32(1) << width) - 1
	return uint8((val * 255) / maxVal)
}

var _ = syscall.Errno(0)
