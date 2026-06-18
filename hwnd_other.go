//go:build !windows

package main

import "fyne.io/fyne/v2"

func GetHWND(win fyne.Window) uintptr { return 0 }
