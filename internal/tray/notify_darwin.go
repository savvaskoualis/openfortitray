//go:build darwin

package tray

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework UserNotifications
#include <stdlib.h>
#include "notify_darwin.h"
*/
import "C"

import (
	"sync"
	"unsafe"
)

var (
	notifyOnce    sync.Once
	notifyNative  bool
	notifyClickMu sync.Mutex
	notifyClickFn func()
)

// initNativeNotify sets up UNUserNotificationCenter once. The first call
// triggers macOS's one-time "allow notifications" prompt.
func initNativeNotify() {
	notifyOnce.Do(func() { notifyNative = C.oft_notify_init() != 0 })
}

// postNative posts through Notification Center as this app — so the banner
// carries the app's icon and a click comes back to us (see
// OnNotificationClick). Reports false when unavailable (no bundle identifier,
// or the user denied permission), for the caller to fall back to beeep.
func postNative(title, body string) bool {
	initNativeNotify()
	if !notifyNative {
		return false
	}
	ct, cb := C.CString(title), C.CString(body)
	defer C.free(unsafe.Pointer(ct))
	defer C.free(unsafe.Pointer(cb))
	return C.oft_notify_post(ct, cb) != 0
}

//export oftNotificationClicked
func oftNotificationClicked() {
	notifyClickMu.Lock()
	fn := notifyClickFn
	notifyClickMu.Unlock()
	if fn != nil {
		// Runs on the main queue (the delegate's default), the same thread the
		// Dock-activation handler shows the window from.
		fn()
	}
}

// InitNotifications sets up native notifications early (at launch) so the
// permission prompt and delegate are in place before the first banner, and
// a click on a banner delivered while the app was not running still lands.
func InitNotifications() { initNativeNotify() }
