//go:build darwin

package main

/*
#cgo LDFLAGS: -framework CoreGraphics -framework CoreFoundation
#include <CoreGraphics/CoreGraphics.h>
#include <CoreFoundation/CoreFoundation.h>

// oft_user_present reports 1 when the main display is awake and the console
// session's screen is not locked. A Power Nap dark wake has the display
// asleep; a lid-open wake sits at the lock screen until the user logs back in.
static int oft_user_present(void) {
	if (CGDisplayIsAsleep(CGMainDisplayID())) {
		return 0;
	}
	CFDictionaryRef session = CGSessionCopyCurrentDictionary();
	if (session == NULL) {
		// No window-server session for this user (fast user switching to
		// another account): nobody we can show a browser to.
		return 0;
	}
	int present = 1;
	CFBooleanRef locked = CFDictionaryGetValue(session, CFSTR("CGSSessionScreenIsLocked"));
	if (locked != NULL && CFBooleanGetValue(locked)) {
		present = 0;
	}
	CFBooleanRef onConsole = CFDictionaryGetValue(session, kCGSessionOnConsoleKey);
	if (onConsole != NULL && !CFBooleanGetValue(onConsole)) {
		present = 0;
	}
	CFRelease(session);
	return present;
}
*/
import "C"

// userPresentNow reports whether someone is at the machine: main display
// awake, screen unlocked, and this user's session on the console.
func userPresentNow() bool { return C.oft_user_present() != 0 }

func userPresentFunc() func() bool { return userPresentNow }
