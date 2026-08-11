package autostart

import (
	"strings"
	"testing"
)

func TestDarwinPlist(t *testing.T) {
	p := DarwinPlist("/usr/local/bin/openfortitray")
	for _, want := range []string{
		"<key>Label</key>", "io.github.savvaskoualis.openfortitray",
		"<key>RunAtLoad</key>", "<true/>",
		"/usr/local/bin/openfortitray",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("plist missing %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "KeepAlive") {
		t.Error("plist must not contain KeepAlive (app supervises itself)")
	}
}

func TestLinuxDesktop(t *testing.T) {
	d := LinuxDesktop("/usr/local/bin/openfortitray")
	for _, want := range []string{
		"[Desktop Entry]", "Type=Application", "Name=OpenFortiTray",
		"Exec=/usr/local/bin/openfortitray",
	} {
		if !strings.Contains(d, want) {
			t.Errorf(".desktop missing %q:\n%s", want, d)
		}
	}
}
