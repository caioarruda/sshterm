//go:build windows

package main

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver"
)

// GetHWND extracts the Win32 HWND from a Fyne window.
// Requires Fyne v2.4+ which implements driver.NativeWindow.
func GetHWND(win fyne.Window) uintptr {
	var hwnd uintptr
	if nw, ok := win.(driver.NativeWindow); ok {
		nw.RunNative(func(ctx any) {
			if wctx, ok := ctx.(driver.WindowsWindowContext); ok {
				hwnd = uintptr(wctx.HWND)
			}
		})
	}
	return hwnd
}
