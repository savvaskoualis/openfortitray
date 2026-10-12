//go:build windows

package tray

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procGetForegroundWindow      = user32.NewProc("GetForegroundWindow")
	procGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
	procGetClassNameW            = user32.NewProc("GetClassNameW")
)

// shellTrayClasses are the window classes of the taskbar and the
// notification-area flyouts. Clicking a tray icon briefly hands the foreground
// to one of these as the taskbar/flyout closes, so a switch to them must not
// count as the user clicking away from the window.
var shellTrayClasses = map[string]bool{
	"Shell_TrayWnd":                       true, // primary taskbar
	"Shell_SecondaryTrayWnd":              true, // taskbar on other monitors
	"NotifyIconOverflowWindow":            true, // Windows 10 hidden-icons flyout
	"TopLevelWindowForOverflowXamlIsland": true, // Windows 11 hidden-icons flyout
	"XamlExplorerHostIslandWindow":        true, // Windows 11 taskbar XAML host
}

// ForegroundWindow returns the current foreground window and whether it
// belongs to something else — a window that is neither this process's own
// nor the taskbar/tray flyout. ok is false when the foreground can't be read.
func ForegroundWindow() (hwnd uintptr, elsewhere bool, ok bool) {
	hwnd, _, _ = procGetForegroundWindow.Call()
	if hwnd == 0 {
		return 0, false, false
	}
	var pid uint32
	procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid == windows.GetCurrentProcessId() {
		return hwnd, false, true
	}
	var buf [256]uint16
	n, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n > 0 && shellTrayClasses[windows.UTF16ToString(buf[:n])] {
		return hwnd, false, true
	}
	return hwnd, true, true
}
