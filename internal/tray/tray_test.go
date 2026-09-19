package tray

import (
	"errors"
	"testing"

	"github.com/savvaskoualis/openfortitray/internal/tunnel"
	"github.com/savvaskoualis/openfortitray/internal/uistate"
)

// fakeApp is a no-widget stand-in for App: it records which method was
// called instead of doing anything real, so tests can assert on wiring
// without a live tray.
type fakeApp struct {
	calls            []string
	autostartEnabled bool
	autostartErr     error
	version          string
}

func (f *fakeApp) Connect()    { f.calls = append(f.calls, "Connect") }
func (f *fakeApp) Disconnect() { f.calls = append(f.calls, "Disconnect") }
func (f *fakeApp) SetAutostart(on bool) error {
	f.calls = append(f.calls, "SetAutostart")
	return f.autostartErr
}
func (f *fakeApp) AutostartEnabled() bool { return f.autostartEnabled }
func (f *fakeApp) LogPath() string        { return "/tmp/log" }
func (f *fakeApp) Version() string        { return f.version }
func (f *fakeApp) ShowSettings()          { f.calls = append(f.calls, "ShowSettings") }
func (f *fakeApp) ShowStatus()            { f.calls = append(f.calls, "ShowStatus") }
func (f *fakeApp) Quit()                  { f.calls = append(f.calls, "Quit") }
func (f *fakeApp) UpdateClicked()         { f.calls = append(f.calls, "UpdateClicked") }

func TestIconForMatchesKind(t *testing.T) {
	tests := []struct {
		kind uistate.Kind
		want []byte
	}{
		{uistate.KindIdle, iconGray},
		{uistate.KindBusy, iconYellow},
		{uistate.KindOK, iconGreen},
		{uistate.KindBad, iconRed},
	}
	for _, tt := range tests {
		got := iconFor(tt.kind)
		if len(got) == 0 {
			t.Fatalf("iconFor(%v): got empty bytes", tt.kind)
		}
		if string(got) != string(tt.want) {
			t.Errorf("iconFor(%v) = %d bytes, want the bytes of the matching embedded asset", tt.kind, len(got))
		}
	}
}

func TestActionForCanConnect(t *testing.T) {
	app := &fakeApp{}
	v := uistate.View{CanConnect: true}
	label, target := actionFor(v, app)
	if label != "Connect" {
		t.Errorf("label = %q, want Connect", label)
	}
	target()
	if len(app.calls) != 1 || app.calls[0] != "Connect" {
		t.Errorf("calls = %v, want [Connect]", app.calls)
	}
}

func TestActionForBusy(t *testing.T) {
	app := &fakeApp{}
	v := uistate.View{Kind: uistate.KindBusy}
	label, target := actionFor(v, app)
	if label != "Cancel" {
		t.Errorf("label = %q, want Cancel", label)
	}
	target()
	if len(app.calls) != 1 || app.calls[0] != "Disconnect" {
		t.Errorf("calls = %v, want [Disconnect]", app.calls)
	}
}

func TestActionForConnected(t *testing.T) {
	app := &fakeApp{}
	v := uistate.View{Kind: uistate.KindOK, CanDisconnect: true}
	label, target := actionFor(v, app)
	if label != "Disconnect" {
		t.Errorf("label = %q, want Disconnect", label)
	}
	target()
	if len(app.calls) != 1 || app.calls[0] != "Disconnect" {
		t.Errorf("calls = %v, want [Disconnect]", app.calls)
	}
}

func TestApplyUpdatesCurrentKindAndLastView(t *testing.T) {
	app := &fakeApp{}
	c := &Controller{app: app, currentKind: uistate.KindIdle}

	c.Apply(tunnel.Event{State: tunnel.Connected})

	if c.currentKind != uistate.KindOK {
		t.Errorf("currentKind = %v, want KindOK", c.currentKind)
	}
	if !c.lastView.CanDisconnect {
		t.Errorf("lastView.CanDisconnect = false, want true after Connected")
	}
	if c.lastView.MenuLabel != "Connected" {
		t.Errorf("lastView.MenuLabel = %q, want Connected", c.lastView.MenuLabel)
	}
}

func TestApplyUpdatesToDisconnected(t *testing.T) {
	app := &fakeApp{}
	c := &Controller{app: app, currentKind: uistate.KindOK}

	c.Apply(tunnel.Event{})

	if c.currentKind != uistate.KindIdle {
		t.Errorf("currentKind = %v, want KindIdle", c.currentKind)
	}
	if !c.lastView.CanConnect {
		t.Errorf("lastView.CanConnect = false, want true after a zero-value (disconnected) event")
	}
}

// fakeAutoItem stands in for *systray.MenuItem's Checked/Check/Uncheck (the
// autostartCheckbox interface) so applyAutostartToggle — the real production
// decision function toggleAutostart calls — can be exercised directly,
// without a live *systray.MenuItem (which can't be constructed outside
// systray.Run). It mirrors the real MenuItem's contract exactly: checked only
// ever changes via an explicit Check()/Uncheck() call, never on its own.
type fakeAutoItem struct{ checked bool }

func (f *fakeAutoItem) Checked() bool { return f.checked }
func (f *fakeAutoItem) Check()        { f.checked = true }
func (f *fakeAutoItem) Uncheck()      { f.checked = false }

// TestApplyAutostartToggleEnablesFromUnchecked proves the fix for Finding 1:
// clicking a currently-unchecked box must call SetAutostart with true (the
// OPPOSITE of the checkbox's pre-click state, since energye/systray never
// flips it for us — see applyAutostartToggle's comment) and, only on
// success, leave the checkbox checked.
func TestApplyAutostartToggleEnablesFromUnchecked(t *testing.T) {
	app := &fakeApp{}
	item := &fakeAutoItem{checked: false}

	applyAutostartToggle(item, app.SetAutostart)

	if len(app.calls) != 1 || app.calls[0] != "SetAutostart" {
		t.Errorf("calls = %v, want [SetAutostart]", app.calls)
	}
	if !item.Checked() {
		t.Errorf("item.Checked() = false, want true after enabling from unchecked")
	}
}

// TestApplyAutostartToggleDisablesFromChecked proves the opposite direction:
// clicking a currently-checked box must call SetAutostart with false and, on
// success, leave the checkbox unchecked.
func TestApplyAutostartToggleDisablesFromChecked(t *testing.T) {
	app := &fakeApp{}
	item := &fakeAutoItem{checked: true}

	applyAutostartToggle(item, app.SetAutostart)

	if len(app.calls) != 1 || app.calls[0] != "SetAutostart" {
		t.Errorf("calls = %v, want [SetAutostart]", app.calls)
	}
	if item.Checked() {
		t.Errorf("item.Checked() = true, want false after disabling from checked")
	}
}

// TestApplyAutostartToggleLeavesCheckboxOnFailure proves the checkbox is left
// exactly as it was (never optimistically flipped, so nothing to revert) when
// SetAutostart fails.
func TestApplyAutostartToggleLeavesCheckboxOnFailure(t *testing.T) {
	app := &fakeApp{autostartErr: errors.New("boom")}
	item := &fakeAutoItem{checked: false}

	applyAutostartToggle(item, app.SetAutostart)

	if item.Checked() {
		t.Errorf("item.Checked() = true, want false (untouched) after a failed enable")
	}
}
