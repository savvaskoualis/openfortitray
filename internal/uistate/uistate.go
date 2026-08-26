// Package uistate turns a tunnel event into everything the UI needs to render
// it, and keeps a short history of those events.
//
// It exists because two surfaces now show the same state — the tray menu and the
// status window — and they must never disagree. Before this package the mapping
// lived privately inside internal/tray, so a second consumer would have meant a
// second copy of "what does Reconnecting say", which is exactly the kind of
// duplicate that drifts.
//
// Nothing here imports fyne. The state→appearance decisions are ordinary data,
// so they are tested without a toolkit, a display, or a running app.
package uistate

import (
	"strings"
	"time"

	"github.com/savvaskoualis/openfortitray/internal/tunnel"
)

// Kind is the visual severity of a state: it picks the status dot's colour and
// nothing else. It is deliberately coarser than tunnel.State — three different
// states are all "busy, amber" — because the dot has three colours and a
// neutral, not six.
type Kind int

const (
	KindIdle Kind = iota // not connected; neutral
	KindBusy             // authenticating, connecting, reconnecting; warning
	KindOK               // connected; success
	KindBad              // error
)

// MaxDetail caps the detail text. Process output can run to many lines and
// hundreds of characters; the tray's status row is a single menu item, and the
// full text is already in the log file.
const MaxDetail = 60

// View is everything one event changes about the UI.
//
// CanConnect and CanDisconnect are NOT opposites. While a browser login is in
// flight there is nothing to connect and no tunnel to bring down, so both are
// false and the surfaces offer Cancel instead. Deriving one from the other would
// silently enable Disconnect mid-login.
type View struct {
	State tunnel.State
	Kind  Kind

	// Title is the state on its own ("Connected"), for the status window's
	// heading. Detail is the short supporting line, already cut to one line and
	// truncated to MaxDetail.
	Title  string
	Detail string

	// MenuLabel is the single-line label the tray's status row shows. It keeps the
	// tray's long-standing wording — "Connected — 10.0.0.88", "Reconnecting — …",
	// "Error: …", and a bare "Connecting…" when there is no detail.
	MenuLabel string

	// AssignedIP is the address the gateway handed us, and is set only when
	// Connected. Other states put their text in Detail instead.
	AssignedIP string

	CanConnect    bool
	CanDisconnect bool
}

// Busy reports whether something is in flight, so a surface can offer Cancel
// rather than Connect or Disconnect.
func (v View) Busy() bool { return v.Kind == KindBusy }

// ViewFor maps one tunnel event to a View.
func ViewFor(e tunnel.Event) View {
	detail := short(e.Detail)
	v := View{State: e.State, Detail: detail}

	switch e.State {
	case tunnel.Connected:
		v.Kind = KindOK
		v.Title = "Connected"
		v.AssignedIP = detail
		v.CanDisconnect = true
		v.MenuLabel = "Connected"
		if detail != "" {
			v.MenuLabel += " — " + detail
		}

	case tunnel.Authenticating, tunnel.Connecting, tunnel.Reconnecting:
		v.Kind = KindBusy
		v.Title = e.State.String()
		// "Connecting…" when there is nothing to add, "Connecting — <detail>" when
		// there is. Both forms predate this package and are kept verbatim.
		if detail != "" {
			v.MenuLabel = e.State.String() + " — " + detail
		} else {
			v.MenuLabel = e.State.String() + "…"
		}

	case tunnel.Error:
		// Error is terminal for a run: no Disconnected event follows it, so Connect
		// has to be offered again from here or the app looks wedged.
		v.Kind = KindBad
		v.Title = "Error"
		v.CanConnect = true
		v.MenuLabel = "Error"
		if detail != "" {
			v.MenuLabel = "Error: " + detail
		}

	default:
		v.Kind = KindIdle
		v.Title = "Disconnected"
		v.CanConnect = true
		v.MenuLabel = "Disconnected"
		// The window's header wants a sub-line even here; the tray's row does not,
		// which is why this is set on Detail and not folded into MenuLabel.
		if v.Detail == "" {
			v.Detail = "not connected"
		}
	}
	return v
}

// short reduces event detail to one short line: first line only, trimmed, and
// truncated by RUNES so a multi-byte character is never cut in half.
func short(detail string) string {
	if i := strings.IndexAny(detail, "\r\n"); i >= 0 {
		detail = detail[:i]
	}
	detail = strings.TrimSpace(detail)
	if r := []rune(detail); len(r) > MaxDetail {
		return strings.TrimSpace(string(r[:MaxDetail])) + "…"
	}
	return detail
}

// Entry is one line of the activity history.
type Entry struct {
	At   time.Time
	Text string
}

// Ring is a fixed-capacity history of state transitions, newest first. It is not
// safe for concurrent use: every caller runs on the fyne UI goroutine, and adding
// a mutex would only hide a threading mistake rather than prevent one.
type Ring struct {
	buf  []Entry
	n    int // number of live entries
	head int // index of the next write
}

// NewRing returns a ring holding at most cap entries. A non-positive cap is
// treated as 1 rather than producing a ring that silently stores nothing.
func NewRing(cap int) *Ring {
	if cap < 1 {
		cap = 1
	}
	return &Ring{buf: make([]Entry, cap)}
}

// Add records an event, unless it repeats the newest entry.
//
// The suppression is what makes the list readable: a flapping tunnel emits the
// same Reconnecting event over and over, and without this the history becomes a
// hundred identical rows and everything useful scrolls out of the ring. Only
// CONSECUTIVE duplicates are dropped, so a genuine flap still reads as a flap.
func (r *Ring) Add(e tunnel.Event, at time.Time) {
	text := textFor(e)
	if r.n > 0 {
		if prev := r.buf[(r.head-1+len(r.buf))%len(r.buf)]; prev.Text == text {
			return
		}
	}
	r.buf[r.head] = Entry{At: at, Text: text}
	r.head = (r.head + 1) % len(r.buf)
	if r.n < len(r.buf) {
		r.n++
	}
}

// Entries returns the history newest first, as a fresh slice — callers render it
// and must not be able to reach the ring's storage.
func (r *Ring) Entries() []Entry {
	out := make([]Entry, 0, r.n)
	for i := 1; i <= r.n; i++ {
		out = append(out, r.buf[(r.head-i+len(r.buf))%len(r.buf)])
	}
	return out
}

// textFor is the activity line for an event: the state, plus its detail when the
// EVENT carried one.
//
// It reads the event's own detail rather than View.Detail on purpose. View fills
// an empty Disconnected detail with "not connected", which is right for the
// window's header — a heading with nothing under it looks broken — but in a
// timestamped history it would render the tautology "Disconnected — not
// connected" on every single disconnect.
//
// It is also not MenuLabel: the history reads better as "Connected — 10.0.0.88"
// than as a menu row, and the two should be free to diverge.
func textFor(e tunnel.Event) string {
	title := ViewFor(e).Title
	if d := short(e.Detail); d != "" {
		return title + " — " + d
	}
	return title
}
