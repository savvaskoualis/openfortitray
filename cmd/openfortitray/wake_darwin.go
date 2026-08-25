//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#include "wake_darwin.h"
*/
import "C"

// onSystemWakeFn is what a system wake runs. Set by watchSystemSleep before the
// observer is installed; nil means "do nothing", so a wake before wiring cannot
// panic. Mirrors onDockActivate in dockpolicy_darwin.go.
var onSystemWakeFn func()

// oftSystemWoke is called from Objective-C on the main queue every time the
// system resumes from sleep.
//
//export oftSystemWoke
func oftSystemWoke() {
	if onSystemWakeFn != nil {
		onSystemWakeFn()
	}
}

// watchSystemSleep makes fn run whenever the system resumes from sleep.
func watchSystemSleep(fn func()) {
	onSystemWakeFn = fn
	C.oft_watch_wake()
}
