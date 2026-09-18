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

func TestClampToScreenWindowBiggerThanScreen(t *testing.T) {
	x, y := ClampToScreen(100, 100, 300, 200, 380, 600, 8)
	if x < 0 {
		t.Errorf("x = %d, want >= 0", x)
	}
	if y < 0 {
		t.Errorf("y = %d, want >= 0", y)
	}
}
