//go:build darwin

package tray

/*
#include "mainthread_darwin.h"
*/
import "C"

import "runtime/cgo"

//export goMainThreadTrampoline
func goMainThreadTrampoline(handle C.ulong) {
	h := cgo.Handle(handle)
	fn := h.Value().(func())
	h.Delete()
	fn()
}

// runOnMainThread synchronously invokes fn on the process's real OS main
// thread via libdispatch, blocking the caller until it returns -- even when
// called from a goroutine that Go never scheduled onto that thread. Required
// before calling c.start(): energye/systray's darwin nativeStart creates the
// NSStatusItem/NSWindow directly (not through systray's own
// performSelectorOnMainThread-guarded helpers), and Wails' OnStartup hook
// runs on a goroutine its own frontend spawns (Frontend.Run.func1), not on
// the goroutine locked to OS thread 0 -- calling c.start() there directly
// hits AppKit's own thread assertion, "NSWindow should only be instantiated
// on the main thread!" (a real, shipped v0.3.0 crash; see git history).
func runOnMainThread(fn func()) {
	h := cgo.NewHandle(fn)
	C.oft_run_on_main_thread(C.ulong(h))
}
