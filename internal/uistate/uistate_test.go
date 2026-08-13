package uistate

import (
	"strings"
	"testing"
	"time"

	"github.com/savvaskoualis/openfortitray/internal/tunnel"
)

// The tray's status row text is asserted here rather than in internal/tray,
// because this is now the only place that decides it. These expectations are the
// tray's PRE-EXISTING labels, character for character — the refactor that moves
// the tray onto this package must not change a single one of them.
func TestViewForEveryState(t *testing.T) {
	cases := []struct {
		name          string
		event         tunnel.Event
		kind          Kind
		title         string
		detail        string
		menuLabel     string
		assignedIP    string
		canConnect    bool
		canDisconnect bool
	}{
		{
			name:       "disconnected",
			event:      tunnel.Event{State: tunnel.Disconnected},
			kind:       KindIdle,
			title:      "Disconnected",
			detail:     "not connected",
			menuLabel:  "Disconnected",
			canConnect: true,
		},
		{
			name:      "authenticating",
			event:     tunnel.Event{State: tunnel.Authenticating},
			kind:      KindBusy,
			title:     "Authenticating",
			detail:    "",
			menuLabel: "Authenticating…",
		},
		{
			name:      "authenticating with detail",
			event:     tunnel.Event{State: tunnel.Authenticating, Detail: "finish signing in in your browser"},
			kind:      KindBusy,
			title:     "Authenticating",
			detail:    "finish signing in in your browser",
			menuLabel: "Authenticating — finish signing in in your browser",
		},
		{
			name:      "connecting",
			event:     tunnel.Event{State: tunnel.Connecting},
			kind:      KindBusy,
			title:     "Connecting",
			menuLabel: "Connecting…",
		},
		{
			name:      "reconnecting with detail",
			event:     tunnel.Event{State: tunnel.Reconnecting, Detail: "gateway refused the session — signing in again"},
			kind:      KindBusy,
			title:     "Reconnecting",
			detail:    "gateway refused the session — signing in again",
			menuLabel: "Reconnecting — gateway refused the session — signing in again",
		},
		{
			name:          "connected carries the assigned IP",
			event:         tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.88"},
			kind:          KindOK,
			title:         "Connected",
			detail:        "10.0.0.88",
			menuLabel:     "Connected — 10.0.0.88",
			assignedIP:    "10.0.0.88",
			canDisconnect: true,
		},
		{
			name:          "connected without a reported IP",
			event:         tunnel.Event{State: tunnel.Connected},
			kind:          KindOK,
			title:         "Connected",
			menuLabel:     "Connected",
			canDisconnect: true,
		},
		{
			// Error is terminal for a run — no Disconnected event follows it — so
			// Connect has to be offered again from here.
			name:       "error",
			event:      tunnel.Event{State: tunnel.Error, Detail: "couldn't connect — click Connect to try again"},
			kind:       KindBad,
			title:      "Error",
			detail:     "couldn't connect — click Connect to try again",
			menuLabel:  "Error: couldn't connect — click Connect to try again",
			canConnect: true,
		},
		{
			name:       "error without detail",
			event:      tunnel.Event{State: tunnel.Error},
			kind:       KindBad,
			title:      "Error",
			menuLabel:  "Error",
			canConnect: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := ViewFor(tc.event)
			if v.State != tc.event.State {
				t.Errorf("State = %v, want %v", v.State, tc.event.State)
			}
			if v.Kind != tc.kind {
				t.Errorf("Kind = %v, want %v", v.Kind, tc.kind)
			}
			if v.Title != tc.title {
				t.Errorf("Title = %q, want %q", v.Title, tc.title)
			}
			if v.Detail != tc.detail {
				t.Errorf("Detail = %q, want %q", v.Detail, tc.detail)
			}
			if v.MenuLabel != tc.menuLabel {
				t.Errorf("MenuLabel = %q, want %q", v.MenuLabel, tc.menuLabel)
			}
			if v.AssignedIP != tc.assignedIP {
				t.Errorf("AssignedIP = %q, want %q", v.AssignedIP, tc.assignedIP)
			}
			if v.CanConnect != tc.canConnect {
				t.Errorf("CanConnect = %v, want %v", v.CanConnect, tc.canConnect)
			}
			if v.CanDisconnect != tc.canDisconnect {
				t.Errorf("CanDisconnect = %v, want %v", v.CanDisconnect, tc.canDisconnect)
			}
		})
	}
}

// Authenticating is the one state where neither Connect nor Disconnect applies:
// a browser login is in flight, so there is nothing to connect and no tunnel to
// bring down. Cancel is the action, and it is not one of these two flags. This is
// asserted on its own because "CanDisconnect == !CanConnect" is the obvious wrong
// simplification and it would silently enable Disconnect mid-login.
func TestNeitherActionAppliesWhileAuthenticating(t *testing.T) {
	for _, st := range []tunnel.State{tunnel.Authenticating, tunnel.Connecting, tunnel.Reconnecting} {
		v := ViewFor(tunnel.Event{State: st})
		if v.CanConnect || v.CanDisconnect {
			t.Errorf("%v: CanConnect=%v CanDisconnect=%v, want both false", st, v.CanConnect, v.CanDisconnect)
		}
		if !v.Busy() {
			t.Errorf("%v: Busy() = false, want true", st)
		}
	}
}

// Process output can run to many lines; the status row is one line in a menu, and
// the full text is already in the log file.
func TestDetailIsCutAtTheFirstLineBreak(t *testing.T) {
	v := ViewFor(tunnel.Event{State: tunnel.Error, Detail: "first line\nsecond line"})
	if v.Detail != "first line" {
		t.Errorf("Detail = %q, want %q", v.Detail, "first line")
	}
	if strings.Contains(v.MenuLabel, "second") {
		t.Errorf("MenuLabel leaked the second line: %q", v.MenuLabel)
	}
}

func TestLongDetailIsTruncatedWithEllipsis(t *testing.T) {
	long := strings.Repeat("x", MaxDetail+20)
	v := ViewFor(tunnel.Event{State: tunnel.Error, Detail: long})
	if !strings.HasSuffix(v.Detail, "…") {
		t.Errorf("Detail = %q, want a trailing ellipsis", v.Detail)
	}
	if n := len([]rune(v.Detail)); n > MaxDetail+1 {
		t.Errorf("Detail is %d runes, want at most %d", n, MaxDetail+1)
	}
}

// Truncation counts RUNES, not bytes: a multi-byte detail must not be cut
// mid-character, which would render as a replacement glyph.
func TestTruncationIsRuneSafe(t *testing.T) {
	v := ViewFor(tunnel.Event{State: tunnel.Error, Detail: strings.Repeat("é", MaxDetail+10)})
	for _, r := range v.Detail {
		if r == '�' {
			t.Fatalf("truncation split a multi-byte rune: %q", v.Detail)
		}
	}
}

func TestRingKeepsNewestFirstAndEvicts(t *testing.T) {
	r := NewRing(3)
	base := time.Date(2026, 8, 13, 14, 22, 0, 0, time.UTC)
	r.Add(tunnel.Event{State: tunnel.Connecting}, base)
	r.Add(tunnel.Event{State: tunnel.Authenticating}, base.Add(time.Second))
	r.Add(tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.88"}, base.Add(2*time.Second))

	got := r.Entries()
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if !strings.HasPrefix(got[0].Text, "Connected") {
		t.Errorf("newest entry = %q, want the Connected one first", got[0].Text)
	}
	if !strings.HasPrefix(got[2].Text, "Connecting") {
		t.Errorf("oldest entry = %q, want the Connecting one last", got[2].Text)
	}

	// A fourth entry evicts the oldest rather than growing.
	r.Add(tunnel.Event{State: tunnel.Disconnected}, base.Add(3*time.Second))
	got = r.Entries()
	if len(got) != 3 {
		t.Fatalf("after eviction len = %d, want 3", len(got))
	}
	for _, e := range got {
		if strings.HasPrefix(e.Text, "Connecting") {
			t.Errorf("the oldest entry survived eviction: %v", got)
		}
	}
}

// A flapping tunnel emits the same event repeatedly. Without suppression the
// activity list becomes a hundred identical rows and the useful history scrolls
// out of the ring.
func TestRingSuppressesConsecutiveDuplicates(t *testing.T) {
	r := NewRing(10)
	base := time.Date(2026, 8, 13, 14, 22, 0, 0, time.UTC)
	e := tunnel.Event{State: tunnel.Reconnecting, Detail: "gateway refused the session"}
	for i := 0; i < 5; i++ {
		r.Add(e, base.Add(time.Duration(i)*time.Second))
	}
	if n := len(r.Entries()); n != 1 {
		t.Errorf("len = %d, want 1 (consecutive duplicates suppressed)", n)
	}

	// A different event, then the first one again, both record: only CONSECUTIVE
	// duplicates are dropped, so a genuine flap is still visible as a flap.
	r.Add(tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.88"}, base.Add(6*time.Second))
	r.Add(e, base.Add(7*time.Second))
	if n := len(r.Entries()); n != 3 {
		t.Errorf("len = %d, want 3", n)
	}
}

// View fills an empty Disconnected detail with "not connected" so the window's
// header is not a heading with nothing under it. The activity history must NOT
// inherit that filler, or every disconnect logs the tautology
// "Disconnected — not connected".
func TestRingDoesNotEchoTheSyntheticDisconnectedDetail(t *testing.T) {
	r := NewRing(4)
	r.Add(tunnel.Event{State: tunnel.Disconnected}, time.Now())
	got := r.Entries()[0].Text
	if got != "Disconnected" {
		t.Errorf("entry = %q, want %q", got, "Disconnected")
	}
}

func TestRingEntriesIsACopy(t *testing.T) {
	r := NewRing(2)
	r.Add(tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.88"}, time.Now())
	got := r.Entries()
	got[0].Text = "mutated"
	if r.Entries()[0].Text == "mutated" {
		t.Error("Entries returned a slice aliasing the ring's storage")
	}
}

func TestNewRingRejectsNonPositiveCapacity(t *testing.T) {
	for _, n := range []int{0, -1} {
		r := NewRing(n)
		r.Add(tunnel.Event{State: tunnel.Connected}, time.Now())
		if len(r.Entries()) == 0 {
			t.Errorf("NewRing(%d) produced a ring that stores nothing", n)
		}
	}
}
