//go:build linux

package main

/*
#cgo LDFLAGS: -lX11
#include "glass_linux.h"
*/
import "C"
import (
	"log"

	qt "github.com/mappu/miqt/qt6"
)

// attachNativeGlass runs on the linux build. Qt can run on X11 or Wayland
// at runtime. We detect which one via QGuiApplication::platformName().
func attachNativeGlass(nativeHandle uintptr) {
	platformName := qt.QGuiApplication_PlatformName()
	switch platformName {
	case "xcb":
		// X11: nativeHandle is an X11 Window XID
		if nativeHandle == 0 {
			log.Print("glass: received zero X11 window, skipping")
			return
		}
		C.oft_attach_x11_blur(C.ulong(nativeHandle))
	case "wayland":
		// No-op: native Wayland compositor blur needs a separate protocol
		// (org_kde_kwin_blur) this app does not implement. The
		// translucent theme background (internal/uitheme) still applies —
		// just without live blur.
		log.Print("glass: running under Wayland, no native blur attached (theme translucency only)")
	default:
		log.Printf("glass: unknown platform: %s, skipping blur", platformName)
	}
}
