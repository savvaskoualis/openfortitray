package tray

import "testing"

func TestCornerPositionTopRight(t *testing.T) {
	x, y := CornerPosition("top-right", 1920, 1080, 380, 600, 8)
	wantX, wantY := 1920-380-8, 8
	if x != wantX || y != wantY {
		t.Errorf("CornerPosition(top-right) = (%d,%d), want (%d,%d)", x, y, wantX, wantY)
	}
	// Must stay fully inside the screen.
	if x < 0 || x+380 > 1920 {
		t.Errorf("x=%d puts window outside [0,1920] horizontally", x)
	}
	if y < 0 || y+600 > 1080 {
		t.Errorf("y=%d puts window outside [0,1080] vertically", y)
	}
}

func TestCornerPositionBottomRight(t *testing.T) {
	x, y := CornerPosition("bottom-right", 1920, 1080, 380, 600, 8)
	wantX, wantY := 1920-380-8, 1080-600-8
	if x != wantX || y != wantY {
		t.Errorf("CornerPosition(bottom-right) = (%d,%d), want (%d,%d)", x, y, wantX, wantY)
	}
	if x < 0 || x+380 > 1920 {
		t.Errorf("x=%d puts window outside [0,1920] horizontally", x)
	}
	if y < 0 || y+600 > 1080 {
		t.Errorf("y=%d puts window outside [0,1080] vertically", y)
	}
}

func TestCornerPositionClampsWhenWindowBiggerThanScreen(t *testing.T) {
	// A screen smaller than the window in both dimensions must clamp to 0,
	// never go negative.
	x, y := CornerPosition("top-right", 300, 200, 380, 600, 8)
	if x != 0 {
		t.Errorf("x = %d, want 0 (clamped, not negative)", x)
	}
	if y != 8 {
		// y = margin (8) fits within the fallback branch's math for top-right
		// since only x would have gone negative here; assert it's non-negative
		// either way.
		if y < 0 {
			t.Errorf("y = %d, want >= 0", y)
		}
	}

	x, y = CornerPosition("bottom-right", 300, 200, 380, 600, 8)
	if x < 0 {
		t.Errorf("x = %d, want >= 0 (clamped, not negative)", x)
	}
	if y < 0 {
		t.Errorf("y = %d, want >= 0 (clamped, not negative)", y)
	}
}

func TestCornerPositionUnknownCornerFallsBackToMargin(t *testing.T) {
	x, y := CornerPosition("center", 1920, 1080, 380, 600, 8)
	if x != 8 || y != 8 {
		t.Errorf("CornerPosition(unknown corner) = (%d,%d), want (8,8) margin-offset default", x, y)
	}
}

func TestClampToScreenNoOpWhenAlreadyOnScreen(t *testing.T) {
	x, y := ClampToScreen(500, 400, 1920, 1080, 380, 600, 8)
	if x != 500 || y != 400 {
		t.Errorf("ClampToScreen no-op case = (%d,%d), want (500,400)", x, y)
	}
}

func TestClampToScreenPullsBackFromRightEdge(t *testing.T) {
	// A click near the right edge would otherwise place the window's right
	// edge off-screen.
	x, y := ClampToScreen(1900, 400, 1920, 1080, 380, 600, 8)
	wantX := 1920 - 380 - 8
	if x != wantX {
		t.Errorf("x = %d, want %d (pulled back from the right edge)", x, wantX)
	}
	if x+380 > 1920 {
		t.Errorf("x=%d still puts the window off-screen horizontally", x)
	}
	if y != 400 {
		t.Errorf("y = %d, want unchanged 400 (only x should move)", y)
	}
}

func TestClampToScreenPullsBackFromBottomEdge(t *testing.T) {
	x, y := ClampToScreen(500, 1050, 1920, 1080, 380, 600, 8)
	wantY := 1080 - 600 - 8
	if y != wantY {
		t.Errorf("y = %d, want %d (pulled back from the bottom edge)", y, wantY)
	}
	if y+600 > 1080 {
		t.Errorf("y=%d still puts the window off-screen vertically", y)
	}
	if x != 500 {
		t.Errorf("x = %d, want unchanged 500 (only y should move)", x)
	}
}

func TestClampToScreenPullsBackFromNegativeOrigin(t *testing.T) {
	// A click very near the top-left corner, with a wide margin, must not
	// place the window partially off the top/left edges.
	x, y := ClampToScreen(-50, -50, 1920, 1080, 380, 600, 8)
	if x != 8 {
		t.Errorf("x = %d, want 8 (clamped to the margin, not negative)", x)
	}
	if y != 8 {
		t.Errorf("y = %d, want 8 (clamped to the margin, not negative)", y)
	}
}

func TestMacOSFrameOriginRoundTripsWithCursorPositionFormula(t *testing.T) {
	// Mirrors the worked example from this code's own review: feed a point
	// through the SAME conversion oft_cursor_position uses (primary origin
	// (0,0), height 1080, AppKit point (500,800) -> top-left-relative
	// (500,280)), then back through MacOSFrameOrigin with a 200px-tall
	// window, and confirm the window's reconstructed TOP edge lands back
	// on the original AppKit y (800), not just some arbitrary value.
	const primaryOriginX, primaryOriginY, primaryHeight = 0.0, 0.0, 1080.0
	const appKitX, appKitY = 500.0, 800.0
	const windowHeight = 200.0

	// oft_cursor_position's own formula, inlined here as the "given":
	topLeftX := appKitX - primaryOriginX
	topLeftY := primaryHeight - (appKitY - primaryOriginY)

	bottomLeftX, bottomLeftY := MacOSFrameOrigin(topLeftX, topLeftY, primaryOriginX, primaryOriginY, primaryHeight, windowHeight)

	if bottomLeftX != appKitX {
		t.Errorf("bottomLeftX = %v, want %v (the original AppKit x)", bottomLeftX, appKitX)
	}
	reconstructedTopY := bottomLeftY + windowHeight
	if reconstructedTopY != appKitY {
		t.Errorf("reconstructed top edge y = %v, want %v (the original AppKit y)", reconstructedTopY, appKitY)
	}
}

func TestMacOSFrameOriginNonZeroPrimaryOrigin(t *testing.T) {
	// The formula must stay correct even if AppKit's own guarantee that
	// screens[0]'s origin is always (0,0) ever turned out to be violated
	// on some future macOS version -- exercise it with a non-zero origin
	// so the test isn't silently relying on that guarantee too.
	bottomLeftX, bottomLeftY := MacOSFrameOrigin(50, 30, 10, 20, 900, 200)
	wantX := 10.0 + 50.0
	wantY := 20.0 + 900.0 - 30.0 - 200.0
	if bottomLeftX != wantX {
		t.Errorf("bottomLeftX = %v, want %v", bottomLeftX, wantX)
	}
	if bottomLeftY != wantY {
		t.Errorf("bottomLeftY = %v, want %v", bottomLeftY, wantY)
	}
}

func TestClampToScreenWindowBiggerThanScreen(t *testing.T) {
	x, y := ClampToScreen(100, 100, 300, 200, 380, 600, 8)
	if x < 0 {
		t.Errorf("x = %d, want >= 0", x)
	}
	if y < 0 {
		t.Errorf("y = %d, want >= 0", y)
	}
}
