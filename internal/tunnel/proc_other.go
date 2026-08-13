//go:build !windows

package tunnel

import "os/exec"

// hideChildConsole is a no-op off Windows: only Windows allocates a console window
// for a console child of a GUI parent. See proc_windows.go.
func hideChildConsole(cmd *exec.Cmd) {}
