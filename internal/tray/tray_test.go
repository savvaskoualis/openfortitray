package tray

import (
	"os"
	"runtime"
	"testing"

	qt "github.com/mappu/miqt/qt6"

	"github.com/savvaskoualis/openfortitray/internal/tunnel"
	"github.com/savvaskoualis/openfortitray/internal/uistate"
)

func init() {
	// Qt's Cocoa integration on macOS requires anything that materializes a
	// real native window — including QSystemTrayIcon.Show(), which Setup
	// calls — to run on the process's real initial OS thread, or it aborts
	// with "NSWindow should only be instantiated on the main thread!". `go
	// test` runs every Test function, top-level ones included, on a goroutine
	// it spawns fresh via t.Run -> go tRunner(...), never on the initial
	// goroutine. So every Setup call below happens in TestMain (which
	// testing.M.Run calls directly on this goroutine) instead, with the
	// results captured into package vars; the Test functions just assert on
	// what was captured. init() runs on the initial goroutine before any
	// other goroutine exists, so locking here keeps it pinned to the real
	// main OS thread for the life of the process. (Same pattern as
	// internal/shell/shell_test.go and cmd/openfortitray/qtapp_test.go.)
	runtime.LockOSThread()
}

// fakeApp records which App method each menu item invoked, so the wiring from
// a click to the application can be checked without a live menu bar.
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

var errFake = &fakeErr{}

type fakeErr struct{}

func (*fakeErr) Error() string { return "autostart failed" }

// --- results captured by TestMain; see init() for why. ---

type setupResult struct {
	err     error
	ctrlNil bool
}

type headerResult struct {
	titleText     string
	titleEnabled  bool
	statusEnabled bool
}

type wiringResult struct {
	connects, statusShows, settings, updateClicks, quits int
}

type actionStartResult struct {
	text    string
	enabled bool
}

type stateResult struct {
	name                  string
	label                 string
	enabled               bool
	connects, disconnects int
}

type noAccumResult struct {
	connects, disconnects int
}

type statusRowResult struct {
	label     string
	lastTitle string
}

type updateWireResult struct {
	before       string
	after        string
	afterAvail   bool
	updateClicks int
}

type badgeSwitchResult struct {
	beforePlain bool
	afterBadged bool
}

type autostartCase struct {
	startChecked bool
	afterChecked bool
	setCalls     []bool
}

type tooltipResult struct {
	afterSetup string
}

var (
	setup               setupResult
	header              headerResult
	wiring              wiringResult
	actionStart         actionStartResult
	states              []stateResult
	noAccum             noAccumResult
	statusRow           statusRowResult
	updateWire          updateWireResult
	badgeSwitch         badgeSwitchResult
	autoSuccess         autostartCase
	autoFailure         autostartCase
	autoStartsChecked   bool
	reassertDidNotPanic bool
	tooltip             tooltipResult
)

func TestMain(m *testing.M) {
	// The offscreen platform plugin is Qt's own documented mechanism for
	// headless test/CI environments — GitHub Actions runners have no logged-in
	// GUI session, so constructing real native windows without it risks a
	// crash during teardown (reproduced directly on two machines before this
	// was added).
	os.Setenv("QT_QPA_PLATFORM", "offscreen")
	qt.NewQApplication(os.Args)

	// TestSetupSucceeds
	{
		c, err := Setup(&fakeApp{})
		setup = setupResult{err: err, ctrlNil: c == nil}
	}

	// TestMenuHeaderRows
	{
		f := &fakeApp{}
		c, _ := Setup(f)
		header = headerResult{
			titleText:     c.titleAction.Text(),
			titleEnabled:  c.titleAction.IsEnabled(),
			statusEnabled: c.statusAction.IsEnabled(),
		}
	}

	// TestMenuActionsWireToApp
	{
		f := &fakeApp{}
		c, _ := Setup(f)
		c.actionItem.Trigger()
		c.openAction.Trigger()
		c.settingsAction.Trigger()
		c.updateAction.Trigger()
		c.quitAction.Trigger()
		wiring = wiringResult{
			connects:     f.connects,
			statusShows:  f.statusShows,
			settings:     f.settings,
			updateClicks: f.updateClicks,
			quits:        f.quits,
		}
	}

	// TestActionRowStartsAsConnect
	{
		c, _ := Setup(&fakeApp{})
		actionStart = actionStartResult{
			text:    c.actionItem.Text(),
			enabled: c.actionItem.IsEnabled(),
		}
	}

	// TestActionRowMatchesTheState
	{
		cases := []struct {
			name  string
			state tunnel.State
		}{
			{"Disconnected", tunnel.Disconnected},
			{"Error", tunnel.Error},
			{"Connected", tunnel.Connected},
			{"Authenticating", tunnel.Authenticating},
			{"Connecting", tunnel.Connecting},
			{"Reconnecting", tunnel.Reconnecting},
		}
		for _, tc := range cases {
			f := &fakeApp{}
			c, _ := Setup(f)
			c.Apply(tunnel.Event{State: tc.state})
			label := c.actionItem.Text()
			enabled := c.actionItem.IsEnabled()
			c.actionItem.Trigger()
			states = append(states, stateResult{
				name:        tc.name,
				label:       label,
				enabled:     enabled,
				connects:    f.connects,
				disconnects: f.disconnects,
			})
		}
	}

	// TestActionRowDoesNotAccumulateActionsAcrossStates
	{
		f := &fakeApp{}
		c, _ := Setup(f)
		c.Apply(tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.88"})
		c.Apply(tunnel.Event{State: tunnel.Disconnected})
		c.Apply(tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.88"})
		c.actionItem.Trigger()
		noAccum = noAccumResult{connects: f.connects, disconnects: f.disconnects}
	}

	// TestApplyUpdatesStatusRow
	{
		c, _ := Setup(&fakeApp{})
		c.Apply(tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.5"})
		statusRow = statusRowResult{label: c.statusAction.Text(), lastTitle: c.lastView.Title}
	}

	// TestUpdateItemWiresAndRelabels
	{
		f := &fakeApp{}
		c, _ := Setup(f)
		before := c.updateAction.Text()
		c.SetUpdateAvailable("v1.2.3")
		after := c.updateAction.Text()
		avail := c.updateAvailable
		c.updateAction.Trigger()
		updateWire = updateWireResult{before: before, after: after, afterAvail: avail, updateClicks: f.updateClicks}
	}

	// TestSetUpdateAvailableSwitchesToBadgedIcon
	{
		c, _ := Setup(&fakeApp{})
		before := c.iconForCurrent() == c.icons[c.currentKind]
		c.SetUpdateAvailable("v9")
		after := c.iconForCurrent() == c.badgedIcons[c.currentKind]
		badgeSwitch = badgeSwitchResult{beforePlain: before, afterBadged: after}
	}

	// TestAutostartToggle/success
	{
		f := &fakeApp{autostartOn: false}
		c, _ := Setup(f)
		start := c.autoAction.IsChecked()
		c.autoAction.Trigger()
		autoSuccess = autostartCase{
			startChecked: start,
			afterChecked: c.autoAction.IsChecked(),
			setCalls:     f.autostartSet,
		}
	}

	// TestAutostartToggle/failure
	{
		f := &fakeApp{autostartOn: false, setAutostartE: errFake}
		c, _ := Setup(f)
		start := c.autoAction.IsChecked()
		c.autoAction.Trigger()
		autoFailure = autostartCase{
			startChecked: start,
			afterChecked: c.autoAction.IsChecked(),
			setCalls:     f.autostartSet,
		}
	}

	// TestAutostartToggle/starts checked when already enabled
	{
		c, _ := Setup(&fakeApp{autostartOn: true})
		autoStartsChecked = c.autoAction.IsChecked()
	}

	// TestReassertTrayIsIdempotent
	{
		var empty Controller
		empty.ReassertTray() // must not panic with a nil icon

		real, _ := Setup(&fakeApp{})
		real.ReassertTray()
		real.ReassertTray()
		reassertDidNotPanic = true // reaching here proves none of the above aborted
	}

	// TestSetTooltipReachesTheInstalledTray
	{
		c, _ := Setup(&fakeApp{})
		SetTooltip("OpenFortiTray")
		tooltip = tooltipResult{afterSetup: c.icon.ToolTip()}
	}

	os.Exit(m.Run())
}

func TestSetupSucceeds(t *testing.T) {
	if setup.err != nil {
		t.Fatalf("Setup returned error: %v", setup.err)
	}
	if setup.ctrlNil {
		t.Fatal("Setup returned nil controller")
	}
}

// The title row is a fixed, disabled, click-less header that names the app and
// its build in the popover, followed by a disabled status line.
func TestMenuHeaderRows(t *testing.T) {
	if want := "OpenFortiTray v9.9.9-test"; header.titleText != want {
		t.Errorf("title = %q, want %q", header.titleText, want)
	}
	if header.titleEnabled {
		t.Error("title row must be disabled")
	}
	if header.statusEnabled {
		t.Error("status row must be disabled")
	}
}

// A tray click must reach the matching App method.
func TestMenuActionsWireToApp(t *testing.T) {
	if wiring.connects != 1 {
		t.Errorf("action row (Connect) fired %d connects, want 1", wiring.connects)
	}
	if wiring.statusShows != 1 {
		t.Errorf("Open fired %d ShowStatus calls, want 1", wiring.statusShows)
	}
	if wiring.settings != 1 {
		t.Errorf("Settings… fired %d ShowSettings calls, want 1", wiring.settings)
	}
	if wiring.updateClicks != 1 {
		t.Errorf("update row fired %d UpdateClicked calls, want 1", wiring.updateClicks)
	}
	if wiring.quits != 1 {
		t.Errorf("Quit fired %d quits, want 1 (teardown must run)", wiring.quits)
	}
}

// At launch there is one connection row, offering Connect — not a separate
// permanently-disabled Disconnect row.
func TestActionRowStartsAsConnect(t *testing.T) {
	if actionStart.text != "Connect" {
		t.Errorf("action row = %q, want %q", actionStart.text, "Connect")
	}
	if !actionStart.enabled {
		t.Error("the action row must never be disabled; it is the only connection control")
	}
}

// Apply must relabel the action row and repoint its click target together, so
// they never disagree. Disconnect stays clickable through the busy states,
// because it is the only way out of a connect that hangs or a reconnect loop
// that will not settle — uistate.View.CanDisconnect is false there (no tunnel
// exists yet), so this asserts the row's behaviour against the states rather
// than against that field.
func TestActionRowMatchesTheState(t *testing.T) {
	want := map[string]struct {
		label                 string
		connects, disconnects int
	}{
		"Disconnected":   {"Connect", 1, 0},
		"Error":          {"Connect", 1, 0},
		"Connected":      {"Disconnect", 0, 1},
		"Authenticating": {"Cancel", 0, 1},
		"Connecting":     {"Cancel", 0, 1},
		"Reconnecting":   {"Cancel", 0, 1},
	}
	if len(states) != len(want) {
		t.Fatalf("captured %d state results, want %d", len(states), len(want))
	}
	for _, got := range states {
		t.Run(got.name, func(t *testing.T) {
			w, ok := want[got.name]
			if !ok {
				t.Fatalf("unexpected state %q", got.name)
			}
			if got.label != w.label {
				t.Errorf("label = %q, want %q", got.label, w.label)
			}
			if !got.enabled {
				t.Error("the action row must never be disabled")
			}
			if got.connects != w.connects || got.disconnects != w.disconnects {
				t.Errorf("fired %d connects / %d disconnects, want %d / %d",
					got.connects, got.disconnects, w.connects, w.disconnects)
			}
		})
	}
}

// Repeatedly Apply-ing different states must not stack up extra click targets
// from earlier states — only one OnTriggered handler is ever registered (see
// the currentAction field comment in tray.go).
func TestActionRowDoesNotAccumulateActionsAcrossStates(t *testing.T) {
	if noAccum.connects != 0 {
		t.Errorf("connects = %d, want 0", noAccum.connects)
	}
	if noAccum.disconnects != 1 {
		t.Errorf("disconnects = %d, want exactly 1 (a stale target would double-fire or fire the wrong method)", noAccum.disconnects)
	}
}

// Apply must keep the status row's text in step with the current view.
func TestApplyUpdatesStatusRow(t *testing.T) {
	if want := "Connected — 10.0.0.5"; statusRow.label != want {
		t.Errorf("status label = %q, want %q", statusRow.label, want)
	}
	if statusRow.lastTitle == "" {
		t.Error("Apply must record the view it rendered")
	}
}

// The update row starts as a manual check and its click target must reach
// UpdateClicked; SetUpdateAvailable must relabel it to the one-click offer
// without changing what it's wired to.
func TestUpdateItemWiresAndRelabels(t *testing.T) {
	if updateWire.before != "Check for Updates…" {
		t.Fatalf("initial update label = %q, want %q", updateWire.before, "Check for Updates…")
	}
	if want := "Update to v1.2.3 & Restart"; updateWire.after != want {
		t.Errorf("after SetUpdateAvailable label = %q, want %q", updateWire.after, want)
	}
	if !updateWire.afterAvail {
		t.Error("SetUpdateAvailable must set updateAvailable")
	}
	if updateWire.updateClicks != 1 {
		t.Errorf("update action fired %d UpdateClicked calls, want 1", updateWire.updateClicks)
	}
}

// Once an update is available, iconForCurrent must switch to the badged
// variant for whatever state is current.
func TestSetUpdateAvailableSwitchesToBadgedIcon(t *testing.T) {
	if !badgeSwitch.beforePlain {
		t.Error("before an update, iconForCurrent should return the plain icon")
	}
	if !badgeSwitch.afterBadged {
		t.Error("after SetUpdateAvailable, iconForCurrent should return the badged icon")
	}
}

// The auto-connect checkbox toggles the login item and only then flips the
// checkmark; a failed SetAutostart leaves the mark where it was.
func TestAutostartToggle(t *testing.T) {
	t.Run("success flips the checkmark", func(t *testing.T) {
		if autoSuccess.startChecked {
			t.Fatal("auto-connect should start unchecked (AutostartEnabled=false)")
		}
		if len(autoSuccess.setCalls) != 1 || autoSuccess.setCalls[0] != true {
			t.Errorf("SetAutostart calls = %v, want [true]", autoSuccess.setCalls)
		}
		if !autoSuccess.afterChecked {
			t.Error("checkmark should be set after a successful enable")
		}
	})

	t.Run("failure leaves the checkmark unchanged", func(t *testing.T) {
		if autoFailure.afterChecked != autoFailure.startChecked {
			t.Errorf("checkmark = %v after a failed SetAutostart, want it unchanged (%v)", autoFailure.afterChecked, autoFailure.startChecked)
		}
	})

	t.Run("starts checked when already enabled", func(t *testing.T) {
		if !autoStartsChecked {
			t.Error("auto-connect should start checked (AutostartEnabled=true)")
		}
	})
}

// The state→appearance mapping lives in internal/uistate and is tested there.
// What stays this package's job is turning a view's severity into a tray
// glyph — and the glyph must never be empty. This is a pure function over raw
// bytes, so unlike everything else in this file it needs no QApplication and
// can run directly in a Test function.
func TestIconForKind(t *testing.T) {
	nameOf := func(icon []byte) string {
		for _, c := range []struct {
			name string
			data []byte
		}{{"gray", iconGray}, {"green", iconGreen}, {"yellow", iconYellow}, {"red", iconRed}} {
			if string(icon) == string(c.data) {
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
		// A Kind this package has not been taught about must still produce a
		// real icon rather than nothing.
		{uistate.Kind(99), "gray"},
	}
	for _, tc := range cases {
		got := iconFor(tc.kind)
		if len(got) == 0 {
			t.Fatalf("kind %v produced an empty icon", tc.kind)
		}
		if n := nameOf(got); n != tc.want {
			t.Errorf("kind %v = %s icon, want %s", tc.kind, n, tc.want)
		}
	}
}

// ReassertTray must be safe to call any number of times, including before
// Setup has installed an icon. The exercise runs in TestMain (see init()); if
// any of it had aborted the process, this test would never run at all.
func TestReassertTrayIsIdempotent(t *testing.T) {
	if !reassertDidNotPanic {
		t.Fatal("ReassertTray exercise in TestMain did not complete")
	}
}

// SetTooltip must be safe to call before any tray has been set up: this runs
// directly in the Test function (not TestMain) because with no tray installed
// it makes no Qt call at all — trayIcon is nil and SetTooltip just returns.
func TestSetTooltipIsSafeWithNoTrayYet(t *testing.T) {
	saved := trayIcon
	trayIcon = nil
	defer func() { trayIcon = saved }()
	SetTooltip("should not panic")
}

func TestSetTooltipReachesTheInstalledTray(t *testing.T) {
	if tooltip.afterSetup != "OpenFortiTray" {
		t.Errorf("tooltip = %q, want %q", tooltip.afterSetup, "OpenFortiTray")
	}
}
