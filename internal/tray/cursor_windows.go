//go:build windows

package tray

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32           = windows.NewLazySystemDLL("user32.dll")
	procGetCursorPos = user32.NewProc("GetCursorPos")
)

// winPoint mirrors the Win32 POINT struct (LONG x, y) -- the shape
// GetCursorPos writes into.
type winPoint struct {
	X, Y int32
}

// CursorPosition returns the mouse cursor's current screen position in
// pixels, top-left-origin -- the same convention Wails'
// runtime.WindowSetPosition uses on Windows, so no conversion is needed
// here (unlike darwin, which is natively bottom-left-origin). ok is false
// only if the underlying Win32 call fails; GetCursorPos is documented to
// fail only in exceptional circumstances (e.g. no desktop), which we treat
// as "no cursor position available" rather than a fatal error.
func CursorPosition() (x, y int, ok bool) {
	var pt winPoint
	ret, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	if ret == 0 {
		return 0, 0, false
	}
	return int(pt.X), int(pt.Y), true
}
