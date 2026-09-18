package main

import (
	"runtime"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/savvaskoualis/openfortitray/internal/tray"
)

// windowW/windowH/positionMargin size the fixed-corner placement
// positionWindow computes. They mirror the Wails window's own configured
// size (see buildAppOptions) rather than querying it at runtime — Wails
// exposes no WindowGetSize-before-WindowSetPosition ordering guarantee worth
// depending on here, and the window's size is fixed at startup anyway.
const (
	windowW        = 380
	windowH        = 600
	positionMargin = 8
)

// positionWindow places the app's window at a fixed corner of the PRIMARY
// screen: top-right on macOS/Linux (mirroring where a menu-bar/system-tray
// icon conventionally lives), bottom-right on Windows (mirroring the
// taskbar's tray corner).
//
// This is a deliberate scope reduction from true tray-icon-relative
// positioning: neither github.com/energye/systray v1.0.3 nor Wails'
// runtime.ScreenGetAll expose the tray icon's actual on-screen position (no
// icon-geometry query exists in systray's API, and runtime.Screen carries
// only Size/PhysicalSize + IsCurrent/IsPrimary — no origin/X/Y field), so
// there is nothing to position "relative to" the icon. Fixed-corner
// placement is the closest achievable approximation without adding
// per-platform native code to query icon geometry, which would reintroduce
// exactly the platform-specific complexity this migration removes.
//
// It only moves the window; it does not show it — onTrayClick (tray.Setup's
// onIconClick callback) calls this and then app.ShowStatus, which does the
// actual WindowShow + nav:status emit.
func (a *app) positionWindow() {
	ctx := a.ctxSnapshot()
	if ctx == nil {
		return
	}

	corner := "top-right"
	if runtime.GOOS == "windows" {
		corner = "bottom-right"
	}

	// Safe fallback (a common 1440x900 display) used if ScreenGetAll errors,
	// returns no screens, or marks none as primary — so a lookup failure
	// still places the window sanely rather than leaving it off-screen or
	// crashing.
	screenW, screenH := 1440, 900
	if screens, err := wailsruntime.ScreenGetAll(ctx); err == nil {
		for _, s := range screens {
			if s.IsPrimary {
				screenW, screenH = s.Size.Width, s.Size.Height
				break
			}
		}
	}

	x, y := tray.CornerPosition(corner, screenW, screenH, windowW, windowH, positionMargin)
	wailsruntime.WindowSetPosition(ctx, x, y)
}
