package main

import qt "github.com/mappu/miqt/qt6"

// attachGlass attaches native blur/acrylic behind w. Must be called after
// w's underlying native window exists (i.e. after Show()) — WinId() can
// return an invalid handle before then. Safe to call repeatedly
// (idempotent per platform — see glass_darwin.m's identifier-tag guard).
func attachGlass(w *qt.QWidget) {
	_ = w
	// Disabled: confirmed live (not just suspected) that wrapping the
	// window's contentView — the technique glass_darwin.m/glass_windows.go
	// inherited from the Fyne-era implementation, which worked because Fyne
	// draws via a custom OpenGL context that ignores AppKit's own subview
	// compositing — breaks Qt's macOS repaint entirely once contentView is
	// no longer literally `window.contentView`. Switching the shell's
	// QStackedWidget page (Status -> Connection -> Advanced) stopped
	// repainting anything at all with glass attached; the same build with
	// this call disabled renders every page correctly. Qt's widgets are
	// AppKit-composited (unlike Fyne/GLFW's raw-GL view), so Qt almost
	// certainly needs a different vibrancy technique — e.g. making the
	// NSVisualEffectView the window's actual contentView and adding Qt's
	// view AS ITS SUBVIEW (the reverse nesting of what's here now), or a
	// Qt-native platform-plugin-level approach — not this session's
	// Fyne-proven wrap-and-swap. Left off until that's designed and
	// verified against real QStackedWidget navigation, not just a static
	// single-page spike; correctness (working tabs) matters more than the
	// vibrancy effect. See cmd/openfortitray/glass_darwin.m for the
	// disabled implementation this replaces.
}
