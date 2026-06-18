//go:build !windows

package main

// RegisterDropTarget is a no-op on non-Windows platforms.
// Fyne's DroppedFiles interface handles DnD on Linux/macOS.
func RegisterDropTarget(hwnd uintptr, a *App) {}
