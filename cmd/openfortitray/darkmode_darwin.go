//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#include "darkmode_darwin.h"
*/
import "C"

// isDarkMode reports whether the user's current macOS appearance is Dark.
// miqt v0.14.0's QStyleHints has no ColorScheme accessor (verified against
// gen_qstylehints.go), so this reads the same NSUserDefaults key AppKit
// itself derives Dark Mode from, rather than asking Qt.
func isDarkMode() bool {
	return C.oft_is_dark_mode() != 0
}
