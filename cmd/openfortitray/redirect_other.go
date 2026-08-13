//go:build !windows && !darwin && !linux

package main

import "os"

// redirectStderr is a no-op on the platforms with neither a Windows console
// handle nor a POSIX dup2 (see redirect_windows.go and redirect_unix.go).
// os.Stderr has already been pointed at the log file by the caller, so Go's own
// writes are captured either way.
func redirectStderr(f *os.File) {}
