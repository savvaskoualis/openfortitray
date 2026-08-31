// Package uitheme owns OpenFortiTray's color and size tokens and renders
// them as a Qt stylesheet. The Background token is alpha-bearing on
// purpose: Qt's WA_TranslucentBackground needs the widget's own paint to
// leave native vibrancy (NSVisualEffectView / DWM Acrylic / X11 blur)
// showing through, the same role this token played as Fyne's GL clear
// color.
package uitheme

import (
	"fmt"

	qt "github.com/mappu/miqt/qt6"
)

// Tokens exposes size constants as methods so call sites read like
// tok.TextSize() rather than a bare package-level constant grab-bag.
type Tokens struct{}

func (Tokens) TextSize() float64           { return 13 }
func (Tokens) CaptionTextSize() float64    { return 11 }
func (Tokens) SubHeadingTextSize() float64 { return 15 }
func (Tokens) HeadingTextSize() float64    { return 20 }
func (Tokens) Padding() float64            { return 5 }
func (Tokens) InnerPadding() float64       { return 10 }
func (Tokens) CardRadius() float64         { return 14 }
func (Tokens) ButtonRadius() float64       { return 10 }
func (Tokens) InputRadius() float64        { return 10 }
func (Tokens) SeparatorThickness() float64 { return 1 }
func (Tokens) StatusDotDiameter() float64  { return 22 }
func (Tokens) FieldMinHeight() float64     { return 20 }
func (Tokens) FieldTextSize() float64      { return 14 }

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
	border: 1px solid rgba(%[30]s);
	border-radius: %[5]vpx;
	padding: 7px 14px;
	font-weight: 600;
}
QPushButton:disabled {
	color: %[6]s;
}
QPushButton:checked {
	background: %[15]s;
	color: %[16]s;
	border: none;
}
QPushButton:hover {
	background: rgba(%[17]s);
}
QPushButton:pressed {
	background: rgba(%[17]s);
	color: %[2]s;
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
QLabel[role="sectionHeader"] {
	color: %[15]s;
	font-size: %[10]vpx;
	font-weight: 700;
	padding-top: 6px;
	padding-bottom: 2px;
	border-bottom: 1px solid rgba(%[29]s);
	margin-bottom: 4px;
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
	min-height: %[20]vpx;
	max-height: %[20]vpx;
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
QWidget[role="card"] {
	background: rgba(%[22]s);
	border: 1px solid rgba(%[30]s);
	border-radius: %[23]vpx;
	padding: 12px 16px;
}
QPushButton[role="danger"] {
	background: %[21]s;
	color: %[16]s;
	border: none;
}
QPushButton[role="danger"]:hover {
	background: rgba(%[24]s);
}
QPushButton[role="success"] {
	background: %[18]s;
	color: %[16]s;
	border: none;
}
QPushButton[role="success"]:hover {
	background: rgba(%[25]s);
}
QLabel#statusDot {
	border-radius: %[26]vpx;
}
QLabel#statusDot[role="success"] { background: %[18]s; }
QLabel#statusDot[role="warning"] { background: %[19]s; }
QLabel#statusDot[role="error"] { background: %[21]s; }
QLabel#statusDot[role="caption"] { background: %[6]s; }
QLineEdit, QComboBox {
	min-height: %[27]vpx;
	padding: 8px 10px;
	font-size: %[28]vpx;
}
QLineEdit:focus, QComboBox:focus {
	border: 1px solid %[15]s;
}
QComboBox:hover {
	border: 1px solid %[15]s;
}
QComboBox::drop-down {
	border: none;
	width: 24px;
}
QComboBox::down-arrow {
	image: none;
	border-left: 4px solid transparent;
	border-right: 4px solid transparent;
	border-top: 5px solid %[6]s;
	width: 0;
	height: 0;
	margin-right: 8px;
}
QComboBox QAbstractItemView {
	background: %[7]s;
	border: 1px solid %[8]s;
	border-radius: %[9]vpx;
	outline: none;
	padding: 4px;
	selection-background-color: %[15]s;
	selection-color: %[16]s;
}
QComboBox QAbstractItemView::item {
	min-height: %[27]vpx;
	padding: 4px 8px;
	border-radius: %[5]vpx;
}
QProgressBar {
	background: %[7]s;
	border: none;
	border-radius: %[26]vpx;
}
QProgressBar::chunk {
	background: %[19]s;
	border-radius: %[26]vpx;
}
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
		rgbaCSS(p.overlayBackground, 0x33),       // 22 — card fill, a raised surface distinct from the ambient wash
		t.CardRadius(),                           // 23
		rgbaCSS(p.error_, 0xE6),                  // 24 — danger button hover, slightly translucent
		rgbaCSS(p.success, 0xE6),                 // 25 — success button hover, slightly translucent
		t.StatusDotDiameter()/2,                  // 26 — radius = half the widget's fixed diameter, for a true circle
		t.FieldMinHeight(),                       // 27
		t.FieldTextSize(),                        // 28
		rgbaCSS(p.primary, 0x30),                 // 29 — sectionHeader's underline, a soft accent-tinted hairline
		rgbaCSS(p.foreground, 0x16),              // 30 — a near-invisible hairline for card/button edge definition
	)
}

// StatusDotDiameter is the fixed width/height internal/status sets on the
// state badge widget — exported so that widget and this package's QSS
// border-radius rule can never drift out of sync with each other.
func StatusDotDiameter() float64 { return Tokens{}.StatusDotDiameter() }

// Elevate gives w a soft drop shadow — the "lifted off the background" look
// Material/Fyne-style designs use for cards and primary actions. QSS has no
// box-shadow property, so this is the one piece of the modern look that has
// to be a real QGraphicsEffect rather than a stylesheet rule.
func Elevate(w *qt.QWidget) {
	shadow := qt.NewQGraphicsDropShadowEffect()
	shadow.SetBlurRadius(28)
	shadow.SetOffset2(0, 6)
	shadowColor := qt.NewQColor()
	shadowColor.SetRgb(0, 0, 0)
	shadowColor.SetAlpha(70)
	shadow.SetColor(shadowColor)
	w.SetGraphicsEffect(shadow.QGraphicsEffect)
}

func rgbaCSS(hex string, alpha uint8) string {
	r, g, b := hexToRGB(hex)
	return fmt.Sprintf("%d,%d,%d,%d", r, g, b, alpha)
}
