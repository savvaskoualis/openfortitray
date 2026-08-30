//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#include "glass_darwin.h"
*/
import "C"

func attachNativeGlass(nativeHandle uintptr) {
	C.oft_attach_glass(C.uintptr_t(nativeHandle))
}
