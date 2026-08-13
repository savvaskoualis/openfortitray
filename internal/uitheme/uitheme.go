// Package uitheme is OpenFortiTray's fyne theme: one palette per variant, a
// tightened type scale, and softer corner radii.
//
// It exists because fyne's default theme is a toolkit default — it looks like a
// demo of fyne rather than like a product. Everything here is a token override;
// the widget code never names a colour or a size directly, which is what keeps
// the two variants in step.
//
// Both variants are always defined. fyne resolves the variant from the OS
// setting and passes it to Color, so light and dark are two tables rather than
// two code paths, and a token defined in only one of them would render one
// theme's text on the other theme's ground. uitheme_test.go asserts that no
// token is missing from either table.
//
// No font is embedded. fyne 2.8 already bundles Inter for the sans face and
// DejaVu Sans Mono for the monospace one, so Font delegates entirely to the
// default theme rather than growing the binary by a megabyte to restate it.
package uitheme

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// Theme is the app's fyne.Theme. Construct it with New.
type Theme struct {
	light, dark map[fyne.ThemeColorName]color.Color
	sizes       map[fyne.ThemeSizeName]float32
}

// New returns the app theme. Install it once, before any window is built:
//
//	fyneApp.Settings().SetTheme(uitheme.New())
func New() fyne.Theme {
	return &Theme{light: lightColors, dark: darkColors, sizes: sizes}
}

// Color returns the token for the variant fyne resolved from the OS, falling
// back to fyne's own theme for anything not overridden — never a zero colour,
// which would paint as transparent black.
func (t *Theme) Color(name fyne.ThemeColorName, v fyne.ThemeVariant) color.Color {
	table := t.light
	if v == theme.VariantDark {
		table = t.dark
	}
	if c, ok := table[name]; ok {
		return c
	}
	return theme.DefaultTheme().Color(name, v)
}

// Font delegates to the default theme: fyne already bundles Inter and a
// monospace face, and nothing here improves on them.
func (t *Theme) Font(s fyne.TextStyle) fyne.Resource { return theme.DefaultTheme().Font(s) }

// Icon delegates to the default theme. The tray glyphs are our own (see
// internal/tray/assets) but they never travel through the theme.
func (t *Theme) Icon(n fyne.ThemeIconName) fyne.Resource { return theme.DefaultTheme().Icon(n) }

// Size returns the token, falling back to the default theme for the ones we do
// not restate (scrollbars, inline icons, window chrome).
func (t *Theme) Size(n fyne.ThemeSizeName) float32 {
	if s, ok := t.sizes[n]; ok {
		return s
	}
	return theme.DefaultTheme().Size(n)
}

// OverriddenColorNames lists every colour token these tables define. It exists
// for the test that checks both variants cover the same set — the guard against
// a token added to one table and forgotten in the other.
func OverriddenColorNames() []fyne.ThemeColorName {
	names := make([]fyne.ThemeColorName, 0, len(lightColors))
	for n := range lightColors {
		names = append(names, n)
	}
	return names
}

// rgb builds an opaque colour from a 0xRRGGBB literal, so the tables below read
// as the hex values in the design spec.
func rgb(v uint32) color.Color {
	return color.NRGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 0xff}
}

// The palettes. Neutrals are biased a few points cool rather than being pure
// grey — a pure mid-grey is the thing that reads as unconsidered.
//
// Primary is the ONLY accent, and it is used only for primary actions, focus and
// selection. Success/Warning/Error are semantic and separate from it: they appear
// in the status dot and nowhere else, so "there is an accent colour on screen"
// never has to compete with "the tunnel is up".
var (
	lightColors = map[fyne.ThemeColorName]color.Color{
		theme.ColorNameBackground:        rgb(0xF6F7F9),
		theme.ColorNameHeaderBackground:  rgb(0xFFFFFF),
		theme.ColorNameMenuBackground:    rgb(0xFFFFFF),
		theme.ColorNameOverlayBackground: rgb(0xFFFFFF),
		theme.ColorNameForeground:        rgb(0x171A1F),
		theme.ColorNamePlaceHolder:       rgb(0x5C6470),
		theme.ColorNameDisabled:          rgb(0x5C6470),
		theme.ColorNameSeparator:         rgb(0xE2E5EA),
		theme.ColorNameInputBorder:       rgb(0xE2E5EA),
		theme.ColorNamePrimary:           rgb(0x2F6FEB),

		theme.ColorNameForegroundOnPrimary: rgb(0xFFFFFF),
		theme.ColorNameInputBackground:     rgb(0xFFFFFF),
		theme.ColorNameButton:              rgb(0xFFFFFF),
		theme.ColorNameSuccess:             rgb(0x2E9E5B),
		theme.ColorNameWarning:             rgb(0xB87514),
		theme.ColorNameError:               rgb(0xC4362F),

		// Hover is a wash over whatever is beneath it, so it is the one token
		// specified with alpha rather than as an opaque hex.
		theme.ColorNameHover: color.NRGBA{A: 0x10},
	}

	darkColors = map[fyne.ThemeColorName]color.Color{
		theme.ColorNameBackground:        rgb(0x16181C),
		theme.ColorNameHeaderBackground:  rgb(0x1E2128),
		theme.ColorNameMenuBackground:    rgb(0x1E2128),
		theme.ColorNameOverlayBackground: rgb(0x1E2128),
		theme.ColorNameForeground:        rgb(0xEDEFF2),
		theme.ColorNamePlaceHolder:       rgb(0x9AA2AE),
		theme.ColorNameDisabled:          rgb(0x9AA2AE),
		theme.ColorNameSeparator:         rgb(0x2C313A),
		theme.ColorNameInputBorder:       rgb(0x2C313A),
		theme.ColorNamePrimary:           rgb(0x5B93F5),

		// The dark accent is light enough that white-on-it would be illegible, so
		// the foreground riding on Primary inverts with the variant.
		theme.ColorNameForegroundOnPrimary: rgb(0x0E1013),
		theme.ColorNameInputBackground:     rgb(0x22262E),
		theme.ColorNameButton:              rgb(0x22262E),
		theme.ColorNameSuccess:             rgb(0x41BE77),
		theme.ColorNameWarning:             rgb(0xE0A140),
		theme.ColorNameError:               rgb(0xE86A62),

		theme.ColorNameHover: color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x12},
	}

	// The type scale is deliberately short: fyne offers no font-weight axis
	// beyond bold, so hierarchy comes from size, weight and colour alone, and a
	// long scale would just produce sizes nobody can tell apart.
	sizes = map[fyne.ThemeSizeName]float32{
		theme.SizeNameText:               13,
		theme.SizeNameCaptionText:        11,
		theme.SizeNameSubHeadingText:     15,
		theme.SizeNameHeadingText:        20,
		theme.SizeNamePadding:            5,
		theme.SizeNameInnerPadding:       10,
		theme.SizeNameCardRadius:         8,
		theme.SizeNameButtonRadius:       6,
		theme.SizeNameInputRadius:        6,
		theme.SizeNameSeparatorThickness: 1,
	}
)
