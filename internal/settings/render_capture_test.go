package settings

import (
	"image/color"
	"image/png"
	"os"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"github.com/savvaskoualis/openfortitray/internal/config"
	"github.com/savvaskoualis/openfortitray/internal/tunnel"
	"github.com/savvaskoualis/openfortitray/internal/uitheme"
)

// forcedVariant pins a theme to one variant so a capture can render light and
// dark without an OS setting to change.
type forcedVariant struct {
	fyne.Theme
	v fyne.ThemeVariant
}

func (f forcedVariant) Color(n fyne.ThemeColorName, _ fyne.ThemeVariant) color.Color {
	return f.Theme.Color(n, f.v)
}

type captureHost struct{ cfg *config.Config }

func (h *captureHost) Config() *config.Config        { return h.cfg }
func (h *captureHost) Commit(c *config.Config) error { h.cfg = c; return nil }
func (h *captureHost) Connect()                      {}
func (h *captureHost) Disconnect()                   {}

// TestCaptureWindowRenders writes PNGs of the settings window so the grouped
// layout can be looked at without launching the app — which on a machine with a
// live tunnel would mean quitting a working VPN. Env-gated, so it is a no-op in
// CI and in a normal test run:
//
//	OFT_CAPTURE_DIR=/tmp/caps go test ./internal/settings/ -run TestCaptureWindowRenders
func TestCaptureWindowRenders(t *testing.T) {
	dir := os.Getenv("OFT_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set OFT_CAPTURE_DIR to capture window PNGs")
	}

	for name, v := range map[string]fyne.ThemeVariant{"light": 1, "dark": 0} {
		for _, tab := range []struct {
			label string
			index int
		}{{"basic", 0}, {"advanced", 1}} {
			a := test.NewApp()
			a.Settings().SetTheme(forcedVariant{Theme: uitheme.New(), v: v})

			work := config.NewProfile("Work VPN")
			work.Gateway = "vpn.example.com"
			work.Port = 10443
			work.CustomPort = true
			cfg := &config.Config{
				ActiveProfile: "Work VPN",
				Autostart:     true,
				Profiles:      []config.Profile{work, config.NewProfile("Lab gateway")},
			}

			w := test.NewWindow(nil)
			c := New(&captureHost{cfg: cfg}, w)
			c.tabs.SelectIndex(tab.index)
			c.Apply(tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.88"})

			w.Resize(fyne.NewSize(720, 560))
			f, err := os.Create(dir + "/settings-" + tab.label + "-" + name + ".png")
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
}
