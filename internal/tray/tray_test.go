package tray

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"github.com/savvaskoualis/openfortitray/internal/tunnel"
	"github.com/savvaskoualis/openfortitray/internal/uistate"
)

// fakeApp records which App method each menu item invoked, so the wiring from a
// click to the application can be checked without a live menu bar.
type fakeApp struct {
	connects      int
	disconnects   int
	quits         int
	settings      int
	statusShows   int
	autostartSet  []bool
	autostartOn   bool
	setAutostartE error
	updateClicks  int
}

func (f *fakeApp) Connect()               { f.connects++ }
func (f *fakeApp) Disconnect()            { f.disconnects++ }
func (f *fakeApp) Quit()                  { f.quits++ }
func (f *fakeApp) ShowSettings()          { f.settings++ }
func (f *fakeApp) ShowStatus()            { f.statusShows++ }
func (f *fakeApp) AutostartEnabled() bool { return f.autostartOn }
func (f *fakeApp) LogPath() string        { return "" }
func (f *fakeApp) Version() string        { return "v9.9.9-test" }
func (f *fakeApp) UpdateClicked()         { f.updateClicks++ }
func (f *fakeApp) SetAutostart(on bool) error {
	f.autostartSet = append(f.autostartSet, on)
	if f.setAutostartE != nil {
		return f.setAutostartE
	}
	f.autostartOn = on
	return nil
}

// The update row starts as a manual check and its Action must reach
// UpdateClicked; SetUpdateAvailable must relabel it to the one-click offer.
func TestUpdateItemWiresAndRelabels(t *testing.T) {
	test.NewTempApp(t) // CurrentApp so (*Menu).Refresh() is a safe no-op

	f := &fakeApp{}
	c := newController(f)

	it := itemByLabel(c.menu, "Check for Updates…")
	if it == nil {
		t.Fatal("update item not found by its initial label")
	}
	it.Action()
	if f.updateClicks != 1 {
		t.Errorf("update action fired %d UpdateClicked calls, want 1", f.updateClicks)
	}

	c.SetUpdateAvailable("v1.2.3")
	if c.updateItem.Label != "Update to v1.2.3 & Restart" {
		t.Errorf("after SetUpdateAvailable label = %q, want the one-click offer", c.updateItem.Label)
	}
	if itemByLabel(c.menu, "Check for Updates…") != nil {
		t.Error("old update label still present after relabel")
	}
}

// Once an update is available the menu-bar icon must carry the red dot: for each
// state icon, resourceFor returns a badged variant that is non-nil and not
// byte-equal to the plain resource (a pixel-exact check is unnecessary). Before
// SetUpdateAvailable it returns the plain resource unchanged.
func TestUpdateBadgeOverlaysTrayIcon(t *testing.T) {
	test.NewTempApp(t) // CurrentApp so (*Menu).Refresh() is a safe no-op

	f := &fakeApp{}
	c := newController(f)

	for _, tc := range []struct {
		name string
		icon []byte
	}{
		{"gray", iconGray},
		{"green", iconGreen},
		{"yellow", iconYellow},
		{"red", iconRed},
	} {
		if got := c.resourceFor(tc.icon); !bytes.Equal(got.Content(), tc.icon) {
			t.Errorf("%s: before an update, resourceFor should return the plain icon", tc.name)
		}
	}

	c.SetUpdateAvailable("v9")
	if !c.updateAvailable {
		t.Fatal("SetUpdateAvailable must set updateAvailable")
	}

	for _, tc := range []struct {
		name string
		icon []byte
	}{
		{"gray", iconGray},
		{"green", iconGreen},
		{"yellow", iconYellow},
		{"red", iconRed},
	} {
		got := c.resourceFor(tc.icon)
		if got == nil {
			t.Fatalf("%s: badged resource is nil", tc.name)
		}
		if len(got.Content()) == 0 {
			t.Errorf("%s: badged resource has no bytes", tc.name)
		}
		if bytes.Equal(got.Content(), tc.icon) {
			t.Errorf("%s: badged resource is byte-equal to the plain icon; the red dot was not composed", tc.name)
		}
	}
}

func itemByLabel(m *fyne.Menu, label string) *fyne.MenuItem {
	for _, it := range m.Items {
		if it.Label == label {
			return it
		}
	}
	return nil
}

// A tray click must reach the matching App method. systray used per-item
// channels; fyne uses per-item Action closures — this asserts each closure calls
// the method the old channel case used to.
func TestMenuActionsWireToApp(t *testing.T) {
	test.NewTempApp(t) // establishes CurrentApp so (*Menu).Refresh() is a safe no-op

	f := &fakeApp{}
	c := newController(f)

	for _, tc := range []struct {
		label   string
		invoke  func()
		wantErr string
	}{
		{label: "Connect"},
		{label: "Quit"},
	} {
		it := itemByLabel(c.menu, tc.label)
		if it == nil {
			t.Fatalf("menu has no %q item", tc.label)
		}
		if it.Action == nil {
			t.Fatalf("%q item has no action", tc.label)
		}
		it.Action()
	}
	if f.connects != 1 {
		t.Errorf("Connect item fired %d connects, want 1", f.connects)
	}
	if f.quits != 1 {
		t.Errorf("Quit item fired %d quits, want 1 (teardown must run, not fyne's default quit)", f.quits)
	}

	// The title row is the first item: a fixed, disabled, action-less header that
	// names the app in the popover. It sits above the status line with a
	// separator between them.
	if len(c.menu.Items) == 0 {
		t.Fatal("menu has no items")
	}
	// One header row carries identity AND build: two rows said nothing the one row
	// does not, and the version is only ever read in the same glance as the name.
	title := c.menu.Items[0]
	if title.Label != "OpenFortiTray "+f.Version() || !title.Disabled || title.Action != nil {
		t.Errorf("first item = %+v, want a disabled, action-less \"OpenFortiTray <version>\" header", title)
	}
	if len(c.menu.Items) < 2 || !c.menu.Items[1].IsSeparator {
		t.Error("the header must be followed by a separator, then the status line")
	}

	// The status item exists, is disabled, and carries no action (it is a label).
	if s := itemByLabel(c.menu, "Disconnected"); s == nil || !s.Disabled || s.Action != nil {
		t.Errorf("status item = %+v, want a disabled, action-less label", s)
	}
	// There is exactly ONE connection row, and at launch it offers Connect. The old
	// menu had a Connect and a Disconnect row of which one was always greyed out.
	if d := itemByLabel(c.menu, "Disconnect"); d != nil {
		t.Error("a separate Disconnect row is dead weight; the action row relabels instead")
	}
	// View logs is present and wired (side-effecting, so not invoked here).
	if l := itemByLabel(c.menu, "View logs"); l == nil || l.Action == nil {
		t.Error("View logs item should exist with an action")
	}

	// Settings… opens the (already-built, hidden) settings window.
	s := itemByLabel(c.menu, "Settings…")
	if s == nil || s.Action == nil {
		t.Fatal("Settings… item should exist with an action")
	}
	s.Action()
	if f.settings != 1 {
		t.Errorf("Settings… fired %d ShowSettings, want 1", f.settings)
	}
}

// The auto-connect checkbox toggles the login item and only then flips the
// checkmark; a failed SetAutostart leaves the mark where it was.
func TestAutostartToggle(t *testing.T) {
	test.NewTempApp(t)

	t.Run("success flips the checkmark", func(t *testing.T) {
		f := &fakeApp{autostartOn: false}
		c := newController(f)
		auto := itemByLabel(c.menu, "Auto-connect at login")
		if auto == nil {
			t.Fatal("no auto-connect item")
		}
		if auto.Checked {
			t.Fatal("auto-connect should start unchecked (AutostartEnabled=false)")
		}
		auto.Action()
		if len(f.autostartSet) != 1 || f.autostartSet[0] != true {
			t.Errorf("SetAutostart calls = %v, want [true]", f.autostartSet)
		}
		if !auto.Checked {
			t.Error("checkmark should be set after a successful enable")
		}
	})

	t.Run("failure leaves the checkmark unchanged", func(t *testing.T) {
		f := &fakeApp{autostartOn: false, setAutostartE: errFake}
		c := newController(f)
		auto := itemByLabel(c.menu, "Auto-connect at login")
		auto.Action()
		if auto.Checked {
			t.Error("checkmark must not change when SetAutostart fails")
		}
	})
}

var errFake = &fakeErr{}

type fakeErr struct{}

func (*fakeErr) Error() string { return "autostart failed" }

// The state→appearance mapping now lives in internal/uistate and is tested
// there. What stays this package's job is turning a view's severity into a tray
// glyph — and the glyph must never be empty, because systray.SetIcon indexes
// iconBytes[0] and would panic on a zero-length slice.
func TestIconForKind(t *testing.T) {
	nameOf := func(icon []byte) string {
		for _, c := range []struct {
			name string
			data []byte
		}{{"gray", iconGray}, {"green", iconGreen}, {"yellow", iconYellow}, {"red", iconRed}} {
			if bytes.Equal(icon, c.data) {
				return c.name
			}
		}
		return "unknown"
	}
	cases := []struct {
		kind uistate.Kind
		want string
	}{
		{uistate.KindIdle, "gray"},
		{uistate.KindBusy, "yellow"},
		{uistate.KindOK, "green"},
		{uistate.KindBad, "red"},
		// A Kind this package has not been taught about must still produce a real
		// icon rather than nothing.
		{uistate.Kind(99), "gray"},
	}
	for _, tc := range cases {
		got := iconFor(tc.kind)
		if len(got) == 0 {
			t.Fatalf("kind %v produced an empty icon; systray.SetIcon would panic", tc.kind)
		}
		if n := nameOf(got); n != tc.want {
			t.Errorf("kind %v = %s icon, want %s", tc.kind, n, tc.want)
		}
	}
}

// Disconnect stays clickable through the busy states, because it is the only way
// out of a connect that hangs or a reconnect loop that will not settle — a state
// this app reaches for real. uistate.View.CanDisconnect is false there (no tunnel
// exists yet) and wiring the row to it would silently strip that escape hatch, so
// this asserts the row's enablement against the states rather than against the
// field.
func TestActionRowMatchesTheState(t *testing.T) {
	test.NewTempApp(t)

	cases := []struct {
		state     tunnel.State
		wantLabel string
		// which host method the row must call
		wantConnects, wantDisconnects int
	}{
		{tunnel.Disconnected, "Connect", 1, 0},
		{tunnel.Error, "Connect", 1, 0},
		{tunnel.Connected, "Disconnect", 0, 1},
		// In flight: there is no connection yet, so the row says Cancel — but it
		// must stay CLICKABLE, because it is the only way out of a connect that
		// hangs or a retry loop that will not settle.
		{tunnel.Authenticating, "Cancel", 0, 1},
		{tunnel.Connecting, "Cancel", 0, 1},
		{tunnel.Reconnecting, "Cancel", 0, 1},
	}
	for _, tc := range cases {
		t.Run(tc.state.String(), func(t *testing.T) {
			f := &fakeApp{}
			c := newController(f)
			c.Apply(tunnel.Event{State: tc.state})
			if c.actionItem.Label != tc.wantLabel {
				t.Errorf("label = %q, want %q", c.actionItem.Label, tc.wantLabel)
			}
			if c.actionItem.Disabled {
				t.Error("the action row must never be disabled; it is the only connection control")
			}
			if c.actionItem.Action == nil {
				t.Fatal("the action row has no action")
			}
			c.actionItem.Action()
			if f.connects != tc.wantConnects || f.disconnects != tc.wantDisconnects {
				t.Errorf("fired %d connects / %d disconnects, want %d / %d",
					f.connects, f.disconnects, tc.wantConnects, tc.wantDisconnects)
			}
		})
	}
}

// The Open row opens the app's window — named for what it does now that Status is
// a section of one window rather than a window of its own.
func TestOpenItemOpensTheWindow(t *testing.T) {
	test.NewTempApp(t)
	f := &fakeApp{}
	c := newController(f)

	it := itemByLabel(c.menu, "Open")
	if it == nil || it.Action == nil {
		t.Fatal("Open item should exist with an action")
	}
	it.Action()
	if f.statusShows != 1 {
		t.Errorf("Open fired %d ShowStatus calls, want 1", f.statusShows)
	}
}

// Apply must keep the fyne items in step: they are what the menu renders, and the
// labels asserted here are the tray's long-standing wording — unchanged by the
// move of the mapping into internal/uistate.
func TestApplyUpdatesFyneItemsAsFallback(t *testing.T) {
	test.NewTempApp(t) // CurrentApp so (*Menu).Refresh() is a safe no-op
	c := newController(&fakeApp{})

	c.Apply(tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.5"})
	if !strings.Contains(c.statusItem.Label, "Connected") {
		t.Errorf("status label = %q, want it to mention Connected", c.statusItem.Label)
	}
	if c.actionItem.Label != "Disconnect" {
		t.Errorf("action row = %q, want Disconnect while connected", c.actionItem.Label)
	}

	c.Apply(tunnel.Event{State: tunnel.Disconnected})
	if c.actionItem.Label != "Connect" {
		t.Errorf("action row = %q, want Connect while disconnected", c.actionItem.Label)
	}
	// And the view is remembered, so a later takeover adopts the current state
	// instead of resetting the tray to the defaults.
	if c.lastView.Title == "" {
		t.Error("Apply must record the view it rendered for the native takeover to adopt")
	}
}

// The autostart toggle must not tick the row when persisting the login item fails
// — the menu would then claim a state the OS does not have.
func TestAutostartToggleLeavesRowUnchangedOnFailure(t *testing.T) {
	test.NewTempApp(t) // CurrentApp so (*Menu).Refresh() is a safe no-op
	app := &fakeApp{setAutostartE: errors.New("nope")}
	c := newController(app)

	before := c.autoItem.Checked
	c.toggleAutostart()
	if c.autoItem.Checked != before {
		t.Errorf("checkmark = %v after a failed SetAutostart, want it unchanged (%v)", c.autoItem.Checked, before)
	}
}
