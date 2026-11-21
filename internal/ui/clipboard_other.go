//go:build !windows

package ui

import "fmt"

var errNativeClipboardUnsupported = fmt.Errorf("native clipboard reader is Windows-only")

func readClipboardImageNative() ([]byte, []string, error) {
	return nil, nil, errNativeClipboardUnsupported
}

func readClipboardTextNative() (string, error) {
	return "", errNativeClipboardUnsupported
}
