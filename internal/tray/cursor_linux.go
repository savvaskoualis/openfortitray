//go:build linux

package tray

/*
#cgo LDFLAGS: -lX11
#include <stdlib.h>
#include "cursor_linux.h"
*/
import "C"

import "unsafe"

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

// CursorScreenFrame is not implemented on Linux yet -- ok is always false,
// so positionWindow falls back to its existing primary-screen-sized clamp
// (see cmd/openfortitray/position.go). The multi-monitor fix (clamping
// against the screen the cursor is actually on, not always primary) is
// darwin-only for now.
func CursorScreenFrame() (originX, originY, w, h int, ok bool) {
	return 0, 0, 0, 0, false
}

// SetWindowPosition moves the top-level window whose title matches title
// to an absolute root-window position (x, y) -- the same coordinate space
// CursorPosition already returns -- via the EWMH _NET_MOVERESIZE_WINDOW
// client message (see cursor_linux.c). This deliberately bypasses Wails'
// own runtime.WindowSetPosition: Wails' Linux SetPosition (window.c) adds
// the offset of whichever monitor the window is CURRENTLY on
// (gdk_display_get_monitor_at_window) before calling gtk_window_move --
// the same "relative to current screen/monitor, not absolute" bug class
// this whole feature exists to work around on macOS/Windows, not the
// already-absolute behavior an earlier pass through this code assumed.
// ok is false if no X server is reachable, no window with that title was
// found, or the window manager doesn't support the EWMH properties this
// depends on (_NET_CLIENT_LIST, _NET_MOVERESIZE_WINDOW) — in any of those
// cases the caller falls back to wailsruntime.WindowSetPosition, which is
// still correct on a single-monitor Linux session (the case this bypass
// doesn't change).
func SetWindowPosition(title string, x, y int) (ok bool) {
	ctitle := C.CString(title)
	defer C.free(unsafe.Pointer(ctitle))
	return C.oft_set_window_position(ctitle, C.int(x), C.int(y)) != 0
}
