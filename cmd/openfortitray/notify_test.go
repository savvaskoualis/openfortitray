package main

import (
	"strings"
	"testing"

	"github.com/savvaskoualis/openfortitray/internal/tunnel"
)

type toast struct{ title, body string }

// notifyRecorder builds an app whose notify seam records instead of posting.
func notifyRecorder() (*app, *[]toast) {
	var got []toast
	a := &app{}
	a.notify = func(title, body string) { got = append(got, toast{title, body}) }
	return a, &got
}

// feed drives notifyFor over a sequence of events, as the pump does.
func feed(a *app, evs ...tunnel.Event) {
	for _, e := range evs {
		a.notifyFor(e)
	}
}

func TestNotifyOnConnectAndDrop(t *testing.T) {
	a, got := notifyRecorder()
	feed(a,
		tunnel.Event{State: tunnel.Authenticating},
		tunnel.Event{State: tunnel.Connecting},
		tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.5"},
		tunnel.Event{State: tunnel.Reconnecting, Detail: "connection lost — reconnecting"},
	)
	if len(*got) != 2 {
		t.Fatalf("want 2 notifications (connected, dropped), got %d: %+v", len(*got), *got)
	}
	if (*got)[0].title != "VPN connected" || !strings.Contains((*got)[0].body, "10.0.0.5") {
		t.Errorf("connect notification = %+v, want title 'VPN connected' and the IP in the body", (*got)[0])
	}
	if (*got)[1].title != "VPN dropped" {
		t.Errorf("drop notification = %+v, want title 'VPN dropped'", (*got)[1])
	}
}

// The supervisor re-emits the same state on every backoff round; a toast per
// round is exactly the noise notifications are supposed to avoid.
func TestNotifyOnlyOnTransition(t *testing.T) {
	a, got := notifyRecorder()
	feed(a,
		tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.5"},
		tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.5"},
		tunnel.Event{State: tunnel.Reconnecting},
		tunnel.Event{State: tunnel.Reconnecting},
		tunnel.Event{State: tunnel.Reconnecting},
	)
	if len(*got) != 2 {
		t.Fatalf("want 2 notifications (one per transition), got %d: %+v", len(*got), *got)
	}
}

// A retry that never had a healthy session now DOES notify, once, and says
// something different from a drop.
//
// It used to stay silent, on the reasoning that the menu-bar icon already showed
// it trying. That was wrong in practice: the icon is a colour in the corner of the
// screen, and a connect that quietly retried for ninety seconds before giving up
// read as the app having done nothing at all. The wording must not claim a drop —
// nothing was dropped.
func TestRetryWithoutHealthySessionNotifiesOnce(t *testing.T) {
	a, got := notifyRecorder()
	feed(a,
		tunnel.Event{State: tunnel.Authenticating},
		tunnel.Event{State: tunnel.Connecting},
		tunnel.Event{State: tunnel.Reconnecting, Detail: "gateway refused the session — signing in again"},
		tunnel.Event{State: tunnel.Connecting},
	)
	if len(*got) != 1 {
		t.Fatalf("want exactly one retry notification, got %d: %+v", len(*got), *got)
	}
	if (*got)[0].title == "VPN dropped" {
		t.Error("a retry that never connected must not claim the VPN dropped")
	}
	if (*got)[0].title != "VPN reconnecting" {
		t.Errorf("title = %q, want \"VPN reconnecting\"", (*got)[0].title)
	}
	if !strings.Contains((*got)[0].body, "gateway refused") {
		t.Errorf("body = %q, want the supervisor's reason", (*got)[0].body)
	}
}

// The retry rounds alternate Reconnecting → Connecting → Reconnecting, so every
// Reconnecting looks like a fresh transition. Without per-episode gating this is
// one toast per round — which is exactly the noise the transition rule exists to
// prevent, reintroduced through the back door.
func TestRetryStormNotifiesOncePerEpisode(t *testing.T) {
	a, got := notifyRecorder()
	evs := []tunnel.Event{{State: tunnel.Connecting}}
	for i := 0; i < 6; i++ {
		evs = append(evs,
			tunnel.Event{State: tunnel.Reconnecting, Detail: "still trying"},
			tunnel.Event{State: tunnel.Connecting})
	}
	feed(a, evs...)
	if len(*got) != 1 {
		t.Fatalf("want 1 notification for the whole episode, got %d: %+v", len(*got), *got)
	}
}

// A drop after a healthy session, then retries, then success: three toasts, in
// that order — and the second one must be the drop wording, not the retry wording.
func TestDropThenRetriesThenReconnect(t *testing.T) {
	a, got := notifyRecorder()
	feed(a,
		tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.5"},
		tunnel.Event{State: tunnel.Reconnecting, Detail: "connection lost"},
		tunnel.Event{State: tunnel.Connecting},
		tunnel.Event{State: tunnel.Reconnecting, Detail: "connection lost"},
		tunnel.Event{State: tunnel.Connecting},
		tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.7"},
	)
	if len(*got) != 3 {
		t.Fatalf("want 3 notifications (connected, dropped, connected), got %d: %+v", len(*got), *got)
	}
	if (*got)[1].title != "VPN dropped" {
		t.Errorf("second notification = %+v, want the drop wording", (*got)[1])
	}
	if (*got)[2].title != "VPN connected" || !strings.Contains((*got)[2].body, "10.0.0.7") {
		t.Errorf("third notification = %+v, want the reconnect with the new IP", (*got)[2])
	}
}

// A second episode, after the first ended, must be able to speak again — the
// per-episode flag is a mute for one storm, not a permanent one.
func TestNewEpisodeNotifiesAgain(t *testing.T) {
	a, got := notifyRecorder()
	feed(a,
		tunnel.Event{State: tunnel.Reconnecting, Detail: "first"},
		tunnel.Event{State: tunnel.Error, Detail: "gave up"},
		tunnel.Event{State: tunnel.Connecting},
		tunnel.Event{State: tunnel.Reconnecting, Detail: "second"},
	)
	if len(*got) != 3 {
		t.Fatalf("want 3 (retry, error, retry-again), got %d: %+v", len(*got), *got)
	}
	if (*got)[2].title != "VPN reconnecting" {
		t.Errorf("third = %+v, want the second episode to notify", (*got)[2])
	}
}

// A user Disconnect ends the episode too, so a later retry is not muted by a flag
// left set from before.
func TestUserDisconnectClearsTheEpisode(t *testing.T) {
	a, got := notifyRecorder()
	feed(a,
		tunnel.Event{State: tunnel.Reconnecting, Detail: "first"},
		tunnel.Event{State: tunnel.Disconnected},
		tunnel.Event{State: tunnel.Reconnecting, Detail: "second"},
	)
	if len(*got) != 2 {
		t.Fatalf("want 2 retry notifications either side of the disconnect, got %d: %+v", len(*got), *got)
	}
}

// The terminal error carries the supervisor's clean detail (session taken,
// sign-in didn't complete, broken install) — never raw openconnect stderr,
// which friendlyDetail already stripped.
func TestNotifyOnTerminalError(t *testing.T) {
	a, got := notifyRecorder()
	feed(a,
		tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.5"},
		tunnel.Event{State: tunnel.Reconnecting},
		tunnel.Event{State: tunnel.Error, Detail: "VPN session ended — click Connect to sign in"},
	)
	last := (*got)[len(*got)-1]
	if last.title != "VPN disconnected" || !strings.Contains(last.body, "session ended") {
		t.Errorf("error notification = %+v, want title 'VPN disconnected' and the session-ended detail", last)
	}
}

// An error with no detail still says something actionable.
func TestNotifyErrorWithoutDetailHasBody(t *testing.T) {
	a, got := notifyRecorder()
	feed(a, tunnel.Event{State: tunnel.Error})
	if len(*got) != 1 || (*got)[0].body == "" {
		t.Fatalf("want one error notification with a non-empty body, got %+v", *got)
	}
}

// Disconnect is the user's own doing — no toast for it.
func TestNoNotificationOnUserDisconnect(t *testing.T) {
	a, got := notifyRecorder()
	feed(a,
		tunnel.Event{State: tunnel.Connected, Detail: "10.0.0.5"},
		tunnel.Event{State: tunnel.Disconnected},
	)
	if len(*got) != 1 {
		t.Fatalf("want only the connect notification, got %+v", *got)
	}
}

// notify is nil until main() wires it to tray.ShowMessage after tray.Setup
// (and in most tests, which never call main()); notifyFor must tolerate that
// rather than panic in the pump.
func TestNotifyForNilSeam(t *testing.T) {
	a := &app{}
	a.notifyFor(tunnel.Event{State: tunnel.Connected}) // must not panic
}
