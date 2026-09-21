//go:build !darwin

package tray

// runOnMainThread is a no-op passthrough outside darwin: neither
// energye/systray's windows nativeStart (spawns its own message-pump
// goroutine) nor its unix/linux nativeStart (D-Bus registration, no native
// GUI toolkit call) requires running on any particular OS thread. Only
// darwin's nativeStart calls AppKit APIs (NSStatusItem/NSWindow) directly,
// which assert they're on the real main thread -- see mainthread_darwin.go.
func runOnMainThread(fn func()) {
	fn()
}
