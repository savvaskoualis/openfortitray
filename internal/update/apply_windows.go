//go:build windows

package update

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// createNoWindow is Windows' CREATE_NO_WINDOW: the child gets its own console,
// but hidden. It replaces DETACHED_PROCESS, which gave the child NO console at
// all — and powershell.exe is a console application, so with no console it could
// exit immediately without running a line of the script. The app itself is built
// -H=windowsgui and so has no console to inherit either, which is why nothing ran
// and update.log stayed empty: the file was created by the redirection below, the
// app quit, and the updater was already gone.
const createNoWindow = 0x08000000

// applyWindows launches the detached Windows updater: wait for the app to exit,
// run the verified Setup.exe silently, relaunch via the elevated logon task. The
// app runs elevated (requireAdministrator), so the child inherits elevation with
// no second UAC prompt. installerPath is re-validated inside buildWindowsScript.
//
// The script goes to a file rather than powershell -Command: a multi-line script
// passed as one command-line argument depends on quoting surviving Go, the Win32
// command line and PowerShell's own parsing, and -File sidesteps all of it.
func applyWindows(installerPath string, pid int) error {
	script, err := buildWindowsScript(pid, installerPath)
	if err != nil {
		return err
	}
	path, err := writeUpdaterScript(script)
	if err != nil {
		return err
	}
	// -ExecutionPolicy Bypass because a machine policy that blocks unsigned .ps1
	// files would otherwise stop the updater dead, with the app already exiting.
	return spawnDetached("powershell", []string{
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", path,
	})
}

// writeUpdaterScript writes the updater script next to the update log (a
// user-owned directory that outlives this process, unlike a temp file some
// cleaner may remove) and returns its path.
func writeUpdaterScript(script string) (string, error) {
	dir := filepath.Dir(updateLogPath())
	if dir == "" || dir == "." {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("update: cannot create %s: %w", dir, err)
	}
	path := filepath.Join(dir, "update.ps1")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		return "", fmt.Errorf("update: cannot write the updater script: %w", err)
	}
	return path, nil
}

// applyHomebrew is unreachable on windows; it exists so the package compiles.
func applyHomebrew(pid int) error {
	return fmt.Errorf("update: homebrew apply is not available on windows")
}

// spawnDetached starts name+args in its own process group with a hidden console,
// so it survives the app exiting, with stdout/stderr appended to the update log
// (best effort). It never waits — the updater must outlive us.
func spawnDetached(name string, args []string) error {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNoWindow | syscall.CREATE_NEW_PROCESS_GROUP,
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
