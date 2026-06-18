//go:build windows

package main

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver"
)

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
