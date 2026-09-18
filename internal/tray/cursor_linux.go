//go:build linux

package tray

/*
#cgo LDFLAGS: -lX11
#include "cursor_linux.h"
*/
import "C"

// CursorPosition returns the mouse cursor's current position via X11's
// XQueryPointer, top-left-origin (X11's native convention, matching what
// Wails' runtime.WindowSetPosition expects on linux -- no conversion
// needed). ok is false on a pure-Wayland session (no X server reachable --
// see cursor_linux.c) or if the query fails for any other reason; the
// caller falls back to fixed-corner placement in that case.
func CursorPosition() (x, y int, ok bool) {
	var cx, cy C.int
	if C.oft_cursor_position(&cx, &cy) == 0 {
		return 0, 0, false
	}
	return int(cx), int(cy), true
}

// SetWindowPosition always returns false on Linux: unlike macOS/Windows,
// Wails' own runtime.WindowSetPosition already uses gtk_window_move, which
// moves a GTK window using absolute root-window coordinates -- the exact
// same reference frame XQueryPointer's root_x/root_y (what CursorPosition
// returns here) already uses, with no per-monitor relativity to correct
// for. So there is nothing to bypass on this platform; the caller falls
// back to wailsruntime.WindowSetPosition directly.
func SetWindowPosition(title string, x, y int) (ok bool) {
	return false
}
