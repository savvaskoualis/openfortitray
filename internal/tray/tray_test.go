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

func TestWantedAutostartStateSuccess(t *testing.T) {
	if got := wantedAutostartState(true, nil); got != true {
		t.Errorf("wantedAutostartState(true, nil) = %v, want true", got)
	}
	if got := wantedAutostartState(false, nil); got != false {
		t.Errorf("wantedAutostartState(false, nil) = %v, want false", got)
	}
}

func TestWantedAutostartStateFailureReverts(t *testing.T) {
	errFail := errors.New("boom")
	if got := wantedAutostartState(true, errFail); got != false {
		t.Errorf("wantedAutostartState(true, err) = %v, want false (reverted)", got)
	}
	if got := wantedAutostartState(false, errFail); got != true {
		t.Errorf("wantedAutostartState(false, err) = %v, want true (reverted)", got)
	}
}
