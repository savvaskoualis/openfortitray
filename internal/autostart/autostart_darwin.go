package autostart

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// bundleExec is the menu-bar app executable installed by scripts/install.sh.
// The LaunchAgent must point here, not at a bare /usr/local/bin binary: launchd
// then starts the executable inside the .app, so macOS reads the enclosing
// Info.plist (LSUIElement=1) and the login-launched process behaves as the
// agent — no Dock icon, reliable status item — exactly as a manual launch does.
const bundleExec = "/Applications/OpenFortiTray.app/Contents/MacOS/openfortitray"

func plistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", "io.github.savvaskoualis.openfortitray.plist")
}

// bundleExecPath is the executable to record in the LaunchAgent. When the app is
// already running from inside a .app bundle we honour that exact path (a dev
// build under dist/, or an install to a non-default location); otherwise we
// record the canonical /Applications bundle, because a bare-binary path would
// launch the tray without its Info.plist and macOS would treat it as a Dock app.
func bundleExecPath(exePath string) string {
	if strings.Contains(exePath, ".app/Contents/MacOS/") {
		return exePath
	}
	return bundleExec
}

// Enable writes the LaunchAgent plist for exePath and loads it best-effort.
func Enable(exePath string) error {
	p := plistPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(DarwinPlist(bundleExecPath(exePath))), 0o644); err != nil {
		return err
	}
	// Best-effort load; "already bootstrapped" is fine.
	_ = exec.Command("launchctl", "bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), p).Run()
	return nil
}

// Disable unloads the LaunchAgent best-effort and removes the plist.
func Disable() error {
	_ = exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/io.github.savvaskoualis.openfortitray", os.Getuid())).Run()
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
