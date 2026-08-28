//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"syscall"
)

// createNoWindow is Windows' CREATE_NO_WINDOW — matches
// internal/update/apply_windows.go's own constant of the same value; no
// console for a detached background helper.
const createNoWindow = 0x08000000

// relaunchCommand builds the detached "wait for pid to exit, then start exe"
// helper relaunchSelf spawns. CREATE_NEW_PROCESS_GROUP detaches the helper
// from this process's group so it survives the os.Exit that follows — the
// same technique internal/update/apply_windows.go already uses for the
// identical problem (relaunching after this process is gone).
func relaunchCommand(pid int, exe string) (*exec.Cmd, error) {
	script := fmt.Sprintf(
		"while (Get-Process -Id %d -ErrorAction SilentlyContinue) { Start-Sleep -Milliseconds 300 }; Start-Process -FilePath '%s'",
		pid, exe)
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNoWindow | syscall.CREATE_NEW_PROCESS_GROUP,
	}
	return cmd, nil
}
