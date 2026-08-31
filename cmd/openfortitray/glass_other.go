//go:build !darwin && !windows && !linux

package main

// attachNativeGlass is a no-op on any platform without a specific
// implementation. The translucent theme background (internal/uitheme)
// still applies — just without a live native blur behind it.
func attachNativeGlass(nativeHandle uintptr) {}
