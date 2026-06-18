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
	user32find     = windows.NewLazySystemDLL("user32.dll")
	procFindWindow = user32find.NewProc("FindWindowW")
)

func findWindowByTitle(title string) uintptr {
	ptr, _ := windows.UTF16PtrFromString(title)
	hwnd, _, _ := procFindWindow.Call(0, uintptr(unsafe.Pointer(ptr)))
	return hwnd
}

// GetHWND extracts the Win32 HWND and immediately registers drop target
// from within RunNative, ensuring we're on the correct Win32 thread.
func GetHWND(win fyne.Window) uintptr {
	nw, ok := win.(driver.NativeWindow)
	if !ok {
		return findWindowByTitle("SSH Terminal")
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
	return findWindowByTitle("SSH Terminal")
}

// RegisterDropTargetOnThread registers DnD on the Win32 thread that owns the window.
// Must be called after win.Show().
func RegisterDropTargetOnThread(win fyne.Window, a *App) {
	nw, ok := win.(driver.NativeWindow)
	if !ok {
		if hwnd := findWindowByTitle("SSH Terminal"); hwnd != 0 {
			RegisterDropTarget(hwnd, a)
		}
		return
	}
	// RunNative executes on the OS thread that owns the window
	nw.RunNative(func(ctx any) {
		if wctx, ok := ctx.(driver.WindowsWindowContext); ok {
			RegisterDropTarget(uintptr(wctx.HWND), a)
		}
	})
}