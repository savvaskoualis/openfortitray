package tray

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"github.com/savvaskoualis/openfortitray/internal/tunnel"
)

// fakeApp records which App method each menu item invoked, so the wiring from a
// click to the application can be checked without a live menu bar.
type fakeApp struct {
	connects      int
	disconnects   int
	quits         int
	settings      int
	autostartSet  []bool
	autostartOn   bool
	setAutostartE error
}

func (f *fakeApp) Connect()               { f.connects++ }
func (f *fakeApp) Disconnect()            { f.disconnects++ }
func (f *fakeApp) Quit()                  { f.quits++ }
func (f *fakeApp) ShowSettings()          { f.settings++ }
func (f *fakeApp) AutostartEnabled() bool { return f.autostartOn }
func (f *fakeApp) LogPath() string        { return "" }
func (f *fakeApp) Events() <-chan tunnel.Event {
	return make(chan tunnel.Event)
}
func (f *fakeApp) SetAutostart(on bool) error {
	f.autostartSet = append(f.autostartSet, on)
	if f.setAutostartE != nil {
		return f.setAutostartE
	}
	f.autostartOn = on
	return nil
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
		{label: "Disconnect"},
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
	if f.disconnects != 1 {
		t.Errorf("Disconnect item fired %d disconnects, want 1", f.disconnects)
	}
	if f.quits != 1 {
		t.Errorf("Quit item fired %d quits, want 1 (teardown must run, not fyne's default quit)", f.quits)
	}

	// The status item exists, is disabled, and carries no action (it is a label).
	if s := itemByLabel(c.menu, "Disconnected"); s == nil || !s.Disabled || s.Action != nil {
		t.Errorf("status item = %+v, want a disabled, action-less label", s)
	}
	// Disconnect starts disabled: nothing is connected at launch.
	if d := itemByLabel(c.menu, "Disconnect"); d == nil || !d.Disabled {
		t.Error("Disconnect should start disabled")
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

// short() feeds a fixed-width menu item, and its input is process output: many
// lines, arbitrary length, and — because openconnect reports gateway hostnames
// and error text from the server — not necessarily ASCII. Slicing bytes instead
// of runes there would emit a broken UTF-8 sequence into the status line.
func TestShort(t *testing.T) {
	tests := []struct {
		name   string
		detail string
		want   string
	}{{
		name:   "empty stays empty, so the caller can tell there is nothing to append",
		detail: "",
		want:   "",
	}, {
		name:   "a short single line is passed through untouched",
		detail: "10.0.0.5",
		want:   "10.0.0.5",
	}, {
		name:   "surrounding whitespace is dropped",
		detail: "  10.0.0.5\t",
		want:   "10.0.0.5",
	}, {
		// The interesting case: a wrapped openconnect error is many lines, and
		// only the first says what happened.
		name:   "only the first line survives",
		detail: "openconnect exited: exit status 1\nFailed to connect to host vpn.example.com\nmore",
		want:   "openconnect exited: exit status 1",
	}, {
		name:   "carriage returns end a line too",
		detail: "first\r\nsecond",
		want:   "first",
	}, {
		name:   "a line of exactly the cap is not truncated",
		detail: strings.Repeat("a", maxDetail),
		want:   strings.Repeat("a", maxDetail),
	}, {
		name:   "one rune over the cap is truncated and marked",
		detail: strings.Repeat("a", maxDetail+1),
		want:   strings.Repeat("a", maxDetail) + "…",
	}, {
		// Truncation must not split a multi-byte rune: maxDetail runes of a
		// 3-byte character is well past maxDetail bytes.
		name:   "truncation counts runes, not bytes",
		detail: strings.Repeat("パ", maxDetail+5),
		want:   strings.Repeat("パ", maxDetail) + "…",
	}, {
		name:   "whitespace exposed by truncation is trimmed before the ellipsis",
		detail: strings.Repeat("a", maxDetail-1) + "   tail",
		want:   strings.Repeat("a", maxDetail-1) + "…",
	}, {
		name:   "a leading newline yields nothing rather than the second line",
		detail: "\nsomething",
		want:   "",
	}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := short(tc.detail)
			if got != tc.want {
				t.Errorf("short(%q) = %q, want %q", tc.detail, got, tc.want)
			}
			if n := len([]rune(got)); n > maxDetail+1 { // +1 for the ellipsis
				t.Errorf("short(%q) returned %d runes, want at most %d", tc.detail, n, maxDetail+1)
			}
			if strings.ContainsAny(got, "\r\n") {
				t.Errorf("short(%q) = %q, want a single line", tc.detail, got)
			}
			// A byte-level slice would cut a multi-byte rune in half here.
			if !utf8.ValidString(got) {
				t.Errorf("short(%q) = %q, which is not valid UTF-8", tc.detail, got)
			}
		})
	}
}

// The state→appearance mapping is what the user actually reads, and the icon and
// the two menu items have to agree with it: an enabled Disconnect in a state that
// has no tunnel does nothing, and a disabled Connect in a terminal state leaves
// no way out but restarting the app.
func TestViewFor(t *testing.T) {
	nameOf := func(icon []byte) string {
		for _, candidate := range []struct {
			name string
			data []byte
		}{{"gray", iconGray}, {"green", iconGreen}, {"yellow", iconYellow}, {"red", iconRed}} {
			if bytes.Equal(icon, candidate.data) {
				return candidate.name
			}
		}
		return "unknown"
	}

	tests := []struct {
		name           string
		event          tunnel.Event
		wantIcon       string
		wantTitle      string
		wantCanConnect bool
	}{{
		name:           "disconnected",
		event:          tunnel.Event{State: tunnel.Disconnected},
		wantIcon:       "gray",
		wantTitle:      "Disconnected",
		wantCanConnect: true,
	}, {
		name:      "connected shows the assigned address",
		event:     tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.5"},
		wantIcon:  "green",
		wantTitle: "Connected — 10.0.0.5",
	}, {
		// The IP arrives with the Connected event, but a run that reports up
		// without one must not render a dangling separator.
		name:      "connected without an address has no trailing dash",
		event:     tunnel.Event{State: tunnel.Connected},
		wantIcon:  "green",
		wantTitle: "Connected",
	}, {
		name:      "authenticating",
		event:     tunnel.Event{State: tunnel.Authenticating},
		wantIcon:  "yellow",
		wantTitle: "Authenticating…",
	}, {
		name:      "connecting",
		event:     tunnel.Event{State: tunnel.Connecting},
		wantIcon:  "yellow",
		wantTitle: "Connecting…",
	}, {
		name:      "reconnecting carries the reason instead of the ellipsis",
		event:     tunnel.Event{State: tunnel.Reconnecting, Detail: "openconnect exited: exit status 1\ndetail"},
		wantIcon:  "yellow",
		wantTitle: "Reconnecting — openconnect exited: exit status 1",
	}, {
		// Error is terminal: no Disconnected follows it, so Connect has to be
		// clickable from here or the app needs a restart to retry.
		name:           "error re-enables connect",
		event:          tunnel.Event{State: tunnel.Error, Detail: "gateway not set — see config.json"},
		wantIcon:       "red",
		wantTitle:      "Error: gateway not set — see config.json",
		wantCanConnect: true,
	}, {
		name:           "error without a detail still says error",
		event:          tunnel.Event{State: tunnel.Error},
		wantIcon:       "red",
		wantTitle:      "Error",
		wantCanConnect: true,
	}, {
		// A state the supervisor does not currently emit must still render
		// something safe rather than an empty menu item.
		name:           "unknown state falls back to disconnected",
		event:          tunnel.Event{State: tunnel.State(99)},
		wantIcon:       "gray",
		wantTitle:      "Disconnected",
		wantCanConnect: true,
	}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := viewFor(tc.event)
			if name := nameOf(got.icon); name != tc.wantIcon {
				t.Errorf("icon = %s, want %s", name, tc.wantIcon)
			}
			if got.title != tc.wantTitle {
				t.Errorf("title = %q, want %q", got.title, tc.wantTitle)
			}
			if got.canConnect != tc.wantCanConnect {
				t.Errorf("canConnect = %v, want %v (Disconnect gets the opposite)",
					got.canConnect, tc.wantCanConnect)
			}
			if len(got.icon) == 0 {
				t.Error("no icon: systray.SetIcon indexes iconBytes[0] and would panic")
			}
			if n := len([]rune(got.title)); n > maxDetail+32 {
				t.Errorf("title is %d runes (%q); the status item is not that wide", n, got.title)
			}
		})
	}
}
