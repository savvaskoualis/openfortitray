package shell

import (
	"image/color"
	"image/png"
	"os"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/savvaskoualis/openfortitray/internal/uitheme"
)

type forcedVariant struct {
	fyne.Theme
	v fyne.ThemeVariant
}

func (f forcedVariant) Color(n fyne.ThemeColorName, _ fyne.ThemeVariant) color.Color {
	return f.Theme.Color(n, f.v)
}

// TestCaptureShell renders the window's chrome with placeholder sections, so the
// navigation, rail and footer arrangement can be looked at without the app.
func TestCaptureShell(t *testing.T) {
	dir := os.Getenv("OFT_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set OFT_CAPTURE_DIR to capture window PNGs")
	}
	for name, v := range map[string]fyne.ThemeVariant{"light": 1, "dark": 0} {
		a := test.NewApp()
		a.Settings().SetTheme(forcedVariant{Theme: uitheme.New(), v: v})

		w := test.NewWindow(nil)
		p, _ := parts()
		p.Status = widget.NewLabel("(status section)")
		s := New(w, p)
		s.Select(SectionConnection)
		w.Resize(fyne.NewSize(width, heightSetting))

		f, err := os.Create(dir + "/shell-" + name + ".png")
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, w.Canvas().Capture()); err != nil {
			t.Fatal(err)
		}
		f.Close()
		w.Close()
	}
}
