//go:build windows

package main

import (
	"log"
	"unsafe"

	"golang.org/x/sys/windows"
)

// dwmwaSystemBackdropType, dwmsbtTransientWindow and dwmwaUseImmersiveDarkMode
// are from dwmapi.h's DWMWINDOWATTRIBUTE and DWM_SYSTEMBACKDROP_TYPE enums
// (verified against Microsoft Learn's dwmapi.h reference, not
// golang.org/x/sys/windows — these attributes are not in that package's
// bindings). TransientWindow is Acrylic on Windows 11: more translucent than
// Mica (MainWindow), the closer match to the Menu material this app uses on
// macOS.
const (
	dwmwaUseImmersiveDarkMode = 20
	dwmwaSystemBackdropType   = 38
	dwmsbtTransientWindow     = 3
)

// margins is dwmapi.h's MARGINS struct, needed by DwmExtendFrameIntoClientArea.
type margins struct {
	cxLeftWidth, cxRightWidth, cyTopHeight, cyBottomHeight int32
}

var (
	dwmapi                           = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmSetWindowAttribute        = dwmapi.NewProc("DwmSetWindowAttribute")
	procDwmExtendFrameIntoClientArea = dwmapi.NewProc("DwmExtendFrameIntoClientArea")
)

// attachNativeGlass runs on the windows build.
//
// DWMWA_SYSTEMBACKDROP_TYPE alone only DECLARES which material the window
// wants; it is not enough on its own — confirmed live (a real Windows
// screenshot showed a flat, opaque gray window, no blur at all, despite this
// attribute already being set). Per Microsoft's own System Backdrops sample
// for raw Win32 apps, DWM only actually composites the material into regions
// the window has extended the DWM frame into via DwmExtendFrameIntoClientArea;
// without that call, Qt's own opaque-painted client area covers the backdrop
// everywhere and nothing is ever visible. Margins of -1 on all four sides
// means "the whole client rect".
//
// Also sets DWMWA_USE_IMMERSIVE_DARK_MODE from isDarkMode() so the native
// title bar (minimize/maximize/close chrome) matches the app's theme instead
// of defaulting to light regardless of it.
//
// Best-effort throughout: these DWM calls return a non-zero HRESULT on
// Windows versions/configurations that do not support them (pre-Windows-11,
// or DWM composition disabled) — logged, never fatal.
func attachNativeGlass(hwnd uintptr) {
	if hwnd == 0 {
		log.Print("glass: received zero HWND, skipping")
		return
	}
	if err := procDwmSetWindowAttribute.Find(); err != nil {
		log.Printf("glass: DwmSetWindowAttribute unavailable: %v", err)
		return
	}

	dark := int32(0)
	if isDarkMode() {
		dark = 1
	}
	if hr, _, _ := procDwmSetWindowAttribute.Call(
		hwnd,
		uintptr(dwmwaUseImmersiveDarkMode),
		uintptr(unsafe.Pointer(&dark)),
		unsafe.Sizeof(dark),
	); hr != 0 {
		log.Printf("glass: DWMWA_USE_IMMERSIVE_DARK_MODE failed: HRESULT 0x%x", uint32(hr))
	}

	backdrop := int32(dwmsbtTransientWindow)
	if hr, _, _ := procDwmSetWindowAttribute.Call(
		hwnd,
		uintptr(dwmwaSystemBackdropType),
		uintptr(unsafe.Pointer(&backdrop)),
		unsafe.Sizeof(backdrop),
	); hr != 0 {
		log.Printf("glass: DWMWA_SYSTEMBACKDROP_TYPE failed: HRESULT 0x%x", uint32(hr))
		return
	}

	if err := procDwmExtendFrameIntoClientArea.Find(); err != nil {
		log.Printf("glass: DwmExtendFrameIntoClientArea unavailable: %v", err)
		return
	}
	m := margins{-1, -1, -1, -1}
	if hr, _, _ := procDwmExtendFrameIntoClientArea.Call(
		hwnd,
		uintptr(unsafe.Pointer(&m)),
	); hr != 0 {
		log.Printf("glass: DwmExtendFrameIntoClientArea failed: HRESULT 0x%x", uint32(hr))
	}
}
