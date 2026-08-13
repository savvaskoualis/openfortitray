package update

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Apply performs the one-click, in-place update for `method` and relaunches the
// app. It does so by spawning a DETACHED updater process that first waits for
// process `pid` (the running app) to exit, then applies the update, then
// relaunches OpenFortiTray. Apply returns as soon as that updater is launched
// (or an error if it could not be launched); the CALLER must then quit the app
// so the updater can proceed. Because Apply returns an error BEFORE the caller
// quits when it cannot start the updater, a launch failure leaves the current
// app running rather than half-updated.
//
// installerPath is the verified installer to run and is used only by the Windows
// path (produced by DownloadAndVerify under a private temp dir); the Homebrew
// path ignores it because `brew` re-downloads and re-verifies via the cask sha.
// MethodManual is not handled here — the caller opens the releases page instead.
//
// SECURITY: the command the updater runs is assembled ONLY from the integer
// `pid`, compile-time constants, a `brew` path resolved here from a fixed
// allowlist (never $PATH), and (Windows) the installer path this program itself
// produced and re-validates below. No release metadata, asset name, tag, or any
// other network-derived text ever reaches the command line.
func Apply(method Method, installerPath string, pid int) error {
	switch method {
	case MethodHomebrew:
		return applyHomebrew(pid)
	case MethodWindowsInstaller:
		return applyWindows(installerPath, pid)
	default:
		return fmt.Errorf("update: automatic apply is not supported for %s installs", method)
	}
}

// brewAllowlist is the fixed set of absolute brew paths we will run, in order:
// the Apple Silicon prefix then the Intel prefix. resolveBrewPath returns the
// first that exists. We never consult $PATH — the updater must run a known-good
// binary, not whatever a manipulated environment resolves.
var brewAllowlist = []string{
	"/opt/homebrew/bin/brew",
	"/usr/local/bin/brew",
}

// resolveBrewPath returns the first existing brew binary from brewAllowlist, or
// an error if none is present (the caller then falls back to manual update).
func resolveBrewPath() (string, error) {
	for _, p := range brewAllowlist {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("update: no brew binary found in %v", brewAllowlist)
}

// buildBrewScript builds the /bin/sh script the detached mac updater runs: wait
// for the app to exit, upgrade the cask, relaunch. pid is formatted as a literal
// integer; brewPath is single-quoted. brewPath comes from brewAllowlist and must
// not contain a single quote — rejected fail-closed in case the allowlist ever
// changes to something exotic.
func buildBrewScript(pid int, brewPath string) (string, error) {
	if strings.ContainsRune(brewPath, '\'') {
		return "", fmt.Errorf("update: refusing brew path containing a single quote: %q", brewPath)
	}
	// %d is an int (no injection); '%s' is the allowlisted, quote-free brew path.
	// `brew update` FIRST: our cask lives in a custom tap (not Homebrew's central
	// API), so brew's local tap clone can be stale and `upgrade` would no-op with
	// "latest already installed" even when a newer release exists. Refresh the tap,
	// then upgrade, then relaunch.
	return fmt.Sprintf(
		"while kill -0 %d 2>/dev/null; do sleep 0.3; done\n"+
			"'%s' update\n"+
			"'%s' upgrade --cask openfortitray\n"+
			"open -a OpenFortiTray\n",
		pid, brewPath, brewPath), nil
}

// buildWindowsScript builds the PowerShell script the detached Windows updater
// runs: wait for the app to exit, run the verified Setup.exe silently, relaunch
// via the elevated logon task. installerPath is validated by validateInstallerPath
// before interpolation and single-quoted (PowerShell single-quote literal).
func buildWindowsScript(pid int, installerPath string) (string, error) {
	clean, err := validateInstallerPath(installerPath)
	if err != nil {
		return "", err
	}
	// Every step announces itself, because this script runs after the app has
	// exited: if it fails there is no UI left to report it, and an empty update.log
	// (which is exactly what the DETACHED_PROCESS bug produced) says nothing about
	// how far it got. $ErrorActionPreference stays at Continue so one failed step
	// cannot skip the relaunch below.
	//
	// The relaunch is deliberately belt-and-braces: schtasks runs the elevated logon
	// task, and if that is missing or fails the app is started directly, because the
	// worst outcome here is the user left with no app at all — which is what
	// "the app closes and that's it" was.
	return fmt.Sprintf(`$log = '%s'
function Say($m) { "$(Get-Date -Format s) $m" | Out-File -FilePath $log -Append -Encoding utf8 }
Say "updater: waiting for pid %d to exit"
Wait-Process -Id %d -ErrorAction SilentlyContinue
Say "updater: running installer %s"
try {
  $p = Start-Process -FilePath '%s' -ArgumentList '/VERYSILENT','/SUPPRESSMSGBOXES','/NORESTART' -Wait -PassThru
  Say "updater: installer exit code $($p.ExitCode)"
} catch {
  Say "updater: installer failed to start: $_"
}
Say "updater: relaunching via scheduled task"
$ok = $false
try {
  schtasks /Run /TN %s 2>&1 | ForEach-Object { Say "updater: schtasks: $_" }
  if ($LASTEXITCODE -eq 0) { $ok = $true }
} catch { Say "updater: schtasks threw: $_" }
if (-not $ok) {
  Say "updater: scheduled task did not start the app; starting it directly"
  try { Start-Process -FilePath '%s' } catch { Say "updater: direct start failed: $_" }
}
Say "updater: done"
`, updateLogPath(), pid, pid, clean, clean, windowsTaskName, exePathForRelaunch()), nil
}

// windowsTaskName is the scheduled task internal/autostart creates for the
// elevated logon launch, and the one the updater asks to run after installing.
const windowsTaskName = "OpenFortiTray"

// exePathForRelaunch returns this executable's path, used as the updater's
// fallback way to bring the app back if the scheduled task cannot. An empty
// string (os.Executable failing) simply leaves the fallback a no-op rather than
// interpolating something unusable.
func exePathForRelaunch() string {
	exe, err := os.Executable()
	if err != nil || strings.ContainsAny(exe, "'\"`") {
		return ""
	}
	return exe
}

// validateInstallerPath fails closed unless p, once cleaned, is an absolute path
// that is a genuine descendant of the system temp root (where DownloadAndVerify
// writes), an existing regular file, and free of quote/backtick/control
// characters that could break out of the quoted PowerShell argument. It returns
// the CLEANED path so the caller interpolates a canonical form (no "." or ".."
// components). This program produced the path, but it is re-validated here
// because it crosses into a command line.
func validateInstallerPath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("update: empty installer path")
	}
	if strings.ContainsAny(p, "'\"`") {
		return "", fmt.Errorf("update: installer path contains a quote or backtick: %q", p)
	}
	for _, r := range p {
		if r < 0x20 {
			return "", fmt.Errorf("update: installer path contains a control character")
		}
	}
	clean := filepath.Clean(p)
	if !filepath.IsAbs(clean) {
		return "", fmt.Errorf("update: installer path not absolute: %q", p)
	}
	// Containment checked AFTER Clean (so ".." cannot escape) and via Rel (so a
	// sibling like "/tmpX" cannot pass a bare string prefix of "/tmp").
	root := filepath.Clean(os.TempDir())
	rel, err := filepath.Rel(root, clean)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("update: installer path %q is not under the temp root %q", p, root)
	}
	fi, err := os.Stat(clean)
	if err != nil {
		return "", fmt.Errorf("update: installer path not readable: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("update: installer path is not a regular file: %q", p)
	}
	return clean, nil
}

// updateLogPath returns the file the detached updater's stdout/stderr are
// redirected to, so a failed upgrade is diagnosable. Best-effort: an empty
// return means "do not redirect".
func updateLogPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "openfortitray", "update.log")
}
