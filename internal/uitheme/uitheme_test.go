package uitheme

import (
	"image/color"
	"os"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
)

// Some default-theme tokens (Hyperlink among them) resolve through the current
// fyne app's settings, so DefaultTheme() panics with "Attempt to access current
// Fyne app when none is started" outside one. Our fallback path calls into it, so
// the tests need a headless app — which is also how the app behaves in reality,
// where an app always exists before a theme is ever asked for a colour.
func TestMain(m *testing.M) {
	test.NewApp()
	os.Exit(m.Run())
}

// hex parses "#RRGGBB" into the opaque NRGBA the theme returns, so the tables
// below can be read against the design spec without decoding by eye.
func hex(t *testing.T, s string) color.Color {
	t.Helper()
	if len(s) != 7 || s[0] != '#' {
		t.Fatalf("bad hex %q", s)
	}
	var v [3]uint8
	for i := 0; i < 3; i++ {
		var n uint8
		for _, c := range s[1+i*2 : 3+i*2] {
			n <<= 4
			switch {
			case c >= '0' && c <= '9':
				n |= uint8(c - '0')
			case c >= 'A' && c <= 'F':
				n |= uint8(c-'A') + 10
			case c >= 'a' && c <= 'f':
				n |= uint8(c-'a') + 10
			default:
				t.Fatalf("bad hex digit %q in %q", c, s)
			}
		}
		v[i] = n
	}
	return color.NRGBA{R: v[0], G: v[1], B: v[2], A: 0xff}
}

func sameColor(a, b color.Color) bool {
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	return ar == br && ag == bg && ab == bb && aa == ba
}

// The whole point of a two-variant theme is that BOTH variants are defined. A
// colour that exists in only one is how half a UI turns unreadable — the app
// renders one theme's text on the other theme's ground — so every overridden
// token must answer in both, and must answer DIFFERENTLY: a token that is
// identical in light and dark is a copy-paste slip in the tables, not a design
// choice. The three that are legitimately shared are listed out.
func TestEveryOverriddenColorIsDefinedInBothVariants(t *testing.T) {
	// Tokens whose value is deliberately the same in both variants.
	sharedByDesign := map[fyne.ThemeColorName]bool{}

	th := New()
	names := OverriddenColorNames()
	if len(names) == 0 {
		t.Fatal("no colour tokens are overridden; the theme would be the default one")
	}
	for _, n := range names {
		light := th.Color(n, theme.VariantLight)
		dark := th.Color(n, theme.VariantDark)
		if light == nil {
			t.Errorf("%s: nil in the light variant", n)
			continue
		}
		if dark == nil {
			t.Errorf("%s: nil in the dark variant", n)
			continue
		}
		if !sharedByDesign[n] && sameColor(light, dark) {
			t.Errorf("%s: identical in both variants (%v) — a table slip, or add it to sharedByDesign", n, light)
		}
	}
}

// The spec's token table, verbatim. If a value here and the spec disagree, one
// of the two is wrong and this test is where that surfaces.
func TestColorsMatchTheSpec(t *testing.T) {
	th := New()
	cases := []struct {
		name        fyne.ThemeColorName
		light, dark string
	}{
		{theme.ColorNameBackground, "#F6F7F9", "#16181C"},
		{theme.ColorNameHeaderBackground, "#FFFFFF", "#1E2128"},
		{theme.ColorNameMenuBackground, "#FFFFFF", "#1E2128"},
		{theme.ColorNameOverlayBackground, "#FFFFFF", "#1E2128"},
		{theme.ColorNameForeground, "#171A1F", "#EDEFF2"},
		{theme.ColorNamePlaceHolder, "#5C6470", "#9AA2AE"},
		{theme.ColorNameDisabled, "#5C6470", "#9AA2AE"},
		{theme.ColorNameSeparator, "#E2E5EA", "#2C313A"},
		{theme.ColorNameInputBorder, "#E2E5EA", "#2C313A"},
		{theme.ColorNamePrimary, "#2F6FEB", "#5B93F5"},
		{theme.ColorNameForegroundOnPrimary, "#FFFFFF", "#0E1013"},
		{theme.ColorNameInputBackground, "#FFFFFF", "#22262E"},
		{theme.ColorNameSuccess, "#2E9E5B", "#41BE77"},
		{theme.ColorNameWarning, "#B87514", "#E0A140"},
		{theme.ColorNameError, "#C4362F", "#E86A62"},
	}
	for _, tc := range cases {
		if got, want := th.Color(tc.name, theme.VariantLight), hex(t, tc.light); !sameColor(got, want) {
			t.Errorf("%s light = %v, want %s", tc.name, got, tc.light)
		}
		if got, want := th.Color(tc.name, theme.VariantDark), hex(t, tc.dark); !sameColor(got, want) {
			t.Errorf("%s dark = %v, want %s", tc.name, got, tc.dark)
		}
	}
}

// Hover is the one token specified with alpha — a translucent wash over whatever
// is beneath it, which is why it cannot be an opaque hex like the rest.
func TestHoverIsTranslucent(t *testing.T) {
	th := New()
	for _, v := range []fyne.ThemeVariant{theme.VariantLight, theme.VariantDark} {
		c := th.Color(theme.ColorNameHover, v)
		if _, _, _, a := c.RGBA(); a == 0 || a == 0xffff {
			t.Errorf("variant %d: hover alpha = %d, want partial transparency", v, a>>8)
		}
	}
}

// Anything not in our tables must fall through to fyne's own theme rather than
// returning a zero colour, which would render as transparent black.
func TestUnknownTokenFallsBackToDefault(t *testing.T) {
	th := New()
	for _, n := range []fyne.ThemeColorName{theme.ColorNameHyperlink, theme.ColorNameScrollBar, theme.ColorNameShadow} {
		for _, v := range []fyne.ThemeVariant{theme.VariantLight, theme.VariantDark} {
			got := th.Color(n, v)
			want := theme.DefaultTheme().Color(n, v)
			if !sameColor(got, want) {
				t.Errorf("%s variant %d = %v, want the default theme's %v", n, v, got, want)
			}
		}
	}
}

func TestSizesMatchTheSpec(t *testing.T) {
	th := New()
	cases := []struct {
		name fyne.ThemeSizeName
		want float32
	}{
		{theme.SizeNameText, 13},
		{theme.SizeNameCaptionText, 11},
		{theme.SizeNameSubHeadingText, 15},
		{theme.SizeNameHeadingText, 20},
		{theme.SizeNamePadding, 5},
		{theme.SizeNameInnerPadding, 10},
		{theme.SizeNameCardRadius, 8},
		{theme.SizeNameButtonRadius, 6},
		{theme.SizeNameInputRadius, 6},
		{theme.SizeNameSeparatorThickness, 1},
	}
	for _, tc := range cases {
		if got := th.Size(tc.name); got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestUnknownSizeFallsBackToDefault(t *testing.T) {
	th := New()
	for _, n := range []fyne.ThemeSizeName{theme.SizeNameScrollBar, theme.SizeNameInlineIcon} {
		if got, want := th.Size(n), theme.DefaultTheme().Size(n); got != want {
			t.Errorf("%s = %v, want the default theme's %v", n, got, want)
		}
	}
}

// No font is embedded: fyne 2.8 already bundles Inter, so the binary must not
// grow by a megabyte to restate that. This test is the guard against someone
// "helpfully" adding one.
func TestFontDelegatesToDefault(t *testing.T) {
	th := New()
	for _, st := range []fyne.TextStyle{{}, {Bold: true}, {Italic: true}, {Monospace: true}} {
		got, want := th.Font(st), theme.DefaultTheme().Font(st)
		if got == nil {
			t.Fatalf("style %+v: nil font", st)
		}
		if got.Name() != want.Name() {
			t.Errorf("style %+v: font %q, want the default theme's %q", st, got.Name(), want.Name())
		}
	}
}

func TestIconDelegatesToDefault(t *testing.T) {
	th := New()
	got, want := th.Icon(theme.IconNameSettings), theme.DefaultTheme().Icon(theme.IconNameSettings)
	if got == nil || got.Name() != want.Name() {
		t.Errorf("icon = %v, want the default theme's %v", got, want)
	}
}
