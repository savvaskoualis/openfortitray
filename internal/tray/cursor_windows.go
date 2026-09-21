//go:build windows

package tray

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32           = windows.NewLazySystemDLL("user32.dll")
	procGetCursorPos = user32.NewProc("GetCursorPos")
	procFindWindowW  = user32.NewProc("FindWindowW")
	procSetWindowPos = user32.NewProc("SetWindowPos")
)

// SWP_NOSIZE/SWP_NOZORDER/SWP_NOACTIVATE (winuser.h): move only, keep the
// window's current size, don't reorder it in the z-stack, and don't steal
// focus (a background reposition, not a user-initiated activation).
const (
	swpNoSize     = 0x0001
	swpNoZOrder   = 0x0004
	swpNoActivate = 0x0010
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

// CursorScreenFrame is not implemented on Windows yet -- ok is always false,
// so positionWindow falls back to its existing primary-screen-sized clamp
// (see cmd/openfortitray/position.go). This means the multi-monitor fix
// (clamping against the screen the cursor is actually on, not always
// primary) is darwin-only for now; unlike SetWindowPosition, a Windows
// MonitorFromPoint/GetMonitorInfo equivalent has not been added here.
func CursorScreenFrame() (originX, originY, w, h int, ok bool) {
	return 0, 0, 0, 0, false
}

// SetWindowPosition moves the top-level window whose exact title matches
// title (found via FindWindowW, no class-name filter) to an absolute
// virtual-screen position (x, y) -- the same coordinate space
// GetCursorPos/CursorPosition already returns. This deliberately bypasses
// Wails' own runtime.WindowSetPosition, which is relative to whichever
// monitor's work area the window CURRENTLY occupies (via
// MonitorFromWindow) rather than the virtual-screen origin, so it silently
// mis-targets a cursor-anchored popover once the window's current monitor
// isn't the primary one or has a non-(0,0) work-area origin (e.g. the
// taskbar docked left/top, or any secondary monitor). ok is false if no
// window with that title exists or the underlying Win32 call fails.
func SetWindowPosition(title string, x, y int) (ok bool) {
	titlePtr, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return false
	}
	hwnd, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(titlePtr)))
	if hwnd == 0 {
		return false
	}
	ret, _, _ := procSetWindowPos.Call(
		hwnd, 0,
		uintptr(x), uintptr(y),
		0, 0,
		uintptr(swpNoSize|swpNoZOrder|swpNoActivate),
	)
	return ret != 0
}
