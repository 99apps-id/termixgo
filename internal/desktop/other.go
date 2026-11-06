//go:build !linux && !darwin && !windows

package desktop

import (
	"context"
	"fmt"
)

// unsupportedController keeps the package buildable on a host with no desktop
// driver. Every call names the limitation instead of failing silently.
type unsupportedController struct{}

func newController() Controller { return unsupportedController{} }

func requirements() []string {
	return []string{"a supported host: linux, macOS or windows"}
}

func unsupported(what string) error {
	return fmt.Errorf("%s is not supported on this host", what)
}

func (unsupportedController) Capture(context.Context, CaptureRequest) error {
	return unsupported("screen capture")
}
func (unsupportedController) ListWindows(context.Context) ([]Window, error) {
	return nil, unsupported("listing windows")
}
func (unsupportedController) FocusWindow(context.Context, string) error {
	return unsupported("focusing a window")
}
func (unsupportedController) Move(context.Context, Point) error {
	return unsupported("moving the pointer")
}
func (unsupportedController) Click(context.Context, Point, string, int) error {
	return unsupported("clicking")
}
func (unsupportedController) Drag(context.Context, Point, Point) error {
	return unsupported("dragging")
}
func (unsupportedController) Scroll(context.Context, string, int) error {
	return unsupported("scrolling")
}
func (unsupportedController) TypeText(context.Context, string) error {
	return unsupported("typing")
}
func (unsupportedController) PressKey(context.Context, string) error {
	return unsupported("pressing a key")
}
func (unsupportedController) Launch(context.Context, string, []string) error {
	return unsupported("launching an application")
}
