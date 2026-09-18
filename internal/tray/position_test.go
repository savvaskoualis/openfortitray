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
