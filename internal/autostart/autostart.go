// Package autostart registers the app as a per-user login item.
package autostart

import "fmt"

// DarwinPlist renders the launchd LaunchAgent plist for exePath.
func DarwinPlist(exePath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.hyperio.vpn</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
</dict>
</plist>
`, exePath)
}

// LinuxDesktop renders the XDG autostart .desktop entry for exePath.
func LinuxDesktop(exePath string) string {
	return fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=Hyperio VPN
Exec=%s
X-GNOME-Autostart-enabled=true
`, exePath)
}
