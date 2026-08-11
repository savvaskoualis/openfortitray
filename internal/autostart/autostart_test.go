package autostart

import (
	"strings"
	"testing"
)

func TestDarwinPlist(t *testing.T) {
	p := DarwinPlist("/usr/local/bin/postern")
	for _, want := range []string{
		"<key>Label</key>", "io.github.savvaskoualis.postern",
		"<key>RunAtLoad</key>", "<true/>",
		"/usr/local/bin/postern",
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
	d := LinuxDesktop("/usr/local/bin/postern")
	for _, want := range []string{
		"[Desktop Entry]", "Type=Application", "Name=Postern",
		"Exec=/usr/local/bin/postern",
	} {
		if !strings.Contains(d, want) {
			t.Errorf(".desktop missing %q:\n%s", want, d)
		}
	}
}
