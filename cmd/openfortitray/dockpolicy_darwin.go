//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>

// setAccessoryPolicy flips the shared application to the Accessory activation
// policy: no Dock icon and no app menu, which is what a menu-bar (status item)
// app wants. fyne/glfw promotes the process to Regular when it initializes
// NSApp at Run(), overriding Info.plist LSUIElement=1, so this must run AFTER
// that (see the OnStarted call site). It is a no-op if NSApp has not been
// created yet. AppKit requires this on the main thread; the caller guarantees
// that (fyne's OnStarted fires on the UI/main goroutine).
static void setAccessoryPolicy(void) {
	if (NSApp == nil) {
		return;
	}
	[NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
}
*/
import "C"

// setAccessoryActivationPolicy hides the macOS Dock icon by setting the Cocoa
// activation policy to Accessory. It must be called after fyne/glfw has started
// (NSApp exists) and on the main/UI thread — fyne's Lifecycle OnStarted hook
// satisfies both.
func setAccessoryActivationPolicy() {
	C.setAccessoryPolicy()
}
