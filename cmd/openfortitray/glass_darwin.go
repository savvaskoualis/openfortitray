//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#include "glass_darwin.h"
*/
import "C"
import (
	"log"

	"fyne.io/fyne/v2/driver"
)

// attachNativeGlass runs on the darwin build. It only acts on
// driver.MacWindowContext; any other context type (should not happen on
// this platform, but RunNative's contract allows it) is a no-op.
func attachNativeGlass(ctx any) {
	mc, ok := ctx.(driver.MacWindowContext)
	if !ok {
		log.Printf("glass: unexpected native context on darwin: %T", ctx)
		return
	}
	if mc.NSWindow == 0 {
		log.Print("glass: RunNative returned a zero NSWindow, skipping")
		return
	}
	C.oft_attach_glass(C.uintptr_t(mc.NSWindow))
}
