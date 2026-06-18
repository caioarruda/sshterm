//go:build !windows

package main

fyneterm "fyne.io/x/terminal"

func GetHWND(win fyne.Window) uintptr { return 0 }
