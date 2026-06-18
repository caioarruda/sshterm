//go:build windows

package main

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	shell32        = windows.NewLazySystemDLL("shell32.dll")
	comctl32       = windows.NewLazySystemDLL("comctl32.dll")
	user32drop     = windows.NewLazySystemDLL("user32.dll")
	procDragAccept = shell32.NewProc("DragAcceptFiles")
	procDragQuery  = shell32.NewProc("DragQueryFileW")
	procDragFinish = shell32.NewProc("DragFinish")
	procSetSubclass = comctl32.NewProc("SetWindowSubclass")
	procDefSubclass = comctl32.NewProc("DefSubclassProc")
	procChangeWindowMessageFilterEx = user32drop.NewProc("ChangeWindowMessageFilterEx")
)

const (
	wmDropFiles         = 0x0233
	msgfltAllow         = 1
	wmCopyData          = 0x004A
	wmCopyglobaldata    = 0x0049
)

var gApp *App

func subclassProc(hwnd, msg, wParam, lParam, _, _ uintptr) uintptr {
	if msg == wmDropFiles {
		hDrop := wParam
		count, _, _ := procDragQuery.Call(hDrop, 0xFFFFFFFF, 0, 0)
		buf := make([]uint16, 32768)
		for i := uintptr(0); i < count; i++ {
			procDragQuery.Call(hDrop, i, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
			path := windows.UTF16ToString(buf)
			if path != "" {
				p := path
				go gApp.uploadFile(p)
			}
		}
		procDragFinish.Call(hDrop)
		return 0
	}
	ret, _, _ := procDefSubclass.Call(hwnd, msg, wParam, lParam)
	return ret
}

func RegisterDropTarget(hwnd uintptr, a *App) {
	gApp = a

	// Allow WM_DROPFILES through UIPI message filter (needed when running
	// at different integrity levels, e.g. explorer vs app)
	procChangeWindowMessageFilterEx.Call(hwnd, wmDropFiles, msgfltAllow, 0)
	procChangeWindowMessageFilterEx.Call(hwnd, wmCopyData, msgfltAllow, 0)
	procChangeWindowMessageFilterEx.Call(hwnd, wmCopyglobaldata, msgfltAllow, 0)

	// Enable drag-and-drop on this window
	procDragAccept.Call(hwnd, 1)

	// Subclass the window to intercept WM_DROPFILES
	cb := syscall.NewCallback(subclassProc)
	procSetSubclass.Call(hwnd, cb, 1, 0)
}
