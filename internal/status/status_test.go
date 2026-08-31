package status

import (
	"os"
	"strings"
	"testing"
	"time"

	qt "github.com/mappu/miqt/qt6"

	"github.com/savvaskoualis/openfortitray/internal/tunnel"
)

func TestMain(m *testing.M) {
	// The offscreen platform plugin is Qt's own documented mechanism for
	// headless test/CI environments — GitHub Actions runners have no logged-in
	// GUI session, so constructing real native windows without it risks a
	// crash during teardown (reproduced directly on two machines before this
	// was added).
	os.Setenv("QT_QPA_PLATFORM", "offscreen")
	qt.NewQApplication(os.Args)
	os.Exit(m.Run())
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

// newTestController builds a controller on a headless (never-shown) window with a
// clock the test drives, so uptime is deterministic.
func newTestController(t *testing.T) (*Controller, *fakeHost, *time.Time) {
	t.Helper()
	h := &fakeHost{}
	win := qt.NewQMainWindow2()
	c := New(h, win)
	clock := time.Date(2026, 8, 13, 14, 22, 0, 0, time.UTC)
	c.now = func() time.Time { return clock }
	return c, h, &clock
}

// activityRows flattens the history rows to their rendered text, newest first, so
// the assertions are about what is on screen rather than about the widget tree.
func activityRows(c *Controller) []string {
	out := make([]string, 0, len(c.activityRows))
	for _, row := range c.activityRows {
		out = append(out, row.Text())
	}
	return out
}

func TestContentReturnsNonNilWidget(t *testing.T) {
	win := qt.NewQMainWindow2()
	c := New(&fakeHost{}, win)
	if c.Content() == nil {
		t.Fatal("Content() returned nil")
	}
}

// The behavioural contract the brief calls out explicitly: after a Connected
// event, the primary button's click must drive Disconnect, not Connect.
func TestApplyConnectedEnablesDisconnectButton(t *testing.T) {
	win := qt.NewQMainWindow2()
	host := &fakeHost{}
	c := New(host, win)
	c.Apply(tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.88"})

	if c.primary.Text() != "Disconnect" {
		t.Fatalf("primary button = %q, want %q", c.primary.Text(), "Disconnect")
	}
	c.primary.Click()
	if host.disconnects != 1 {
		t.Errorf("disconnects = %d, want 1", host.disconnects)
	}
	if host.connects != 0 {
		t.Errorf("connects = %d, want 0 (clicking Disconnect must never call Connect)", host.connects)
	}
}

func TestApplyRendersEachState(t *testing.T) {
	cases := []struct {
		name        string
		event       tunnel.Event
		wantState   string
		wantSubHas  string
		wantRole    string
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
			wantRole:    "caption",
			wantPrimary: "Connect",
			wantIP:      "—",
		},
		{
			name:        "authenticating offers Cancel, not Disconnect",
			event:       tunnel.Event{State: tunnel.Authenticating, Detail: "finish signing in in your browser"},
			wantState:   "Authenticating",
			wantSubHas:  "browser",
			wantRole:    "warning",
			wantPrimary: "Cancel",
			wantIP:      "—",
		},
		{
			name:        "reconnecting",
			event:       tunnel.Event{State: tunnel.Reconnecting, Detail: "gateway refused the session"},
			wantState:   "Reconnecting",
			wantSubHas:  "gateway refused",
			wantRole:    "warning",
			wantPrimary: "Cancel",
			wantIP:      "—",
		},
		{
			name:        "connected shows the gateway",
			event:       tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.88"},
			wantState:   "Connected",
			wantSubHas:  "vpn.example.com",
			wantRole:    "success",
			wantPrimary: "Disconnect",
			wantIP:      "10.0.0.88",
		},
		{
			name:        "error re-offers Connect",
			event:       tunnel.Event{State: tunnel.Error, Detail: "couldn't connect — click Connect to try again"},
			wantState:   "Error",
			wantSubHas:  "click Connect",
			wantRole:    "error",
			wantPrimary: "Connect",
			wantIP:      "—",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _, _ := newTestController(t)
			c.Apply(tc.event)

			if got := c.stateText.Text(); got != tc.wantState {
				t.Errorf("state = %q, want %q", got, tc.wantState)
			}
			if got := c.subText.Text(); !strings.Contains(got, tc.wantSubHas) {
				t.Errorf("sub-line = %q, want it to contain %q", got, tc.wantSubHas)
			}
			if got := c.dot.Property("role").ToString(); got != tc.wantRole {
				t.Errorf("dot role = %q, want %q", got, tc.wantRole)
			}
			if got := c.primary.Text(); got != tc.wantPrimary {
				t.Errorf("primary button = %q, want %q", got, tc.wantPrimary)
			}
			if got := c.ipValue.Text(); got != tc.wantIP {
				t.Errorf("assigned IP = %q, want %q", got, tc.wantIP)
			}
			// The protocol row comes from config, so it reads the same in every state
			// — a blank row would look like a bug. The gateway is deliberately NOT
			// repeated here: the hero sub-line carries it (asserted above).
			if got := c.protoValue.Text(); !strings.Contains(got, "Fortinet") || !strings.Contains(got, "DTLS off") {
				t.Errorf("protocol = %q, want it to name Fortinet and the DTLS setting", got)
			}
		})
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
			c.primary.Click()
			if h.connects != tc.wantConnects {
				t.Errorf("connects = %d, want %d", h.connects, tc.wantConnects)
			}
			if h.disconnects != tc.wantDisconnects {
				t.Errorf("disconnects = %d, want %d", h.disconnects, tc.wantDisconnects)
			}
		})
	}
}

// Clicking the primary button across several different states must not stack up
// extra actions from earlier states — only ONE click handler is ever wired (see
// the primaryAction field comment in status.go).
func TestPrimaryButtonDoesNotAccumulateActionsAcrossStates(t *testing.T) {
	c, h, _ := newTestController(t)
	c.Apply(tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.88"})
	c.Apply(tunnel.Event{State: tunnel.Disconnected})
	c.Apply(tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.88"})

	c.primary.Click()
	if h.connects != 0 {
		t.Errorf("connects = %d, want 0", h.connects)
	}
	if h.disconnects != 1 {
		t.Errorf("disconnects = %d, want exactly 1 (a stale connection would double-fire)", h.disconnects)
	}
}

// Navigation to Settings belongs to the shell's rail, not to a button in here —
// two routes to one place, one of them dressed as an action.
func TestLogButtonDrivesTheHost(t *testing.T) {
	c, h, _ := newTestController(t)
	c.logBtn.Click()
	if h.logOpens != 1 {
		t.Errorf("OpenLog called %d times, want 1", h.logOpens)
	}
}

// The uptime is the one thing on screen that changes without an event, so it has
// its own path: Tick recomputes the sub-line and touches nothing else.
func TestUptimeTicksWhileConnected(t *testing.T) {
	c, _, clock := newTestController(t)
	c.Apply(tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.88"})
	if got := c.timerText.Text(); got != "00:00:00" {
		t.Fatalf("clock at connect = %q, want 00:00:00", got)
	}
	// The gateway sits on its OWN line now, so a ticking clock cannot shift it.
	if got := c.subText.Text(); !strings.Contains(got, "vpn.example.com") {
		t.Errorf("sub-line = %q, want the gateway", got)
	}

	*clock = clock.Add(94 * time.Second)
	c.Tick()
	if got := c.timerText.Text(); got != "00:01:34" {
		t.Errorf("clock after 94s = %q, want 00:01:34", got)
	}
	if got := c.sinceValue.Text(); got != "14:22" {
		t.Errorf("connected-since = %q, want the wall-clock time of the connect", got)
	}
}

// Ticking while not connected must not invent an uptime, and must not overwrite
// the state's own detail text.
func TestTickIsANoopWhenNotConnected(t *testing.T) {
	c, _, clock := newTestController(t)
	c.Apply(tunnel.Event{State: tunnel.Reconnecting, Detail: "gateway refused the session"})
	before := c.subText.Text()
	*clock = clock.Add(time.Hour)
	c.Tick()
	if got := c.subText.Text(); got != before {
		t.Errorf("sub-line changed on a disconnected tick: %q -> %q", before, got)
	}
	// And no clock is invented for a state that has no session.
	if got := c.timerText.Text(); got != "" {
		t.Errorf("clock = %q, want empty when nothing is connected", got)
	}
	if got := c.sinceValue.Text(); got != "—" {
		t.Errorf("connected-since = %q, want an em dash", got)
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

	if got := c.timerText.Text(); got != "00:00:00" {
		t.Errorf("clock after a reconnect = %q, want it restarted", got)
	}
	if got := c.sinceValue.Text(); got != "14:32" {
		t.Errorf("connected-since = %q, want the SECOND connect's time", got)
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
	if got := c.timerText.Text(); got != "00:05:00" {
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

// Opening the history must put it ON SCREEN. It is a hand-rolled disclosure
// rather than a fancier widget precisely because it has to report WHEN it was
// opened and grow the window — see toggleActivity.
func TestActivityToggleTogglesVisibility(t *testing.T) {
	c, _, _ := newTestController(t)

	if !c.activityScroll.IsHidden() {
		t.Error("the history should start folded away")
	}
	c.toggleActivity()
	if c.activityScroll.IsHidden() {
		t.Fatal("toggling did not reveal the history")
	}
	if !c.activityOpen {
		t.Error("activityOpen not recorded")
	}
	// The list is bounded, so a long history scrolls instead of demanding an
	// unreachable window height.
	if h := c.activityScroll.MaximumHeight(); h != activityHeight {
		t.Errorf("list height = %v, want the bounded %v", h, activityHeight)
	}

	c.toggleActivity()
	if !c.activityScroll.IsHidden() {
		t.Error("toggling again did not fold the history away")
	}
}

// The count is the only clue a folded section gives about whether it holds
// anything.
func TestActivityToggleShowsTheCount(t *testing.T) {
	c, _, clock := newTestController(t)
	if got := c.activityToggle.Text(); got != "Activity" {
		t.Errorf("empty history label = %q, want a bare %q", got, "Activity")
	}
	c.Apply(tunnel.Event{State: tunnel.Connecting})
	*clock = clock.Add(time.Second)
	c.Apply(tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.88"})
	if got := c.activityToggle.Text(); got != "Activity (2)" {
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

// Capacity is what makes the history useful during a flap; it was capped at 12
// only because the list had no scroller.
func TestHistoryKeepsEnoughToBeUseful(t *testing.T) {
	if activityDepth < 50 {
		t.Errorf("activityDepth = %d; a flapping tunnel burns through a dozen transitions in minutes", activityDepth)
	}
}
