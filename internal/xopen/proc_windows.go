//go:build windows

package xopen

import (
	"os/exec"
	"syscall"
)

// createNoWindow is Windows' CREATE_NO_WINDOW.
const createNoWindow = 0x08000000

// hideConsole keeps the launcher's console window off the screen.
//
// The app is built -H=windowsgui and so has no console of its own, which means
// Windows allocates a NEW one for any console child. `cmd /c start` is a console
// program, so opening the browser for a SAML sign-in flashed a black window on
// screen every time. Cosmetic, but it looked like something had gone wrong at the
// exact moment the user was being asked to trust a login prompt.
func hideConsole(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createNoWindow
	cmd.SysProcAttr.HideWindow = true
}
