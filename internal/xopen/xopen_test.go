package xopen

import (
	"strings"
	"testing"
)

// The Windows file launcher must be Explorer, not `cmd /c start`.
//
// This is the regression guard for "View logs does nothing on Windows": the app
// runs elevated, and an elevated process cannot ShellExecute an MSIX-packaged
// handler such as Windows 11's Notepad, so `start` failed silently. Explorer hands
// the request to the unelevated desktop shell instead.
func TestWindowsFileUsesExplorer(t *testing.T) {
	name, args := fileArgv("windows", `C:\Users\me\AppData\Roaming\openfortitray\openfortitray.log`)
	if name != "explorer.exe" {
		t.Errorf("launcher = %q, want explorer.exe (start fails silently when elevated)", name)
	}
	if len(args) != 1 {
		t.Fatalf("args = %v, want exactly one", args)
	}
	// Explorer rejects a space after the comma, which is the easy way to break
	// this while it still looks correct.
	if !strings.HasPrefix(args[0], "/select,") || strings.HasPrefix(args[0], "/select, ") {
		t.Errorf("args[0] = %q, want a /select, prefix with no space", args[0])
	}
	if !strings.HasSuffix(args[0], `openfortitray.log`) {
		t.Errorf("args[0] = %q, want the path appended", args[0])
	}
}

// URLs keep the `start` launcher: a browser is an ordinary Win32 app that an
// elevated process can launch, and this is the path SAML sign-in depends on.
func TestWindowsURLKeepsStart(t *testing.T) {
	name, args := urlArgv("windows", "https://login.example.com/saml")
	if name != "cmd" {
		t.Errorf("launcher = %q, want cmd", name)
	}
	want := []string{"/c", "start", "", "https://login.example.com/saml"}
	if len(args) != len(want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args = %v, want %v", args, want)
		}
	}
	// The empty title argument is load-bearing: without it `start` treats a quoted
	// URL as the window title and opens nothing.
	if args[2] != "" {
		t.Error("start needs an empty title argument before the target")
	}
}

func TestDarwinAndLinuxLaunchers(t *testing.T) {
	for _, tc := range []struct {
		goos, want string
	}{{"darwin", "open"}, {"linux", "xdg-open"}, {"freebsd", "xdg-open"}} {
		if name, args := fileArgv(tc.goos, "/tmp/x.log"); name != tc.want || len(args) != 1 || args[0] != "/tmp/x.log" {
			t.Errorf("fileArgv(%s) = %q %v, want %q [/tmp/x.log]", tc.goos, name, args, tc.want)
		}
		if name, args := urlArgv(tc.goos, "https://x"); name != tc.want || len(args) != 1 || args[0] != "https://x" {
			t.Errorf("urlArgv(%s) = %q %v, want %q [https://x]", tc.goos, name, args, tc.want)
		}
	}
}
