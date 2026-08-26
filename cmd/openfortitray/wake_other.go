//go:build !darwin && !windows && !linux

package main

// watchSystemSleep is a no-op on platforms with no wired sleep/wake hook.
func watchSystemSleep(fn func()) {}
