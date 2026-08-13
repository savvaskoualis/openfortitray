//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// redirectStderr points the process's standard-error handle at f. The Windows
// build links with -H=windowsgui, so it has no console and no valid stderr:
// a Go runtime panic or a cgo/OpenGL crash writes to fd 2 and is lost. Repointing
// STD_ERROR_HANDLE at the (already-open) log file captures that output in the log
// alongside the app's own lines. Best-effort — a failure here is not worth
// aborting startup over, and log.SetOutput has already been pointed at the file.
func redirectStderr(f *os.File) {
	_ = windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(f.Fd()))
}
