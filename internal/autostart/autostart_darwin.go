package autostart

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func plistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", "io.github.savvaskoualis.postern.plist")
}

// Enable writes the LaunchAgent plist for exePath and loads it best-effort.
func Enable(exePath string) error {
	p := plistPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(DarwinPlist(exePath)), 0o644); err != nil {
		return err
	}
	// Best-effort load; "already bootstrapped" is fine.
	_ = exec.Command("launchctl", "bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), p).Run()
	return nil
}

// Disable unloads the LaunchAgent best-effort and removes the plist.
func Disable() error {
	_ = exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/io.github.savvaskoualis.postern", os.Getuid())).Run()
	err := os.Remove(plistPath())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// IsEnabled reports whether the LaunchAgent plist exists.
func IsEnabled() bool {
	_, err := os.Stat(plistPath())
	return err == nil
}
