//go:build windows

package main

import (
	"log"
	"unsafe"

	"golang.org/x/sys/windows"
)

// dwmwaSystemBackdropType and dwmsbtTransientWindow are from dwmapi.h's
// DWMWINDOWATTRIBUTE and DWM_SYSTEMBACKDROP_TYPE enums (verified against
// Microsoft Learn's dwmapi.h reference, not golang.org/x/sys/windows —
// this attribute is not in that package's bindings). TransientWindow is
// Acrylic on Windows 11: more translucent than Mica (MainWindow), the
// closer match to the Menu material this app uses on macOS.
const (
	dwmwaSystemBackdropType = 38
	dwmsbtTransientWindow   = 3
)

var (
	dwmapi                    = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")
)

// attachNativeGlass runs on the windows build.
//
// Best-effort: DwmSetWindowAttribute returns a non-zero HRESULT on
// Windows versions/configurations that do not support this attribute
// (pre-Windows-11, or DWM composition disabled) — logged, never fatal.
// This has not been run on real Windows hardware; only cross-compiled.
func attachNativeGlass(hwnd uintptr) {
	if hwnd == 0 {
		log.Print("glass: received zero HWND, skipping")
		return
	}
	if err := procDwmSetWindowAttribute.Find(); err != nil {
		log.Printf("glass: DwmSetWindowAttribute unavailable: %v", err)
		return
	}
	backdrop := int32(dwmsbtTransientWindow)
	hr, _, _ := procDwmSetWindowAttribute.Call(
		hwnd,
		uintptr(dwmwaSystemBackdropType),
		uintptr(unsafe.Pointer(&backdrop)),
		unsafe.Sizeof(backdrop),
	)
	if hr != 0 {
		log.Printf("glass: DwmSetWindowAttribute failed: HRESULT 0x%x", uint32(hr))
	}
}
