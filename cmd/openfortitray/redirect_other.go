//go:build !windows

package main

import "os"

// redirectStderr is a no-op off Windows: macOS and Linux builds keep a working
// stderr (a terminal or the launchd/systemd journal), and os.Stderr has already
// been pointed at the log file by the caller.
func redirectStderr(f *os.File) {}
