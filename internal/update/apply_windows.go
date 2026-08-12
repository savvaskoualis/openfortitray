//go:build windows

package update

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// detachedProcess is Windows' DETACHED_PROCESS creation flag; combined with
// CREATE_NEW_PROCESS_GROUP it lets the updater outlive the app with no console.
const detachedProcess = 0x00000008

// applyWindows launches the detached Windows updater: wait for the app to exit,
// run the verified Setup.exe silently, relaunch via the elevated logon task. The
// app runs elevated (requireAdministrator), so the child inherits elevation with
// no second UAC prompt. installerPath is re-validated inside buildWindowsScript.
func applyWindows(installerPath string, pid int) error {
	script, err := buildWindowsScript(pid, installerPath)
	if err != nil {
		return err
	}
	return spawnDetached("powershell", []string{"-NoProfile", "-NonInteractive", "-Command", script})
}

// applyHomebrew is unreachable on windows; it exists so the package compiles.
func applyHomebrew(pid int) error {
	return fmt.Errorf("update: homebrew apply is not available on windows")
}

// spawnDetached starts name+args detached (own process group, no console) so it
// survives the app exiting, with stdout/stderr appended to the update log (best
// effort). It never waits — the updater must outlive us.
func spawnDetached(name string, args []string) error {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: detachedProcess | syscall.CREATE_NEW_PROCESS_GROUP,
	}
	if lp := updateLogPath(); lp != "" {
		_ = os.MkdirAll(filepath.Dir(lp), 0o700)
		if f, err := os.OpenFile(lp, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			cmd.Stdout = f
			cmd.Stderr = f
		}
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("update: failed to launch updater: %w", err)
	}
	_ = cmd.Process.Release()
	return nil
}
