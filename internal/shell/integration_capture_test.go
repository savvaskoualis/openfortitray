package shell_test

import (
	"image/color"
	"image/png"
	"os"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"github.com/savvaskoualis/openfortitray/internal/config"
	"github.com/savvaskoualis/openfortitray/internal/settings"
	"github.com/savvaskoualis/openfortitray/internal/shell"
	"github.com/savvaskoualis/openfortitray/internal/status"
	"github.com/savvaskoualis/openfortitray/internal/tunnel"
	"github.com/savvaskoualis/openfortitray/internal/uitheme"
)

type forced struct {
	fyne.Theme
	v fyne.ThemeVariant
}

func (f forced) Color(n fyne.ThemeColorName, _ fyne.ThemeVariant) color.Color {
	return f.Theme.Color(n, f.v)
}

type host struct{ cfg *config.Config }

func (h *host) Config() *config.Config        { return h.cfg }
func (h *host) Commit(c *config.Config) error { h.cfg = c; return nil }
func (h *host) Connect()                      {}
func (h *host) Disconnect()                   {}
func (h *host) ShowSettings()                 {}
func (h *host) OpenLog()                      {}
func (h *host) GatewayLabel() string          { return "vpn.example.com:10443" }
func (h *host) DTLSLabel() string             { return "DTLS off" }

// TestCaptureWholeApp renders the real window — shell chrome plus the actual
// status and settings content — which is the only render that shows what a user
// gets. The per-package captures each check a piece in isolation; this checks they
// fit together.
func TestCaptureWholeApp(t *testing.T) {
	dir := os.Getenv("OFT_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set OFT_CAPTURE_DIR to capture window PNGs")
	}

	sections := []struct {
		name string
		sec  shell.Section
	}{
		{"status", shell.SectionStatus},
		{"connection", shell.SectionConnection},
		{"advanced", shell.SectionAdvanced},
	}

	for themeName, v := range map[string]fyne.ThemeVariant{"light": 1, "dark": 0} {
		for _, sc := range sections {
			a := test.NewApp()
			a.Settings().SetTheme(forced{Theme: uitheme.New(), v: v})

			work := config.NewProfile("Work VPN")
			work.Gateway = "vpn.example.com"
			work.Port = 10443
			cfg := &config.Config{
				ActiveProfile: "Work VPN",
				Autostart:     true,
				Profiles:      []config.Profile{work, config.NewProfile("Lab gateway")},
			}

			w := test.NewWindow(nil)
			h := &host{cfg: cfg}
			set := settings.New(h, w)
			st := status.New(h, w)
			base := time.Date(2026, 8, 14, 14, 22, 0, 0, time.UTC)
			st.SetClock(func() time.Time { return base.Add(872 * time.Second) })
			st.Apply(tunnel.Event{State: tunnel.Connecting})
			st.Apply(tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.88"})
			set.Apply(tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.88"})

			sh := shell.New(w, shell.Parts{
				Status:     st.Content(),
				Connection: set.ConnectionContent(),
				Advanced:   set.AdvancedContent(),
				ProfileBar: set.ProfileBar(),
				Banner:     set.Banner(),
				Footer:     set.Footer(),
			})
			sh.Select(sc.sec)
			w.Resize(fyne.NewSize(780, 620))

			f, err := os.Create(dir + "/app-" + sc.name + "-" + themeName + ".png")
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
