package tray

// CornerPosition returns the top-left (x, y) for a windowW x windowH window
// placed in the given corner of a screenW x screenH primary screen, offset
// inward by margin on both axes so the window never touches the screen edge.
//
// This is the fixed-corner replacement for true tray-icon-relative
// positioning: neither github.com/energye/systray v1.0.3 nor Wails'
// runtime.ScreenGetAll expose the tray icon's on-screen position (confirmed
// against both APIs directly), so there is no icon coordinate to clamp a
// popover against. Instead the window is anchored to a fixed corner of the
// PRIMARY screen — top-right on macOS/Linux (where a menu-bar/system-tray
// icon conventionally lives), bottom-right on Windows (mirroring the
// taskbar's tray corner). See cmd/openfortitray's positionWindow, which picks
// the corner via runtime.GOOS and supplies the real primary-screen size.
//
// A screen smaller than the window in either dimension clamps to 0 on that
// axis rather than going negative, so the window is never placed off-screen.
// An unrecognised corner string falls back to a margin-offset top-left
// position rather than guessing.
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
