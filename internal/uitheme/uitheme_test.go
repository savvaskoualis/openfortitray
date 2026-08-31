package uitheme

import (
	"strings"
	"testing"
)

func TestBackgroundColorIsTranslucentInBothModes(t *testing.T) {
	for _, dark := range []bool{false, true} {
		_, _, _, a := BackgroundColor(dark)
		if a == 0xFF {
			t.Fatalf("BackgroundColor(dark=%v) alpha = 0xFF, want translucent (<0xFF) — Qt's WA_TranslucentBackground needs a non-opaque background to let native vibrancy show through", dark)
		}
		if a == 0x00 {
			t.Fatalf("BackgroundColor(dark=%v) alpha = 0x00, want non-zero — fully transparent would make Qt's own content invisible too", dark)
		}
	}
}

func TestStyleSheetContainsCoreTokens(t *testing.T) {
	for _, dark := range []bool{false, true} {
		ss := StyleSheet(dark)
		for _, want := range []string{"background", "color", "border-radius"} {
			if !strings.Contains(ss, want) {
				t.Errorf("StyleSheet(dark=%v) missing %q property", dark, want)
			}
		}
	}
}

func TestStyleSheetDiffersBetweenLightAndDark(t *testing.T) {
	if StyleSheet(false) == StyleSheet(true) {
		t.Fatal("light and dark stylesheets must differ")
	}
}

// TestStyleSheetHasCheckedButtonRule guards against the nav rail's selected
// button being invisible: once a bare QPushButton rule exists (it does, for
// background/border-radius/padding), Qt's stylesheet engine suppresses the
// native checked-state rendering entirely unless the stylesheet supplies its
// own QPushButton:checked rule.
func TestStyleSheetHasCheckedButtonRule(t *testing.T) {
	for _, dark := range []bool{false, true} {
		ss := StyleSheet(dark)
		if !strings.Contains(ss, "QPushButton:checked") {
			t.Errorf("StyleSheet(dark=%v) missing a QPushButton:checked rule", dark)
		}
	}
}

// TestStyleSheetSeparatorUsesMinMaxHeight guards against Qt's stylesheet
// engine silently ignoring a bare `height` property (unsupported on
// QWidget-based selectors) — only min-height/max-height are honored.
func TestStyleSheetSeparatorUsesMinMaxHeight(t *testing.T) {
	for _, dark := range []bool{false, true} {
		ss := StyleSheet(dark)
		if !strings.Contains(ss, "min-height:") || !strings.Contains(ss, "max-height:") {
			t.Errorf("StyleSheet(dark=%v) separator rule missing min-height/max-height", dark)
		}
	}
}

func TestTokensSizesMatchFyneOriginal(t *testing.T) {
	tok := Tokens{}
	if tok.TextSize() != 13 || tok.CaptionTextSize() != 11 || tok.HeadingTextSize() != 20 {
		t.Fatal("size tokens must match the values ported from the Fyne theme")
	}
}
