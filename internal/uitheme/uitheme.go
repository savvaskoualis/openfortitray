// Package uitheme owns OpenFortiTray's color and size tokens and renders
// them as a Qt stylesheet. The Background token is alpha-bearing on
// purpose: Qt's WA_TranslucentBackground needs the widget's own paint to
// leave native vibrancy (NSVisualEffectView / DWM Acrylic / X11 blur)
// showing through, the same role this token played as Fyne's GL clear
// color.
package uitheme

import "fmt"

// Tokens exposes size constants as methods so call sites read like
// tok.TextSize() rather than a bare package-level constant grab-bag.
type Tokens struct{}

func (Tokens) TextSize() float64           { return 13 }
func (Tokens) CaptionTextSize() float64    { return 11 }
func (Tokens) SubHeadingTextSize() float64 { return 15 }
func (Tokens) HeadingTextSize() float64    { return 20 }
func (Tokens) Padding() float64            { return 5 }
func (Tokens) InnerPadding() float64       { return 10 }
func (Tokens) CardRadius() float64         { return 8 }
func (Tokens) ButtonRadius() float64       { return 6 }
func (Tokens) InputRadius() float64        { return 6 }
func (Tokens) SeparatorThickness() float64 { return 1 }

type palette struct {
	background, headerBackground, menuBackground, overlayBackground string
	backgroundAlpha                                                 uint8
	foreground, placeholder, disabled, separator, inputBorder       string
	primary, foregroundOnPrimary, inputBackground, button           string
	success, warning, error_, hover                                 string
	hoverAlpha                                                      uint8
}

var light = palette{
	background:          "#F6F7F9",
	backgroundAlpha:     0x40,
	headerBackground:    "#FFFFFF",
	menuBackground:      "#FFFFFF",
	overlayBackground:   "#FFFFFF",
	foreground:          "#171A1F",
	placeholder:         "#5C6470",
	disabled:            "#5C6470",
	separator:           "#E2E5EA",
	inputBorder:         "#E2E5EA",
	primary:             "#2F6FEB",
	foregroundOnPrimary: "#FFFFFF",
	inputBackground:     "#FFFFFF",
	button:              "#FFFFFF",
	success:             "#2E9E5B",
	warning:             "#B87514",
	error_:              "#C4362F",
	hover:               "#000000",
	hoverAlpha:          0x10,
}

var dark = palette{
	background:          "#16181C",
	backgroundAlpha:     0x40,
	headerBackground:    "#1E2128",
	menuBackground:      "#1E2128",
	overlayBackground:   "#1E2128",
	foreground:          "#EDEFF2",
	placeholder:         "#9AA2AE",
	disabled:            "#9AA2AE",
	separator:           "#2C313A",
	inputBorder:         "#2C313A",
	primary:             "#5B93F5",
	foregroundOnPrimary: "#0E1013",
	inputBackground:     "#22262E",
	button:              "#22262E",
	success:             "#41BE77",
	warning:             "#E0A140",
	error_:              "#E86A62",
	hover:               "#FFFFFF",
	hoverAlpha:          0x12,
}

// BackgroundColor returns the alpha-bearing background token as separate
// RGBA components, for the glass-attach native code (Task 8) which needs
// the raw alpha value directly rather than a QSS string.
func BackgroundColor(dark bool) (r, g, b, a uint8) {
	p := paletteFor(dark)
	r, g, b = hexToRGB(p.background)
	return r, g, b, p.backgroundAlpha
}

func paletteFor(dark bool) palette {
	if dark {
		return darkPalette()
	}
	return lightPalette()
}

func lightPalette() palette { return light }
func darkPalette() palette  { return dark }

func hexToRGB(hex string) (r, g, b uint8) {
	var ri, gi, bi int
	fmt.Sscanf(hex, "#%02x%02x%02x", &ri, &gi, &bi)
	return uint8(ri), uint8(gi), uint8(bi)
}

// StyleSheet renders the full QSS the app applies once at startup via
// (*qt.QWidget).SetStyleSheet on the central widget — Qt propagates it to
// every descendant widget unless overridden locally.
func StyleSheet(dark bool) string {
	p := paletteFor(dark)
	t := Tokens{}
	return fmt.Sprintf(`
QWidget {
	background: rgba(%[1]s);
	color: %[2]s;
	font-size: %[3]vpx;
}
QPushButton {
	background: %[4]s;
	color: %[2]s;
	border-radius: %[5]vpx;
	padding: 6px 12px;
}
QPushButton:disabled {
	color: %[6]s;
}
QLineEdit, QComboBox {
	background: %[7]s;
	border: 1px solid %[8]s;
	border-radius: %[9]vpx;
	padding: 4px 6px;
}
QLabel[role="caption"] {
	color: %[6]s;
	font-size: %[10]vpx;
}
QLabel[role="headerBackground"] {
	background: %[11]s;
}
QLabel[role="menuBackground"] {
	background: %[12]s;
}
QLabel[role="overlayBackground"] {
	background: %[13]s;
}
QLabel[role="separator"] {
	background: %[14]s;
	height: %[20]vpx;
}
QLabel[role="primary"] {
	color: %[15]s;
}
QLabel[role="foregroundOnPrimary"] {
	color: %[16]s;
}
QLabel[role="hover"] {
	background: rgba(%[17]s);
}
QLabel[role="success"] { color: %[18]s; }
QLabel[role="warning"] { color: %[19]s; }
QLabel[role="error"] { color: %[21]s; }
`,
		rgbaCSS(p.background, p.backgroundAlpha), // 1
		p.foreground,                             // 2
		t.TextSize(),                             // 3
		p.button,                                 // 4
		t.ButtonRadius(),                         // 5
		p.disabled,                               // 6
		p.inputBackground,                        // 7
		p.inputBorder,                            // 8
		t.InputRadius(),                          // 9
		t.CaptionTextSize(),                      // 10
		p.headerBackground,                       // 11
		p.menuBackground,                         // 12
		p.overlayBackground,                      // 13
		p.separator,                              // 14
		p.primary,                                // 15
		p.foregroundOnPrimary,                    // 16
		rgbaCSS(p.hover, p.hoverAlpha),           // 17
		p.success,                                // 18
		p.warning,                                // 19
		t.SeparatorThickness(),                   // 20
		p.error_,                                 // 21
	)
}

func rgbaCSS(hex string, alpha uint8) string {
	r, g, b := hexToRGB(hex)
	return fmt.Sprintf("%d,%d,%d,%d", r, g, b, alpha)
}
