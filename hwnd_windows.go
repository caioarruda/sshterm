//go:build windows

package main

import (
	"time"
	"unsafe"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver"
	"golang.org/x/sys/windows"
)

var (
	user32find      = windows.NewLazySystemDLL("user32.dll")
	procFindWindow  = user32find.NewProc("FindWindowW")
	procGetForegroundWindow = user32find.NewProc("GetForegroundWindow")
)

func findWindowByTitle(title string) uintptr {
	ptr, _ := windows.UTF16PtrFromString(title)
	hwnd, _, _ := procFindWindow.Call(0, uintptr(unsafe.Pointer(ptr)))
	return hwnd
}

// GetHWND extracts the Win32 HWND from a Fyne window.
// Tries Fyne NativeWindow first, falls back to FindWindowW by title.
func GetHWND(win fyne.Window) uintptr {
	nw, ok := win.(driver.NativeWindow)
	if !ok {
		return findWindowByTitle("SSH Terminal")
	}

	// Retry up to 500ms — Fyne creates the Win32 window asynchronously
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

	// Final fallback: find by window title
	return findWindowByTitle("SSH Terminal")
}