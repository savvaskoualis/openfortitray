package tray

// ClampToScreen adjusts a proposed windowW x windowH window position (x, y)
// so the window stays fully within a screenW x screenH screen, inset by
// margin on every edge it would otherwise cross. Used to keep a
// cursor-anchored popover (see CursorPosition) on-screen when the click
// happens near an edge or corner.
func ClampToScreen(x, y, screenW, screenH, windowW, windowH, margin int) (int, int) {
	if x+windowW > screenW-margin {
		x = screenW - windowW - margin
	}
	if x < margin {
		x = margin
	}
	if y+windowH > screenH-margin {
		y = screenH - windowH - margin
	}
	if y < margin {
		y = margin
	}
	return x, y
}

// CornerPosition returns the top-left (x, y) for a windowW x windowH window
// placed in the given corner of a screenW x screenH primary screen, offset
// inward by margin on both axes so the window never touches the screen edge.
//
// This is the Wayland fallback for window positioning: CursorPosition
// (cursor_darwin.go / cursor_windows.go / cursor_linux.go) gives real
// tray-click-relative placement on macOS, Windows, and X11/XWayland Linux
// sessions; a pure-Wayland session forbids an arbitrary client from querying
// the global cursor position at all (a deliberate Wayland security-model
// restriction, not a library gap), so there is nothing to anchor to there.
// CornerPosition is what cmd/openfortitray's positionWindow falls back to
// only in that one case — top-right on macOS/Linux (where a menu-bar/
// system-tray icon conventionally lives), bottom-right on Windows (mirroring
// the taskbar's tray corner).
//
// A screen smaller than the window in either dimension clamps to 0 on that
// axis rather than going negative. An unrecognised corner string falls back
// to a margin-offset top-left position rather than guessing.
func CornerPosition(corner string, screenW, screenH, windowW, windowH, margin int) (x, y int) {
	switch corner {
	case "top-right":
		x = screenW - windowW - margin
		y = margin
	case "bottom-right":
		x = screenW - windowW - margin
		y = screenH - windowH - margin
	default:
		x, y = margin, margin
	}
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	return x, y
}
