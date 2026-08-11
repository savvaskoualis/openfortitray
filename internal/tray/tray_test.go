package tray

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/savvaskoualis/openfortitray/internal/tunnel"
)

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
