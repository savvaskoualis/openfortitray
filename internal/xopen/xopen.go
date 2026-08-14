// Package xopen opens files and URLs with the OS default application.
package xopen

import (
	"os/exec"
	"runtime"
)

// File opens path with the OS default handler.
func File(path string) error {
	name, args := fileArgv(runtime.GOOS, path)
	cmd := exec.Command(name, args...)
	hideConsole(cmd)
	return cmd.Start()
}

// URL opens rawURL in the OS default browser.
func URL(rawURL string) error {
	name, args := urlArgv(runtime.GOOS, rawURL)
	cmd := exec.Command(name, args...)
	hideConsole(cmd)
	return cmd.Start()
}

// fileArgv builds the command that opens a FILE. It is separate from urlArgv
// because Windows needs a different launcher for the two, and it is a pure
// function so the argv can be table-tested on any host.
//
// WINDOWS: this uses Explorer, not `cmd /c start`, and that is the fix for "View
// logs does nothing".
//
// The app ships a requireAdministrator manifest, so it runs elevated. `start`
// resolves the file's association through ShellExecute in the CALLING process —
// and an elevated process cannot launch an MSIX-packaged app, which is exactly
// what Notepad is on Windows 11. The call failed silently: no window, no error the
// user could see, because Start() only reports whether cmd.exe itself launched.
// A file type with no association at all fails the same way.
//
// explorer.exe sidesteps both. Explorer refuses to run elevated: it hands the
// request to the already-running desktop shell, which opens the file at the
// user's normal integrity level, where MSIX handlers work. `/select,` shows the
// containing folder with the file highlighted rather than opening it directly —
// one extra click, in exchange for always producing something visible instead of
// sometimes producing nothing. There is no space after the comma; Explorer does
// not accept one.
//
// This is worth revisiting once the Windows privilege split lands and the GUI
// stops running elevated (see docs/superpowers/specs/…-windows-privilege-split-…):
// at that point plain ShellExecute would work again.
func fileArgv(goos, path string) (string, []string) {
	switch goos {
	case "darwin":
		return "open", []string{path}
	case "windows":
		return "explorer.exe", []string{"/select," + path}
	default:
		return "xdg-open", []string{path}
	}
}

// urlArgv builds the command that opens a URL.
//
// Windows keeps `cmd /c start` here rather than Explorer: a URL's handler is an
// ordinary Win32 browser, which an elevated process can launch, and this is the
// path the SAML sign-in depends on — it works today and is not worth destabilising
// for a problem it does not have. The empty "" is the window TITLE argument, which
// `start` requires before a quoted target or it treats the target as the title.
//
// It does mean the browser inherits the elevated token, which is its own (smaller)
// problem and another thing the privilege split fixes.
func urlArgv(goos, rawURL string) (string, []string) {
	switch goos {
	case "darwin":
		return "open", []string{rawURL}
	case "windows":
		return "cmd", []string{"/c", "start", "", rawURL}
	default:
		return "xdg-open", []string{rawURL}
	}
}
