//go:build windows

package main

import (
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver"
)

// GetHWND extracts the Win32 HWND from a Fyne window.
// Retries after a short delay because Fyne may not have created
// the Win32 window yet immediately after Show().
func GetHWND(win fyne.Window) uintptr {
	nw, ok := win.(driver.NativeWindow)
	if !ok {
		return 0
	}
	for i := 0; i < 10; i++ {
		var hwnd uintptr
		nw.RunNative(func(ctx any) {
			if wctx, ok := ctx.(driver.WindowsWindowContext); ok {
				hwnd = uintptr(wctx.HWND)
			}
		})
		if hwnd != 0 {
			return hwnd
		}
		time.Sleep(50 * time.Millisecond)
	}
	return 0
}