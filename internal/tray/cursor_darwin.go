//go:build darwin

package tray

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#include "cursor_darwin.h"
*/
import "C"

// CursorPosition returns the mouse cursor's current location as (x, y)
// pixels from the top-left corner of the primary (menu-bar) screen -- the
// coordinate convention Wails' runtime.WindowSetPosition expects on this
// platform (see cursor_darwin.m for the exact conversion and why no further
// flip is needed at the call site). ok is false only if AppKit reports zero
// screens, which should not happen on a real desktop session.
func CursorPosition() (x, y int, ok bool) {
	var cx, cy C.double
	if C.oft_cursor_position(&cx, &cy) == 0 {
		return 0, 0, false
	}
	return int(cx), int(cy), true
}
