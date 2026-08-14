package status

import (
	"image/color"
	"os"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/savvaskoualis/openfortitray/internal/tunnel"
)

func TestMain(m *testing.M) {
	test.NewApp()
	os.Exit(m.Run())
}

// widgetHigh names the importance the primary button must carry, so the
// assertion reads as intent rather than as an enum constant.
const widgetHigh = widget.HighImportance

func sameColor(a, b color.Color) bool {
	if a == nil || b == nil {
		return a == b
	}
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	return ar == br && ag == bg && ab == bb && aa == ba
}

// activityRows flattens the history rows to "<timestamp> <text>" strings, so the
// assertions are about what is on screen rather than about the widget tree.
// The history is one FormLayout container holding alternating timestamp/text
// labels, so the two columns line up across rows; this pairs them back up.
func activityRows(c *Controller) []string {
	objs := c.activity.Objects
	out := make([]string, 0, len(objs)/2)
	for i := 0; i+1 < len(objs); i += 2 {
		ts, ok1 := objs[i].(*widget.Label)
		txt, ok2 := objs[i+1].(*widget.Label)
		if !ok1 || !ok2 {
			continue
		}
		out = append(out, ts.Text+" "+txt.Text)
	}
	return out
}

type fakeHost struct {
	connects    int
	disconnects int
	settings    int
	logOpens    int
}

func (f *fakeHost) Connect()             { f.connects++ }
func (f *fakeHost) Disconnect()          { f.disconnects++ }
func (f *fakeHost) ShowSettings()        { f.settings++ }
func (f *fakeHost) OpenLog()             { f.logOpens++ }
func (f *fakeHost) GatewayLabel() string { return "vpn.example.com:10443" }
func (f *fakeHost) DTLSLabel() string    { return "DTLS off" }

// newTestController builds a controller on a headless window with a clock the
// test drives, so uptime is deterministic.
func newTestController(t *testing.T) (*Controller, *fakeHost, *time.Time) {
	t.Helper()
	h := &fakeHost{}
	w := test.NewWindow(nil)
	t.Cleanup(w.Close)
	c := New(h, w)
	clock := time.Date(2026, 8, 13, 14, 22, 0, 0, time.UTC)
	c.now = func() time.Time { return clock }
	return c, h, &clock
}

func TestApplyRendersEachState(t *testing.T) {
	cases := []struct {
		name        string
		event       tunnel.Event
		wantState   string
		wantSubHas  string
		wantColor   fyne.ThemeColorName
		wantPrimary string
		wantIP      string
	}{
		{
			// Idle names the gateway it WOULD connect to. "not connected" under a
			// heading reading "Disconnected" says nothing.
			name:        "disconnected names the gateway",
			event:       tunnel.Event{State: tunnel.Disconnected},
			wantState:   "Disconnected",
			wantSubHas:  "vpn.example.com:10443",
			wantColor:   theme.ColorNameDisabled,
			wantPrimary: "Connect",
			wantIP:      "—",
		},
		{
			name:        "authenticating offers Cancel, not Disconnect",
			event:       tunnel.Event{State: tunnel.Authenticating, Detail: "finish signing in in your browser"},
			wantState:   "Authenticating",
			wantSubHas:  "browser",
			wantColor:   theme.ColorNameWarning,
			wantPrimary: "Cancel",
			wantIP:      "—",
		},
		{
			name:        "reconnecting",
			event:       tunnel.Event{State: tunnel.Reconnecting, Detail: "gateway refused the session"},
			wantState:   "Reconnecting",
			wantSubHas:  "gateway refused",
			wantColor:   theme.ColorNameWarning,
			wantPrimary: "Cancel",
			wantIP:      "—",
		},
		{
			name:        "connected shows the gateway",
			event:       tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.88"},
			wantState:   "Connected",
			wantSubHas:  "vpn.example.com",
			wantColor:   theme.ColorNameSuccess,
			wantPrimary: "Disconnect",
			wantIP:      "10.0.0.88",
		},
		{
			name:        "error re-offers Connect",
			event:       tunnel.Event{State: tunnel.Error, Detail: "couldn't connect — click Connect to try again"},
			wantState:   "Error",
			wantSubHas:  "click Connect",
			wantColor:   theme.ColorNameError,
			wantPrimary: "Connect",
			wantIP:      "—",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _, _ := newTestController(t)
			c.Apply(tc.event)

			if c.stateText.Text != tc.wantState {
				t.Errorf("state = %q, want %q", c.stateText.Text, tc.wantState)
			}
			if !strings.Contains(c.subText.Text, tc.wantSubHas) {
				t.Errorf("sub-line = %q, want it to contain %q", c.subText.Text, tc.wantSubHas)
			}
			if want := theme.Color(tc.wantColor); !sameColor(c.dot.FillColor, want) {
				t.Errorf("dot colour = %v, want the %s token %v", c.dot.FillColor, tc.wantColor, want)
			}
			if c.primary.Text != tc.wantPrimary {
				t.Errorf("primary button = %q, want %q", c.primary.Text, tc.wantPrimary)
			}
			if c.ipValue.Text != tc.wantIP {
				t.Errorf("assigned IP = %q, want %q", c.ipValue.Text, tc.wantIP)
			}
			// The protocol row comes from config, so it reads the same in every state
			// — a blank row would look like a bug. The gateway is deliberately NOT
			// repeated here: the hero sub-line carries it (asserted above).
			if !strings.Contains(c.protoValue.Text, "Fortinet") || !strings.Contains(c.protoValue.Text, "DTLS off") {
				t.Errorf("protocol = %q, want it to name Fortinet and the DTLS setting", c.protoValue.Text)
			}
		})
	}
}

// Exactly one high-importance button may be on screen: two competing primary
// actions is the thing that makes a window look unconsidered.
func TestOnlyThePrimaryButtonIsHighImportance(t *testing.T) {
	c, _, _ := newTestController(t)
	c.Apply(tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.88"})
	if c.primary.Importance != widgetHigh {
		t.Errorf("primary importance = %v, want high", c.primary.Importance)
	}
	if c.settingsBtn.Importance == widgetHigh {
		t.Error("the Settings button must not compete with the primary action")
	}
}

func TestPrimaryButtonDrivesTheHost(t *testing.T) {
	cases := []struct {
		state           tunnel.State
		wantConnects    int
		wantDisconnects int
	}{
		{tunnel.Disconnected, 1, 0},
		{tunnel.Error, 1, 0},
		{tunnel.Connected, 0, 1},
		// Cancel routes to Disconnect: tearing down the attempt is what stops a
		// browser login or a reconnect loop.
		{tunnel.Authenticating, 0, 1},
		{tunnel.Connecting, 0, 1},
		{tunnel.Reconnecting, 0, 1},
	}
	for _, tc := range cases {
		t.Run(tc.state.String(), func(t *testing.T) {
			c, h, _ := newTestController(t)
			c.Apply(tunnel.Event{State: tc.state})
			test.Tap(c.primary)
			if h.connects != tc.wantConnects {
				t.Errorf("connects = %d, want %d", h.connects, tc.wantConnects)
			}
			if h.disconnects != tc.wantDisconnects {
				t.Errorf("disconnects = %d, want %d", h.disconnects, tc.wantDisconnects)
			}
		})
	}
}

func TestSettingsAndLogButtonsDriveTheHost(t *testing.T) {
	c, h, _ := newTestController(t)
	test.Tap(c.settingsBtn)
	if h.settings != 1 {
		t.Errorf("ShowSettings called %d times, want 1", h.settings)
	}
	test.Tap(c.logBtn)
	if h.logOpens != 1 {
		t.Errorf("OpenLog called %d times, want 1", h.logOpens)
	}
}

// The uptime is the one thing on screen that changes without an event, so it has
// its own path: Tick recomputes the sub-line and touches nothing else.
func TestUptimeTicksWhileConnected(t *testing.T) {
	c, _, clock := newTestController(t)
	c.Apply(tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.88"})
	if got := c.timerText.Text; got != "00:00:00" {
		t.Fatalf("clock at connect = %q, want 00:00:00", got)
	}
	// The gateway sits on its OWN line now, so a ticking clock cannot shift it.
	if !strings.Contains(c.subText.Text, "vpn.example.com") {
		t.Errorf("sub-line = %q, want the gateway", c.subText.Text)
	}

	*clock = clock.Add(94 * time.Second)
	c.Tick()
	if got := c.timerText.Text; got != "00:01:34" {
		t.Errorf("clock after 94s = %q, want 00:01:34", got)
	}
	if c.sinceValue.Text != "14:22" {
		t.Errorf("connected-since = %q, want the wall-clock time of the connect", c.sinceValue.Text)
	}
}

// Ticking while not connected must not invent an uptime, and must not overwrite
// the state's own detail text.
func TestTickIsANoopWhenNotConnected(t *testing.T) {
	c, _, clock := newTestController(t)
	c.Apply(tunnel.Event{State: tunnel.Reconnecting, Detail: "gateway refused the session"})
	before := c.subText.Text
	*clock = clock.Add(time.Hour)
	c.Tick()
	if c.subText.Text != before {
		t.Errorf("sub-line changed on a disconnected tick: %q -> %q", before, c.subText.Text)
	}
	// And no clock is invented for a state that has no session.
	if c.timerText.Text != "" {
		t.Errorf("clock = %q, want empty when nothing is connected", c.timerText.Text)
	}
	if c.sinceValue.Text != "—" {
		t.Errorf("connected-since = %q, want an em dash", c.sinceValue.Text)
	}
}

// A drop must reset the clock, or the next connect reports an uptime that
// includes the time the tunnel was down.
func TestUptimeResetsAcrossADrop(t *testing.T) {
	c, _, clock := newTestController(t)
	c.Apply(tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.88"})
	*clock = clock.Add(10 * time.Minute)
	c.Apply(tunnel.Event{State: tunnel.Reconnecting, Detail: "dropped"})
	*clock = clock.Add(30 * time.Second)
	c.Apply(tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.88"})

	if got := c.timerText.Text; got != "00:00:00" {
		t.Errorf("clock after a reconnect = %q, want it restarted", got)
	}
	if c.sinceValue.Text != "14:32" {
		t.Errorf("connected-since = %q, want the SECOND connect's time", c.sinceValue.Text)
	}
}

// Re-applying Connected (the supervisor re-reports it on a health check) must not
// restart the clock — that would make a long-lived tunnel permanently read as
// freshly connected.
func TestRepeatedConnectedDoesNotRestartTheClock(t *testing.T) {
	c, _, clock := newTestController(t)
	c.Apply(tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.88"})
	*clock = clock.Add(5 * time.Minute)
	c.Apply(tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.88"})
	if got := c.timerText.Text; got != "00:05:00" {
		t.Errorf("clock = %q, want the original connect time preserved", got)
	}
}

func TestActivityListIsNewestFirst(t *testing.T) {
	c, _, clock := newTestController(t)
	c.Apply(tunnel.Event{State: tunnel.Connecting})
	*clock = clock.Add(time.Second)
	c.Apply(tunnel.Event{State: tunnel.Authenticating})
	*clock = clock.Add(time.Second)
	c.Apply(tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.88"})

	rows := activityRows(c)
	if len(rows) < 3 {
		t.Fatalf("activity has %d rows, want at least 3", len(rows))
	}
	if !strings.Contains(rows[0], "Connected") {
		t.Errorf("first row = %q, want the newest event", rows[0])
	}
	if !strings.Contains(rows[0], "14:22:02") {
		t.Errorf("first row = %q, want it timestamped", rows[0])
	}
	if !strings.Contains(rows[len(rows)-1], "Connecting") {
		t.Errorf("last row = %q, want the oldest event", rows[len(rows)-1])
	}
}

// Closing the window hides it. The app is a tray app: a closed window that quit
// the process would take the tunnel down with it.
func TestCloseHidesRatherThanQuitting(t *testing.T) {
	c, _, _ := newTestController(t)
	if c.closeRequested == nil {
		t.Fatal("no close intercept was installed")
	}
	c.closeRequested()
	// The controller must still be usable afterwards — a closed window is hidden,
	// not torn down.
	c.Apply(tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.88"})
	if c.stateText.Text != "Connected" {
		t.Errorf("after a close the window stopped rendering: %q", c.stateText.Text)
	}
	if fyne.CurrentApp() == nil {
		t.Error("closing the status window must not quit the app")
	}
}

// Opening the history must put it ON SCREEN. It is a hand-rolled disclosure rather
// than a widget.Accordion precisely because the accordion cannot report when it was
// opened: at the closed window height the expanded list started below the last
// pixel, so the one thing the user had just asked to see was the one thing not
// visible. Measured before the fix: 1061px of content in a 560px window.
func TestActivityToggleGrowsTheWindow(t *testing.T) {
	c, _, _ := newTestController(t)

	if c.activityScroll.Visible() {
		t.Error("the history should start folded away")
	}
	c.toggleActivity()
	if !c.activityScroll.Visible() {
		t.Fatal("toggling did not reveal the history")
	}
	if !c.activityOpen {
		t.Error("activityOpen not recorded")
	}
	// The list is bounded, so a long history scrolls instead of demanding an
	// unreachable window height.
	if h := c.activityScroll.MinSize().Height; h != activityHeight {
		t.Errorf("list height = %v, want the bounded %v", h, activityHeight)
	}

	c.toggleActivity()
	if c.activityScroll.Visible() {
		t.Error("toggling again did not fold the history away")
	}
}

// The count is the only clue a folded section gives about whether it holds
// anything.
func TestActivityToggleShowsTheCount(t *testing.T) {
	c, _, clock := newTestController(t)
	if got := c.activityToggle.Text; got != "Activity" {
		t.Errorf("empty history label = %q, want a bare %q", got, "Activity")
	}
	c.Apply(tunnel.Event{State: tunnel.Connecting})
	*clock = clock.Add(time.Second)
	c.Apply(tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.88"})
	if got := c.activityToggle.Text; got != "Activity (2)" {
		t.Errorf("label = %q, want %q", got, "Activity (2)")
	}
}

// A bare clock is ambiguous once the app has been up overnight, which for a VPN
// client that starts at login is the normal case.
func TestOlderEntriesCarryTheirDate(t *testing.T) {
	c, _, clock := newTestController(t)
	// Yesterday, then today.
	*clock = clock.Add(-26 * time.Hour)
	c.Apply(tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.1"})
	*clock = clock.Add(26 * time.Hour)
	c.Apply(tunnel.Event{State: tunnel.Reconnecting, Detail: "dropped"})

	rows := activityRows(c)
	if len(rows) != 2 {
		t.Fatalf("rows = %v, want 2", rows)
	}
	// Newest first: today's entry is a bare clock, yesterday's carries a date.
	if strings.Contains(rows[0], "Aug") {
		t.Errorf("today's row %q should not carry a date", rows[0])
	}
	if !strings.Contains(rows[1], "Aug") {
		t.Errorf("yesterday's row %q should carry its date", rows[1])
	}
}

// Capacity is what makes the history useful during a flap; it was capped at 12 only
// because the list had no scroller.
func TestHistoryKeepsEnoughToBeUseful(t *testing.T) {
	if activityDepth < 50 {
		t.Errorf("activityDepth = %d; a flapping tunnel burns through a dozen transitions in minutes", activityDepth)
	}
}
