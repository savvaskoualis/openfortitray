//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#include "darkmode_darwin.h"
*/
import "C"

// isDarkMode reports whether the user's current macOS appearance is Dark.
// This reads the same NSUserDefaults key AppKit itself derives Dark Mode
// from directly via cgo, rather than going through Wails/WebView2 (whose
// webview has no cross-platform "OS is in dark mode" query) — a leftover of
// the same approach used in the Qt era (miqt's QStyleHints had no
// ColorScheme accessor either), kept because it is still the simplest
// correct source of truth on macOS.
func isDarkMode() bool {
	return C.oft_is_dark_mode() != 0
}
