// Package xopen opens files with the OS default application.
package xopen

import (
	"os/exec"
	"runtime"
)

// File opens path with the OS default handler.
func File(path string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", path).Start()
	case "windows":
		return exec.Command("cmd", "/c", "start", "", path).Start()
	default:
		return exec.Command("xdg-open", path).Start()
	}
}

// URL opens rawURL in the OS default browser. The same launchers File uses
// (open / start / xdg-open) all accept a URL as readily as a path.
func URL(rawURL string) error {
	return File(rawURL)
}
