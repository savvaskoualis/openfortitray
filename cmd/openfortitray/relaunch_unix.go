//go:build !windows

package main

import (
	"fmt"
	"os/exec"
	"syscall"
)

// relaunchCommand builds the detached "wait for pid to exit, then exec exe"
// helper relaunchSelf spawns. Setpgid detaches the helper from this
// process's process group so it survives the SIGKILL-equivalent os.Exit
// that follows — the same technique internal/update's own spawnDetached
// already uses for the identical problem (relaunching after this process
// is gone).
func relaunchCommand(pid int, exe string) (*exec.Cmd, error) {
	script := fmt.Sprintf("while kill -0 %d 2>/dev/null; do sleep 0.3; done\nexec '%s'\n", pid, exe)
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd, nil
}
