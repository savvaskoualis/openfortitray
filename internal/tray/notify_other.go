//go:build !darwin

package tray

import "sync"

var (
	notifyClickMu sync.Mutex
	notifyClickFn func()
)

// postNative is unavailable off macOS; ShowMessage falls back to beeep.
func postNative(title, body string) bool { return false }

// InitNotifications is a no-op off macOS.
func InitNotifications() {}
