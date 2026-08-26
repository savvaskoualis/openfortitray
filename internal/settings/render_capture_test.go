package settings

import (
	"image/color"
	"image/png"
	"os"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"

	"github.com/savvaskoualis/openfortitray/internal/config"
	"github.com/savvaskoualis/openfortitray/internal/credstore"
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
			pick  func(*Controller) fyne.CanvasObject
		}{
			{"basic", func(c *Controller) fyne.CanvasObject { return c.ConnectionContent() }},
			{"advanced", func(c *Controller) fyne.CanvasObject { return c.AdvancedContent() }},
		} {
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
			c.Apply(tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.88"})
			// The shell arranges these in production; the capture mirrors that
			// arrangement so the render is what a user would see.
			w.SetContent(container.NewBorder(
				container.NewVBox(c.Banner(), c.ProfileBar()), c.Footer(), nil, nil,
				tab.pick(c)))

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

// TestResetClearsUnsavedIPsecSecret confirms reset() (called by both Show and
// Cancel) discards a typed-but-unsaved IPsec PSK, matching its documented
// contract of discarding "any edits left from a previous session" across the
// whole working copy — not just the profile currently on screen. Before
// ipsecSecretDirty/ipsecSecretValue became per-profile maps,
// loadProfile unconditionally blanked the (then-flat) PSK field on every
// call, so Cancel/Show got this "discard" behavior for free as a side effect
// of the very bug that fix corrected; now that loadProfile reads from the
// maps instead of blindly blanking, reset() has to clear them itself.
func TestResetClearsUnsavedIPsecSecret(t *testing.T) {
	test.NewApp()

	work := config.NewProfile("Work")
	work.Gateway = "vpn.example.com"
	work.Backend = config.BackendIPsec
	work.IPsec.AuthMethod = config.IPsecAuthPSK
	cfg := &config.Config{ActiveProfile: "Work", Profiles: []config.Profile{work}}

	w := test.NewWindow(nil)
	defer w.Close()
	c := New(&captureHost{cfg: cfg}, w)

	c.ipsecSecretEntry.SetText("typed-but-not-saved")
	if !c.ipsecSecretDirty[c.sel] {
		t.Fatal("typing into the PSK entry should have marked it dirty")
	}
	if c.ipsecSecretValue[c.sel] != "typed-but-not-saved" {
		t.Fatalf("ipsecSecretValue[%d] = %q, want the typed text", c.sel, c.ipsecSecretValue[c.sel])
	}

	c.reset() // what both Show and Cancel do

	if c.ipsecSecretEntry.Text != "" {
		t.Errorf("after reset, the PSK entry shows %q, want blank", c.ipsecSecretEntry.Text)
	}
	if c.ipsecSecretDirty[c.sel] {
		t.Error("after reset, the PSK entry should no longer be marked dirty")
	}
	if v, ok := c.ipsecSecretValue[c.sel]; ok {
		t.Errorf("after reset, ipsecSecretValue still holds %q for profile %d, want the map cleared", v, c.sel)
	}
}

// TestSavePersistsAllDirtyIPsecPSKsNotJustSelected is Important #4: typing a
// PSK for profile A, switching to profile B (which — per
// ipsecSecretDirty/ipsecSecretValue being per-profile maps — must NOT lose
// A's unsaved edit), typing a different PSK for B, then hitting Save must
// persist BOTH profiles' PSKs to their own credstore keys, not just the one
// shown in the form when Save was clicked.
func TestSavePersistsAllDirtyIPsecPSKsNotJustSelected(t *testing.T) {
	test.NewApp()
	restore := credstore.SetBackend(credstore.NewMemory())
	defer restore()

	profA := config.NewProfile("A")
	profA.Gateway = "a.example.com"
	profA.Backend = config.BackendIPsec
	profA.IPsec.AuthMethod = config.IPsecAuthPSK

	profB := config.NewProfile("B")
	profB.Gateway = "b.example.com"
	profB.Backend = config.BackendIPsec
	profB.IPsec.AuthMethod = config.IPsecAuthPSK

	cfg := &config.Config{ActiveProfile: "A", Profiles: []config.Profile{profA, profB}}

	w := test.NewWindow(nil)
	defer w.Close()
	c := New(&captureHost{cfg: cfg}, w)

	// Type A's PSK (profile A is shown first, matching ActiveProfile), switch
	// to B, and type a different PSK there.
	c.ipsecSecretEntry.SetText("psk-for-A")
	c.loadProfile(1)
	c.ipsecSecretEntry.SetText("psk-for-B")

	c.save(false)

	gotA, err := credstore.Get(config.IPsecPSKCredstoreKey("a.example.com"))
	if err != nil {
		t.Fatalf("credstore.Get(A): %v", err)
	}
	if gotA != "psk-for-A" {
		t.Errorf("profile A's PSK = %q, want %q — an edit made before switching to B must survive Save", gotA, "psk-for-A")
	}
	gotB, err := credstore.Get(config.IPsecPSKCredstoreKey("b.example.com"))
	if err != nil {
		t.Fatalf("credstore.Get(B): %v", err)
	}
	if gotB != "psk-for-B" {
		t.Errorf("profile B's PSK = %q, want %q", gotB, "psk-for-B")
	}
	if len(c.ipsecSecretDirty) != 0 {
		t.Errorf("ipsecSecretDirty has %d leftover entries after Save, want all cleared: %v", len(c.ipsecSecretDirty), c.ipsecSecretDirty)
	}
	if len(c.ipsecSecretValue) != 0 {
		t.Errorf("ipsecSecretValue has %d leftover entries after Save, want the plaintext dropped from memory: %v", len(c.ipsecSecretValue), c.ipsecSecretValue)
	}
}
