//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#include "dock_darwin.h"
*/
import "C"

// onDockActivate is what a Dock click (or any app activation) runs. Set by
// watchDockActivation before the observer is installed; nil means "do nothing",
// so an activation before wiring cannot panic.
var onDockActivate func()

// oftDockActivated is called from Objective-C on the main queue every time the
// app becomes active. It is deliberately thin: the decision about what to show
// lives in the app, not here.
//
//export oftDockActivated
func oftDockActivated() {
	if onDockActivate != nil {
		onDockActivate()
	}
}

// setDockActivationPolicy gives the app a macOS Dock icon by setting the Cocoa
// activation policy to Regular.
//
// It has to be asserted at runtime rather than left to Info.plist because
// Qt's own Cocoa platform integration sets its own policy while initializing
// NSApp — constructed synchronously inside the QApplication constructor (see
// main.go's newQApplication call), unlike the old fyne/glfw build, which
// deferred that to Run() behind an OnStarted-style lifecycle hook. This must
// therefore run after the QApplication is constructed and on the main/UI
// thread; main() calls it directly, right after that constructor returns —
// no lifecycle callback needed today.
func setDockActivationPolicy() {
	C.oft_set_regular_policy()
}

// watchDockActivation makes fn run whenever the app is activated, which is the
// only way to give a Dock icon any effect: Qt does not implement the reopen
// delegate method either, so without this the icon is inert.
func watchDockActivation(fn func()) {
	onDockActivate = fn
	C.oft_watch_activation()
}
