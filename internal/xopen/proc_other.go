//go:build !windows

package xopen

import "os/exec"

// hideConsole is a no-op off Windows, where launching `open`/`xdg-open` creates
// no window of its own.
func hideConsole(cmd *exec.Cmd) {}
