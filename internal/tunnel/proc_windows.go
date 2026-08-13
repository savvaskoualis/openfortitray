//go:build windows

package tunnel

import (
	"os/exec"
	"syscall"
)

// createNoWindow is Windows' CREATE_NO_WINDOW: the child gets a console, but it
// is never shown.
const createNoWindow = 0x08000000

// hideChildConsole keeps openconnect's console window off the screen.
//
// The app is built -H=windowsgui and so has no console of its own. openconnect is
// a console program, so Windows allocates a NEW console for it — a black window
// titled openconnect.exe that sat on top of the user's work for as long as the
// tunnel was up. It is not only ugly: that window is interactive, so closing it or
// pressing Ctrl-C in it kills the VPN.
//
// Hiding the console does not change how the process is stopped. On Windows the
// direct path already terminates openconnect rather than signalling it
// (os.Process.Signal(os.Interrupt) is unimplemented there), and a hidden console
// is no less reachable than a visible one would have been from a GUI parent.
//
// stdout/stderr are unaffected: they are redirected to a pipe, which is
// independent of the console.
func hideChildConsole(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createNoWindow
	cmd.SysProcAttr.HideWindow = true
}
