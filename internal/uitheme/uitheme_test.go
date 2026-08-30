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

func TestTokensSizesMatchFyneOriginal(t *testing.T) {
	tok := Tokens{}
	if tok.TextSize() != 13 || tok.CaptionTextSize() != 11 || tok.HeadingTextSize() != 20 {
		t.Fatal("size tokens must match the values ported from the Fyne theme")
	}
}
