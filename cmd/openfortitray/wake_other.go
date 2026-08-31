//go:build !darwin && !windows && !linux

package main

// watchSystemSleep is a no-op on platforms with no wired sleep/wake hook.
func watchSystemSleep(fn func()) {}

// watchScreenWake is a no-op on platforms with no wired display-wake hook —
// the display-sleep-without-full-system-sleep gap this exists to cover was
// diagnosed on macOS specifically; see wake_darwin.go's doc comment.
func watchScreenWake(fn func()) {}
