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
	// windowTitle must match buildAppOptions' options.App.Title exactly --
	// it's how tray.SetWindowPosition finds the app's own window natively
	// on ALL THREE platforms ([NSApp windows] on macOS, FindWindowW on
	// Windows, EWMH _NET_WM_NAME/WM_NAME on Linux — see that function's
	// doc comment for why a native lookup is needed at all).
	windowTitle = "OpenFortiTray"
)

// positionWindow places the app's window near the tray icon click that
// triggered it: it reads the OS cursor position (internal/tray.
// CursorPosition — real per-platform native calls: NSEvent.mouseLocation on
// macOS, GetCursorPos on Windows, XQueryPointer on X11/XWayland Linux) and
// anchors the window just below it, clamped so it never renders off the edge
// of whichever screen the cursor is actually on — the same placement a
// native OS tray flyout uses.
//
// The computed (x, y) is applied via tray.SetWindowPosition first — a
// native, per-platform window move using the SAME absolute coordinate space
// CursorPosition reports in (see that function's doc comment) — because
// Wails' own runtime.WindowSetPosition is relative to whichever screen/
// monitor the window CURRENTLY occupies, not the primary screen or the
// virtual-screen origin, on ALL THREE platforms (an earlier pass through
// this code assumed Linux's gtk_window_move was already absolute; it
// isn't — Wails' own window.c adds the current monitor's offset there
// too). On a real multi-monitor system, once the window's current screen
// differs from primary (macOS), its monitor's work area doesn't start at
// (0,0) (Windows), or its current GDK monitor geometry isn't (0,0)
// (Linux), that mismatch would silently place a cursor-anchored popover on
// the wrong screen. tray.SetWindowPosition returns false only if it could
// not find the app's own window (by title on macOS/Windows, by EWMH
// _NET_CLIENT_LIST on Linux) or the platform lacks what the bypass needs
// (e.g. a non-EWMH Linux window manager) — in which case the caller falls
// through to wailsruntime.WindowSetPosition, which is still correct on a
// single-monitor session on any platform (the case this bypass doesn't
// change).
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
	screenX, screenY, screenW, screenH := 0, 0, 1440, 900
	if screens, err := wailsruntime.ScreenGetAll(ctx); err == nil {
		for _, s := range screens {
			if s.IsPrimary {
				screenW, screenH = s.Size.Width, s.Size.Height
				break
			}
		}
	}
	// tray.CursorScreenFrame (darwin only for now) reports the bounds of the
	// screen the cursor is ACTUALLY on, which is neither necessarily primary
	// nor rooted at (0,0). Without this, clamping below always used
	// primary's own size starting at (0,0) — which, on a multi-monitor Mac,
	// silently dragged the window back onto primary any time the click that
	// triggered this happened on a different screen. Falls back to
	// (0, 0, primary size) — today's existing behaviour — when unavailable.
	if ox, oy, w, h, ok := tray.CursorScreenFrame(); ok {
		screenX, screenY, screenW, screenH = ox, oy, w, h
	}

	if cx, cy, ok := tray.CursorPosition(); ok {
		x, y := tray.ClampToScreen(cx-windowW/2-screenX, cy+positionMargin-screenY, screenW, screenH, windowW, windowH, positionMargin)
		x, y = x+screenX, y+screenY
		if tray.SetWindowPosition(windowTitle, x, y) {
			return
		}
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
