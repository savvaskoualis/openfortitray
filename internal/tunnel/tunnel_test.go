package tunnel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// collector drains events into a slice from a background goroutine. Access is
// mutex-guarded so tests can inspect progress while the supervisor runs
// (the brief's sketch shared the slice unsynchronised, which races).
type collector struct {
	mu   sync.Mutex
	seen []Event
	stop chan struct{}
	done chan struct{}
}

func collect(ch <-chan Event) *collector {
	c := &collector{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(c.done)
		for {
			select {
			case e := <-ch:
				c.mu.Lock()
				c.seen = append(c.seen, e)
				c.mu.Unlock()
			case <-c.stop:
				return
			}
		}
	}()
	return c
}

func (c *collector) close() {
	close(c.stop)
	<-c.done
}

func (c *collector) snapshot() []Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Event(nil), c.seen...)
}

// waitFor blocks until an event with the wanted state has been observed.
func (c *collector) waitFor(t *testing.T, want State, timeout time.Duration) Event {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		for _, e := range c.snapshot() {
			if e.State == want {
				return e
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("state %v never reached; got %+v", want, c.snapshot())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitForDetail is waitFor for the cases where two events share a state and only
// the detail tells them apart (a Reconnecting caused by a plain exit, versus one
// caused by a permanent-looking failure the supervisor decided to retry).
func (c *collector) waitForDetail(t *testing.T, want State, detail string, timeout time.Duration) Event {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		for _, e := range c.snapshot() {
			if e.State == want && strings.Contains(e.Detail, detail) {
				return e
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no %v event with a detail containing %q; got %+v", want, detail, c.snapshot())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestStateString(t *testing.T) {
	want := []string{"Disconnected", "Authenticating", "Connecting", "Connected", "Reconnecting", "Error"}
	for i, w := range want {
		if got := State(i).String(); got != w {
			t.Errorf("State(%d).String() = %q, want %q", i, got, w)
		}
	}
	if got := State(99).String(); got == "" {
		t.Error("out-of-range State must not panic or return empty string")
	}
}

func TestConnectHappyPath(t *testing.T) {
	events := make(chan Event, 64)
	c := collect(events)
	defer c.close()

	auth := func(ctx context.Context) (string, error) { return "COOKIE", nil }
	run := func(ctx context.Context, cookie string, connected func(string)) error {
		if cookie != "COOKIE" {
			t.Errorf("wrong cookie %q", cookie)
		}
		connected("10.0.0.5")
		<-ctx.Done() // stay "up" until disconnected
		return ctx.Err()
	}
	s := New(auth, run, events)
	s.Connect()
	e := c.waitFor(t, Connected, 2*time.Second)
	if e.Detail != "10.0.0.5" {
		t.Errorf("Connected detail = %q, want assigned IP", e.Detail)
	}
	s.Disconnect()
	c.waitFor(t, Disconnected, 2*time.Second)
}

func TestConnectIsIdempotent(t *testing.T) {
	events := make(chan Event, 64)
	c := collect(events)
	defer c.close()

	var authCalls atomic.Int32
	auth := func(ctx context.Context) (string, error) { authCalls.Add(1); return "C", nil }
	run := func(ctx context.Context, cookie string, connected func(string)) error {
		connected("10.0.0.1")
		<-ctx.Done()
		return ctx.Err()
	}
	s := New(auth, run, events)
	s.Connect()
	s.Connect()
	s.Connect()
	c.waitFor(t, Connected, 2*time.Second)
	if n := authCalls.Load(); n != 1 {
		t.Errorf("auth calls = %d, want 1 (Connect must be idempotent)", n)
	}
	s.Disconnect()
	s.Disconnect() // must not panic
	c.waitFor(t, Disconnected, 2*time.Second)
}

func TestReconnectOnDrop(t *testing.T) {
	events := make(chan Event, 64)
	c := collect(events)
	defer c.close()

	runs := make(chan struct{}, 8)
	auth := func(ctx context.Context) (string, error) { return "C", nil }
	run := func(ctx context.Context, cookie string, connected func(string)) error {
		if cookie != "C" {
			t.Errorf("expected same cookie to be reused, got %q", cookie)
		}
		select {
		case runs <- struct{}{}:
		default:
		}
		connected("10.0.0.5")
		return errors.New("link dropped") // simulated network drop
	}
	s := New(auth, run, events)
	s.backoffBase = 20 * time.Millisecond // test hook: shrink backoff
	s.Connect()
	// expect at least 2 runs (initial + restart after drop)
	for i := 0; i < 2; i++ {
		select {
		case <-runs:
		case <-time.After(3 * time.Second):
			t.Fatalf("run %d never happened", i+1)
		}
	}
	e := c.waitFor(t, Reconnecting, 2*time.Second)
	if e.Detail != "connection lost — reconnecting" {
		t.Errorf("Reconnecting detail = %q, want the clean reconnecting text", e.Detail)
	}
	s.Disconnect()
	c.waitFor(t, Disconnected, 2*time.Second)
}

func TestAuthRejectedTriggersReauth(t *testing.T) {
	events := make(chan Event, 64)
	c := collect(events)
	defer c.close()

	var authCalls atomic.Int32
	auth := func(ctx context.Context) (string, error) { authCalls.Add(1); return "C", nil }
	var runs atomic.Int32
	run := func(ctx context.Context, cookie string, connected func(string)) error {
		if runs.Add(1) == 1 {
			return ErrAuthRejected
		}
		connected("10.0.0.5")
		<-ctx.Done()
		return ctx.Err()
	}
	s := New(auth, run, events)
	s.backoffBase = 20 * time.Millisecond
	// This test pins the clear-cookie + re-auth fallback itself, so disable the
	// quiet startup re-mints (TestQuietEarlyRetry* covers those) and ask for a
	// re-mint on every refused round rather than the periodic default — the
	// periodic default exists to avoid a browser tab per round and is covered by
	// TestRefusedRoundsDoNotRemintEveryTime.
	s.maxEarlyRetries = 0
	s.remintEveryRounds = 1
	s.Connect()
	c.waitFor(t, Connected, 3*time.Second)
	if n := authCalls.Load(); n < 2 {
		t.Fatalf("expected re-auth after ErrAuthRejected, auth calls = %d", n)
	}
	s.Disconnect()
	c.waitFor(t, Disconnected, 2*time.Second)
}

// A cookie that carried a healthy session and is then rejected (server-side
// session kill) must be replaced without waiting out the reconnect backoff.
func TestAuthRejectedAfterHealthySessionReauthsImmediately(t *testing.T) {
	events := make(chan Event, 64)
	c := collect(events)
	defer c.close()

	var authCalls atomic.Int32
	auth := func(ctx context.Context) (string, error) { authCalls.Add(1); return "C", nil }
	var runs atomic.Int32
	run := func(ctx context.Context, cookie string, connected func(string)) error {
		connected("10.0.0.5")
		if runs.Add(1) == 1 {
			time.Sleep(40 * time.Millisecond) // healthy session, then rejected
			return ErrAuthRejected
		}
		<-ctx.Done()
		return ctx.Err()
	}
	s := New(auth, run, events)
	s.backoffBase = time.Hour // must not be waited out on this path
	s.minHealthy = 20 * time.Millisecond
	s.Connect()
	deadline := time.Now().Add(2 * time.Second)
	for authCalls.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("no immediate re-auth after mid-session rejection, auth calls = %d", authCalls.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
	c.waitFor(t, Connected, 2*time.Second)
	s.Disconnect()
	c.waitFor(t, Disconnected, 2*time.Second)
}

// After a healthy session, if reconnect attempts keep getting rejected WITHOUT
// coming back up (the one-per-user gateway handed our slot to another login), the
// supervisor must STOP with a "session ended — click Connect" Error rather than
// spin SAML (which pops the browser and, unattended, times out).
func TestSessionTakenAfterHealthyStops(t *testing.T) {
	events := make(chan Event, 128)
	c := collect(events)
	defer c.close()

	var runs atomic.Int32
	auth := func(ctx context.Context) (string, error) { return "C", nil }
	run := func(ctx context.Context, cookie string, connected func(string)) error {
		if runs.Add(1) == 1 {
			connected("10.0.0.5")
			time.Sleep(20 * time.Millisecond) // one healthy session...
			return ErrAuthRejected            // ...then the slot is taken
		}
		return ErrAuthRejected // reconnects rejected without ever coming up
	}
	s := New(auth, run, events)
	s.backoffBase, s.backoffMax, s.minHealthy = 5*time.Millisecond, 5*time.Millisecond, 10*time.Millisecond
	s.maxEarlyRetries = 0
	s.Connect()
	e := c.waitForDetail(t, Error, "session ended", 3*time.Second)
	if !strings.Contains(e.Detail, "Connect") {
		t.Errorf("session-ended detail should prompt Connect, got %q", e.Detail)
	}
	settled := runs.Load()
	time.Sleep(120 * time.Millisecond) // a spinning supervisor would keep re-running
	if got := runs.Load(); got != settled {
		t.Errorf("supervisor kept re-running after session-ended (SAML spin): runs %d -> %d", settled, got)
	}
	// Terminal by design: the supervisor stays in the Error state (Connect
	// clickable) rather than emitting Disconnected — exactly like ErrPermanent.
	s.Disconnect() // must not panic on an already-stopped supervisor
}

// Connecting briefly and then being rejected must not buy a zero-delay re-auth:
// otherwise a flapping gateway opens one SAML browser window per iteration.
func TestShortLivedConnectionRejectedBacksOff(t *testing.T) {
	events := make(chan Event, 64)
	c := collect(events)
	defer c.close()

	var authCalls atomic.Int32
	auth := func(ctx context.Context) (string, error) { authCalls.Add(1); return "C", nil }
	run := func(ctx context.Context, cookie string, connected func(string)) error {
		connected("10.0.0.5")
		return ErrAuthRejected // never healthy for minHealthy
	}
	s := New(auth, run, events)
	s.backoffBase = time.Hour
	s.minHealthy = time.Hour
	s.Connect()
	c.waitFor(t, Reconnecting, 2*time.Second)
	time.Sleep(50 * time.Millisecond)
	if n := authCalls.Load(); n != 1 {
		t.Errorf("auth calls = %d, want 1 (short-lived connection must not earn a fast re-auth)", n)
	}
	s.Disconnect()
	c.waitFor(t, Disconnected, 2*time.Second)
}

// Consecutive zero-delay re-auths are capped: after one fast re-auth the loop
// must pay the backoff before minting another cookie.
func TestImmediateReauthIsCapped(t *testing.T) {
	events := make(chan Event, 64)
	c := collect(events)
	defer c.close()

	var authCalls atomic.Int32
	auth := func(ctx context.Context) (string, error) { authCalls.Add(1); return "C", nil }
	run := func(ctx context.Context, cookie string, connected func(string)) error {
		connected("10.0.0.5")
		time.Sleep(30 * time.Millisecond) // always "healthy", always rejected
		return ErrAuthRejected
	}
	s := New(auth, run, events)
	s.backoffBase = time.Hour // second rejection must stall here
	s.minHealthy = 10 * time.Millisecond
	s.Connect()
	c.waitFor(t, Reconnecting, 3*time.Second)
	time.Sleep(100 * time.Millisecond)
	if n := authCalls.Load(); n != 2 {
		t.Errorf("auth calls = %d, want 2 (one immediate re-auth, then backoff)", n)
	}
	s.Disconnect()
	c.waitFor(t, Disconnected, 2*time.Second)
}

// A freshly minted cookie that is rejected must not spin: the loop backs off
// before asking for another one.
func TestFreshCookieRejectedBacksOff(t *testing.T) {
	events := make(chan Event, 64)
	c := collect(events)
	defer c.close()

	var authCalls atomic.Int32
	auth := func(ctx context.Context) (string, error) { authCalls.Add(1); return "C", nil }
	run := func(ctx context.Context, cookie string, connected func(string)) error {
		return ErrAuthRejected
	}
	s := New(auth, run, events)
	s.backoffBase = time.Hour
	// Pin the backoff fallback: disable the quiet startup grace so a rejected
	// fresh cookie goes straight to the backoff wait, as this test asserts. The
	// grace itself (retry same cookie first) is covered by TestQuietEarlyRetry*.
	s.maxEarlyRetries = 0
	s.Connect()
	c.waitFor(t, Reconnecting, 2*time.Second)
	time.Sleep(50 * time.Millisecond)
	if n := authCalls.Load(); n != 1 {
		t.Errorf("auth calls = %d, want 1 (must wait out backoff before re-auth)", n)
	}
	s.Disconnect()
	c.waitFor(t, Disconnected, 2*time.Second)
}

// Once the resend budget is spent (maxSameCookieRetries = 0 here), a cookie the
// gateway keeps refusing is treated as dead and RE-MINTED — a quiet,
// non-interactive re-SAML — while staying on Connecting… and never flashing
// Reconnecting/Error, recovering once the fresh cookie is accepted. The resend
// that comes first is covered by TestRejectedCookieIsResentBeforeRemint.
func TestQuietEarlyRetryRemintsCookieAndStaysConnecting(t *testing.T) {
	events := make(chan Event, 64)
	c := collect(events)
	defer c.close()

	var authCalls atomic.Int32
	auth := func(ctx context.Context) (string, error) {
		return fmt.Sprintf("C%d", authCalls.Add(1)), nil
	}
	var mu sync.Mutex
	var cookiesSeen []string
	var runs atomic.Int32
	run := func(ctx context.Context, cookie string, connected func(string)) error {
		mu.Lock()
		cookiesSeen = append(cookiesSeen, cookie)
		mu.Unlock()
		if runs.Add(1) == 1 {
			return ErrAuthRejected // gateway still holds the previous run's session
		}
		connected("10.0.0.5") // the FRESH cookie is accepted
		<-ctx.Done()
		return ctx.Err()
	}
	s := New(auth, run, events)
	s.earlyRetryDelay = 10 * time.Millisecond
	s.backoffBase = time.Hour // the quiet re-mint, NOT the backoff, must be what recovers
	s.Connect()
	c.waitFor(t, Connected, 2*time.Second)

	if n := authCalls.Load(); n < 2 {
		t.Errorf("auth calls = %d, want >= 2 (the quiet early retry must re-mint, not re-send)", n)
	}
	mu.Lock()
	seen := append([]string(nil), cookiesSeen...)
	mu.Unlock()
	if len(seen) < 2 || seen[0] == seen[1] {
		t.Errorf("early retry must use a fresh cookie, got %v", seen)
	}
	for _, e := range c.snapshot() {
		if e.State == Reconnecting || e.State == Error {
			t.Errorf("quiet early retry emitted %v; the tray must stay on Connecting…", e.State)
		}
	}
	s.Disconnect()
	c.waitFor(t, Disconnected, 2*time.Second)
}

// The same across more than one quiet re-mint (resend budget disabled): the
// gateway refuses the first two cookies and accepts the third. Every re-mint
// must produce a DISTINCT cookie and the tunnel must reach
// Connected within maxEarlyRetries quiet re-auths — before any loud
// Reconnecting/Error and before the backoff.
func TestQuietEarlyRetryRemintsFreshCookieEachRetry(t *testing.T) {
	events := make(chan Event, 64)
	c := collect(events)
	defer c.close()

	var authCalls atomic.Int32
	auth := func(ctx context.Context) (string, error) {
		return fmt.Sprintf("C%d", authCalls.Add(1)), nil
	}
	var mu sync.Mutex
	var cookiesSeen []string
	var runs atomic.Int32
	run := func(ctx context.Context, cookie string, connected func(string)) error {
		mu.Lock()
		cookiesSeen = append(cookiesSeen, cookie)
		mu.Unlock()
		if runs.Add(1) <= 2 {
			return ErrAuthRejected // first two fresh cookies are still refused
		}
		connected("10.0.0.5") // the third is accepted
		<-ctx.Done()
		return ctx.Err()
	}
	s := New(auth, run, events)
	s.earlyRetryDelay = 5 * time.Millisecond
	s.maxEarlyRetries = 2
	s.backoffBase = time.Hour // the quiet re-mints, NOT the backoff, must be what recovers
	s.Connect()
	c.waitFor(t, Connected, 2*time.Second)

	// (a) the auth function was called again for each retry.
	if n := authCalls.Load(); n < 2 {
		t.Errorf("auth calls = %d, want >= 2 (each early retry must re-mint)", n)
	}
	mu.Lock()
	seen := append([]string(nil), cookiesSeen...)
	mu.Unlock()
	// (b) each early retry used a DIFFERENT cookie value.
	if len(seen) < 3 {
		t.Fatalf("runFn saw %d cookies, want at least 3 (two rejected + one accepted); got %v", len(seen), seen)
	}
	for i, ck := range seen[:3] {
		for j := i + 1; j < 3; j++ {
			if ck == seen[j] {
				t.Errorf("early retries must each use a fresh cookie; seen[%d] == seen[%d] == %q (all: %v)", i, j, ck, seen)
			}
		}
	}
	// (c) Connected was reached quietly, within maxEarlyRetries — no loud fallback.
	for _, e := range c.snapshot() {
		if e.State == Reconnecting || e.State == Error {
			t.Errorf("reached Connected only via the loud fallback (%v); the quiet re-mints must recover within maxEarlyRetries", e.State)
		}
	}
	s.Disconnect()
	c.waitFor(t, Disconnected, 2*time.Second)
}

// Once the quiet grace is exhausted the loop must fall back to the loud path:
// re-authenticate and back off. Each quiet retry re-mints a FRESH cookie (the
// gateway rejects a stale one outright), so the initial attempt plus the
// maxEarlyRetries quiet re-mints are all distinct cookies, and only after them
// does the loud Reconnecting fire.
func TestQuietEarlyRetryExhaustsThenReauthsAndBacksOff(t *testing.T) {
	events := make(chan Event, 128)
	c := collect(events)
	defer c.close()

	var mu sync.Mutex
	var cookiesSeen []string
	var authN atomic.Int32
	auth := func(ctx context.Context) (string, error) {
		return fmt.Sprintf("C%d", authN.Add(1)), nil
	}
	run := func(ctx context.Context, cookie string, connected func(string)) error {
		mu.Lock()
		cookiesSeen = append(cookiesSeen, cookie)
		mu.Unlock()
		return ErrAuthRejected // never connects: the gateway refuses every cookie
	}
	s := New(auth, run, events)
	s.earlyRetryDelay = 5 * time.Millisecond
	s.maxEarlyRetries = 2
	s.backoffBase = 10 * time.Millisecond
	s.Connect()

	// A Reconnecting event is only emitted on the loud fallback, so its arrival
	// proves the grace was exhausted first. By then the quiet phase (initial +
	// maxEarlyRetries attempts) has already run, each with its own fresh cookie.
	c.waitFor(t, Reconnecting, 2*time.Second)
	s.Disconnect()
	c.waitFor(t, Disconnected, 2*time.Second)

	mu.Lock()
	seen := append([]string(nil), cookiesSeen...)
	mu.Unlock()
	quiet := 1 + s.maxEarlyRetries
	if len(seen) < quiet {
		t.Fatalf("runFn saw %d cookies, want at least %d (initial + %d quiet re-mints); saw %v",
			len(seen), quiet, s.maxEarlyRetries, seen)
	}
	// The quiet phase must re-mint a distinct cookie every time — never re-send a
	// rejected one.
	for i := 0; i < quiet; i++ {
		for j := i + 1; j < quiet; j++ {
			if seen[i] == seen[j] {
				t.Errorf("quiet retries must each re-mint a fresh cookie; seen[%d] == seen[%d] == %q (all: %v)",
					i, j, seen[i], seen)
			}
		}
	}
}

// A cookie that has carried a healthy session (everConnected) does NOT get the
// quiet startup grace: a mid-session server-side kill must still take the
// immediate-reauth path, not sit on the same dead cookie. Guards against the
// early-retry gate leaking into the proven-cookie path.
func TestQuietEarlyRetryDoesNotApplyAfterConnected(t *testing.T) {
	events := make(chan Event, 64)
	c := collect(events)
	defer c.close()

	var authCalls atomic.Int32
	auth := func(ctx context.Context) (string, error) { authCalls.Add(1); return "C", nil }
	var runs atomic.Int32
	run := func(ctx context.Context, cookie string, connected func(string)) error {
		connected("10.0.0.5")
		if runs.Add(1) == 1 {
			time.Sleep(30 * time.Millisecond) // healthy long enough to be "proven"
			return ErrAuthRejected            // then a server-side session kill
		}
		<-ctx.Done()
		return ctx.Err()
	}
	s := New(auth, run, events)
	s.minHealthy = 10 * time.Millisecond
	s.earlyRetryDelay = time.Hour // must NOT be paid: this path is immediate re-auth
	s.backoffBase = time.Hour
	s.Connect()

	deadline := time.Now().Add(2 * time.Second)
	for authCalls.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("proven cookie rejected mid-session did not re-auth immediately; auth calls = %d", authCalls.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
	c.waitFor(t, Connected, 2*time.Second)
	s.Disconnect()
	c.waitFor(t, Disconnected, 2*time.Second)
}

func TestAuthFailureStopsWithError(t *testing.T) {
	events := make(chan Event, 64)
	c := collect(events)
	defer c.close()

	var authCalls atomic.Int32
	auth := func(ctx context.Context) (string, error) {
		authCalls.Add(1)
		return "", errors.New("saml timeout")
	}
	run := func(ctx context.Context, cookie string, connected func(string)) error {
		t.Error("run must not be called when auth fails")
		return nil
	}
	s := New(auth, run, events)
	s.backoffBase = 20 * time.Millisecond
	s.Connect()
	e := c.waitFor(t, Error, 2*time.Second)
	if e.Detail == "" {
		t.Error("Error event must carry a detail")
	}
	time.Sleep(100 * time.Millisecond)
	// Error is terminal for this run: a trailing Disconnected would wipe the
	// error text from a latest-state UI.
	seen := c.snapshot()
	if last := seen[len(seen)-1]; last.State != Error {
		t.Errorf("last event = %v, want Error to be terminal; got %+v", last.State, seen)
	}
	if n := authCalls.Load(); n != 1 {
		t.Errorf("auth calls = %d, want 1 (loop must stop after auth failure)", n)
	}
	// Loop stopped, so Connect must be able to start it again.
	s.Connect()
	deadline := time.Now().Add(2 * time.Second)
	for authCalls.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("Connect after auth failure did not restart the loop")
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.Disconnect()
}

// A broken installation cannot be retried away. Before this was classified, a
// missing openconnect or a missing sudoers rule left the tray cycling
// "Reconnecting…" forever: the one state that promises the app is handling it,
// while every attempt failed identically and the user was never told what to fix.
//
// The counterweight is that a *working* tunnel must not be killed off by a
// permanent-looking blip, so the terminal verdict is gated on the tunnel never
// having come up since this Connect — the last case below is that gate.
func TestPermanentFailureIsTerminalUnlessTheTunnelWorked(t *testing.T) {
	// A stub sudo that prints one thing and fails, standing in for the two ways
	// the privileged path can be missing.
	failingSudo := func(t *testing.T, output string) RunFunc {
		t.Helper()
		sudo := writeScript(t, t.TempDir(), "sudo", "#!/bin/sh\n"+
			"cat >/dev/null\n"+ // swallow the cookie on stdin
			"printf '%s\\n' "+strconv.Quote(output)+" >&2\n"+
			"exit 1\n")
		return RunOpenconnect(Options{
			Gateway:    "gw.example.com:10443",
			HelperPath: "/opt/custom/openfortitray-tunnel",
			UseSudo:    true,
			sudoPath:   sudo,
		})
	}

	tests := []struct {
		name       string
		needsShell bool // uses a shell stub, so POSIX only
		runFn      func(t *testing.T) RunFunc
		wantState  State
		wantDetail string
		wantRuns   int32 // exact when terminal, a minimum otherwise
		terminal   bool
	}{{
		// The real runner, so exec.ErrNotFound → ErrPermanent is exercised end to
		// end rather than injected.
		name: "backend binary is not on PATH",
		runFn: func(t *testing.T) RunFunc {
			return RunOpenconnect(Options{
				OpenconnectPath: "openfortitray-openconnect-does-not-exist",
				Gateway:         "gw.example.com:10443",
			})
		},
		wantState:  Error,
		wantDetail: installHintFor(runtime.GOOS), // OS-aware: the detail must lead with the fix
		wantRuns:   1,
		terminal:   true,
	}, {
		name:       "sudoers rule is missing, so sudo -n wants a password",
		needsShell: true,
		runFn: func(t *testing.T) RunFunc {
			return failingSudo(t, "sudo: a password is required")
		},
		wantState:  Error,
		wantDetail: "install is broken", // clean first-line hint; the sudo/openconnect specifics stay in the log
		wantRuns:   1,
		terminal:   true,
	}, {
		name:       "helper is in place but never went through the installer",
		needsShell: true,
		runFn: func(t *testing.T) RunFunc {
			return failingSudo(t, "openfortitray-tunnel: not installed: run scripts/install.sh, "+
				"which bakes in the openconnect path")
		},
		wantState:  Error,
		wantDetail: "install is broken", // clean first-line hint; the diagnostic tail stays in the log
		wantRuns:   1,
		terminal:   true,
	}, {
		// The gate: the install demonstrably worked, so the same marker is now
		// far more likely to be transient (sudo unavailable for a moment, config
		// management rewriting /etc/sudoers.d) than a real misconfiguration.
		name: "the same marker after a healthy connection keeps retrying",
		runFn: func(t *testing.T) RunFunc {
			var attempts atomic.Int32
			return func(ctx context.Context, cookie string, connected func(string)) error {
				if attempts.Add(1) == 1 {
					connected("10.0.0.5")
					return nil // the link dropped; nothing wrong with the install
				}
				return fmt.Errorf("%w: %s\nsudo: a password is required", ErrPermanent, installHint)
			}
		},
		wantState:  Reconnecting,
		wantDetail: "install is broken", // clean first-line hint; the sudo/openconnect specifics stay in the log
		wantRuns:   2,
		terminal:   false,
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.needsShell && runtime.GOOS == "windows" {
				t.Skip("shell stub: POSIX only (the privileged path is macOS/Linux anyway)")
			}
			events := make(chan Event, 64)
			c := collect(events)
			defer c.close()

			var runs atomic.Int32
			inner := tc.runFn(t)
			run := func(ctx context.Context, cookie string, connected func(string)) error {
				runs.Add(1)
				return inner(ctx, cookie, connected)
			}
			s := New(func(ctx context.Context) (string, error) { return "C", nil }, run, events)
			s.backoffBase = 20 * time.Millisecond
			s.backoffMax = 20 * time.Millisecond
			defer s.Disconnect()

			s.Connect()
			e := c.waitForDetail(t, tc.wantState, tc.wantDetail, 5*time.Second)
			time.Sleep(200 * time.Millisecond) // time for a retry, or a stray event, to land
			seen := c.snapshot()

			// The tray shows the first line of a detail, clipped at 60 runes. The
			// instruction has to survive that, or the user is told there is an
			// error and nothing about how to clear it.
			first := strings.SplitN(e.Detail, "\n", 2)[0]
			if !strings.Contains(first, installHintFor(runtime.GOOS)) {
				t.Errorf("first line of the detail is %q; the OS-aware fix hint %q must "+
					"appear there, not after the process output the menu clips away",
					first, installHintFor(runtime.GOOS))
			}
			if n := len([]rune(first)); n > 60 {
				t.Errorf("first line of the detail is %d runes (%q); the menu clips at 60", n, first)
			}

			if !tc.terminal {
				for _, e := range seen {
					if e.State == Error {
						t.Fatalf("Error after a healthy connection: the supervisor must keep "+
							"retrying instead; got %+v", seen)
					}
				}
				if n := runs.Load(); n < tc.wantRuns {
					t.Errorf("backend started %d times, want at least %d", n, tc.wantRuns)
				}
				return
			}
			// Error is the last word: a trailing Disconnected would wipe the
			// explanation out of a latest-state UI, and Reconnecting would promise
			// a recovery that cannot happen.
			if last := seen[len(seen)-1]; last.State != Error {
				t.Errorf("last event = %v, want Error to be terminal; got %+v", last.State, seen)
			}
			for _, e := range seen {
				if e.State == Reconnecting {
					t.Errorf("emitted Reconnecting for a permanent failure; got %+v", seen)
					break
				}
			}
			if n := runs.Load(); n != tc.wantRuns {
				t.Errorf("backend started %d times, want exactly %d: a permanent failure "+
					"must not be retried", n, tc.wantRuns)
			}
		})
	}
}

// Events must arrive in lifecycle order, with no surprises before Connected.
func TestEventOrderOnConnect(t *testing.T) {
	events := make(chan Event, 64)
	c := collect(events)
	defer c.close()

	auth := func(ctx context.Context) (string, error) { return "C", nil }
	run := func(ctx context.Context, cookie string, connected func(string)) error {
		connected("10.0.0.5")
		<-ctx.Done()
		return ctx.Err()
	}
	s := New(auth, run, events)
	s.Connect()
	c.waitFor(t, Connected, 2*time.Second)
	want := []State{Authenticating, Connecting, Connected}
	seen := c.snapshot()
	if len(seen) < len(want) {
		t.Fatalf("got %+v, want at least %v", seen, want)
	}
	for i, w := range want {
		if seen[i].State != w {
			t.Fatalf("event %d = %v, want %v (full sequence %+v)", i, seen[i].State, w, seen)
		}
	}
	s.Disconnect()
	c.waitFor(t, Disconnected, 2*time.Second)
	time.Sleep(50 * time.Millisecond)
	seen = c.snapshot()
	if last := seen[len(seen)-1]; last.State != Disconnected {
		t.Errorf("last event = %v, want Disconnected; got %+v", last.State, seen)
	}
}

// Disconnect immediately followed by Connect must leave exactly one loop
// running, with no stale events from the loop that is winding down.
func TestDisconnectThenConnectRestarts(t *testing.T) {
	events := make(chan Event, 64)
	c := collect(events)
	defer c.close()

	release := make(chan struct{})
	var authCalls atomic.Int32
	auth := func(ctx context.Context) (string, error) { authCalls.Add(1); return "C", nil }
	var inRun, maxInRun atomic.Int32
	run := func(ctx context.Context, cookie string, connected func(string)) error {
		n := inRun.Add(1)
		for {
			m := maxInRun.Load()
			if n <= m || maxInRun.CompareAndSwap(m, n) {
				break
			}
		}
		defer inRun.Add(-1)
		<-release // hold the first loop inside runFn so its teardown lags
		connected("10.0.0.5")
		<-ctx.Done()
		return ctx.Err()
	}
	s := New(auth, run, events)
	s.Connect()
	deadline := time.Now().Add(2 * time.Second)
	for authCalls.Load() < 1 {
		if time.Now().After(deadline) {
			t.Fatal("first loop never authenticated")
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.Disconnect()
	s.Connect() // second loop starts while the first is still winding down
	close(release)
	c.waitFor(t, Connected, 3*time.Second)
	time.Sleep(100 * time.Millisecond) // give any stale event time to land

	if n := maxInRun.Load(); n > 1 {
		t.Errorf("%d backends ran concurrently, want at most 1", n)
	}
	seen := c.snapshot()
	connectedAt := -1
	for i, e := range seen {
		if e.State == Connected {
			connectedAt = i
			break
		}
	}
	for _, e := range seen[connectedAt+1:] {
		if e.State == Disconnected {
			t.Errorf("stale Disconnected after Connected: %+v", seen)
			break
		}
	}
	s.Disconnect()
	c.waitFor(t, Disconnected, 2*time.Second)
}

// A backend that ignores every teardown mechanism (a root openconnect the app
// cannot signal, with the helper unreachable) must not leave the next Connect
// waiting forever: without a bound the tray sits in Connecting with no
// explanation and no way out.
func TestWedgedPreviousBackendFailsConnectInsteadOfHanging(t *testing.T) {
	events := make(chan Event, 64)
	c := collect(events)
	defer c.close()

	wedged := make(chan struct{}) // never closed: the first backend never exits
	auth := func(ctx context.Context) (string, error) { return "C", nil }
	var runs atomic.Int32
	run := func(ctx context.Context, cookie string, connected func(string)) error {
		runs.Add(1)
		<-wedged
		return nil
	}
	s := New(auth, run, events)
	s.prevWait = 150 * time.Millisecond
	s.Connect()
	c.waitFor(t, Connecting, 2*time.Second)

	s.Disconnect() // cancels the context; the backend ignores it
	s.Connect()    // must give up on the wedged predecessor, not block

	c.waitFor(t, Error, 2*time.Second)
	for _, e := range c.snapshot() {
		if e.State == Error && !strings.Contains(e.Detail, "restart the app") {
			t.Errorf("Error detail = %q, want actionable advice", e.Detail)
		}
	}
	// Crucially it must not have started a second backend: two openconnects
	// would fight over the routing table.
	if n := runs.Load(); n != 1 {
		t.Errorf("runFn called %d times, want 1: no second backend may start while the first is wedged", n)
	}
	// The terminal Error must not be papered over by a trailing Disconnected.
	last := c.snapshot()
	if e := last[len(last)-1]; e.State != Error {
		t.Errorf("last event = %+v, want the Error to stay terminal", e)
	}
	close(wedged)
}

// A blocked events channel must never stall the supervisor.
func TestEmitDoesNotBlockOnFullChannel(t *testing.T) {
	events := make(chan Event) // unbuffered, nobody reading
	auth := func(ctx context.Context) (string, error) { return "C", nil }
	connectedCh := make(chan struct{})
	run := func(ctx context.Context, cookie string, connected func(string)) error {
		connected("10.0.0.1")
		close(connectedCh)
		<-ctx.Done()
		return ctx.Err()
	}
	s := New(auth, run, events)
	s.Connect()
	select {
	case <-connectedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("supervisor blocked on unread events channel")
	}
	s.Disconnect()
}

// An absolute openconnect_path that does not exist fails at execve with ENOENT
// rather than as exec.ErrNotFound (os/exec only does a PATH lookup for bare
// names), and that is the shape Windows produces — where openconnect_path is an
// absolute install path. It has to be classified as permanent too, or that
// platform retries a missing binary forever.
func TestRunOpenconnectStartFailure(t *testing.T) {
	run := RunOpenconnect(Options{
		OpenconnectPath: filepath.Join(t.TempDir(), "openconnect-openfortitray-test"),
		Gateway:         "vpn.example.com:443",
	})
	err := run(context.Background(), "COOKIE", func(string) { t.Error("connected must not be called") })
	if err == nil {
		t.Fatal("expected an error when the binary does not exist")
	}
	if !errors.Is(err, ErrPermanent) {
		t.Errorf("err = %v, want ErrPermanent: retrying cannot conjure up a missing binary", err)
	}
	if !strings.Contains(err.Error(), installHintFor(runtime.GOOS)) {
		t.Errorf("err = %v, want it to carry the OS-aware install-fix hint %q", err, installHintFor(runtime.GOOS))
	}
}

// The IP is scraped out of openconnect's progress output, so the pattern is
// pinned to wording taken from real openconnect format strings. Getting this
// wrong means the tunnel never reports Connected: the UI stays yellow and the
// supervisor never counts the cookie as proven.
func TestConnectedRegex(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{{
		name: "openconnect 9.x IPv4",
		line: "Configured as 10.0.0.5, with SSL connected and DTLS connected",
		want: "10.0.0.5",
	}, {
		name: "openconnect 9.x ESP disabled",
		line: "Configured as 10.0.0.5, with SSL connected and ESP disabled",
		want: "10.0.0.5",
	}, {
		name: "openconnect 9.x dual stack reports the legacy IP first",
		line: "Configured as 10.0.0.5 + 2001:db8::5, with SSL connected and ESP in progress",
		want: "10.0.0.5",
	}, {
		name: "openconnect 7.x wording",
		line: "Connected as 10.0.0.9, using SSL + LZ4",
		want: "10.0.0.9",
	}, {
		// This line carries the *gateway* address; reporting it as the tunnel
		// address would show the user a bogus IP and mark a dead cookie proven.
		name: "gateway connection line must not match",
		line: "Connected to 203.0.113.7:10443",
		want: "",
	}, {
		name: "unrelated progress line",
		line: "SSL negotiation with vpn.example.com",
		want: "",
	}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ""
			if m := connectedRe.FindStringSubmatch(tc.line); m != nil {
				got = m[1]
			}
			if got != tc.want {
				t.Errorf("parsed %q from %q, want %q", got, tc.line, tc.want)
			}
		})
	}
}

func TestIsAuthRejected(t *testing.T) {
	tests := []struct {
		name string
		tail string
		want bool
	}{{
		name: "openconnect 9.x rejection, capitalised as printed",
		tail: "Cookie was rejected by server; exiting.",
		want: true,
	}, {
		name: "session invalidated mid-run",
		tail: "Cookie is no longer valid, ending session\n",
		want: true,
	}, {
		name: "server killed the session",
		tail: "Session terminated by server; exiting.",
		want: true,
	}, {
		name: "auth handshake refused",
		tail: "Failed to complete authentication\n",
		want: true,
	}, {
		// The one that actually fires on a stale SVPNCOOKIE against FortiOS 5+:
		// the config fetch gets 401/403 and openconnect prints this instead of
		// anything mentioning a cookie. Missing it means retrying a dead cookie
		// forever.
		name: "fortigate refused the config request (dead SVPNCOOKIE)",
		tail: "Fortinet server is rejecting request for connection options. This\n" +
			"has been observed after reconnection in some cases. Please report to\n",
		want: true,
	}, {
		name: "cookie supplied is not a usable SVPNCOOKIE",
		tail: "No cookie named SVPNCOOKIE.\n",
		want: true,
	}, {
		// Plain link trouble must be retried with the existing cookie: treating
		// it as a rejection would pop a SAML browser window on every hiccup.
		name: "network failure is not a rejection",
		tail: "Failed to connect to host vpn.example.com\nError establishing Fortinet connection\n",
		want: false,
	}, {
		name: "clean output",
		tail: "Configured as 10.0.0.5, with SSL connected and ESP disabled",
		want: false,
	}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isAuthRejected(tc.tail); got != tc.want {
				t.Errorf("isAuthRejected(%q) = %v, want %v", tc.tail, got, tc.want)
			}
		})
	}
}

// Matching lowercases the output, so an upper-case marker could never match.
func TestAuthRejectedMarkersAreLowercase(t *testing.T) {
	for _, m := range authRejectedMarkers {
		if m != strings.ToLower(m) {
			t.Errorf("marker %q must be lower case: matching is done against lowercased output", m)
		}
	}
}

func TestIsPermanent(t *testing.T) {
	tests := []struct {
		name string
		tail string
		want bool
	}{{
		// sudo's own wording when /etc/sudoers.d/openfortitray is gone, does not name
		// this user, or names a different helper path.
		name: "sudo -n with no NOPASSWD rule",
		tail: "sudo: a password is required\n",
		want: true,
	}, {
		name: "the helper's own uninstalled guard",
		tail: "openfortitray-tunnel: not installed: run scripts/install.sh, which bakes in " +
			"the openconnect path\n",
		want: true,
	}, {
		// Matching lowercases the output, so capitalisation must not matter.
		name: "capitalisation is irrelevant",
		tail: "Sudo: A Password Is Required",
		want: true,
	}, {
		// Everything below is worth retrying: treating it as permanent would
		// strand the user in a terminal Error over a hiccup, and only Connect
		// gets them out of that.
		name: "network trouble",
		tail: "Failed to connect to host vpn.example.com\nError establishing Fortinet connection\n",
		want: false,
	}, {
		name: "a rejected cookie is handled by re-authenticating, not by giving up",
		tail: "Cookie was rejected by server; exiting.",
		want: false,
	}, {
		name: "clean output",
		tail: "Configured as 10.0.0.5, with SSL connected and ESP disabled",
		want: false,
	}, {
		name: "empty output",
		tail: "",
		want: false,
	}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPermanent(tc.tail); got != tc.want {
				t.Errorf("isPermanent(%q) = %v, want %v", tc.tail, got, tc.want)
			}
		})
	}
}

// Matching lowercases the output, so an upper-case marker could never match.
func TestPermanentMarkersAreLowercase(t *testing.T) {
	for _, m := range permanentMarkers {
		if m != strings.ToLower(m) {
			t.Errorf("marker %q must be lower case: matching is done against lowercased output", m)
		}
	}
}

// TestInstallHintIsOSAware pins the remediation hint to each platform's real
// repair path: scripts/install.sh does not exist on Windows (no privileged
// helper there), so the Windows hint must not send the user to it, and must fit
// the tray's 60-rune first-line clip. unix keeps the install.sh guidance.
func TestInstallHintIsOSAware(t *testing.T) {
	const clip = 60

	win := installHintFor("windows")
	if strings.Contains(win, "install.sh") {
		t.Errorf("windows hint must not reference scripts/install.sh (it does not exist on Windows): %q", win)
	}
	if !strings.Contains(win, "Administrator") {
		t.Errorf("windows hint should point at running as Administrator: %q", win)
	}

	for _, goos := range []string{"darwin", "linux"} {
		if got := installHintFor(goos); got != "re-run scripts/install.sh" {
			t.Errorf("installHintFor(%q) = %q, want the install.sh guidance", goos, got)
		}
	}

	for _, goos := range []string{"windows", "darwin", "linux"} {
		if n := len([]rune(installHintFor(goos))); n > clip {
			t.Errorf("installHintFor(%q) is %d runes, exceeds the tray's %d-rune clip", goos, n, clip)
		}
	}
}

func TestStartArgv(t *testing.T) {
	tests := []struct {
		name     string
		opts     Options
		wantName string
		wantArgs []string
	}{{
		name:     "direct run (Windows: already elevated), DTLS on and dual-stack on emit no extra flags",
		opts:     Options{OpenconnectPath: "openconnect", Gateway: "gw.example.com:10443", DTLS: true, DualStack: true},
		wantName: "openconnect",
		wantArgs: []string{"--protocol=fortinet", "--cookie-on-stdin", "--non-inter", "gw.example.com:10443"},
	}, {
		name:     "DTLS off appends --no-dtls",
		opts:     Options{OpenconnectPath: "openconnect", Gateway: "gw:443", DTLS: false, DualStack: true},
		wantName: "openconnect",
		wantArgs: []string{"--protocol=fortinet", "--cookie-on-stdin", "--non-inter", "--no-dtls", "gw:443"},
	}, {
		name:     "dual-stack off appends --disable-ipv6",
		opts:     Options{OpenconnectPath: "openconnect", Gateway: "gw:443", DTLS: true, DualStack: false},
		wantName: "openconnect",
		wantArgs: []string{"--protocol=fortinet", "--cookie-on-stdin", "--non-inter", "--disable-ipv6", "gw:443"},
	}, {
		name:     "pin mode appends --servercert <pin>",
		opts:     Options{OpenconnectPath: "openconnect", Gateway: "gw:443", DTLS: true, DualStack: true, ServerCertMode: "pin", ServerCertPin: "sha256:AB:CD"},
		wantName: "openconnect",
		wantArgs: []string{"--protocol=fortinet", "--cookie-on-stdin", "--non-inter", "--servercert", "sha256:AB:CD", "gw:443"},
	}, {
		name:     "trust mode with a pin appends --servercert <pin>",
		opts:     Options{OpenconnectPath: "openconnect", Gateway: "gw:443", DTLS: true, DualStack: true, ServerCertMode: "trust", ServerCertPin: "AB:CD"},
		wantName: "openconnect",
		wantArgs: []string{"--protocol=fortinet", "--cookie-on-stdin", "--non-inter", "--servercert", "AB:CD", "gw:443"},
	}, {
		name:     "trust mode with no pin emits no cert flag (openconnect has no accept-invalid option)",
		opts:     Options{OpenconnectPath: "openconnect", Gateway: "gw:443", DTLS: true, DualStack: true, ServerCertMode: "trust"},
		wantName: "openconnect",
		wantArgs: []string{"--protocol=fortinet", "--cookie-on-stdin", "--non-inter", "gw:443"},
	}, {
		name:     "warn mode emits no cert flag",
		opts:     Options{OpenconnectPath: "openconnect", Gateway: "gw:443", DTLS: true, DualStack: true, ServerCertMode: "warn"},
		wantName: "openconnect",
		wantArgs: []string{"--protocol=fortinet", "--cookie-on-stdin", "--non-inter", "gw:443"},
	}, {
		name:     "all toggles together preserve flag order",
		opts:     Options{OpenconnectPath: "openconnect", Gateway: "gw:443", DTLS: false, DualStack: false, ServerCertMode: "pin", ServerCertPin: "FF"},
		wantName: "openconnect",
		wantArgs: []string{"--protocol=fortinet", "--cookie-on-stdin", "--non-inter", "--no-dtls", "--disable-ipv6", "--servercert", "FF", "gw:443"},
	}, {
		name:     "helper path gets the toggles after the gateway (allowlisted flags reach the helper)",
		opts:     Options{HelperPath: "/opt/h", Gateway: "gw:443", UseSudo: true, DTLS: false, DualStack: false, ServerCertMode: "pin", ServerCertPin: "FF"},
		wantName: "sudo",
		wantArgs: []string{"-n", "/opt/h", "start", "gw:443", "--no-dtls", "--disable-ipv6", "--servercert", "FF"},
	}, {
		name:     "helper path, DTLS off only appends --no-dtls after the gateway",
		opts:     Options{HelperPath: "/opt/h", Gateway: "gw:443", UseSudo: true, DTLS: false, DualStack: true},
		wantName: "sudo",
		wantArgs: []string{"-n", "/opt/h", "start", "gw:443", "--no-dtls"},
	}, {
		name: "privileged run goes through the helper, never openconnect itself; toggles on emit no flags",
		opts: Options{
			OpenconnectPath: "openconnect",
			HelperPath:      "/opt/custom/openfortitray-tunnel",
			Gateway:         "gw.example.com:10443",
			UseSudo:         true,
			DTLS:            true,
			DualStack:       true,
		},
		wantName: "sudo",
		wantArgs: []string{"-n", "/opt/custom/openfortitray-tunnel", "start", "gw.example.com:10443"},
	}, {
		name:     "empty helper path falls back to the installed location; toggles on emit no flags",
		opts:     Options{Gateway: "gw.example.com:10443", UseSudo: true, DTLS: true, DualStack: true},
		wantName: "sudo",
		wantArgs: []string{"-n", DefaultHelperPath, "start", "gw.example.com:10443"},
	}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name, args := tc.opts.startArgv()
			if name != tc.wantName {
				t.Errorf("command = %q, want %q", name, tc.wantName)
			}
			if !slices.Equal(args, tc.wantArgs) {
				t.Errorf("args = %q, want %q", args, tc.wantArgs)
			}
			// The cookie must never reach the argv: it would be world-readable
			// in the process table.
			for _, a := range args {
				if strings.Contains(a, "cookie=") {
					t.Errorf("argv leaks a cookie value: %q", a)
				}
			}
		})
	}
}

func TestStopArgv(t *testing.T) {
	// Direct runs are signalled, so there is nothing to shell out to.
	if _, _, viaHelper := (Options{OpenconnectPath: "openconnect"}).stopArgv(); viaHelper {
		t.Error("direct path must be torn down by signal, not by a stop command")
	}
	name, args, viaHelper := (Options{HelperPath: "/opt/custom/h", UseSudo: true}).stopArgv()
	if !viaHelper {
		t.Fatal("privileged path must tear down via the helper: a root process cannot be signalled")
	}
	if name != "sudo" || !slices.Equal(args, []string{"-n", "/opt/custom/h", "stop"}) {
		t.Errorf("stop command = %q %q, want sudo -n /opt/custom/h stop", name, args)
	}
}

// ReapStale is the startup self-heal for an orphaned root openconnect. It cannot
// be tested against a real root process, so these assert the wiring: the correct
// helper "stop" argv, the fallback path, the direct-path no-op, and that a runner
// error is surfaced for logging.
func TestReapStale(t *testing.T) {
	t.Run("privileged path invokes the helper stop with the correct argv", func(t *testing.T) {
		var gotName string
		var gotArgs []string
		calls := 0
		opts := Options{
			HelperPath: "/opt/custom/openfortitray-tunnel",
			UseSudo:    true,
			reapRunner: func(ctx context.Context, name string, args []string) error {
				calls++
				gotName, gotArgs = name, args
				return nil
			},
		}
		if err := opts.ReapStale(context.Background()); err != nil {
			t.Fatalf("ReapStale returned error: %v", err)
		}
		if calls != 1 {
			t.Fatalf("reap runner called %d times, want exactly 1", calls)
		}
		if gotName != "sudo" || !slices.Equal(gotArgs, []string{"-n", "/opt/custom/openfortitray-tunnel", "stop"}) {
			t.Errorf("reap invoked %q %q, want sudo -n /opt/custom/openfortitray-tunnel stop", gotName, gotArgs)
		}
	})

	t.Run("empty helper path falls back to the installed location", func(t *testing.T) {
		var gotArgs []string
		opts := Options{
			UseSudo:    true,
			reapRunner: func(ctx context.Context, name string, args []string) error { gotArgs = args; return nil },
		}
		if err := opts.ReapStale(context.Background()); err != nil {
			t.Fatalf("ReapStale returned error: %v", err)
		}
		if !slices.Equal(gotArgs, []string{"-n", DefaultHelperPath, "stop"}) {
			t.Errorf("reap argv = %q, want -n %s stop", gotArgs, DefaultHelperPath)
		}
	})

	t.Run("direct path has no privileged helper, so reap is a no-op", func(t *testing.T) {
		called := false
		opts := Options{
			OpenconnectPath: "openconnect", // UseSudo false → direct path
			reapRunner:      func(ctx context.Context, name string, args []string) error { called = true; return nil },
		}
		if err := opts.ReapStale(context.Background()); err != nil {
			t.Fatalf("ReapStale on the direct path returned error: %v", err)
		}
		if called {
			t.Error("direct path (no helper) must not shell out to a stop command")
		}
	})

	t.Run("a runner error is surfaced so startup can log it", func(t *testing.T) {
		opts := Options{
			UseSudo:    true,
			reapRunner: func(ctx context.Context, name string, args []string) error { return errors.New("boom") },
		}
		if err := opts.ReapStale(context.Background()); err == nil {
			t.Error("ReapStale must return the runner error (best-effort, but logged)")
		}
	})
}

// writeScript writes an executable shell script and returns its path.
func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// The privileged path is what ships on macOS/Linux, and its teardown cannot be
// tested with signals — the whole point is that the child ignores them (it
// stands in for a root process an unprivileged parent may not signal). A stub
// sudo lets us verify the real wiring end to end: argv, cookie on stdin, IP
// parsing, and teardown through the helper's "stop" subcommand.
func TestRunOpenconnectViaHelperStopsThroughHelper(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs: POSIX only (the helper path is macOS/Linux anyway)")
	}
	dir := t.TempDir()
	argvLog := filepath.Join(dir, "argv.log")
	cookieFile := filepath.Join(dir, "cookie")
	stopFile := filepath.Join(dir, "stopped")

	// Stands in for sudo: logs its arguments, then behaves like the helper.
	// "start" ignores SIGINT/SIGTERM, so the only way out is "stop".
	sudo := writeScript(t, dir, "sudo", `#!/bin/sh
# printf, not echo: dash's echo treats a leading "-n" arg (sudo's flag) as its
# own suppress-newline option, eating it and the trailing newline on Linux.
printf '%s\n' "$*" >> `+argvLog+`
case "$3" in
start)
	trap '' INT TERM
	cat > `+cookieFile+`
	echo "Connected to 203.0.113.7:10443"
	echo "Configured as 10.0.0.5, with SSL connected and ESP disabled"
	while [ ! -f `+stopFile+` ]; do sleep 0.05; done
	exit 0
	;;
stop)
	: > `+stopFile+`
	exit 0
	;;
esac
exit 64
`)

	opts := Options{
		Gateway:    "gw.example.com:10443",
		HelperPath: "/opt/custom/openfortitray-tunnel",
		UseSudo:    true,
		sudoPath:   sudo,
	}
	run := RunOpenconnect(opts)

	ctx, cancel := context.WithCancel(context.Background())
	gotIP := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, "COOKIE-VALUE", func(ip string) {
			select {
			case gotIP <- ip:
			default:
			}
		})
	}()

	select {
	case ip := <-gotIP:
		if ip != "10.0.0.5" {
			t.Errorf("connected IP = %q, want the tunnel address 10.0.0.5", ip)
		}
	case err := <-done:
		t.Fatalf("run exited before reporting Connected: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("never reported Connected")
	}

	cancel() // Disconnect: teardown must go through the helper
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("run did not return after cancel: teardown did not reach the helper")
	}

	cookie, err := os.ReadFile(cookieFile)
	if err != nil {
		t.Fatalf("cookie was never written to the backend's stdin: %v", err)
	}
	if got := strings.TrimSpace(string(cookie)); got != "COOKIE-VALUE" {
		t.Errorf("backend received cookie %q, want COOKIE-VALUE", got)
	}
	if _, err := os.Stat(stopFile); err != nil {
		t.Errorf("helper stop was never invoked: %v", err)
	}
	logged, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatal(err)
	}
	wantLines := []string{
		"-n /opt/custom/openfortitray-tunnel start gw.example.com:10443",
		"-n /opt/custom/openfortitray-tunnel stop",
	}
	for _, want := range wantLines {
		if !strings.Contains(string(logged), want) {
			t.Errorf("sudo argv log missing %q; got:\n%s", want, logged)
		}
	}
}

// On the direct path (Windows) nothing changed: the process is interrupted so it
// can restore routing itself.
func TestRunOpenconnectDirectPathIsSignalled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stub: POSIX only")
	}
	dir := t.TempDir()
	interrupted := filepath.Join(dir, "interrupted")
	fake := writeScript(t, dir, "openconnect", `#!/bin/sh
trap 'touch `+interrupted+`; exit 0' INT
echo "Configured as 10.0.0.7, with SSL connected and ESP disabled"
while :; do sleep 0.05; done
`)

	run := RunOpenconnect(Options{OpenconnectPath: fake, Gateway: "gw.example.com:10443"})
	ctx, cancel := context.WithCancel(context.Background())
	gotIP := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, "COOKIE", func(ip string) {
			select {
			case gotIP <- ip:
			default:
			}
		})
	}()
	select {
	case ip := <-gotIP:
		if ip != "10.0.0.7" {
			t.Errorf("connected IP = %q", ip)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("never reported Connected")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("run did not return after cancel")
	}
	if _, err := os.Stat(interrupted); err != nil {
		t.Errorf("backend was not interrupted, so it could not restore routing: %v", err)
	}
}

// A rejected cookie has to be reported as ErrAuthRejected even when openconnect
// exits successfully, or the supervisor would retry forever with a dead cookie.
func TestRunOpenconnectReportsRejectionOnCleanExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stub: POSIX only")
	}
	fake := writeScript(t, t.TempDir(), "openconnect", `#!/bin/sh
cat > /dev/null
echo "Cookie was rejected by server; exiting."
exit 0
`)
	run := RunOpenconnect(Options{OpenconnectPath: fake, Gateway: "gw.example.com:10443"})
	err := run(context.Background(), "STALE", func(string) {
		t.Error("must not report Connected on a rejected cookie")
	})
	if !errors.Is(err, ErrAuthRejected) {
		t.Fatalf("err = %v, want ErrAuthRejected", err)
	}
}

func TestWaitReturnsImmediatelyWhenNeverConnected(t *testing.T) {
	events := make(chan Event, 4)
	s := New(
		func(ctx context.Context) (string, error) { return "C", nil },
		func(ctx context.Context, cookie string, connected func(string)) error { return nil },
		events)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	s.Wait(ctx)
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("Wait blocked %v with no loop ever started, want an immediate return", d)
	}
}

func TestWaitBlocksUntilBackendTornDown(t *testing.T) {
	events := make(chan Event, 64)
	c := collect(events)
	defer c.close()

	const teardown = 150 * time.Millisecond
	var exited atomic.Bool
	auth := func(ctx context.Context) (string, error) { return "C", nil }
	run := func(ctx context.Context, cookie string, connected func(string)) error {
		connected("10.0.0.9")
		<-ctx.Done()
		time.Sleep(teardown) // openconnect winding down and restoring routing
		exited.Store(true)
		return ctx.Err()
	}
	s := New(auth, run, events)
	s.Connect()
	c.waitFor(t, Connected, 2*time.Second)

	s.Disconnect()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	s.Wait(ctx)
	if !exited.Load() {
		t.Fatal("Wait returned before the backend finished tearing down")
	}
	if d := time.Since(start); d < teardown {
		t.Errorf("Wait returned after %v, want at least the %v teardown", d, teardown)
	}
	s.Wait(ctx) // a finished loop must not block a second caller
}

func TestWaitHonoursContextDeadline(t *testing.T) {
	events := make(chan Event, 64)
	c := collect(events)
	defer c.close()

	auth := func(ctx context.Context) (string, error) { return "C", nil }
	run := func(ctx context.Context, cookie string, connected func(string)) error {
		connected("10.0.0.1")
		<-ctx.Done() // stays up: only the deadline can release Wait
		return ctx.Err()
	}
	s := New(auth, run, events)
	s.Connect()
	c.waitFor(t, Connected, 2*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	s.Wait(ctx)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Wait ignored its deadline: blocked %v on a live tunnel", d)
	}
	s.Disconnect()
	c.waitFor(t, Disconnected, 2*time.Second)
}

// The runner must call Logout with the session's cookie once the backend exits —
// and only when a session actually came up. openconnect has no Fortinet logout,
// so this hook is the only thing that ends the session on the gateway; skipping
// it leaves a one-session-per-user gateway refusing reconnects until it times out.
func TestRunOpenconnectLogsOutAfterSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stub: POSIX only")
	}
	dir := t.TempDir()
	fake := writeScript(t, dir, "openconnect", `#!/bin/sh
echo "Configured as 10.0.0.9, with SSL connected"
exit 0
`)
	var mu sync.Mutex
	var got []string
	run := RunOpenconnect(Options{
		OpenconnectPath: fake,
		Gateway:         "gw.example.com:10443",
		Logout:          func(c string) { mu.Lock(); got = append(got, c); mu.Unlock() },
	})
	_ = run(context.Background(), "SESSIONCOOKIE", func(string) {})

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0] != "SESSIONCOOKIE" {
		t.Errorf("Logout calls = %v, want exactly one with the session cookie", got)
	}
}

// No session, nothing to log out: a cookie the gateway refused never created a
// session, so calling logout for it would be a pointless request on every retry.
func TestRunOpenconnectSkipsLogoutWithoutSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stub: POSIX only")
	}
	dir := t.TempDir()
	fake := writeScript(t, dir, "openconnect", `#!/bin/sh
echo "Cookie was rejected by server; exiting."
exit 1
`)
	var mu sync.Mutex
	calls := 0
	run := RunOpenconnect(Options{
		OpenconnectPath: fake,
		Gateway:         "gw.example.com:10443",
		Logout:          func(string) { mu.Lock(); calls++; mu.Unlock() },
	})
	_ = run(context.Background(), "DEAD", func(string) {})

	mu.Lock()
	defer mu.Unlock()
	if calls != 0 {
		t.Errorf("Logout called %d times for a rejected cookie, want 0", calls)
	}
}

// A Connect that keeps being refused must NOT re-authenticate on every round.
// Re-minting is the only thing that opens a browser, and a gateway refusing
// because it still holds a previous session refuses fresh cookies just as
// readily — so a cookie per round produced a browser tab per round while
// changing nothing. Retrying the cookie we already have is silent.
func TestRefusedRoundsDoNotRemintEveryTime(t *testing.T) {
	events := make(chan Event, 256)
	c := collect(events)
	defer c.close()

	var authCalls atomic.Int32
	auth := func(ctx context.Context) (string, error) {
		return fmt.Sprintf("C%d", authCalls.Add(1)), nil
	}
	var runs atomic.Int32
	run := func(ctx context.Context, cookie string, connected func(string)) error {
		runs.Add(1)
		return ErrAuthRejected // the gateway refuses everything, as when it holds a session
	}
	s := New(auth, run, events)
	s.maxEarlyRetries = 0
	s.backoffBase = 5 * time.Millisecond
	s.backoffMax = 5 * time.Millisecond
	s.remintEveryRounds = 4
	s.maxConnectRounds = 8
	s.Connect()

	// It must give up rather than retry forever, with a message that says what to do.
	ev := c.waitFor(t, Error, 3*time.Second)
	if !strings.Contains(ev.Detail, "couldn't connect") || !strings.Contains(ev.Detail, "Connect") {
		t.Errorf("give-up detail = %q, want it to say it could not connect and to click Connect", ev.Detail)
	}

	// Over ~8 refused rounds, a re-mint every 4th means far fewer logins than runs.
	gotAuth, gotRuns := int(authCalls.Load()), int(runs.Load())
	if gotRuns < 4 {
		t.Fatalf("runs = %d, want the loop to have retried several times", gotRuns)
	}
	if gotAuth >= gotRuns {
		t.Errorf("auth calls = %d for %d attempts: a login (browser tab) per round is exactly what must not happen",
			gotAuth, gotRuns)
	}
}

// Once the tunnel HAS come up, retries must stay unbounded: a real session
// deserves indefinite reconnection, and only the never-connected case gives up.
func TestGiveUpDoesNotApplyAfterAHealthySession(t *testing.T) {
	events := make(chan Event, 256)
	c := collect(events)
	defer c.close()

	auth := func(ctx context.Context) (string, error) { return "C", nil }
	var runs atomic.Int32
	run := func(ctx context.Context, cookie string, connected func(string)) error {
		if runs.Add(1) == 1 {
			connected("10.0.0.5") // one healthy bring-up
			return errors.New("network blip")
		}
		return errors.New("still down")
	}
	s := New(auth, run, events)
	s.backoffBase = 5 * time.Millisecond
	s.backoffMax = 5 * time.Millisecond
	s.maxConnectRounds = 3
	s.Connect()
	defer s.Disconnect()

	c.waitFor(t, Connected, 2*time.Second)
	// Well past maxConnectRounds worth of rounds, it must still be retrying.
	time.Sleep(150 * time.Millisecond)
	for _, e := range c.snapshot() {
		if e.State == Error {
			t.Fatalf("gave up after a healthy session (%q); reconnection must stay unbounded there", e.Detail)
		}
	}
	if runs.Load() < 4 {
		t.Errorf("runs = %d, want it to have kept retrying", runs.Load())
	}
}
