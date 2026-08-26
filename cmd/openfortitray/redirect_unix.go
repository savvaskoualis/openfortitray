//go:build darwin || linux

package main

import (
	"log"
	"os"

	"golang.org/x/sys/unix"
)

// redirectStderr points file descriptor 2 at f.
//
// Assigning os.Stderr = f (which the caller already does) only redirects Go's own
// writes. Anything writing to fd 2 directly still goes wherever fd 2 pointed when
// the process started — for a GUI launch from a .app bundle, nowhere useful.
//
// KNOWN LIMITATION on macOS: this does not survive. The dup2 succeeds — it logs
// that it did — but a bundled .app launched through LaunchServices has fd 2 wired
// to the system-log socket, and Cocoa re-establishes that during fyne's driver
// init, which runs after this. Verified with lsof against the live process: fd 2
// is back to `unix ->0x…` while the log file sits on fd 3.
//
// So NSLog output from fyne — including the notification-authorization failure this
// was written to capture — still does NOT reach this file on macOS. The redirect is
// kept because it is free, it works on Linux, and it captures anything written to
// fd 2 before the driver starts (an early Go panic, a cgo failure during config
// load). It is NOT a basis for concluding that native code logged nothing.
//
// The target is the LITERAL descriptor 2, not os.Stderr.Fd(): the caller sets
// os.Stderr = f immediately before calling this, so os.Stderr.Fd() is already the
// log file's own descriptor and dup2-ing it onto itself is a silent no-op. That
// exact mistake shipped for one build and was caught only by checking fd 2 with
// lsof against the running process — which is the reminder that a redirect is not
// verified until something written to fd 2 turns up in the file.
//
// Best-effort: a failure here is not worth aborting startup over, and log.SetOutput
// has already been pointed at the file.
func redirectStderr(f *os.File) {
	// The error is logged rather than discarded. A silently-failing redirect is
	// worse than no redirect: it makes the log look authoritative about a channel
	// it is not actually capturing, which is exactly how "fyne reported no
	// authorization error" became a conclusion it had not earned.
	if err := unix.Dup2(int(f.Fd()), unix.Stderr); err != nil {
		log.Printf("stderr: could not repoint fd 2 at the log (%v); native (NSLog/GL) output stays outside this file", err)
		return
	}
	log.Print("stderr: fd 2 now points at this log")
}
