package main

import (
	"runtime"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/savvaskoualis/openfortitray/internal/tray"
)

// windowW/windowH/positionMargin size the popover positionWindow computes.
// They mirror the Wails window's own configured size (see buildAppOptions)
// rather than querying it at runtime — Wails exposes no WindowGetSize-
// before-WindowSetPosition ordering guarantee worth depending on here, and
// the window's size is fixed at startup anyway.
const (
	windowW        = 380
	windowH        = 600
	positionMargin = 8
)

// positionWindow places the app's window near the tray icon click that
// triggered it: it reads the OS cursor position (internal/tray.
// CursorPosition — real per-platform native calls: NSEvent.mouseLocation on
// macOS, GetCursorPos on Windows, XQueryPointer on X11/XWayland Linux) and
// anchors the window just below it, clamped so it never renders off the edge
// of whichever screen the cursor is actually on — the same placement a
// native OS tray flyout uses.
//
// Falls back to a fixed corner of the PRIMARY screen (top-right on
// macOS/Linux, bottom-right on Windows) only when no cursor position is
// obtainable at all — in practice, only a pure-Wayland Linux session, which
// deliberately forbids any client from querying the global cursor position
// (a Wayland security-model restriction, not a library gap: see
// internal/tray/cursor_linux.c).
//
// It only moves the window; it does not show it — onTrayClick (tray.Setup's
// onIconClick callback) calls this and then app.ShowStatus, which does the
// actual WindowShow + nav:status emit.
func (a *app) positionWindow() {
	ctx := a.ctxSnapshot()
	if ctx == nil {
		return
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

	if cx, cy, ok := tray.CursorPosition(); ok {
		x, y := tray.ClampToScreen(cx-windowW/2, cy+positionMargin, screenW, screenH, windowW, windowH, positionMargin)
		wailsruntime.WindowSetPosition(ctx, x, y)
		return
	}

	corner := "top-right"
	if runtime.GOOS == "windows" {
		corner = "bottom-right"
	}
	x, y := tray.CornerPosition(corner, screenW, screenH, windowW, windowH, positionMargin)
	wailsruntime.WindowSetPosition(ctx, x, y)
}
