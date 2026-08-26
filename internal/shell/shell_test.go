package shell

import (
	"os"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

func TestMain(m *testing.M) {
	test.NewApp()
	os.Exit(m.Run())
}

// parts builds three distinguishable sections plus the settings-only furniture.
func parts() (Parts, map[string]fyne.CanvasObject) {
	obj := map[string]fyne.CanvasObject{
		"status":     widget.NewLabel("status"),
		"connection": widget.NewLabel("connection"),
		"advanced":   widget.NewLabel("advanced"),
		"profileBar": widget.NewLabel("profiles"),
		"banner":     widget.NewLabel("banner"),
		"footer":     widget.NewLabel("footer"),
	}
	return Parts{
		Status:     obj["status"],
		Connection: obj["connection"],
		Advanced:   obj["advanced"],
		ProfileBar: obj["profileBar"],
		Banner:     obj["banner"],
		Footer:     obj["footer"],
	}, obj
}

func newShell(t *testing.T) (*Shell, fyne.Window, map[string]fyne.CanvasObject) {
	t.Helper()
	w := test.NewWindow(nil)
	t.Cleanup(w.Close)
	p, obj := parts()
	return New(w, p), w, obj
}

// Exactly one section is visible at a time; the others must be hidden rather than
// merely covered, or their height still counts toward the layout.
func TestOneSectionVisibleAtATime(t *testing.T) {
	s, _, obj := newShell(t)

	cases := []struct {
		sec  Section
		name string
	}{
		{SectionStatus, "status"},
		{SectionConnection, "connection"},
		{SectionAdvanced, "advanced"},
	}
	for _, tc := range cases {
		s.Select(tc.sec)
		for _, other := range cases {
			want := other.name == tc.name
			if got := obj[other.name].Visible(); got != want {
				t.Errorf("on %s: %s visible = %v, want %v", tc.name, other.name, got, want)
			}
		}
		if s.Current() != tc.sec {
			t.Errorf("Current() = %v, want %v", s.Current(), tc.sec)
		}
	}
}

// The profile selector and the Save strip belong to the settings sections. On
// Status they would be controls with nothing to act on.
func TestSettingsFurnitureHidesOnStatus(t *testing.T) {
	s, _, obj := newShell(t)

	s.Select(SectionStatus)
	for _, name := range []string{"profileBar", "footer", "banner"} {
		if obj[name].Visible() {
			t.Errorf("%s should be hidden on Status", name)
		}
	}

	for _, sec := range []Section{SectionConnection, SectionAdvanced} {
		s.Select(sec)
		for _, name := range []string{"profileBar", "footer"} {
			if !obj[name].Visible() {
				t.Errorf("%s should be visible on section %v", name, sec)
			}
		}
	}
}

// The banner is raised only by a refused Connect, so navigating must never turn it
// ON — only off, when leaving the sections it belongs to.
func TestNavigationNeverRaisesTheBanner(t *testing.T) {
	s, _, obj := newShell(t)
	obj["banner"].Hide()

	s.Select(SectionConnection)
	if obj["banner"].Visible() {
		t.Error("navigating to a settings section must not raise the banner by itself")
	}
}

// Closing the window hides it. This is a tray app: a close that quit the process
// would take the tunnel down with it, and the only way out is the tray's Quit.
func TestCloseHidesRatherThanQuitting(t *testing.T) {
	s, _, _ := newShell(t)

	if s.closeRequested == nil {
		t.Fatal("no close intercept was installed")
	}
	s.closeRequested()
	// The shell must still work afterwards: a closed window is hidden, not torn down.
	s.Select(SectionAdvanced)
	if s.Current() != SectionAdvanced {
		t.Error("the shell stopped working after a close; it should merely have hidden")
	}
	if fyne.CurrentApp() == nil {
		t.Error("closing the window must not quit the app")
	}
}

// Reveal is the one entry point that shows the window; the glass attach
// must run every time it does, not just the first, since Hide/Reveal is
// a normal cycle for this window (the tray hides it, Reveal brings it
// back) and RunNative's handle can only be trusted once a window is
// actually visible.
func TestRevealAttachesGlassEveryTime(t *testing.T) {
	s, _, _ := newShell(t)
	var calls int
	s.AttachGlass = func(w fyne.Window) { calls++ }

	s.Reveal(SectionStatus)
	s.win.Hide()
	s.Reveal(SectionStatus)

	if calls != 2 {
		t.Errorf("AttachGlass called %d times across two Reveals, want 2", calls)
	}
}

// A nil AttachGlass (the zero value, and every existing shell test's
// configuration) must not panic Reveal.
func TestRevealToleratesNilAttachGlass(t *testing.T) {
	s, _, _ := newShell(t)
	s.Reveal(SectionStatus) // must not panic
}

// A section asking for more room must not shrink the window below the height its
// section needs, and giving up the request must return it.
func TestHeightRequestRaisesAndReleases(t *testing.T) {
	s, _, _ := newShell(t)
	s.Select(SectionStatus)

	s.RequestHeight(heightStatus + 200)
	if s.extraHeight != heightStatus+200 {
		t.Fatalf("extraHeight = %v, want the request to be remembered", s.extraHeight)
	}
	// Switching away and back keeps the request: the history is still open.
	s.Select(SectionConnection)
	s.Select(SectionStatus)
	if s.extraHeight != heightStatus+200 {
		t.Error("the height request was forgotten across a navigation")
	}

	s.RequestHeight(0)
	if s.extraHeight != 0 {
		t.Error("giving up the request should release the extra height")
	}
}
