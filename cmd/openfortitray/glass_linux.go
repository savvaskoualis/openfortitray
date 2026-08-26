//go:build linux

package main

/*
#cgo LDFLAGS: -lX11
#include "glass_linux.h"
*/
import "C"
import (
	"log"

	"fyne.io/fyne/v2/driver"
)

// attachNativeGlass runs on the linux build. Fyne's default (untagged)
// Linux build picks X11 or Wayland at RUNTIME, so RunNative can hand
// back either context on the same binary — see the design doc's "Linux"
// section for why native Wayland blur (a separate protocol,
// org_kde_kwin_blur) is out of scope here.
func attachNativeGlass(ctx any) {
	switch c := ctx.(type) {
	case driver.X11WindowContext:
		if c.WindowHandle == 0 {
			log.Print("glass: RunNative returned a zero X11 window, skipping")
			return
		}
		C.oft_attach_x11_blur(C.ulong(c.WindowHandle))
	case driver.WaylandWindowContext:
		// No-op: native Wayland compositor blur needs a separate protocol
		// (org_kde_kwin_blur) this app does not implement. The
		// translucent theme background (internal/uitheme) still applies —
		// just without live blur.
		log.Print("glass: running under Wayland, no native blur attached (theme translucency only)")
	default:
		log.Printf("glass: unexpected native context on linux: %T", ctx)
	}
}
