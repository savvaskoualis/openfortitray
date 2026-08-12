package autostart

import (
	"os"
	"path/filepath"
)

func desktopPath() string {
	base, _ := os.UserConfigDir()
	return filepath.Join(base, "autostart", "openfortitray.desktop")
}

// Enable writes the XDG autostart .desktop entry for exePath.
func Enable(exePath string) error {
	p := desktopPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(LinuxDesktop(exePath)), 0o644)
}

// Disable removes the XDG autostart .desktop entry.
func Disable() error {
	err := os.Remove(desktopPath())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// IsEnabled reports whether the .desktop entry exists.
func IsEnabled() bool {
	_, err := os.Stat(desktopPath())
	return err == nil
}
