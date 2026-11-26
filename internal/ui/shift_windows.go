//go:build windows

package ui

import "golang.org/x/sys/windows"

var (
	user32DLL            = windows.NewLazySystemDLL("user32.dll")
	procGetAsyncKeyState = user32DLL.NewProc("GetAsyncKeyState")
)

const (
	vkShift  = 0x10
	vkLShift = 0xA0
	vkRShift = 0xA1
)

// shiftKeyDown reports whether either Shift key is currently held down on
// Windows. Bubble Tea v1.3.10 discards SHIFT_PRESSED on VK_RETURN in its
// console input reader, so checking GetAsyncKeyState when KeyEnter arrives
// lets Shift+Enter insert a newline without any Windows Terminal settings.
func shiftKeyDown() bool {
	if err := procGetAsyncKeyState.Find(); err != nil {
		return false
	}
	for _, vk := range []uintptr{vkShift, vkLShift, vkRShift} {
		state, _, _ := procGetAsyncKeyState.Call(vk)
		if state&0x8000 != 0 {
			return true
		}
	}
	return false
}
