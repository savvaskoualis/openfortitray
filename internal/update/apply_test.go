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
	if !strings.Contains(s, "'/opt/homebrew/bin/brew' update\n") {
		t.Errorf("brew update (tap refresh) line missing: %q", s)
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
	clean, err := validateInstallerPath(good)
	if err != nil {
		t.Errorf("valid temp installer path rejected: %v", err)
	}
	if clean != good {
		t.Errorf("clean path = %q, want %q", clean, good)
	}
	cases := map[string]string{
		"relative":            "Setup.exe",
		"single quote":        filepath.Join(dir, "a'b.exe"),
		"double quote":        filepath.Join(dir, "a\"b.exe"),
		"backtick":            filepath.Join(dir, "a`b.exe"),
		"outside temp":        "/etc/passwd",
		"missing under tmp":   filepath.Join(dir, "does-not-exist.exe"),
		"dotdot traversal":    filepath.Join(dir, "..", "..", "..", "etc", "passwd"),
		"raw dotdot escape":   dir + "//../../../etc/passwd",
		"temp sibling prefix": filepath.Clean(os.TempDir()) + "X" + string(os.PathSeparator) + "evil.exe",
	}
	for name, p := range cases {
		if _, err := validateInstallerPath(p); err == nil {
			t.Errorf("%s: expected rejection, got nil for %q", name, p)
		}
	}
}

func TestSafeAssetFilename(t *testing.T) {
	ok := []string{"OpenFortiTray-0.1.8-Setup.exe", "SHA256SUMS", "a_b-c.1.dmg"}
	for _, n := range ok {
		if got, err := safeAssetFilename(n); err != nil || got != n {
			t.Errorf("safeAssetFilename(%q) = %q, %v; want %q, nil", n, got, err, n)
		}
	}
	bad := []string{
		"", ".", "..", ".hidden",
		"a b.exe", "a'b.exe", "a\"b.exe", "a`b.exe", "a;b.exe", "a$b.exe",
		"a\nb.exe",
	}
	for _, n := range bad {
		if _, err := safeAssetFilename(n); err == nil {
			t.Errorf("safeAssetFilename(%q) accepted, want rejection", n)
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

// The updater runs after the app has exited, so it must leave a trail: an empty
// update.log is exactly what the DETACHED_PROCESS bug produced, and it said
// nothing about how far the update got. Every step must announce itself, and the
// relaunch must not be skippable by an earlier failure — the worst outcome is the
// user left with no app at all ("the app closes and that's it").
func TestBuildWindowsScriptLogsAndAlwaysRelaunches(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "Setup.exe")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := buildWindowsScript(4242, p)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"Out-File",                 // it writes its own log
		"updater: waiting for pid", // ...before waiting
		"updater: running installer",
		"installer exit code",             // the installer's result is recorded
		"updater: relaunching via schedu", // and the relaunch attempt
		"updater: done",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("script does not log %q:\n%s", want, s)
		}
	}
	// A failed install must not abort the script before the relaunch.
	if strings.Contains(s, "$ErrorActionPreference = 'Stop'") {
		t.Error("script must not stop on the first error, or a failed install skips the relaunch")
	}
	// Fallback start when the scheduled task cannot run the app.
	if !strings.Contains(s, "starting it directly") {
		t.Errorf("script has no direct-start fallback:\n%s", s)
	}
	if strings.Count(s, "4242") < 2 {
		t.Errorf("pid should appear in both the log line and Wait-Process: %q", s)
	}
}

// The updater script and the launcher must not write the SAME file.
//
// They did, and it broke the updater's entire diagnostic channel: the launcher
// opened update.log and handed the handle to PowerShell as stdout/stderr, while
// the script wrote the same path with Out-File — which does not share a file with
// another writer. Every Say() failed with "The process cannot access the file
// because it is being used by another process", so a Windows update left an
// update.log full of nothing but those errors and no record of what it had done.
func TestUpdaterScriptAndConsoleLogsAreDifferentFiles(t *testing.T) {
	script := updateLogPath()
	console := updateConsoleLogPath()
	if script == "" || console == "" {
		t.Skip("no user config dir on this host")
	}
	if script == console {
		t.Fatalf("both logs are %q; Out-File cannot share a file with the redirected stdout", script)
	}
	if filepath.Dir(script) != filepath.Dir(console) {
		t.Errorf("logs live in different directories (%q vs %q); they should sit together",
			filepath.Dir(script), filepath.Dir(console))
	}
}

// The relaunch must decide from whether the app is RUNNING, not from schtasks'
// exit code. Trusting the exit code started the app twice on a real machine — two
// startup banners in the same second, one losing the single-instance mutex — which
// reads in the log exactly like a crash and restart.
func TestWindowsScriptRelaunchChecksProcessNotExitCode(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "Setup*.exe")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	script, err := buildWindowsScript(4242, f.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, "Get-Process openfortitray") {
		t.Error("script must check whether the app is running before starting it directly")
	}
	if strings.Contains(script, "$LASTEXITCODE") {
		t.Error("script still branches on schtasks' exit code, which double-launched the app")
	}
	// The direct start must still exist: the worst outcome is no app at all.
	if !strings.Contains(script, "starting it directly") {
		t.Error("the direct-start fallback was lost")
	}
}
