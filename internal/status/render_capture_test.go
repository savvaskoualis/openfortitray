package status

import (
	"image/color"
	"image/png"
	"os"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"github.com/savvaskoualis/openfortitray/internal/tunnel"
	"github.com/savvaskoualis/openfortitray/internal/uitheme"
)

// forcedVariant pins a theme to one variant, so a capture can render light and
// dark without an OS setting to change.
type forcedVariant struct {
	fyne.Theme
	v fyne.ThemeVariant
}

func (f forcedVariant) Color(n fyne.ThemeColorName, _ fyne.ThemeVariant) color.Color {
	return f.Theme.Color(n, f.v)
}

// TestCaptureWindowRenders renders the window to PNGs so the layout can be looked
// at without launching the app — which would fight the single-instance lock and,
// on a machine with a live tunnel, mean quitting a working VPN.
//
// This is not decoration. It caught a real bug on its first run: the primary
// button was built with an empty label, so its minimum size was computed for no
// text and "Disconnect" rendered outside the button. No unit test would have
// noticed, because the widget's Text field was correct — only the geometry was.
//
// It writes only when OFT_CAPTURE_DIR is set, so it is a no-op in CI and in a
// normal test run:
//
//	OFT_CAPTURE_DIR=/tmp/caps go test ./internal/status/ -run TestCaptureWindowRenders
func TestCaptureWindowRenders(t *testing.T) {
	dir := os.Getenv("OFT_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set OFT_CAPTURE_DIR to capture window PNGs")
	}

	events := map[string]tunnel.Event{
		"connected":    {State: tunnel.Connected, Detail: "10.0.0.88"},
		"reconnecting": {State: tunnel.Reconnecting, Detail: "gateway refused the session — signing in again"},
		"disconnected": {State: tunnel.Disconnected},
	}
	variants := map[string]fyne.ThemeVariant{"light": 1, "dark": 0}

	for vname, v := range variants {
		for ename, e := range events {
			a := test.NewApp()
			a.Settings().SetTheme(forcedVariant{Theme: uitheme.New(), v: v})

			w := test.NewWindow(nil)
			c := New(&fakeHost{}, w)
			base := time.Date(2026, 8, 13, 14, 22, 0, 0, time.UTC)
			c.now = func() time.Time { return base }
			c.Apply(tunnel.Event{State: tunnel.Connecting})
			c.now = func() time.Time { return base.Add(3 * time.Second) }
			c.Apply(tunnel.Event{State: tunnel.Authenticating, Detail: "finish signing in in your browser"})
			c.now = func() time.Time { return base.Add(6 * time.Second) }
			c.Apply(e)
			if e.State == tunnel.Connected {
				c.now = func() time.Time { return base.Add(6*time.Second + 872*time.Second) }
				c.Tick()
			}

			w.Resize(fyne.NewSize(680, 520))
			img := w.Canvas().Capture()
			f, err := os.Create(dir + "/status-" + ename + "-" + vname + ".png")
			if err != nil {
				t.Fatal(err)
			}
			if err := png.Encode(f, img); err != nil {
				t.Fatal(err)
			}
			f.Close()
			w.Close()
		}
	}
}
