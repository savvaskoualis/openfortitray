package tunnel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
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
	if e.Detail != "link dropped" {
		t.Errorf("Reconnecting detail = %q, want the backend error text", e.Detail)
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
	s.Connect()
	c.waitFor(t, Reconnecting, 2*time.Second)
	time.Sleep(50 * time.Millisecond)
	if n := authCalls.Load(); n != 1 {
		t.Errorf("auth calls = %d, want 1 (must wait out backoff before re-auth)", n)
	}
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

func TestRunOpenconnectStartFailure(t *testing.T) {
	run := RunOpenconnect(Options{
		OpenconnectPath: "/nonexistent/openconnect-postern-test",
		Gateway:         "vpn.example.com:443",
	})
	err := run(context.Background(), "COOKIE", func(string) { t.Error("connected must not be called") })
	if err == nil {
		t.Fatal("expected an error when the binary does not exist")
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

func TestStartArgv(t *testing.T) {
	tests := []struct {
		name     string
		opts     Options
		wantName string
		wantArgs []string
	}{{
		name:     "direct run (Windows: already elevated)",
		opts:     Options{OpenconnectPath: "openconnect", Gateway: "gw.example.com:10443"},
		wantName: "openconnect",
		wantArgs: []string{"--protocol=fortinet", "--cookie-on-stdin", "--non-inter", "gw.example.com:10443"},
	}, {
		name: "privileged run goes through the helper, never openconnect itself",
		opts: Options{
			OpenconnectPath: "openconnect",
			HelperPath:      "/opt/custom/postern-tunnel",
			Gateway:         "gw.example.com:10443",
			UseSudo:         true,
		},
		wantName: "sudo",
		wantArgs: []string{"-n", "/opt/custom/postern-tunnel", "start", "gw.example.com:10443"},
	}, {
		name:     "empty helper path falls back to the installed location",
		opts:     Options{Gateway: "gw.example.com:10443", UseSudo: true},
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
echo "$@" >> `+argvLog+`
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
		HelperPath: "/opt/custom/postern-tunnel",
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
		"-n /opt/custom/postern-tunnel start gw.example.com:10443",
		"-n /opt/custom/postern-tunnel stop",
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
