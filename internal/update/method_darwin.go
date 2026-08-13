//go:build darwin

package update

import (
	"os"
	"path/filepath"
)

// caskPrefixes are the two Homebrew Caskroom roots: the Apple Silicon prefix
// (/opt/homebrew) and the Intel prefix (/usr/local).
var caskPrefixes = []string{
	"/opt/homebrew/Caskroom/openfortitray",
	"/usr/local/Caskroom/openfortitray",
}

// installMethod reports MethodHomebrew iff a Homebrew Caskroom receipt for this
// app exists under one of the real prefixes; otherwise MethodManual.
func installMethod() Method {
	if caskInstalled(caskPrefixes) {
		return MethodHomebrew
	}
	return MethodManual
}

// caskInstalled reports whether any of the given Caskroom receipt directories
// exists as a directory. It is a pure os.Stat filesystem check — no `brew` on
// PATH and no process execution required — so tests can point it at temp dirs.
func caskInstalled(prefixes []string) bool {
	for _, p := range prefixes {
		if p == "" {
			continue
		}
		fi, err := os.Stat(filepath.Clean(p))
		if err == nil && fi.IsDir() {
			return true
		}
	}
	return false
}
