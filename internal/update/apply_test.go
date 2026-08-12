package update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildBrewScript(t *testing.T) {
	s, err := buildBrewScript(4321, "/opt/homebrew/bin/brew")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s, "kill -0 4321 ") {
		t.Errorf("pid not a literal int in wait loop: %q", s)
	}
	if !strings.Contains(s, "'/opt/homebrew/bin/brew' upgrade --cask openfortitray") {
		t.Errorf("brew upgrade line wrong: %q", s)
	}
	if !strings.Contains(s, "open -a OpenFortiTray") {
		t.Errorf("relaunch line missing: %q", s)
	}
}

func TestBuildBrewScriptRejectsQuoteInBrewPath(t *testing.T) {
	if _, err := buildBrewScript(1, "/x/'; rm -rf ~; '/brew"); err == nil {
		t.Fatal("expected rejection of a brew path containing a single quote")
	}
}

func TestValidateInstallerPath(t *testing.T) {
	dir := t.TempDir() // created under os.TempDir()
	good := filepath.Join(dir, "OpenFortiTray-Setup.exe")
	if err := os.WriteFile(good, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateInstallerPath(good); err != nil {
		t.Errorf("valid temp installer path rejected: %v", err)
	}
	cases := map[string]string{
		"relative":          "Setup.exe",
		"single quote":      filepath.Join(dir, "a'b.exe"),
		"double quote":      filepath.Join(dir, "a\"b.exe"),
		"outside temp":      "/etc/passwd",
		"missing under tmp": filepath.Join(dir, "does-not-exist.exe"),
	}
	for name, p := range cases {
		if err := validateInstallerPath(p); err == nil {
			t.Errorf("%s: expected rejection, got nil for %q", name, p)
		}
	}
}

func TestBuildWindowsScript(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "Setup.exe")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := buildWindowsScript(777, p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s, "Wait-Process -Id 777 ") {
		t.Errorf("pid not a literal int: %q", s)
	}
	if !strings.Contains(s, "'"+p+"'") {
		t.Errorf("installer path not single-quoted: %q", s)
	}
	if !strings.Contains(s, "/VERYSILENT") || !strings.Contains(s, "/NORESTART") {
		t.Errorf("silent install flags missing: %q", s)
	}
	if !strings.Contains(s, "schtasks /Run /TN OpenFortiTray") {
		t.Errorf("relaunch line missing: %q", s)
	}
}

func TestBuildWindowsScriptRejectsBadPath(t *testing.T) {
	if _, err := buildWindowsScript(1, "Setup.exe"); err == nil {
		t.Fatal("expected rejection of a non-absolute installer path")
	}
}

func TestApplyManualUnsupported(t *testing.T) {
	if err := Apply(MethodManual, "", 1); err == nil {
		t.Fatal("expected an error applying MethodManual")
	}
}
