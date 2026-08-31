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

// onScreenWakeFn is what a display wake runs. Set by watchScreenWake before
// the observer is installed; nil means "do nothing", so a wake before wiring
// cannot panic.
var onScreenWakeFn func()

// oftScreenWoke is called from Objective-C on the main queue every time the
// display wakes — a distinct, much more frequent event than a full system
// wake (see watchScreenWake).
//
//export oftScreenWoke
func oftScreenWoke() {
	if onScreenWakeFn != nil {
		onScreenWakeFn()
	}
}

// watchScreenWake makes fn run whenever the display wakes. This exists
// alongside watchSystemSleep, not instead of it: on a Mac whose power
// settings, AC power, or an active caffeinate/other assertion keep it from
// ever fully suspending, the display still sleeps and wakes on its own —
// often many times a day — and NSWorkspaceDidWakeNotification never fires at
// all for that cycle. Diagnosed live: this machine logged zero full
// system sleeps in 7 days, yet the tray icon still periodically lost its
// NSStatusItem, correlated in the system log with a display sleep/wake cycle
// and a "Kernel Client Acks: openfortitray timed out" power-notification
// entry (almost certainly GLFW's own OpenGL power-management registration,
// not something this app opts into directly). Re-asserting the tray on every
// display wake covers that gap; it does not force a VPN reconnect the way
// watchSystemSleep's callback does, since a display sleep alone does not
// drop the network.
func watchScreenWake(fn func()) {
	onScreenWakeFn = fn
	C.oft_watch_screen_wake()
}
