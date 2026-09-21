//go:build darwin

package tray

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#include <stdlib.h>
#include "cursor_darwin.h"
*/
import "C"

import "unsafe"

// CursorPosition returns the mouse cursor's current location as (x, y)
// pixels from the top-left corner of the primary (menu-bar) screen. ok is
// false only if AppKit reports zero screens, which should not happen on a
// real desktop session.
func CursorPosition() (x, y int, ok bool) {
	var cx, cy C.double
	if C.oft_cursor_position(&cx, &cy) == 0 {
		return 0, 0, false
	}
	return int(cx), int(cy), true
}

// CursorScreenFrame returns the frame of whichever screen currently contains
// the cursor -- top-left origin (originX, originY) and size (w, h) -- in the
// same primary-relative, top-left-origin space CursorPosition reports in.
// On a multi-monitor Mac, a screen to the left of or above primary reports a
// negative origin here; that's correct and the caller must not clamp it
// away. ok is false only if AppKit reports zero screens.
func CursorScreenFrame() (originX, originY, w, h int, ok bool) {
	var ox, oy, cw, ch C.double
	if C.oft_cursor_screen_frame(&ox, &oy, &cw, &ch) == 0 {
		return 0, 0, 0, 0, false
	}
	return int(ox), int(oy), int(cw), int(ch), true
}

// SetWindowPosition moves the NSWindow whose title matches title so its
// top-left corner sits at (x, y) -- pixels from the top-left of the
// primary screen, the same frame CursorPosition reports in -- by setting
// the window's frame directly in AppKit's own absolute coordinate space.
// This deliberately bypasses Wails' own runtime.WindowSetPosition, which
// is relative to whichever screen the window CURRENTLY occupies rather
// than the primary screen (see cursor_darwin.h for why that's the wrong
// reference frame for a cursor-anchored popover on a real multi-monitor
// Mac). ok is false if no window with that title exists.
//
// Like every other AppKit call in this codebase (setDockActivationPolicy,
// watchDockActivation's activation callback, isDarkMode), this assumes
// it's called on the main thread -- required for [NSApp windows] and
// setFrameOrigin: to behave correctly, not just for window allocation.
// The one production call site (cmd/openfortitray's positionWindow, from
// tray.Setup's onIconClick callback) relies on energye/systray's click
// delivery being genuine Cocoa event dispatch, which is main-thread by
// construction -- the same assumption this project already makes for
// every other click/menu callback into Cocoa-touching code, none of which
// add their own dispatch_async wrapping either. Verified live for
// CursorPosition (a read-only NSEvent/NSScreen query, safe from any
// thread) but NOT independently verified for this function's window
// mutation specifically -- a go test binary cannot allocate a real
// NSWindow off the actual Cocoa main thread to check against (confirmed:
// attempting to do so crashes with AppKit's own "NSWindow should only be
// instantiated on the main thread" assertion, a test-environment
// limitation distinct from whether setFrameOrigin: itself needs the main
// thread for an already-existing window).
func SetWindowPosition(title string, x, y int) (ok bool) {
	ctitle := C.CString(title)
	defer C.free(unsafe.Pointer(ctitle))
	return C.oft_set_window_position(ctitle, C.double(x), C.double(y)) != 0
}
