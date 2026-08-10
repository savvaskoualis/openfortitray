package tunnel

import (
	"context"
	"errors"
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
		connected("10.212.134.5")
		<-ctx.Done() // stay "up" until disconnected
		return ctx.Err()
	}
	s := New(auth, run, events)
	s.Connect()
	e := c.waitFor(t, Connected, 2*time.Second)
	if e.Detail != "10.212.134.5" {
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
		connected("10.212.134.5")
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
		connected("10.212.134.5")
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
		connected("10.212.134.5")
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
		connected("10.212.134.5")
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
		connected("10.212.134.5")
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
		connected("10.212.134.5")
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
		connected("10.212.134.5")
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
	run := RunOpenconnect("/nonexistent/openconnect-hyp-vpn-test", "vpn.example.com:443", false)
	err := run(context.Background(), "COOKIE", func(string) { t.Error("connected must not be called") })
	if err == nil {
		t.Fatal("expected an error when the binary does not exist")
	}
}
